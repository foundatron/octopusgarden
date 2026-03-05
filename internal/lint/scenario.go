package lint

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/foundatron/octopusgarden/internal/scenario"
)

var (
	idPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	captureNamePat = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
	validMethods   = map[string]bool{
		"GET": true, "POST": true, "PUT": true, "PATCH": true,
		"DELETE": true, "HEAD": true, "OPTIONS": true,
	}
	validTypes = map[string]bool{
		"functional": true, "api": true,
	}
)

// CheckScenario reads a single scenario YAML file and returns diagnostics.
func CheckScenario(path string) ([]Diagnostic, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("lint scenario: %w", err)
	}

	return lintScenarioContent(path, data), nil
}

// CheckScenarioDir lints all .yaml/.yml files in a directory.
// In addition to per-file checks, it detects duplicate scenario IDs.
func CheckScenarioDir(dir string) ([]Diagnostic, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("lint scenario dir: %w", err)
	}

	var allDiags []Diagnostic
	type idInfo struct {
		file string
		line int
	}
	seenIDs := make(map[string]idInfo)

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := filepath.Ext(e.Name())
		if ext != ".yaml" && ext != ".yml" {
			continue
		}

		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("lint scenario dir: %w", err)
		}

		diags := lintScenarioContent(path, data)
		allDiags = append(allDiags, diags...)

		// Extract ID for duplicate detection.
		id, line := extractScenarioID(data)
		if id != "" {
			if prev, ok := seenIDs[id]; ok {
				allDiags = append(allDiags, Diagnostic{
					File:    path,
					Line:    line,
					Level:   Error,
					Message: fmt.Sprintf("duplicate scenario id %q (also in %s:%d)", id, prev.file, prev.line),
				})
			} else {
				seenIDs[id] = idInfo{file: path, line: line}
			}
		}
	}

	return allDiags, nil
}

func lintScenarioContent(path string, data []byte) []Diagnostic {
	if len(bytes.TrimSpace(data)) == 0 {
		return []Diagnostic{{
			File:    path,
			Level:   Error,
			Message: "scenario file is empty",
		}}
	}

	// Phase 1: Parse as yaml.Node for line numbers.
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return []Diagnostic{{
			File:    path,
			Level:   Error,
			Message: fmt.Sprintf("invalid YAML: %s", err),
		}}
	}

	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return []Diagnostic{{
			File:    path,
			Level:   Error,
			Message: "empty YAML document",
		}}
	}

	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return []Diagnostic{{
			File:    path,
			Line:    root.Line,
			Level:   Error,
			Message: "scenario must be a YAML mapping",
		}}
	}

	var diags []Diagnostic
	fields := nodeFields(root)

	// Check id.
	diags = append(diags, checkID(path, fields)...)

	// Check type.
	diags = append(diags, checkType(path, fields)...)

	// Check weight.
	diags = append(diags, checkWeight(path, fields)...)

	// Check steps.
	stepsNode, stepsLine := getFieldNode(fields, "steps")
	if stepsNode == nil {
		diags = append(diags, Diagnostic{
			File:    path,
			Level:   Error,
			Message: "missing required field: steps",
		})
	} else if stepsNode.Kind != yaml.SequenceNode || len(stepsNode.Content) == 0 {
		diags = append(diags, Diagnostic{
			File:    path,
			Line:    stepsLine,
			Level:   Error,
			Message: "steps must be a non-empty array",
		})
		stepsNode = nil
	}

	// Check setup steps.
	setupNode, _ := getFieldNode(fields, "setup")

	// Phase 2: Semantic checks — variable capture/reference tracking.
	cs := newCaptureSet()

	if setupNode != nil && setupNode.Kind != yaml.SequenceNode {
		diags = append(diags, Diagnostic{
			File:    path,
			Level:   Error,
			Message: "setup must be an array",
		})
		setupNode = nil
	}
	if setupNode != nil {
		for _, stepNode := range setupNode.Content {
			diags = append(diags, lintStep(path, stepNode, cs, true)...)
		}
	}

	if stepsNode != nil {
		for _, stepNode := range stepsNode.Content {
			diags = append(diags, lintStep(path, stepNode, cs, false)...)
		}
	}

	return diags
}

