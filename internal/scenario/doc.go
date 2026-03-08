// Package scenario loads YAML scenario definitions and executes HTTP, gRPC,
// exec, WebSocket, and browser steps against a running container.
//
// LoadDir, LoadFile, and Load parse YAML into Scenario values. Runner executes
// steps sequentially: setup steps are fatal on failure, while scored steps
// continue so that all expectations are evaluated. Variable capture records
// values from step output via JSON path expressions, and substitution injects
// them into subsequent steps. Judge scores each step's observed output against
// its expectation via an LLM, and Aggregate computes the weighted satisfaction
// score across all scenarios.
package scenario
