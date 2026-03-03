package view

import (
	"encoding/json"
	"io"

	"github.com/foundatron/octopusgarden/internal/scenario"
)

// ValidateOutput is the JSON-serializable view of a validation run.
type ValidateOutput struct {
	Version        int              `json:"version"`
	Target         string           `json:"target"`
	AggregateScore float64          `json:"aggregate_score"`
	Threshold      float64          `json:"threshold"`
	Passed         bool             `json:"passed"`
	TotalCostUSD   float64          `json:"total_cost_usd"`
	Failures       []string         `json:"failures"`
	Scenarios      []ScenarioOutput `json:"scenarios"`
}

// ScenarioOutput is the JSON-serializable view of a single scored scenario.
type ScenarioOutput struct {
	ID     string       `json:"id"`
	Score  float64      `json:"score"`
	Weight float64      `json:"weight"`
	Steps  []StepOutput `json:"steps"`
}

// StepOutput is the JSON-serializable view of a single scored step.
type StepOutput struct {
	Description string   `json:"description"`
	Score       int      `json:"score"`
	Passed      bool     `json:"passed"`
	Reasoning   string   `json:"reasoning"`
	Failures    []string `json:"failures"`
	DurationMs  int64    `json:"duration_ms"`
	Error       *string  `json:"error,omitempty"`
	CostUSD     float64  `json:"cost_usd"`
}

// NewValidateOutput converts domain types into the JSON view.
// stepPassThreshold controls the per-step passed boolean.
func NewValidateOutput(agg scenario.AggregateResult, target string, threshold float64, stepPassThreshold int) ValidateOutput {
	scenarios := make([]ScenarioOutput, 0, len(agg.Scenarios))
	for _, s := range agg.Scenarios {
		steps := make([]StepOutput, 0, len(s.Steps))
		for _, st := range s.Steps {
			var errStr *string
			if st.StepResult.Err != nil {
				e := st.StepResult.Err.Error()
				errStr = &e
			}
			stepFailures := st.StepScore.Failures
			if stepFailures == nil {
				stepFailures = []string{}
			}
			steps = append(steps, StepOutput{
				Description: st.StepResult.Description,
				Score:       st.StepScore.Score,
				Passed:      st.StepScore.Score >= stepPassThreshold,
				Reasoning:   st.StepScore.Reasoning,
				Failures:    stepFailures,
				DurationMs:  st.StepResult.Duration.Milliseconds(),
				Error:       errStr,
				CostUSD:     st.StepScore.CostUSD,
			})
		}
		scenarios = append(scenarios, ScenarioOutput{
			ID:     s.ScenarioID,
			Score:  s.Score,
			Weight: s.Weight,
			Steps:  steps,
		})
	}

	failures := agg.Failures
	if failures == nil {
		failures = []string{}
	}

	passed := true
	if threshold > 0 {
		passed = agg.Satisfaction >= threshold
	}

	return ValidateOutput{
		Version:        1,
		Target:         target,
		AggregateScore: agg.Satisfaction,
		Threshold:      threshold,
		Passed:         passed,
		TotalCostUSD:   agg.TotalCostUSD,
		Failures:       failures,
		Scenarios:      scenarios,
	}
}

// WriteJSON encodes v as indented JSON to w with a trailing newline.
func WriteJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
