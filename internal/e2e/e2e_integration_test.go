//go:build integration

package e2e_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	dockerclient "github.com/docker/docker/client"

	"github.com/foundatron/octopusgarden/internal/attractor"
	"github.com/foundatron/octopusgarden/internal/container"
	"github.com/foundatron/octopusgarden/internal/llm"
	"github.com/foundatron/octopusgarden/internal/scenario"
	"github.com/foundatron/octopusgarden/internal/spec"
	"github.com/foundatron/octopusgarden/internal/store"
)

// testSpec is a trivially simple specification designed to converge in 1-2 iterations.
const testSpec = `# Health Check Service

A minimal HTTP health check service.

## Endpoints

### GET /health

Return a JSON health status response.

- Response: 200 OK with body {"status": "ok"}
- Content-Type: application/json

## Requirements

- Listen on port 8080
- Any programming language
`

// testScenario validates that GET /health returns the expected JSON response.
const testScenario = `id: health-check
description: Health endpoint returns ok status
type: functional
satisfaction_criteria: GET /health returns 200 with JSON body containing status "ok"
steps:
  - description: Health endpoint returns ok status
    request:
      method: GET
      path: /health
    expect: "HTTP status 200. Response body is JSON with a 'status' field equal to 'ok'."
`

const (
	// testGenModel uses haiku for generation to minimize API cost.
	testGenModel   = "claude-haiku-4-5"
	testJudgeModel = "claude-haiku-4-5"
	testThreshold  = 80.0
	testMaxIter    = 5
)

// TestE2EAttractorRun exercises the full attractor loop end-to-end:
// spec → LLM generate → Docker build/run → scenario validate → LLM judge.
//
// Requires: ANTHROPIC_API_KEY env var and a reachable Docker daemon.
// Expected duration: 30–90 s depending on build and LLM latency.
func TestE2EAttractorRun(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}

	ctx := context.Background()
	checkDockerAvailable(ctx, t)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Write spec and scenario to temp directories.
	specFile := filepath.Join(t.TempDir(), "spec.md")
	if err := os.WriteFile(specFile, []byte(testSpec), 0o600); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	scenariosDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(scenariosDir, "health.yaml"), []byte(testScenario), 0o600); err != nil {
		t.Fatalf("write scenario: %v", err)
	}

	// Open a temporary SQLite store isolated to this test.
	st, err := store.NewStore(ctx, filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	// Parse spec and load scenarios.
	parsedSpec, err := spec.ParseFile(specFile)
	if err != nil {
		t.Fatalf("parse spec: %v", err)
	}
	scenarios, err := scenario.LoadDir(scenariosDir)
	if err != nil {
		t.Fatalf("load scenarios: %v", err)
	}

	llmClient := llm.NewAnthropicClient(apiKey, logger)

	containerMgr, err := container.NewManager(logger)
	if err != nil {
		t.Skipf("create container manager: %v", err)
	}
	defer func() { _ = containerMgr.Close() }()

	validateFn := makeValidateFn(t, scenarios, llmClient, logger)

	att := attractor.New(llmClient, containerMgr, logger, nil)
	opts := attractor.RunOptions{
		Model:         testGenModel,
		Language:      "go",
		Threshold:     testThreshold,
		MaxIterations: testMaxIter,
		WorkspaceDir:  t.TempDir(),
		Capabilities:  attractor.ScenarioCapabilities{NeedsHTTP: true},
		Progress: func(p attractor.IterationProgress) {
			t.Logf("iter %d/%d outcome=%s satisfaction=%.1f cost=$%.4f",
				p.Iteration, p.MaxIterations, p.Outcome, p.Satisfaction, p.TotalCostUSD)
		},
	}

	startedAt := time.Now()
	result, err := att.Run(ctx, parsedSpec.RawContent, opts, validateFn, nil, nil)
	finishedAt := time.Now()
	if err != nil {
		t.Fatalf("attractor.Run: %v", err)
	}

	t.Logf("result: status=%s iterations=%d satisfaction=%.1f cost=$%.4f outputDir=%s",
		result.Status, result.Iterations, result.Satisfaction, result.CostUSD, result.OutputDir)

	// Validate RunResult fields are populated.
	if result.RunID == "" {
		t.Error("RunResult.RunID is empty")
	}
	if result.Iterations == 0 {
		t.Error("RunResult.Iterations is 0")
	}
	if result.CostUSD <= 0 {
		t.Errorf("RunResult.CostUSD should be positive, got %v", result.CostUSD)
	}

	// Validate convergence.
	if result.Status != attractor.StatusConverged {
		t.Errorf("expected status %q, got %q (satisfaction=%.1f/%.1f after %d iterations)",
			attractor.StatusConverged, result.Status, result.Satisfaction, testThreshold, result.Iterations)
	}
	if result.Satisfaction < testThreshold {
		t.Errorf("satisfaction %.1f below threshold %.1f", result.Satisfaction, testThreshold)
	}

	// Validate output directory contains generated files.
	if result.OutputDir == "" {
		t.Fatal("RunResult.OutputDir is empty")
	}
	entries, err := os.ReadDir(result.OutputDir)
	if err != nil {
		t.Fatalf("read output dir %s: %v", result.OutputDir, err)
	}
	if len(entries) == 0 {
		t.Errorf("output dir %s is empty, expected generated files", result.OutputDir)
	}
	t.Logf("output dir contains %d entries", len(entries))

	// Record the run in SQLite and verify retrieval.
	run := store.Run{
		ID:           result.RunID,
		SpecPath:     specFile,
		Model:        opts.Model,
		Threshold:    opts.Threshold,
		StartedAt:    startedAt,
		FinishedAt:   &finishedAt,
		Satisfaction: result.Satisfaction,
		Iterations:   result.Iterations,
		TotalCostUSD: result.CostUSD,
		Status:       result.Status,
		Language:     opts.Language,
	}
	if err := st.RecordRun(ctx, run); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}

	stored, err := st.GetRun(ctx, result.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if stored.ID != result.RunID {
		t.Errorf("stored.ID %q != result.RunID %q", stored.ID, result.RunID)
	}
	if stored.Status != result.Status {
		t.Errorf("stored.Status %q != result.Status %q", stored.Status, result.Status)
	}
	if stored.Satisfaction != result.Satisfaction {
		t.Errorf("stored.Satisfaction %v != result.Satisfaction %v", stored.Satisfaction, result.Satisfaction)
	}
	if stored.Iterations != result.Iterations {
		t.Errorf("stored.Iterations %d != result.Iterations %d", stored.Iterations, result.Iterations)
	}
}