func checkID(path string, fields map[string]*fieldEntry) []Diagnostic {
	fe, ok := fields["id"]
	if !ok {
		return []Diagnostic{{
			File:    path,
			Level:   Error,
			Message: "missing required field: id",
		}}
	}

	val := fe.value.Value
	if val == "" {
		return []Diagnostic{{
			File:    path,
			Line:    fe.value.Line,
			Level:   Error,
			Message: "id must not be empty",
		}}
	}

	if !idPattern.MatchString(val) {
		return []Diagnostic{{
			File:    path,
			Line:    fe.value.Line,
			Level:   Error,
			Message: fmt.Sprintf("id %q must match pattern %s", val, idPattern.String()),
		}}
	}

	return nil
}

func checkType(path string, fields map[string]*fieldEntry) []Diagnostic {
	fe, ok := fields["type"]
	if !ok {
		return nil
	}
	val := fe.value.Value
	if !validTypes[val] {
		return []Diagnostic{{
			File:    path,
			Line:    fe.value.Line,
			Level:   Warning,
			Message: fmt.Sprintf("type %q is not one of: functional, api", val),
		}}
	}
	return nil
}

func checkWeight(path string, fields map[string]*fieldEntry) []Diagnostic {
	fe, ok := fields["weight"]
	if !ok {
		return nil
	}
	var w float64
	if err := fe.value.Decode(&w); err != nil {
		return []Diagnostic{{
			File:    path,
			Line:    fe.value.Line,
			Level:   Warning,
			Message: fmt.Sprintf("weight must be a number: %s", err),
		}}
	}
	if w <= 0 {
		return []Diagnostic{{
			File:    path,
			Line:    fe.value.Line,
			Level:   Warning,
			Message: "weight must be positive",
		}}
	}
	return nil
}

func lintStep(path string, node *yaml.Node, cs *captureSet, isSetup bool) []Diagnostic {
	if node.Kind != yaml.MappingNode {
		return []Diagnostic{{
			File:    path,
			Line:    node.Line,
			Level:   Error,
			Message: "step must be a mapping",
		}}
	}

	var diags []Diagnostic
	fields := nodeFields(node)

	// Check step type — exactly one of request, exec, browser, or grpc must be present.
	stepType, typeDiags := lintStepType(path, node, fields, cs)
	diags = append(diags, typeDiags...)

	// Check expect (warning only, not required for setup).
	if !isSetup {
		if _, hasExpect := fields["expect"]; !hasExpect {
			diags = append(diags, Diagnostic{
				File:    path,
				Line:    node.Line,
				Level:   Warning,
				Message: "step missing expect field",
			})
		}
		if _, hasDesc := fields["description"]; !hasDesc {
			diags = append(diags, Diagnostic{
				File:    path,
				Line:    node.Line,
				Level:   Warning,
				Message: "step missing description field",
			})
		}
	}

	// Check captures.
	capFE, hasCap := fields["capture"]
	if hasCap {
		diags = append(diags, lintCaptures(path, capFE.value, cs, stepType)...)
	}

	return diags
}

func lintStepType(path string, node *yaml.Node, fields map[string]*fieldEntry, cs *captureSet) (string, []Diagnostic) {
	reqFE, hasReq := fields["request"]
	execFE, hasExec := fields["exec"]
	browserFE, hasBrowser := fields["browser"]
	grpcFE, hasGRPC := fields["grpc"]

	typeCount := countTrue(hasReq, hasExec, hasBrowser, hasGRPC)

	switch {
	case typeCount > 1:
		return "", []Diagnostic{{
			File:    path,
			Line:    node.Line,
			Level:   Error,
			Message: "step has multiple step types; exactly one of request, exec, browser, or grpc is required",
		}}
	case typeCount == 0:
		return "", []Diagnostic{{
			File:    path,
			Line:    node.Line,
			Level:   Error,
			Message: "step missing step type: exactly one of request, exec, browser, or grpc is required",
		}}
	case hasReq:
		return "request", lintRequest(path, reqFE.value, cs)
	case hasExec:
		return "exec", lintExec(path, execFE.value, cs)
	case hasBrowser:
		return "browser", lintBrowser(path, browserFE.value, cs)
	case hasGRPC:
		return "grpc", lintGRPC(path, grpcFE.value, cs)
	default:
		return "", nil
	}
}

