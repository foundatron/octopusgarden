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

// inferTier assigns a difficulty tier based on weighted complexity scoring.
//
// Scoring:
//   - Each step: +1 base
//   - browser, grpc, or ws step type: +1 extra per step
//   - gRPC step with Stream set: +1 extra
//   - WS step with Receive set: +1 extra
//   - Step with Retry set: +1 extra
//   - Mixed step types (>1 unique type across all steps): +2 flat bonus
//   - 3+ steps with captures: tier 3 regardless of score
//
// Tier 3: score >6 or ≥3 steps with captures.
// Tier 2: score >3 or ≥1 step with captures.
// Tier 1: everything else.
func inferTier(s Scenario) int {
	stepsWithCaptures := 0
	score := 0
	types := make(map[string]struct{})

	for _, step := range s.Steps {
		if len(step.Capture) > 0 {
			stepsWithCaptures++
		}
		score++ // base

		t := step.StepType()
		types[t] = struct{}{}

		switch t {
		case "browser", "grpc", "ws", "tui":
			score++
		}
		if step.GRPC != nil && step.GRPC.Stream != nil {
			score++
		}
		if step.WS != nil && step.WS.Receive != nil {
			score++
		}
		if step.Retry != nil {
			score++
		}
	}

	if len(types) > 1 {
		score += 2
	}

	switch {
	case score > 6 || stepsWithCaptures >= 3:
		return 3
	case score > 3 || stepsWithCaptures >= 1:
		return 2
	default:
		return 1
	}
}