// checkDockerAvailable skips the test if the Docker daemon is unreachable.
func checkDockerAvailable(ctx context.Context, t *testing.T) {
	t.Helper()
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("Docker client unavailable: %v", err)
	}
	defer func() { _ = cli.Close() }()

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := cli.Ping(pingCtx); err != nil { //nolint:gosec // integration test, pinging local Docker daemon
		t.Skipf("Docker daemon not reachable: %v", err)
	}
}

// makeValidateFn builds an attractor.ValidateFn that runs all scenarios sequentially
// and returns the aggregate satisfaction score.
func makeValidateFn(t *testing.T, scenarios []scenario.Scenario, llmClient llm.Client, logger *slog.Logger) attractor.ValidateFn {
	return func(ctx context.Context, baseURL string, _ attractor.RestartFunc, _ int) (float64, []string, float64, error) {
		httpCli := &http.Client{Timeout: 30 * time.Second}
		executors := map[string]scenario.StepExecutor{
			"request": &scenario.HTTPExecutor{Client: httpCli, BaseURL: baseURL},
			"exec":    &scenario.ExecExecutor{Session: nil},
		}
		judge := scenario.NewJudge(llmClient, testJudgeModel, logger)

		scored := make([]scenario.ScoredScenario, 0, len(scenarios))
		for _, sc := range scenarios {
			runner := scenario.NewRunner(executors, logger)
			result, err := runner.Run(ctx, sc)
			if err != nil {
				t.Logf("scenario %s setup failed: %v", sc.ID, err)
				scored = append(scored, scenario.ScoredScenario{ScenarioID: sc.ID, Weight: 1.0})
				continue
			}
			ss, err := judge.ScoreScenario(ctx, sc, result)
			if err != nil {
				return 0, nil, 0, fmt.Errorf("score scenario %s: %w", sc.ID, err)
			}
			scored = append(scored, ss)
		}

		agg := scenario.Aggregate(scored)
		return agg.Satisfaction, agg.Failures, agg.TotalCostUSD, nil
	}
}