func lintExec(path string, node *yaml.Node, cs *captureSet) []Diagnostic {
	if node.Kind != yaml.MappingNode {
		return []Diagnostic{{
			File:    path,
			Line:    node.Line,
			Level:   Error,
			Message: "exec must be a mapping",
		}}
	}

	var diags []Diagnostic
	fields := nodeFields(node)

	// Check command.
	cmdFE, hasCmd := fields["command"]
	if !hasCmd {
		diags = append(diags, Diagnostic{
			File:    path,
			Line:    node.Line,
			Level:   Error,
			Message: "exec missing required field: command",
		})
	} else if cmdFE.value.Value == "" {
		diags = append(diags, Diagnostic{
			File:    path,
			Line:    cmdFE.value.Line,
			Level:   Error,
			Message: "exec command must not be empty",
		})
	} else {
		// Check variable references in command string.
		refs := extractVarRefs(cmdFE.value.Value)
		diags = append(diags, checkVarRefs(refs, cs, path, cmdFE.value.Line)...)
	}

	// Check variable references in stdin.
	if stdinFE, ok := fields["stdin"]; ok {
		refs := extractVarRefs(stdinFE.value.Value)
		diags = append(diags, checkVarRefs(refs, cs, path, stdinFE.value.Line)...)
	}

	// Check env: must be a mapping with string values; check var refs in values.
	if envFE, ok := fields["env"]; ok {
		if envFE.value.Kind != yaml.MappingNode {
			diags = append(diags, Diagnostic{
				File:    path,
				Line:    envFE.value.Line,
				Level:   Error,
				Message: "exec env must be a mapping",
			})
		} else {
			for i := 1; i < len(envFE.value.Content); i += 2 {
				valNode := envFE.value.Content[i]
				refs := extractVarRefs(valNode.Value)
				diags = append(diags, checkVarRefs(refs, cs, path, valNode.Line)...)
			}
		}
	}

	// Check timeout: must be a valid Go duration.
	if timeoutFE, ok := fields["timeout"]; ok {
		if timeoutFE.value.Value != "" {
			if _, err := time.ParseDuration(timeoutFE.value.Value); err != nil {
				diags = append(diags, Diagnostic{
					File:    path,
					Line:    timeoutFE.value.Line,
					Level:   Error,
					Message: fmt.Sprintf("exec timeout %q is not a valid duration", timeoutFE.value.Value),
				})
			}
		}
	}

	return diags
}

func lintRequest(path string, node *yaml.Node, cs *captureSet) []Diagnostic {
	if node.Kind != yaml.MappingNode {
		return []Diagnostic{{
			File:    path,
			Line:    node.Line,
			Level:   Error,
			Message: "request must be a mapping",
		}}
	}

	var diags []Diagnostic
	fields := nodeFields(node)

	// Check method.
	methodFE, hasMethod := fields["method"]
	if !hasMethod {
		diags = append(diags, Diagnostic{
			File:    path,
			Line:    node.Line,
			Level:   Error,
			Message: "request missing required field: method",
		})
	} else {
		method := strings.ToUpper(methodFE.value.Value)
		if !validMethods[method] {
			diags = append(diags, Diagnostic{
				File:    path,
				Line:    methodFE.value.Line,
				Level:   Error,
				Message: fmt.Sprintf("invalid HTTP method: %q", methodFE.value.Value),
			})
		}
	}

	// Check path.
	pathFE, hasPath := fields["path"]
	if !hasPath {
		diags = append(diags, Diagnostic{
			File:    path,
			Line:    node.Line,
			Level:   Error,
			Message: "request missing required field: path",
		})
	} else {
		// Check variable references in path.
		refs := extractVarRefs(pathFE.value.Value)
		diags = append(diags, checkVarRefs(refs, cs, path, pathFE.value.Line)...)
	}

	// Check variable references in headers.
	headersFE, hasHeaders := fields["headers"]
	if hasHeaders && headersFE.value.Kind == yaml.MappingNode {
		for i := 1; i < len(headersFE.value.Content); i += 2 {
			valNode := headersFE.value.Content[i]
			refs := extractVarRefs(valNode.Value)
			diags = append(diags, checkVarRefs(refs, cs, path, valNode.Line)...)
		}
	}

	// Check variable references in body (if it's a string).
	bodyFE, hasBody := fields["body"]
	if hasBody && bodyFE.value.Kind == yaml.ScalarNode {
		refs := extractVarRefs(bodyFE.value.Value)
		diags = append(diags, checkVarRefs(refs, cs, path, bodyFE.value.Line)...)
	}

	return diags
}

var validBrowserActions = map[string]bool{
	"navigate": true, "click": true, "fill": true, "assert": true,
}

