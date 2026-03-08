// Package attractor implements the convergence loop that iteratively generates
// code from a specification until holdout scenarios are satisfied.
//
// The loop proceeds as: generate code via LLM → parse file blocks → build a
// Docker image → run the container → validate against scenarios → repeat until
// the aggregate satisfaction score meets the configured threshold. Failed
// iterations produce steering feedback that is appended to the conversation
// before the next generation attempt.
//
// The primary entry point is Attractor.Run, which accepts a ValidateFn
// callback for external validation and returns a RunResult with final
// satisfaction, cost, and output directory.
package attractor
