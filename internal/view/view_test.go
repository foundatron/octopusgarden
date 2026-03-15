package view

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/foundatron/octopusgarden/internal/scenario"
	"github.com/foundatron/octopusgarden/internal/store"
)

func TestNewValidateOutput(t *testing.T) {
	agg := scenario.AggregateResult{
		Satisfaction: 87.5,
		TotalCostUSD: 0.05,
		Failures:     []string{"missing field"},
		Scenarios: []scenario.ScoredScenario{
			{
				ScenarioID: "crud",
				Weight:     1.0,
				Score:      90.0,
				Steps: []scenario.ScoredStep{
					{
						StepResult: scenario.StepResult{
							Description: "create item",
							Duration:    150 * time.Millisecond,
						},
						StepScore: scenario.StepScore{
							Score:     95,
							Reasoning: "correct",
							CostUSD:   0.001,
						},
					},
					{
						StepResult: scenario.StepResult{
							Description: "bad step",
							Duration:    50 * time.Millisecond,
							Err:         errors.New("connection refused"),
						},
						StepScore: scenario.StepScore{
							Score:     60,
							Reasoning: "failed",
							Failures:  []string{"no response"},
							CostUSD:   0.001,
						},
					},
				},
			},
		},
	}

	out := NewValidateOutput(agg, "http://localhost:8080", 95.0, 80)

	if out.Version != 1 {
		t.Errorf("version = %d, want 1", out.Version)
	}
	if out.Target != "http://localhost:8080" {
		t.Errorf("target = %q, want %q", out.Target, "http://localhost:8080")
	}
	if out.AggregateScore != 87.5 {
		t.Errorf("aggregate_score = %f, want 87.5", out.AggregateScore)
	}
	if out.Threshold != 95.0 {
		t.Errorf("threshold = %f, want 95.0", out.Threshold)
	}
	if out.Passed {
		t.Error("passed = true, want false (87.5 < 95.0)")
	}
	if out.TotalCostUSD != 0.05 {
		t.Errorf("total_cost_usd = %f, want 0.05", out.TotalCostUSD)
	}
	if len(out.Failures) != 1 || out.Failures[0] != "missing field" {
		t.Errorf("failures = %v, want [missing field]", out.Failures)
	}
	if len(out.Scenarios) != 1 {
		t.Fatalf("len(scenarios) = %d, want 1", len(out.Scenarios))
	}

	s := out.Scenarios[0]
	if s.ID != "crud" {
		t.Errorf("scenario.id = %q, want %q", s.ID, "crud")
	}
	if len(s.Steps) != 2 {
		t.Fatalf("len(steps) = %d, want 2", len(s.Steps))
	}

	// Step 1: score 95 >= 80, passed
	if !s.Steps[0].Passed {
		t.Error("step[0].passed = false, want true (score 95 >= 80)")
	}
	if s.Steps[0].DurationMs != 150 {
		t.Errorf("step[0].duration_ms = %d, want 150", s.Steps[0].DurationMs)
	}
	if s.Steps[0].Error != nil {
		t.Errorf("step[0].error = %v, want nil", s.Steps[0].Error)
	}
	if s.Steps[0].Failures == nil {
		t.Error("step[0].failures = nil, want empty slice (not null in JSON)")
	}

	// Step 2: score 60 < 80, failed, has error
	if s.Steps[1].Passed {
		t.Error("step[1].passed = true, want false (score 60 < 80)")
	}
	if s.Steps[1].Error == nil || *s.Steps[1].Error != "connection refused" {
		t.Errorf("step[1].error = %v, want %q", s.Steps[1].Error, "connection refused")
	}
	if len(s.Steps[1].Failures) != 1 {
		t.Errorf("step[1].failures = %v, want [no response]", s.Steps[1].Failures)
	}
}

func TestNewValidateOutputPassedWhenAboveThreshold(t *testing.T) {
	agg := scenario.AggregateResult{Satisfaction: 96.0}
	out := NewValidateOutput(agg, "http://localhost:8080", 95.0, 80)
	if !out.Passed {
		t.Error("passed = false, want true (96.0 >= 95.0)")
	}
}

func TestNewValidateOutputPassedWhenNoThreshold(t *testing.T) {
	agg := scenario.AggregateResult{Satisfaction: 50.0}
	out := NewValidateOutput(agg, "http://localhost:8080", 0, 80)
	if !out.Passed {
		t.Error("passed = false, want true (threshold 0 means no gate)")
	}
}

func TestNewValidateOutputNilFailures(t *testing.T) {
	agg := scenario.AggregateResult{Failures: nil}
	out := NewValidateOutput(agg, "http://localhost:8080", 0, 80)
	if out.Failures == nil {
		t.Error("failures = nil, want empty slice (not null in JSON)")
	}
	if len(out.Failures) != 0 {
		t.Errorf("len(failures) = %d, want 0", len(out.Failures))
	}
}

func TestNewStatusOutput(t *testing.T) {
	started := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	finished := time.Date(2026, 3, 1, 12, 5, 0, 0, time.UTC)
	runs := []store.Run{
		{
			ID:           "abc123",
			SpecPath:     "specs/hello.md",
			Model:        "claude-sonnet-4-6",
			Threshold:    95.0,
			BudgetUSD:    5.0,
			StartedAt:    started,
			FinishedAt:   &finished,
			Satisfaction: 99.0,
			Iterations:   2,
			TotalTokens:  5000,
			TotalCostUSD: 0.15,
			Status:       "converged",
		},
		{
			ID:        "def456",
			SpecPath:  "specs/todo.md",
			Model:     "claude-sonnet-4-6",
			StartedAt: started,
			Status:    "running",
		},
	}

	out := NewStatusOutput(runs)

	if out.Version != 1 {
		t.Errorf("version = %d, want 1", out.Version)
	}
	if len(out.Runs) != 2 {
		t.Fatalf("len(runs) = %d, want 2", len(out.Runs))
	}

	r0 := out.Runs[0]
	if r0.ID != "abc123" {
		t.Errorf("runs[0].id = %q, want %q", r0.ID, "abc123")
	}
	if r0.StartedAt != "2026-03-01T12:00:00Z" {
		t.Errorf("runs[0].started_at = %q, want RFC3339", r0.StartedAt)
	}
	if r0.FinishedAt == nil || *r0.FinishedAt != "2026-03-01T12:05:00Z" {
		t.Errorf("runs[0].finished_at = %v, want RFC3339", r0.FinishedAt)
	}
	if r0.Satisfaction != 99.0 {
		t.Errorf("runs[0].satisfaction = %f, want 99.0", r0.Satisfaction)
	}
	if r0.TotalTokens != 5000 {
		t.Errorf("runs[0].total_tokens = %d, want 5000", r0.TotalTokens)
	}

	r1 := out.Runs[1]
	if r1.FinishedAt != nil {
		t.Errorf("runs[1].finished_at = %v, want nil", r1.FinishedAt)
	}
}

func TestNewStatusOutputEmpty(t *testing.T) {
	out := NewStatusOutput(nil)
	if out.Runs == nil {
		t.Error("runs = nil, want empty slice")
	}
}

func TestWriteJSON(t *testing.T) {
	var buf bytes.Buffer
	data := map[string]int{"version": 1}
	if err := WriteJSON(&buf, data); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got map[string]int
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON output: %v", err)
	}
	if got["version"] != 1 {
		t.Errorf("version = %d, want 1", got["version"])
	}
}