func lintBrowser(path string, node *yaml.Node, cs *captureSet) []Diagnostic {
	if node.Kind != yaml.MappingNode {
		return []Diagnostic{{
			File:    path,
			Line:    node.Line,
			Level:   Error,
			Message: "browser must be a mapping",
		}}
	}

	var diags []Diagnostic
	fields := nodeFields(node)

	// Check action (required).
	actionFE, hasAction := fields["action"]
	if !hasAction {
		diags = append(diags, Diagnostic{
			File:    path,
			Line:    node.Line,
			Level:   Error,
			Message: "browser missing required field: action",
		})
		return diags
	}

	action := actionFE.value.Value
	if !validBrowserActions[action] {
		diags = append(diags, Diagnostic{
			File:    path,
			Line:    actionFE.value.Line,
			Level:   Error,
			Message: fmt.Sprintf("invalid browser action %q; valid actions: navigate, click, fill, assert", action),
		})
		return diags
	}

	// Action-specific required fields.
	diags = append(diags, lintBrowserAction(path, action, node, fields, cs)...)

	// Check variable references in text and text_absent.
	if textFE, ok := fields["text"]; ok {
		diags = append(diags, checkVarRefs(extractVarRefs(textFE.value.Value), cs, path, textFE.value.Line)...)
	}
	if taFE, ok := fields["text_absent"]; ok {
		diags = append(diags, checkVarRefs(extractVarRefs(taFE.value.Value), cs, path, taFE.value.Line)...)
	}

	// Check timeout.
	if timeoutFE, ok := fields["timeout"]; ok {
		if timeoutFE.value.Value != "" {
			if _, err := time.ParseDuration(timeoutFE.value.Value); err != nil {
				diags = append(diags, Diagnostic{
					File:    path,
					Line:    timeoutFE.value.Line,
					Level:   Error,
					Message: fmt.Sprintf("browser timeout: invalid duration %q", timeoutFE.value.Value),
				})
			}
		}
	}

	return diags
}

func lintBrowserAction(path, action string, node *yaml.Node, fields map[string]*fieldEntry, cs *captureSet) []Diagnostic {
	var diags []Diagnostic
	switch action {
	case "navigate":
		diags = append(diags, requireBrowserField(path, node, fields, cs, "url", "navigate")...)
	case "click":
		diags = append(diags, requireBrowserField(path, node, fields, cs, "selector", "click")...)
	case "fill":
		diags = append(diags, requireBrowserField(path, node, fields, cs, "selector", "fill")...)
		diags = append(diags, requireBrowserField(path, node, fields, cs, "value", "fill")...)
	case "assert":
		diags = append(diags, requireBrowserField(path, node, fields, cs, "selector", "assert")...)
		_, hasText := fields["text"]
		_, hasTextAbsent := fields["text_absent"]
		_, hasCount := fields["count"]
		if !hasText && !hasTextAbsent && !hasCount {
			diags = append(diags, Diagnostic{
				File:    path,
				Line:    node.Line,
				Level:   Warning,
				Message: "browser assert has no assertion fields (text, text_absent, or count)",
			})
		}
	}
	return diags
}

func requireBrowserField(path string, node *yaml.Node, fields map[string]*fieldEntry, cs *captureSet, field, action string) []Diagnostic {
	fe, ok := fields[field]
	if !ok || fe.value.Value == "" {
		return []Diagnostic{{
			File:    path,
			Line:    node.Line,
			Level:   Error,
			Message: fmt.Sprintf("browser %s action requires %s", action, field),
		}}
	}
	return checkVarRefs(extractVarRefs(fe.value.Value), cs, path, fe.value.Line)
}

func lintGRPC(path string, node *yaml.Node, cs *captureSet) []Diagnostic {
	if node.Kind != yaml.MappingNode {
		return []Diagnostic{{
			File:    path,
			Line:    node.Line,
			Level:   Error,
			Message: "grpc must be a mapping",
		}}
	}

	var diags []Diagnostic
	fields := nodeFields(node)

	// Check service/method (required unless referencing a background stream by ID).
	_, hasSvc := fields["service"]
	streamFE, hasStream := fields["stream"]
	isStreamCollect := hasStream && !hasSvc
	if !isStreamCollect {
		diags = append(diags, lintGRPCServiceMethod(path, node, fields, cs)...)
	}

	// Check optional fields: body, headers, timeout.
	diags = append(diags, lintGRPCOptionalFields(path, fields, cs)...)

	// Check stream (optional — structural validation).
	if hasStream && streamFE.value.Kind == yaml.MappingNode {
		diags = append(diags, lintGRPCStream(path, streamFE.value, cs)...)
	}

	return diags
}

