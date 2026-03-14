package gene

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

var (
	errInvalidVersion     = errors.New("version must be greater than zero")
	errEmptyGuide         = errors.New("guide must not be empty")
	errUnknownLanguage    = errors.New("unknown language")
	errEmptyComponentName = errors.New("component name must not be empty")
	errDuplicateComponent = errors.New("duplicate component name")
	errMissingDependency  = errors.New("component dependency not found")
	errDependencyCycle    = errors.New("dependency cycle detected")
)

var validLanguages = map[string]bool{
	"go":     true,
	"python": true,
	"node":   true,
	"rust":   true,
}

// Component represents a named architectural component within a Gene,
// with its interface description, patterns, and declared dependencies.
type Component struct {
	Name      string   `json:"name"`
	Interface string   `json:"interface"`
	Patterns  string   `json:"patterns"`
	DependsOn []string `json:"depends_on,omitempty"`
}

// Gene represents an extracted coding guide for a specific language,
// derived from a source repository's patterns and conventions.
type Gene struct {
	Version     int         `json:"version"`
	Source      string      `json:"source"`
	Language    string      `json:"language"`
	ExtractedAt time.Time   `json:"extracted_at"`
	Guide       string      `json:"guide"`
	TokenCount  int         `json:"token_count"`
	Components  []Component `json:"components,omitempty"`
}

// Validate checks that the gene has valid field values.
func Validate(g Gene) error {
	if g.Version < 1 {
		return errInvalidVersion
	}
	if g.Guide == "" {
		return errEmptyGuide
	}
	if !validLanguages[g.Language] {
		return errUnknownLanguage
	}
	if len(g.Components) > 0 {
		if err := validateComponents(g.Components); err != nil {
			return err
		}
	}
	return nil
}

// validateComponents checks for empty/duplicate names, missing dependencies, and dependency cycles.
func validateComponents(components []Component) error {
	names := make(map[string]bool, len(components))
	for _, c := range components {
		if c.Name == "" {
			return errEmptyComponentName
		}
		if names[c.Name] {
			return fmt.Errorf("component %q: %w", c.Name, errDuplicateComponent)
		}
		names[c.Name] = true
	}

	for _, c := range components {
		for _, dep := range c.DependsOn {
			if !names[dep] {
				return fmt.Errorf("component %q depends on %q: %w", c.Name, dep, errMissingDependency)
			}
		}
	}

	return detectComponentCycles(components)
}

// detectComponentCycles runs a DFS over the component dependency graph and returns
// errDependencyCycle (wrapped) if any cycle is found.
func detectComponentCycles(components []Component) error {
	adj := make(map[string][]string, len(components))
	for _, c := range components {
		adj[c.Name] = c.DependsOn
	}
	visited := make(map[string]bool, len(components))
	inStack := make(map[string]bool, len(components))
	for _, c := range components {
		if err := dfsComponentCycle(c.Name, nil, adj, visited, inStack); err != nil {
			return err
		}
	}
	return nil
}

// dfsComponentCycle performs a single DFS step for cycle detection using gray/black coloring.
func dfsComponentCycle(name string, path []string, adj map[string][]string, visited, inStack map[string]bool) error {
	if inStack[name] {
		return fmt.Errorf("dependency cycle %s: %w", strings.Join(append(path, name), " -> "), errDependencyCycle)
	}
	if visited[name] {
		return nil
	}
	inStack[name] = true
	newPath := append(path[:len(path):len(path)], name) // cap-limit forces fresh alloc on append
	for _, dep := range adj[name] {
		if err := dfsComponentCycle(dep, newPath, adj, visited, inStack); err != nil {
			return err
		}
	}
	inStack[name] = false
	visited[name] = true
	return nil
}

// Save marshals the gene as indented JSON and writes it to path with 0600 permissions.
// It validates the gene before writing.
func Save(path string, g Gene) error {
	if err := Validate(g); err != nil {
		return fmt.Errorf("gene: %w", err)
	}
	data, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return fmt.Errorf("gene save marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("gene save: %w", err)
	}
	return nil
}

// Load reads a gene from a JSON file at path, unmarshals it, and validates.
func Load(path string) (Gene, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Gene{}, fmt.Errorf("gene: %w", err)
	}
	var g Gene
	if err := json.Unmarshal(data, &g); err != nil {
		return Gene{}, fmt.Errorf("gene: %w", err)
	}
	if err := Validate(g); err != nil {
		return Gene{}, fmt.Errorf("gene: %w", err)
	}
	return g, nil
}
