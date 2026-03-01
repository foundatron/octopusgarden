package store

import "time"

// Run represents a single attractor run persisted in SQLite.
type Run struct {
	ID           string
	SpecPath     string
	Model        string
	Threshold    float64
	BudgetUSD    float64
	StartedAt    time.Time
	FinishedAt   *time.Time
	Satisfaction float64
	Iterations   int
	TotalTokens  int
	TotalCostUSD float64
	Status       string
}

// Iteration represents one iteration within a run.
type Iteration struct {
	ID           int64
	RunID        string
	Iteration    int
	Satisfaction float64
	InputTokens  int
	OutputTokens int
	CostUSD      float64
	Failures     []string
	CreatedAt    time.Time
}