func lintGRPCServiceMethod(path string, node *yaml.Node, fields map[string]*fieldEntry, cs *captureSet) []Diagnostic {
	var diags []Diagnostic
	svcFE, hasSvc := fields["service"]
	if !hasSvc || svcFE.value.Value == "" {
		diags = append(diags, Diagnostic{
			File:    path,
			Line:    node.Line,
			Level:   Error,
			Message: "grpc missing required field: service",
		})
	} else {
		diags = append(diags, checkVarRefs(extractVarRefs(svcFE.value.Value), cs, path, svcFE.value.Line)...)
	}

	methodFE, hasMethod := fields["method"]
	if !hasMethod || methodFE.value.Value == "" {
		diags = append(diags, Diagnostic{
			File:    path,
			Line:    node.Line,
			Level:   Error,
			Message: "grpc missing required field: method",
		})
	} else {
		diags = append(diags, checkVarRefs(extractVarRefs(methodFE.value.Value), cs, path, methodFE.value.Line)...)
	}
	return diags
}

func lintGRPCOptionalFields(path string, fields map[string]*fieldEntry, cs *captureSet) []Diagnostic {
	var diags []Diagnostic

	if bodyFE, ok := fields["body"]; ok && bodyFE.value.Kind == yaml.ScalarNode {
		diags = append(diags, checkVarRefs(extractVarRefs(bodyFE.value.Value), cs, path, bodyFE.value.Line)...)
	}

	if headersFE, ok := fields["headers"]; ok && headersFE.value.Kind == yaml.MappingNode {
		for i := 1; i < len(headersFE.value.Content); i += 2 {
			valNode := headersFE.value.Content[i]
			diags = append(diags, checkVarRefs(extractVarRefs(valNode.Value), cs, path, valNode.Line)...)
		}
	}

	if timeoutFE, ok := fields["timeout"]; ok && timeoutFE.value.Value != "" {
		if _, err := time.ParseDuration(timeoutFE.value.Value); err != nil {
			diags = append(diags, Diagnostic{
				File:    path,
				Line:    timeoutFE.value.Line,
				Level:   Error,
				Message: fmt.Sprintf("grpc timeout: invalid duration %q", timeoutFE.value.Value),
			})
		}
	}

	return diags
}

func lintGRPCStream(path string, node *yaml.Node, cs *captureSet) []Diagnostic {
	var diags []Diagnostic
	fields := nodeFields(node)

	// Check messages (optional — array of strings, check var refs).
	if msgFE, ok := fields["messages"]; ok {
		if msgFE.value.Kind != yaml.SequenceNode {
			diags = append(diags, Diagnostic{
				File:    path,
				Line:    msgFE.value.Line,
				Level:   Error,
				Message: "grpc stream messages must be an array",
			})
		} else {
			for _, m := range msgFE.value.Content {
				diags = append(diags, checkVarRefs(extractVarRefs(m.Value), cs, path, m.Line)...)
			}
		}
	}

	// Check receive (optional — mapping with timeout, count, background).
	if recvFE, ok := fields["receive"]; ok && recvFE.value.Kind == yaml.MappingNode {
		recvFields := nodeFields(recvFE.value)
		if timeoutFE, ok := recvFields["timeout"]; ok && timeoutFE.value.Value != "" {
			if _, err := time.ParseDuration(timeoutFE.value.Value); err != nil {
				diags = append(diags, Diagnostic{
					File:    path,
					Line:    timeoutFE.value.Line,
					Level:   Error,
					Message: fmt.Sprintf("grpc receive timeout: invalid duration %q", timeoutFE.value.Value),
				})
			}
		}
	}

	return diags
}

var (
	captureSourceOnce sync.Once
	captureSourceMap  map[string]map[string]bool
)

func validCaptureSources() map[string]map[string]bool {
	captureSourceOnce.Do(func() {
		captureSourceMap = scenario.CaptureSourceMap()
	})
	return captureSourceMap
}

