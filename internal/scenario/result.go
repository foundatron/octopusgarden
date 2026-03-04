package scenario

import "time"

// StepScore holds the LLM judge's evaluation of a single step.
type StepScore struct {
	Score     int
	Reasoning string
	Failures  []string
	CostUSD   float64
}

// StepResult captures the outcome of executing a single scenario step.
type StepResult struct {
	Description string
	StepType    string // "request", "exec"
	Observed    string // formatted output for the judge
	CaptureBody string // raw body for capture extraction
	Duration    time.Duration
	Err         error // non-nil only for transport/execution failures
}

// Result holds execution results for all judged steps in a scenario.
type Result struct {
	ScenarioID string
	Steps      []StepResult // judged steps only, not setup
}

// ScoredStep pairs a step's execution result with its judge score.
type ScoredStep struct {
	StepResult StepResult
	StepScore  StepScore
}

// ScoredScenario holds the scored results for an entire scenario.
type ScoredScenario struct {
	ScenarioID string
	Weight     float64
	Steps      []ScoredStep
	Score      float64 // average of step scores
}

// AggregateResult holds the weighted aggregate across all scenarios.
type AggregateResult struct {
	Scenarios    []ScoredScenario
	Satisfaction float64 // weighted average, 0-100
	TotalCostUSD float64
	Failures     []string // deduplicated, sorted
}
