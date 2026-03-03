package lint

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// varRefPattern matches {variable_name} references in strings.
// It avoids matching JSON-like patterns by requiring the name to be
// a valid identifier (letter/underscore start, alphanumeric/underscore body).
var varRefPattern = regexp.MustCompile(`\{([a-zA-Z_][a-zA-Z0-9_]*)\}`)

var (
	errJSONPathMissingPrefix = errors.New("must start with dollar-dot prefix")
	errJSONPathEmptyField    = errors.New("must have at least one field after dollar-dot")
	errJSONPathEmptySegment  = errors.New("empty key segment")
)

// extractVarRefs returns all variable names referenced via {name} in s.
func extractVarRefs(s string) []string {
	matches := varRefPattern.FindAllStringSubmatch(s, -1)
	if len(matches) == 0 {
		return nil
	}
	refs := make([]string, 0, len(matches))
	for _, m := range matches {
		refs = append(refs, m[1])
	}
	return refs
}

// captureSet tracks which variables have been captured and where.
type captureSet struct {
	vars map[string]captureInfo
}

type captureInfo struct {
	file string
	line int // 1-based, 0 = unknown
}

func newCaptureSet() *captureSet {
	return &captureSet{vars: make(map[string]captureInfo)}
}

func (cs *captureSet) add(name, file string, line int) {
	cs.vars[name] = captureInfo{file: file, line: line}
}

func (cs *captureSet) has(name string) bool {
	_, ok := cs.vars[name]
	return ok
}

func (cs *captureSet) info(name string) (captureInfo, bool) {
	ci, ok := cs.vars[name]
	return ci, ok
}

// checkVarRefs checks that all variable references in refs are captured in cs.
// Returns diagnostics for any uncaptured references.
func checkVarRefs(refs []string, cs *captureSet, file string, line int) []Diagnostic {
	var diags []Diagnostic
	seen := make(map[string]bool)
	for _, ref := range refs {
		if seen[ref] {
			continue
		}
		seen[ref] = true
		if !cs.has(ref) {
			diags = append(diags, Diagnostic{
				File:    file,
				Line:    line,
				Level:   Warning,
				Message: fmt.Sprintf("variable {%s} referenced but never captured", ref),
			})
		}
	}
	return diags
}

// validateJSONPathSyntax checks that a jsonpath value follows the $.field.sub pattern.
func validateJSONPathSyntax(path string) error {
	if !strings.HasPrefix(path, "$.") {
		return errJSONPathMissingPrefix
	}
	rest := path[2:]
	if rest == "" {
		return errJSONPathEmptyField
	}
	keys := strings.Split(rest, ".")
	for _, k := range keys {
		if k == "" {
			return errJSONPathEmptySegment
		}
	}
	return nil
}
