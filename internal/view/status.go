package view

import (
	"time"

	"github.com/foundatron/octopusgarden/internal/store"
)

// StatusOutput is the JSON-serializable view of the status command.
type StatusOutput struct {
	Version int         `json:"version"`
	Runs    []RunOutput `json:"runs"`
}

// RunOutput is the JSON-serializable view of a single run.
type RunOutput struct {
	ID           string  `json:"id"`
	SpecPath     string  `json:"spec_path"`
	Model        string  `json:"model"`
	Threshold    float64 `json:"threshold"`
	BudgetUSD    float64 `json:"budget_usd"`
	StartedAt    string  `json:"started_at"`
	FinishedAt   *string `json:"finished_at,omitempty"`
	Satisfaction float64 `json:"satisfaction"`
	Iterations   int     `json:"iterations"`
	TotalTokens  int     `json:"total_tokens"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	Status       string  `json:"status"`
	Language     string  `json:"language,omitempty"`
}

// NewStatusOutput converts domain types into the JSON view.
func NewStatusOutput(runs []store.Run) StatusOutput {
	out := make([]RunOutput, 0, len(runs))
	for _, r := range runs {
		ro := RunOutput{
			ID:           r.ID,
			SpecPath:     r.SpecPath,
			Model:        r.Model,
			Threshold:    r.Threshold,
			BudgetUSD:    r.BudgetUSD,
			StartedAt:    r.StartedAt.Format(time.RFC3339),
			Satisfaction: r.Satisfaction,
			Iterations:   r.Iterations,
			TotalTokens:  r.TotalTokens,
			TotalCostUSD: r.TotalCostUSD,
			Status:       r.Status,
			Language:     r.Language,
		}
		if r.FinishedAt != nil {
			s := r.FinishedAt.Format(time.RFC3339)
			ro.FinishedAt = &s
		}
		out = append(out, ro)
	}
	return StatusOutput{
		Version: 1,
		Runs:    out,
	}
}