func lintCaptures(path string, node *yaml.Node, cs *captureSet, stepType string) []Diagnostic {
	if node.Kind != yaml.SequenceNode {
		return []Diagnostic{{
			File:    path,
			Line:    node.Line,
			Level:   Error,
			Message: "capture must be an array",
		}}
	}

	var diags []Diagnostic
	for _, capNode := range node.Content {
		if capNode.Kind != yaml.MappingNode {
			diags = append(diags, Diagnostic{
				File:    path,
				Line:    capNode.Line,
				Level:   Error,
				Message: "capture entry must be a mapping",
			})
			continue
		}

		fields := nodeFields(capNode)

		// Check name.
		diags = append(diags, lintCaptureName(path, fields, capNode, cs)...)

		// Check source and jsonpath — at least one must be present.
		jpFE, hasJP := fields["jsonpath"]
		srcFE, hasSrc := fields["source"]

		if !hasJP && !hasSrc {
			diags = append(diags, Diagnostic{
				File:    path,
				Line:    capNode.Line,
				Level:   Error,
				Message: "capture requires at least one of jsonpath or source",
			})
		}

		if hasJP {
			jp := jpFE.value.Value
			if err := validateJSONPathSyntax(jp); err != nil {
				diags = append(diags, Diagnostic{
					File:    path,
					Line:    jpFE.value.Line,
					Level:   Error,
					Message: fmt.Sprintf("invalid jsonpath %q: %s", jp, err),
				})
			}
		}

		if hasSrc {
			diags = append(diags, lintCaptureSource(path, srcFE.value, stepType)...)
		}
	}

	return diags
}

func lintCaptureSource(path string, node *yaml.Node, stepType string) []Diagnostic {
	src := node.Value
	allowed := validCaptureSources()[stepType]
	if allowed == nil {
		return []Diagnostic{{
			File:    path,
			Line:    node.Line,
			Level:   Error,
			Message: fmt.Sprintf("source is not supported on %s steps", stepType),
		}}
	}
	if !allowed[src] {
		valid := make([]string, 0, len(allowed))
		for k := range allowed {
			valid = append(valid, k)
		}
		slices.Sort(valid)
		return []Diagnostic{{
			File:    path,
			Line:    node.Line,
			Level:   Error,
			Message: fmt.Sprintf("invalid source %q; valid sources for %s: %s", src, stepType, strings.Join(valid, ", ")),
		}}
	}
	return nil
}

func lintCaptureName(path string, fields map[string]*fieldEntry, capNode *yaml.Node, cs *captureSet) []Diagnostic {
	nameFE, hasName := fields["name"]
	if !hasName {
		return []Diagnostic{{
			File:    path,
			Line:    capNode.Line,
			Level:   Error,
			Message: "capture missing required field: name",
		}}
	}

	name := nameFE.value.Value
	if name == "" {
		return []Diagnostic{{
			File:    path,
			Line:    nameFE.value.Line,
			Level:   Error,
			Message: "capture name must not be empty",
		}}
	}

	if !captureNamePat.MatchString(name) {
		return []Diagnostic{{
			File:    path,
			Line:    nameFE.value.Line,
			Level:   Error,
			Message: fmt.Sprintf("capture name %q must match pattern %s", name, captureNamePat.String()),
		}}
	}

	var diags []Diagnostic
	if prev, ok := cs.info(name); ok {
		diags = append(diags, Diagnostic{
			File:    path,
			Line:    nameFE.value.Line,
			Level:   Warning,
			Message: fmt.Sprintf("capture %q shadows earlier capture at %s:%d", name, prev.file, prev.line),
		})
	}
	cs.add(name, path, nameFE.value.Line)
	return diags
}

// extractScenarioID extracts the id field value and line from raw YAML data.
func extractScenarioID(data []byte) (string, int) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return "", 0
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return "", 0
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return "", 0
	}
	fields := nodeFields(root)
	fe, ok := fields["id"]
	if !ok {
		return "", 0
	}
	return fe.value.Value, fe.value.Line
}

// fieldEntry holds a key-value pair from a YAML mapping node.
type fieldEntry struct {
	key   *yaml.Node
	value *yaml.Node
}

// nodeFields extracts fields from a mapping node into a lookup map.
func nodeFields(node *yaml.Node) map[string]*fieldEntry {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	fields := make(map[string]*fieldEntry, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i]
		val := node.Content[i+1]
		fields[key.Value] = &fieldEntry{key: key, value: val}
	}
	return fields
}

// getFieldNode returns the value node and key line for a field name.
func getFieldNode(fields map[string]*fieldEntry, name string) (*yaml.Node, int) {
	fe, ok := fields[name]
	if !ok {
		return nil, 0
	}
	return fe.value, fe.key.Line
}

func countTrue(vals ...bool) int {
	n := 0
	for _, v := range vals {
		if v {
			n++
		}
	}
	return n
}
