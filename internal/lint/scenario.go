package lint

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
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

	// Check request.
	reqFE, hasReq := fields["request"]
	if !hasReq {
		diags = append(diags, Diagnostic{
			File:    path,
			Line:    node.Line,
			Level:   Error,
			Message: "step missing required field: request",
		})
	} else {
		diags = append(diags, lintRequest(path, reqFE.value, cs)...)
	}

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
		diags = append(diags, lintCaptures(path, capFE.value, cs)...)
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

func lintCaptures(path string, node *yaml.Node, cs *captureSet) []Diagnostic {
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

		// Check jsonpath.
		jpFE, hasJP := fields["jsonpath"]
		if !hasJP {
			diags = append(diags, Diagnostic{
				File:    path,
				Line:    capNode.Line,
				Level:   Error,
				Message: "capture missing required field: jsonpath",
			})
		} else {
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
	}

	return diags
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
