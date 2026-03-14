package scenario

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"gopkg.in/yaml.v3"
)

var (
	errEmptyScenario = errors.New("scenario: empty content")
	errMissingID     = errors.New("scenario: missing id")
)

// Load reads a scenario from r and returns a parsed Scenario.
func Load(r io.Reader) (Scenario, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return Scenario{}, fmt.Errorf("load scenario: %w", err)
	}

	return parseScenario(data)
}

// LoadFile reads a scenario from a YAML file on disk.
func LoadFile(path string) (Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Scenario{}, fmt.Errorf("load scenario file: %w", err)
	}

	return parseScenario(data)
}

// LoadDir reads all .yaml and .yml files in dir and returns scenarios sorted by ID.
func LoadDir(dir string) ([]Scenario, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("load scenario dir: %w", err)
	}

	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := filepath.Ext(e.Name())
		if ext == ".yaml" || ext == ".yml" {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}

	scenarios := make([]Scenario, 0, len(paths))
	for _, p := range paths {
		s, err := LoadFile(p)
		if err != nil {
			return nil, fmt.Errorf("load scenario dir: %w", err)
		}
		scenarios = append(scenarios, s)
	}

	slices.SortFunc(scenarios, func(a, b Scenario) int {
		return cmp.Compare(a.ID, b.ID)
	})

	return scenarios, nil
}

func parseScenario(data []byte) (Scenario, error) {
	if len(data) == 0 {
		return Scenario{}, errEmptyScenario
	}

	var s Scenario
	if err := yaml.Unmarshal(data, &s); err != nil {
		return Scenario{}, fmt.Errorf("parse scenario: %w", err)
	}

	if s.ID == "" {
		return Scenario{}, errMissingID
	}

	// Default weight to 1.0 if not specified.
	// Allocate a fresh float64 per scenario to avoid shared pointer mutation.
	if s.Weight == nil {
		w := 1.0
		s.Weight = &w
	}

	if s.Tier == 0 {
		s.Tier = inferTier(s)
	}

	return s, nil
}

// inferTier assigns a difficulty tier based on step count and capture usage.
// Tier 3: more than 6 judged steps, or 3+ steps with captures.
// Tier 2: more than 3 judged steps, or at least 1 step with captures.
// Tier 1: everything else.
func inferTier(s Scenario) int {
	stepsWithCaptures := 0
	for _, step := range s.Steps {
		if len(step.Capture) > 0 {
			stepsWithCaptures++
		}
	}
	switch {
	case len(s.Steps) > 6 || stepsWithCaptures >= 3:
		return 3
	case len(s.Steps) > 3 || stepsWithCaptures >= 1:
		return 2
	default:
		return 1
	}
}
