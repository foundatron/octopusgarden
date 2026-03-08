// Package preflight validates spec and scenario quality via LLM assessment
// before running the attractor loop.
//
// Check evaluates a spec's goal clarity, constraint clarity, and success
// criteria clarity, returning a Result with per-dimension scores, an aggregate,
// and clarifying questions when the spec falls below the configured threshold.
// CheckScenarios evaluates scenario coverage, feasibility, and isolation,
// returning a ScenarioResult with issues grouped by dimension.
package preflight
