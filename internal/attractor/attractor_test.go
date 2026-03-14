package attractor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/foundatron/octopusgarden/internal/container"
	"github.com/foundatron/octopusgarden/internal/llm"
)

// mockLLMClient is a configurable mock for llm.Client.
type mockLLMClient struct {
	generateFn func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error)
}

func (m *mockLLMClient) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	return m.generateFn(ctx, req)
}

func (m *mockLLMClient) Judge(_ context.Context, _ llm.JudgeRequest) (llm.JudgeResponse, error) {
	return llm.JudgeResponse{}, nil
}

// mockContainerMgr is a configurable mock for ContainerManager.
type mockContainerMgr struct {
	buildFn        func(ctx context.Context, dir, tag string) error
	runFn          func(ctx context.Context, tag string) (container.RunResult, container.StopFunc, error)
	runMultiPortFn func(ctx context.Context, tag string, extraPorts []string) (container.RunResult, container.StopFunc, error)
	runTestFn      func(ctx context.Context, containerID, command string) (container.ExecResult, error)
	waitHealthyFn  func(ctx context.Context, url string, timeout time.Duration) error
	waitPortFn     func(ctx context.Context, addr string, timeout time.Duration) error
	startSessionFn func(ctx context.Context, tag string) (*container.Session, container.StopFunc, error)
}

func (m *mockContainerMgr) Build(ctx context.Context, dir, tag string) error {
	if m.buildFn != nil {
		return m.buildFn(ctx, dir, tag)
	}
	return nil
}

func (m *mockContainerMgr) Run(ctx context.Context, tag string) (container.RunResult, container.StopFunc, error) {
	if m.runFn != nil {
		return m.runFn(ctx, tag)
	}
	return container.RunResult{URL: "http://127.0.0.1:9999", ContainerID: "mock-container-id"}, func() {}, nil
}

func (m *mockContainerMgr) RunTest(ctx context.Context, containerID, command string) (container.ExecResult, error) {
	if m.runTestFn != nil {
		return m.runTestFn(ctx, containerID, command)
	}
	return container.ExecResult{ExitCode: 0}, nil
}

func (m *mockContainerMgr) RunMultiPort(ctx context.Context, tag string, extraPorts []string) (container.RunResult, container.StopFunc, error) {
	if m.runMultiPortFn != nil {
		return m.runMultiPortFn(ctx, tag, extraPorts)
	}
	return container.RunResult{URL: "http://127.0.0.1:9999", ExtraPorts: map[string]string{}}, func() {}, nil
}

func (m *mockContainerMgr) WaitHealthy(ctx context.Context, url string, timeout time.Duration) error {
	if m.waitHealthyFn != nil {
		return m.waitHealthyFn(ctx, url, timeout)
	}
	return nil
}

func (m *mockContainerMgr) WaitPort(ctx context.Context, addr string, timeout time.Duration) error {
	if m.waitPortFn != nil {
		return m.waitPortFn(ctx, addr, timeout)
	}
	return nil
}

func (m *mockContainerMgr) StartSession(ctx context.Context, tag string) (*container.Session, container.StopFunc, error) {
	if m.startSessionFn != nil {
		return m.startSessionFn(ctx, tag)
	}
	return nil, func() {}, nil
}

// validLLMOutput returns LLM output that produces parseable files with a Dockerfile.
func validLLMOutput() string {
	return `=== FILE: main.go ===
package main

func main() {}
=== END FILE ===
=== FILE: Dockerfile ===
FROM scratch
=== END FILE ===`
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func defaultOpts(t *testing.T) RunOptions {
	t.Helper()
	return RunOptions{
		WorkspaceDir:  t.TempDir(),
		MaxIterations: 10,
		StallLimit:    3,
		Threshold:     95,
		HealthTimeout: time.Second,
	}
}

func TestConvergesImmediately(t *testing.T) {
	client := &mockLLMClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		return 100, nil, 0.005, nil
	}

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build a hello world app", defaultOpts(t), validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected status %q, got %q", StatusConverged, result.Status)
	}
	if result.Iterations != 1 {
		t.Errorf("expected 1 iteration, got %d", result.Iterations)
	}
}

func TestConvergesOnIteration2(t *testing.T) {
	var callCount atomic.Int32
	client := &mockLLMClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		n := callCount.Add(1)
		if n == 1 {
			return 60, []string{"missing endpoint"}, 0.005, nil
		}
		return 100, nil, 0.005, nil
	}

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", defaultOpts(t), validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected status %q, got %q", StatusConverged, result.Status)
	}
	if result.Iterations != 2 {
		t.Errorf("expected 2 iterations, got %d", result.Iterations)
	}
}

func TestStalls(t *testing.T) {
	client := &mockLLMClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		return 50, []string{"not good enough"}, 0.005, nil
	}

	opts := defaultOpts(t)
	opts.StallLimit = 3

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusStalled {
		t.Errorf("expected status %q, got %q", StatusStalled, result.Status)
	}
	// First iter sets best (50 > 0), resets stall. Then 3 more iters stall. Total = 4.
	if result.Iterations != 4 {
		t.Errorf("expected 4 iterations, got %d", result.Iterations)
	}
}

func TestStallResetsOnImprovement(t *testing.T) {
	scores := []float64{40, 50, 50, 50, 50}
	var callCount atomic.Int32
	client := &mockLLMClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		n := callCount.Add(1)
		idx := int(n) - 1
		if idx >= len(scores) {
			return 50, []string{"stuck"}, 0.005, nil
		}
		return scores[idx], []string{"needs work"}, 0.005, nil
	}

	opts := defaultOpts(t)
	opts.StallLimit = 3

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusStalled {
		t.Errorf("expected status %q, got %q", StatusStalled, result.Status)
	}
	// Iter 1: 40 (new best, stall=0), Iter 2: 50 (new best, stall=0),
	// Iter 3: 50 (stall=1), Iter 4: 50 (stall=2), Iter 5: 50 (stall=3 → stalled)
	if result.Iterations != 5 {
		t.Errorf("expected 5 iterations, got %d", result.Iterations)
	}
}

func TestBudgetExceeded(t *testing.T) {
	client := &mockLLMClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.04}, nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		return 50, []string{"not done"}, 0.02, nil
	}

	opts := defaultOpts(t)
	opts.BudgetUSD = 0.10

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusBudgetExceeded {
		t.Errorf("expected status %q, got %q", StatusBudgetExceeded, result.Status)
	}
}

func TestBuildFailureFeedback(t *testing.T) {
	var buildCount atomic.Int32
	var lastUserMsg string
	client := &mockLLMClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			if len(req.Messages) > 0 {
				lastUserMsg = req.Messages[0].Content
			}
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	mgr := &mockContainerMgr{
		buildFn: func(_ context.Context, _, _ string) error {
			n := buildCount.Add(1)
			if n == 1 {
				return fmt.Errorf("build failed: missing Dockerfile")
			}
			return nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		return 100, nil, 0.005, nil
	}

	a := New(client, mgr, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", defaultOpts(t), validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected status %q, got %q", StatusConverged, result.Status)
	}
	if !strings.Contains(lastUserMsg, "BUILD FAILURE") {
		t.Errorf("expected build failure header in prompt, got: %s", lastUserMsg)
	}
}

func TestHealthCheckFailure(t *testing.T) {
	var healthCount atomic.Int32
	client := &mockLLMClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	mgr := &mockContainerMgr{
		waitHealthyFn: func(_ context.Context, _ string, _ time.Duration) error {
			n := healthCount.Add(1)
			if n == 1 {
				return fmt.Errorf("health check timed out")
			}
			return nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		return 100, nil, 0.005, nil
	}

	a := New(client, mgr, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", defaultOpts(t), validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected status %q, got %q", StatusConverged, result.Status)
	}
}

func TestMaxIterations(t *testing.T) {
	client := &mockLLMClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		return 80, []string{"close but no cigar"}, 0.005, nil
	}

	opts := defaultOpts(t)
	opts.MaxIterations = 2
	opts.StallLimit = 100 // won't stall

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusMaxIterations {
		t.Errorf("expected status %q, got %q", StatusMaxIterations, result.Status)
	}
	if result.Iterations != 2 {
		t.Errorf("expected 2 iterations, got %d", result.Iterations)
	}
}

func TestEmptySpec(t *testing.T) {
	client := &mockLLMClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{}, nil
		},
	}

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	_, err := a.Run(context.Background(), "", defaultOpts(t), nil, nil, nil)
	if !errors.Is(err, errEmptySpec) {
		t.Fatalf("expected errEmptySpec, got %v", err)
	}

	// Also test whitespace-only spec.
	_, err = a.Run(context.Background(), "   \n\t  ", defaultOpts(t), nil, nil, nil)
	if !errors.Is(err, errEmptySpec) {
		t.Fatalf("expected errEmptySpec for whitespace spec, got %v", err)
	}
}

func TestUnsupportedLanguage(t *testing.T) {
	client := &mockLLMClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{}, nil
		},
	}

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	opts := defaultOpts(t)
	opts.Language = "cobol"
	_, err := a.Run(context.Background(), "some spec", opts, nil, nil, nil)
	if !errors.Is(err, errUnsupportedLanguage) {
		t.Fatalf("expected errUnsupportedLanguage, got %v", err)
	}

	// Valid language should not error on this check.
	opts.Language = "python"
	_, err = a.Run(context.Background(), "some spec", opts, nil, nil, nil)
	if errors.Is(err, errUnsupportedLanguage) {
		t.Fatal("valid language should not return errUnsupportedLanguage")
	}
}

func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	client := &mockLLMClient{
		generateFn: func(ctx context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{}, ctx.Err()
		},
	}

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	_, err := a.Run(ctx, "Build an app", defaultOpts(t), nil, nil, nil)
	if err == nil {
		t.Fatal("expected error from canceled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestCacheControlSet(t *testing.T) {
	var capturedReq llm.GenerateRequest
	client := &mockLLMClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			capturedReq = req
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		return 100, nil, 0.005, nil
	}

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	_, err := a.Run(context.Background(), "Build an app", defaultOpts(t), validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedReq.CacheControl == nil {
		t.Fatal("expected CacheControl to be set")
	}
	if capturedReq.CacheControl.Type != "ephemeral" {
		t.Errorf("expected CacheControl.Type = ephemeral, got %q", capturedReq.CacheControl.Type)
	}
}

func TestCheckpointWritten(t *testing.T) {
	scores := []float64{60, 80, 100}
	var callCount atomic.Int32
	client := &mockLLMClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		n := callCount.Add(1)
		return scores[int(n)-1], nil, 0.005, nil
	}

	opts := defaultOpts(t)
	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected converged, got %q", result.Status)
	}

	// Verify best/ directory has files.
	bestMain := filepath.Join(result.OutputDir, "main.go")
	if _, err := os.Stat(bestMain); os.IsNotExist(err) {
		t.Error("expected best/main.go to exist")
	}
}

func TestContainerRunFailure(t *testing.T) {
	var runCount atomic.Int32
	client := &mockLLMClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	mgr := &mockContainerMgr{
		runFn: func(_ context.Context, _ string) (container.RunResult, container.StopFunc, error) {
			n := runCount.Add(1)
			if n == 1 {
				return container.RunResult{}, nil, fmt.Errorf("port conflict")
			}
			return container.RunResult{URL: "http://127.0.0.1:9999", ContainerID: "mock-container-id"}, func() {}, nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		return 100, nil, 0.005, nil
	}

	a := New(client, mgr, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", defaultOpts(t), validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected status %q, got %q", StatusConverged, result.Status)
	}
}

func TestNeedsBrowserTriggersHTTPContainer(t *testing.T) {
	var runCalled, waitHealthyCalled bool
	client := &mockLLMClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	mgr := &mockContainerMgr{
		runFn: func(_ context.Context, _ string) (container.RunResult, container.StopFunc, error) {
			runCalled = true
			return container.RunResult{URL: "http://127.0.0.1:9999", ContainerID: "mock-container-id"}, func() {}, nil
		},
		waitHealthyFn: func(_ context.Context, _ string, _ time.Duration) error {
			waitHealthyCalled = true
			return nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		return 100, nil, 0.005, nil
	}

	opts := defaultOpts(t)
	opts.Capabilities = ScenarioCapabilities{NeedsBrowser: true}

	a := New(client, mgr, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build a web app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected converged, got %q", result.Status)
	}
	if !runCalled {
		t.Error("expected container Run to be called when NeedsBrowser is true")
	}
	if !waitHealthyCalled {
		t.Error("expected WaitHealthy to be called when NeedsBrowser is true")
	}
}

func TestProgressCallback(t *testing.T) {
	scores := []float64{60, 80, 100}
	var callCount atomic.Int32
	client := &mockLLMClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		n := callCount.Add(1)
		return scores[int(n)-1], []string{"needs work"}, 0.005, nil
	}

	var progress []IterationProgress
	opts := defaultOpts(t)
	opts.Progress = func(p IterationProgress) {
		progress = append(progress, p)
	}

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Fatalf("expected converged, got %q", result.Status)
	}

	// Callback should fire once per iteration.
	if len(progress) != 3 {
		t.Fatalf("expected 3 progress callbacks, got %d", len(progress))
	}

	// Verify iteration numbers increment.
	for i, p := range progress {
		if p.Iteration != i+1 {
			t.Errorf("progress[%d].Iteration = %d, want %d", i, p.Iteration, i+1)
		}
		if p.MaxIterations != opts.MaxIterations {
			t.Errorf("progress[%d].MaxIterations = %d, want %d", i, p.MaxIterations, opts.MaxIterations)
		}
		if p.Outcome != OutcomeValidated {
			t.Errorf("progress[%d].Outcome = %q, want %q", i, p.Outcome, OutcomeValidated)
		}
		if p.Satisfaction != scores[i] {
			t.Errorf("progress[%d].Satisfaction = %.1f, want %.1f", i, p.Satisfaction, scores[i])
		}
		if p.Elapsed < 0 {
			t.Errorf("progress[%d].Elapsed = %v, want non-negative", i, p.Elapsed)
		}
	}

	// TotalCostUSD should increase monotonically.
	for i := 1; i < len(progress); i++ {
		if progress[i].TotalCostUSD <= progress[i-1].TotalCostUSD {
			t.Errorf("TotalCostUSD not increasing: progress[%d]=%.4f, progress[%d]=%.4f",
				i-1, progress[i-1].TotalCostUSD, i, progress[i].TotalCostUSD)
		}
	}

	// Verify trend progression: first is plateau (only 1 score), then improving, then converged.
	expectedTrends := []Trend{TrendPlateau, TrendImproving, TrendConverged}
	for i, p := range progress {
		if p.Trend != expectedTrends[i] {
			t.Errorf("progress[%d].Trend = %q, want %q", i, p.Trend, expectedTrends[i])
		}
	}
}

func TestProgressCallbackBuildFailure(t *testing.T) {
	var buildCount atomic.Int32
	client := &mockLLMClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	mgr := &mockContainerMgr{
		buildFn: func(_ context.Context, _, _ string) error {
			n := buildCount.Add(1)
			if n == 1 {
				return fmt.Errorf("build failed")
			}
			return nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		return 100, nil, 0.005, nil
	}

	var progress []IterationProgress
	opts := defaultOpts(t)
	opts.Progress = func(p IterationProgress) {
		progress = append(progress, p)
	}

	a := New(client, mgr, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Fatalf("expected converged, got %q", result.Status)
	}

	if len(progress) != 2 {
		t.Fatalf("expected 2 progress callbacks, got %d", len(progress))
	}

	// First iteration should be build_fail with zero satisfaction.
	if progress[0].Outcome != OutcomeBuildFail {
		t.Errorf("progress[0].Outcome = %q, want %q", progress[0].Outcome, OutcomeBuildFail)
	}
	if progress[0].Satisfaction != 0 {
		t.Errorf("progress[0].Satisfaction = %.1f, want 0", progress[0].Satisfaction)
	}

	// Second should be validated.
	if progress[1].Outcome != OutcomeValidated {
		t.Errorf("progress[1].Outcome = %q, want %q", progress[1].Outcome, OutcomeValidated)
	}
}

func patchOpts(t *testing.T) RunOptions {
	t.Helper()
	opts := defaultOpts(t)
	opts.PatchMode = true
	return opts
}

// isPatchMessage checks if a user message looks like a patch mode message.
func isPatchMessage(msg string) bool {
	return strings.Contains(msg, "current best version scored")
}

func TestPatchModeUsedOnIteration2(t *testing.T) {
	var capturedMessages []string
	client := &mockLLMClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			if len(req.Messages) > 0 {
				capturedMessages = append(capturedMessages, req.Messages[0].Content)
			}
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	var callCount atomic.Int32
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		n := callCount.Add(1)
		if n == 1 {
			return 60, []string{"missing endpoint"}, 0.005, nil
		}
		return 100, nil, 0.005, nil
	}

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", patchOpts(t), validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected status %q, got %q", StatusConverged, result.Status)
	}
	if result.Iterations != 2 {
		t.Errorf("expected 2 iterations, got %d", result.Iterations)
	}
	if len(capturedMessages) < 2 {
		t.Fatalf("expected at least 2 captured messages, got %d", len(capturedMessages))
	}
	// Iteration 1 should use full generation.
	if isPatchMessage(capturedMessages[0]) {
		t.Error("iteration 1 should not use patch mode message")
	}
	// Iteration 2 should use patch mode (contains best files).
	if !isPatchMessage(capturedMessages[1]) {
		t.Error("iteration 2 should use patch mode message")
	}
	// Patch message should contain the files from iteration 1.
	if !strings.Contains(capturedMessages[1], "package main") {
		t.Error("patch message should contain previous best files content")
	}
}

func TestPatchModeFallbackOnRegressions(t *testing.T) {
	scores := []float64{70, 65, 60, 100}
	var callCount atomic.Int32
	var capturedMessages []string
	client := &mockLLMClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			if len(req.Messages) > 0 {
				capturedMessages = append(capturedMessages, req.Messages[0].Content)
			}
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		n := callCount.Add(1)
		idx := int(n) - 1
		if idx >= len(scores) {
			return 100, nil, 0.005, nil
		}
		return scores[idx], []string{"needs work"}, 0.005, nil
	}

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", patchOpts(t), validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected converged, got %q", result.Status)
	}
	if len(capturedMessages) < 4 {
		t.Fatalf("expected at least 4 messages, got %d", len(capturedMessages))
	}
	// Iter 1: full gen. Iter 2: patch (score 65 < 70, regression 1).
	// Iter 3: patch (score 60 < 70, regression 2 → disable). Iter 4: full gen.
	if isPatchMessage(capturedMessages[0]) {
		t.Error("iteration 1 should use full gen")
	}
	if !isPatchMessage(capturedMessages[1]) {
		t.Error("iteration 2 should use patch mode")
	}
	if !isPatchMessage(capturedMessages[2]) {
		t.Error("iteration 3 should still use patch mode (disabled after this iteration)")
	}
	if isPatchMessage(capturedMessages[3]) {
		t.Error("iteration 4 should use full gen after patch mode disabled")
	}
}

func TestPatchModeRegressionResets(t *testing.T) {
	scores := []float64{70, 65, 80, 75, 100}
	var callCount atomic.Int32
	var capturedMessages []string
	client := &mockLLMClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			if len(req.Messages) > 0 {
				capturedMessages = append(capturedMessages, req.Messages[0].Content)
			}
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		n := callCount.Add(1)
		idx := int(n) - 1
		if idx >= len(scores) {
			return 100, nil, 0.005, nil
		}
		return scores[idx], []string{"needs work"}, 0.005, nil
	}

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", patchOpts(t), validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected converged, got %q", result.Status)
	}
	// Iter 1: 70 (full gen). Iter 2: 65 (patch, regression 1).
	// Iter 3: 80 (patch, improvement → reset regression to 0).
	// Iter 4: 75 (patch, regression 1 — not 2, so patch stays active).
	// Iter 5: 100 (patch, converge).
	// All iterations 2-5 should use patch mode since regression never hit 2.
	for i := 1; i < len(capturedMessages); i++ {
		if !isPatchMessage(capturedMessages[i]) {
			t.Errorf("iteration %d should use patch mode", i+1)
		}
	}
}

func TestPatchModeDisabledByDefault(t *testing.T) {
	var capturedMessages []string
	client := &mockLLMClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			if len(req.Messages) > 0 {
				capturedMessages = append(capturedMessages, req.Messages[0].Content)
			}
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	var callCount atomic.Int32
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		n := callCount.Add(1)
		if n == 1 {
			return 60, []string{"needs work"}, 0.005, nil
		}
		return 100, nil, 0.005, nil
	}

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", defaultOpts(t), validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected converged, got %q", result.Status)
	}
	// Without PatchMode, iteration 2 should NOT use patch message.
	for i, msg := range capturedMessages {
		if isPatchMessage(msg) {
			t.Errorf("iteration %d should not use patch mode when PatchMode is false", i+1)
		}
	}
}

func TestPatchModeNotActiveWithoutBestFiles(t *testing.T) {
	var buildCount atomic.Int32
	var capturedMessages []string
	client := &mockLLMClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			if len(req.Messages) > 0 {
				capturedMessages = append(capturedMessages, req.Messages[0].Content)
			}
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	mgr := &mockContainerMgr{
		buildFn: func(_ context.Context, _, _ string) error {
			n := buildCount.Add(1)
			if n == 1 {
				return fmt.Errorf("build failed")
			}
			return nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		return 100, nil, 0.005, nil
	}

	a := New(client, mgr, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", patchOpts(t), validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected converged, got %q", result.Status)
	}
	if len(capturedMessages) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(capturedMessages))
	}
	// Iteration 1 build failed → no bestFiles. Iteration 2 should use full gen.
	if isPatchMessage(capturedMessages[1]) {
		t.Error("iteration 2 should use full gen when bestFiles is nil (iter 1 build failed)")
	}
}

func TestPatchModeMergesFiles(t *testing.T) {
	// Iteration 1 produces 2 files. Iteration 2 produces only 1 changed file.
	// The merged result should contain both files.
	twoFileOutput := `=== FILE: main.go ===
package main

func main() { serve() }
=== END FILE ===
=== FILE: Dockerfile ===
FROM golang:1.22
=== END FILE ===`

	oneFileOutput := `=== FILE: main.go ===
package main

func main() { serveFixed() }
=== END FILE ===`

	var callCount atomic.Int32
	client := &mockLLMClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			n := callCount.Add(1)
			if n == 1 {
				return llm.GenerateResponse{Content: twoFileOutput, CostUSD: 0.01}, nil
			}
			return llm.GenerateResponse{Content: oneFileOutput, CostUSD: 0.01}, nil
		},
	}

	var builtDir string
	mgr := &mockContainerMgr{
		buildFn: func(_ context.Context, dir, _ string) error {
			builtDir = dir
			return nil
		},
	}

	var valCount atomic.Int32
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		n := valCount.Add(1)
		if n == 1 {
			return 60, []string{"needs fix"}, 0.005, nil
		}
		return 100, nil, 0.005, nil
	}

	a := New(client, mgr, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", patchOpts(t), validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Fatalf("expected converged, got %q", result.Status)
	}

	// The directory built for iteration 2 should contain both files:
	// main.go (updated) and Dockerfile (carried forward).
	mainContent, err := os.ReadFile(filepath.Join(builtDir, "main.go"))
	if err != nil {
		t.Fatalf("failed to read main.go from build dir: %v", err)
	}
	if !strings.Contains(string(mainContent), "serveFixed") {
		t.Error("main.go should contain updated content from iteration 2")
	}

	dockerContent, err := os.ReadFile(filepath.Join(builtDir, "Dockerfile"))
	if err != nil {
		t.Fatalf("failed to read Dockerfile from build dir: %v", err)
	}
	if !strings.Contains(string(dockerContent), "golang:1.22") {
		t.Error("Dockerfile should be carried forward from iteration 1")
	}
}

func TestValidateError(t *testing.T) {
	client := &mockLLMClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		return 0, nil, 0, fmt.Errorf("judge unavailable")
	}

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	_, err := a.Run(context.Background(), "Build an app", defaultOpts(t), validate, nil, nil)
	if err == nil {
		t.Fatal("expected error from validate failure")
	}
	if !strings.Contains(err.Error(), "judge unavailable") {
		t.Errorf("expected validate error in message, got: %v", err)
	}
}

func TestContextBudgetZeroPreservesBehavior(t *testing.T) {
	var generateCalls int
	client := &mockLLMClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			generateCalls++
			// Summarize calls have a different system prompt; generation calls contain the spec.
			if strings.Contains(req.SystemPrompt, "technical writer") {
				t.Error("summarize should not be called when ContextBudget is 0")
			}
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		return 100, nil, 0.005, nil
	}

	opts := defaultOpts(t)
	opts.ContextBudget = 0 // explicitly zero

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), strings.Repeat("x", 40000), opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected converged, got %q", result.Status)
	}
	// Only 1 generate call (no summarize call).
	if generateCalls != 1 {
		t.Errorf("expected 1 generate call, got %d", generateCalls)
	}
}

func TestContextBudgetTriggersSummarization(t *testing.T) {
	var summarizeCalled bool
	var capturedSystemPrompts []string
	var callCount atomic.Int32
	client := &mockLLMClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			if strings.Contains(req.SystemPrompt, "technical writer") {
				summarizeCalled = true
				return llm.GenerateResponse{
					Content: `=== SECTION SUMMARIES ===
### Title
Summary of the spec.

=== OUTLINE ===
- Title: A spec

=== ABSTRACT ===
Brief abstract of the spec.`,
					CostUSD: 0.001,
				}, nil
			}
			capturedSystemPrompts = append(capturedSystemPrompts, req.SystemPrompt)
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		n := callCount.Add(1)
		if n == 1 {
			return 60, []string{"missing endpoint"}, 0.005, nil
		}
		return 100, nil, 0.005, nil
	}

	// Create a spec large enough to exceed the context budget.
	largeSpec := "# Title\n\n" + strings.Repeat("x", 4000) // ~1000 tokens
	opts := defaultOpts(t)
	opts.ContextBudget = 500 // budget is less than spec tokens

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), largeSpec, opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected converged, got %q", result.Status)
	}
	if !summarizeCalled {
		t.Error("expected summarize to be called for large spec with context budget")
	}
	if len(capturedSystemPrompts) < 2 {
		t.Fatalf("expected at least 2 system prompts, got %d", len(capturedSystemPrompts))
	}
	// Both iterations should use summarized content (not full spec) since budget is exceeded.
	for i, prompt := range capturedSystemPrompts {
		if strings.Contains(prompt, strings.Repeat("x", 4000)) {
			t.Errorf("iteration %d should use summarized spec, not full content", i+1)
		}
	}
	// Summarization cost should be tracked in the total.
	if result.CostUSD < 0.001 {
		t.Errorf("expected summarization cost to be tracked, total cost: %.4f", result.CostUSD)
	}
}

func TestContextBudgetSummarizeFailureNonFatal(t *testing.T) {
	var generateCalls int
	client := &mockLLMClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			generateCalls++
			// Fail the summarize call.
			if strings.Contains(req.SystemPrompt, "technical writer") {
				return llm.GenerateResponse{}, fmt.Errorf("summarize unavailable")
			}
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		return 100, nil, 0.005, nil
	}

	largeSpec := "# Title\n\nSome content.\n\n## Section\n\n" + strings.Repeat("y", 4000)
	opts := defaultOpts(t)
	opts.ContextBudget = 500

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), largeSpec, opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected converged, got %q", result.Status)
	}
	// Should have 2 calls: 1 failed summarize + 1 generation.
	if generateCalls != 2 {
		t.Errorf("expected 2 generate calls (1 failed summarize + 1 gen), got %d", generateCalls)
	}
}

func TestAttractorRunWithGenes(t *testing.T) {
	geneContent := "// Always use the repository pattern\nfunc NewRepo() *Repo { ... }"

	var capturedSystemPrompt string
	client := &mockLLMClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			capturedSystemPrompt = req.SystemPrompt
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		return 100, nil, 0.005, nil
	}

	opts := defaultOpts(t)
	opts.Genes = geneContent
	opts.GeneLanguage = "go"

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected converged, got %q", result.Status)
	}
	if !strings.Contains(capturedSystemPrompt, geneContent) {
		t.Error("system prompt should contain gene content")
	}
	if !strings.Contains(capturedSystemPrompt, "PROVEN PATTERNS") {
		t.Error("system prompt should contain gene section header")
	}
}

func TestAttractorRunGenesInSystemPrompt(t *testing.T) {
	geneContent := "GENE_SENTINEL_MARKER"

	var capturedReq llm.GenerateRequest
	client := &mockLLMClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			capturedReq = req
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		return 100, nil, 0.005, nil
	}

	opts := defaultOpts(t)
	opts.Genes = geneContent

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	_, err := a.Run(context.Background(), "Build an app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Genes should be in system prompt (benefiting from cache control), not user messages.
	if !strings.Contains(capturedReq.SystemPrompt, geneContent) {
		t.Error("genes should be in system prompt")
	}
	for _, msg := range capturedReq.Messages {
		if strings.Contains(msg.Content, geneContent) {
			t.Error("genes should not be in user messages")
		}
	}
}

func TestAttractorRunGenesPersistAcrossIterations(t *testing.T) {
	geneContent := "PERSISTENT_GENE_PATTERN"

	var capturedPrompts []string
	client := &mockLLMClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			capturedPrompts = append(capturedPrompts, req.SystemPrompt)
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	var callCount atomic.Int32
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		n := callCount.Add(1)
		if n < 3 {
			return 60, []string{"needs work"}, 0.005, nil
		}
		return 100, nil, 0.005, nil
	}

	opts := defaultOpts(t)
	opts.Genes = geneContent

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected converged, got %q", result.Status)
	}
	if len(capturedPrompts) < 3 {
		t.Fatalf("expected at least 3 iterations, got %d", len(capturedPrompts))
	}
	for i, prompt := range capturedPrompts {
		if !strings.Contains(prompt, geneContent) {
			t.Errorf("iteration %d system prompt should contain gene content", i+1)
		}
	}
}

func TestAttractorCrossLanguagePrompt(t *testing.T) {
	geneContent := "// Always use handler-per-route\nfunc handleGetItems(w http.ResponseWriter, r *http.Request) { ... }"

	var capturedSystemPrompt string
	client := &mockLLMClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			capturedSystemPrompt = req.SystemPrompt
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		return 100, nil, 0.005, nil
	}

	opts := defaultOpts(t)
	opts.Genes = geneContent
	opts.GeneLanguage = "go"
	opts.Language = "python"

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected converged, got %q", result.Status)
	}
	if !strings.Contains(capturedSystemPrompt, "CROSS-LANGUAGE NOTE") {
		t.Error("system prompt should contain CROSS-LANGUAGE NOTE")
	}
	if !strings.Contains(capturedSystemPrompt, "Go") {
		t.Error("cross-language note should mention source language Go")
	}
	if !strings.Contains(capturedSystemPrompt, "Python") {
		t.Error("cross-language note should mention target language Python")
	}
	if !strings.Contains(capturedSystemPrompt, geneContent) {
		t.Error("system prompt should contain gene content")
	}
}

func TestExtractFailureStrings(t *testing.T) {
	history := []iterationFeedback{
		{iteration: 1, kind: feedbackValidation, message: "Satisfaction score: 60.0/100\nFailures:\n- missing endpoint"},
		{iteration: 2, kind: feedbackBuildError, message: "Docker build failed: syntax error"},
		{iteration: 3, kind: feedbackValidation, message: ""},
	}

	failures := extractFailureStrings(history)
	if len(failures) != 2 {
		t.Fatalf("expected 2 non-empty failure strings, got %d", len(failures))
	}
	if !strings.Contains(failures[0], "missing endpoint") {
		t.Errorf("expected first failure to mention missing endpoint, got %q", failures[0])
	}
	if !strings.Contains(failures[1], "syntax error") {
		t.Errorf("expected second failure to mention syntax error, got %q", failures[1])
	}
}

// TestStallNoticeAppearsInGenerateByIteration3 exercises the full cross-file wiring:
// processValidation → failedScenarios → buildSteeringText → wonderReflect trigger → Generate.
// When the same scenario fails in 2+ consecutive validation iterations, wonder/reflect fires.
// On wonder parse failure (mock returns file output, not JSON), it falls back to normal generation
// which injects the STALL NOTICE.
func TestStallNoticeAppearsInGenerateByIteration3(t *testing.T) {
	var capturedMsgs [][]llm.Message
	client := &mockLLMClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			capturedMsgs = append(capturedMsgs, req.Messages)
			// All calls return validLLMOutput — the wonder call will fail to parse JSON
			// and fall back to normal generation.
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}

	// ValidateFn always returns the same failing scenario in the format parsed by parseFailedScenarios.
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		return 45, []string{"✗ stall-scenario (45/100)"}, 0.005, nil
	}

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", defaultOpts(t), validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusStalled {
		t.Errorf("expected stalled status, got %q", result.Status)
	}

	// Iteration 1 sets best (45 > 0, stallCount=0); iterations 2, 3, 4 stall (stallCount 1, 2, 3).
	// Starting at iteration 3, wonder fires then falls back — so iters 3 and 4 each have 2 calls.
	// Total calls: 1 + 1 + 2 + 2 = 6 (or more).
	if len(capturedMsgs) < 4 {
		t.Fatalf("expected at least 4 Generate calls, got %d", len(capturedMsgs))
	}

	// msgsContain returns true if any message in the slice contains the substring.
	msgsContain := func(msgs []llm.Message, sub string) bool {
		for _, m := range msgs {
			if strings.Contains(m.Content, sub) {
				return true
			}
		}
		return false
	}

	// Iteration 1 (index 0): empty history → simple "Generate" prompt, no STALL NOTICE.
	if msgsContain(capturedMsgs[0], "STALL NOTICE") {
		t.Error("iteration 1 Generate call should not contain STALL NOTICE")
	}

	// Iteration 2 (index 1): only 1 consecutive validation failure → no wonder, no STALL NOTICE.
	if msgsContain(capturedMsgs[1], "STALL NOTICE") {
		t.Error("iteration 2 Generate call should not contain STALL NOTICE")
	}

	// Iteration 3 wonder call (index 2): wonder prompt — contains stall-scenario info but
	// not as a "STALL NOTICE" header; the wonder prompt uses a different diagnostic format.
	if !msgsContain(capturedMsgs[2], "stall-scenario") {
		t.Errorf("iteration 3 wonder call should mention stall-scenario in failure history, got: %v", capturedMsgs[2])
	}

	// Iteration 3 fallback normal gen (index 3): wonder JSON parse failed → fell back to
	// normal generation which injects STALL NOTICE into the user message.
	if !msgsContain(capturedMsgs[3], "STALL NOTICE") {
		t.Errorf("iteration 3 fallback Generate call should contain STALL NOTICE, got: %v", capturedMsgs[3])
	}
	if !msgsContain(capturedMsgs[3], "stall-scenario") {
		t.Errorf("iteration 3 fallback Generate call should mention stall-scenario, got: %v", capturedMsgs[3])
	}
}

func TestMinimalismPromptAppearsAbove80(t *testing.T) {
	tests := []struct {
		name     string
		score    float64
		wantMini bool
	}{
		{"below threshold", 70, false},
		{"at threshold boundary", 80, false},
		{"above threshold", 81, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var reqs []llm.GenerateRequest
			client := &mockLLMClient{
				generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
					reqs = append(reqs, req)
					return llm.GenerateResponse{Content: validLLMOutput()}, nil
				},
			}
			failures := []string{FormatScenarioFailureLine("test-scenario", 50)}
			var validateCount atomic.Int32
			validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
				n := validateCount.Add(1)
				if n == 1 {
					return tt.score, failures, 0, nil
				}
				return 100, nil, 0, nil
			}
			a := New(client, &mockContainerMgr{}, testLogger(), nil)
			_, err := a.Run(context.Background(), "spec", defaultOpts(t), validate, nil, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(reqs) < 2 {
				t.Fatalf("expected at least 2 generate calls, got %d", len(reqs))
			}
			var userContent string
			for _, msg := range reqs[1].Messages {
				if msg.Role == "user" {
					userContent += msg.Content
				}
			}
			hasMini := strings.Contains(userContent, "SMALLEST")
			if hasMini != tt.wantMini {
				t.Errorf("score %.0f: SMALLEST in prompt = %v, want %v\ncontent: %s", tt.score, hasMini, tt.wantMini, userContent)
			}
		})
	}
}

func TestMinimalismPromptIncludesFailingScenarios(t *testing.T) {
	var reqs []llm.GenerateRequest
	client := &mockLLMClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			reqs = append(reqs, req)
			return llm.GenerateResponse{Content: validLLMOutput()}, nil
		},
	}
	failures := []string{
		FormatScenarioFailureLine("create-user", 40),
		FormatScenarioFailureLine("list-items", 55),
	}
	var validateCount atomic.Int32
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		n := validateCount.Add(1)
		if n == 1 {
			return 85, failures, 0, nil
		}
		return 100, nil, 0, nil
	}
	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	_, err := a.Run(context.Background(), "spec", defaultOpts(t), validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(reqs) < 2 {
		t.Fatalf("expected at least 2 generate calls, got %d", len(reqs))
	}
	var userContent string
	for _, msg := range reqs[1].Messages {
		if msg.Role == "user" {
			userContent += msg.Content
		}
	}
	for _, name := range []string{"create-user", "list-items"} {
		if !strings.Contains(userContent, name) {
			t.Errorf("expected prompt to contain scenario name %q:\n%s", name, userContent)
		}
	}
}

func TestMinimalismPromptProgression(t *testing.T) {
	scores := []float64{60, 85}
	// Use DIFFERENT failing scenarios on each iteration to avoid triggering a stall streak.
	// If the same scenario failed 2+ consecutive iterations, wonder/reflect would fire instead
	// of the normal generation path with minimalism suffix. Distinct scenarios prevent the streak.
	scenarioFailures := [][]string{
		{FormatScenarioFailureLine("alpha", 50)},
		{FormatScenarioFailureLine("beta", 70)}, // different scenario — no consecutive streak
	}
	var reqs []llm.GenerateRequest
	client := &mockLLMClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			reqs = append(reqs, req)
			return llm.GenerateResponse{Content: validLLMOutput()}, nil
		},
	}
	var validateCount atomic.Int32
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		n := int(validateCount.Add(1)) - 1
		if n < len(scores) {
			return scores[n], scenarioFailures[n], 0, nil
		}
		return 100, nil, 0, nil
	}
	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	_, err := a.Run(context.Background(), "spec", defaultOpts(t), validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(reqs) < 3 {
		t.Fatalf("expected at least 3 generate calls, got %d", len(reqs))
	}

	// Iterations 1 and 2 should not contain minimalism (scores 0 and 60 are both ≤ 80).
	for i, req := range reqs[:2] {
		for _, msg := range req.Messages {
			if msg.Role == "user" && strings.Contains(msg.Content, "SMALLEST") {
				t.Errorf("req %d should not contain minimalism prompt, but found SMALLEST", i+1)
			}
		}
	}

	// Iteration 3 should contain minimalism (previous score 85 > 80).
	var thirdContent string
	for _, msg := range reqs[2].Messages {
		if msg.Role == "user" {
			thirdContent += msg.Content
		}
	}
	if !strings.Contains(thirdContent, "SMALLEST") {
		t.Errorf("req 3 should contain minimalism prompt (score 85 > 80), got:\n%s", thirdContent)
	}
}

// TestRegressionFeedbackInjected verifies that when a scenario passes in one iteration
// and then fails in the next, a REGRESSIONS feedback entry is injected into the messages
// for the following iteration.
func TestRegressionFeedbackInjected(t *testing.T) {
	// Iter 1: aggregate below threshold so run continues; scenario-a scores above threshold.
	// Iter 2: scenario-a drops below threshold → regression detected.
	// Iter 3: Generate call should contain "REGRESSIONS" in messages.
	iter1Failures := []string{"✓ scenario-a (98/100)"}
	iter2Failures := []string{"✗ scenario-a (45/100)"}

	// capturedMsgs is appended from generateFn without synchronization.
	// This is safe because the attractor loop calls Generate sequentially (never concurrently).
	var capturedMsgs [][]llm.Message
	var validateCount atomic.Int32
	client := &mockLLMClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			capturedMsgs = append(capturedMsgs, req.Messages)
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		n := validateCount.Add(1)
		switch n {
		case 1:
			// aggregate 60 < threshold 95 — does not converge; scenario-a is at 98.
			return 60, iter1Failures, 0.005, nil
		case 2:
			// scenario-a dropped from 98 to 45 → regression.
			return 50, iter2Failures, 0.005, nil
		default:
			return 100, nil, 0.005, nil
		}
	}

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", defaultOpts(t), validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected converged, got %q", result.Status)
	}
	if len(capturedMsgs) < 3 {
		t.Fatalf("expected at least 3 Generate calls, got %d", len(capturedMsgs))
	}

	// Iter 3 messages should contain REGRESSIONS feedback.
	msgsContain := func(msgs []llm.Message, sub string) bool {
		for _, m := range msgs {
			if strings.Contains(m.Content, sub) {
				return true
			}
		}
		return false
	}
	if !msgsContain(capturedMsgs[2], "REGRESSIONS") {
		t.Errorf("iteration 3 Generate call should contain REGRESSIONS feedback, got: %v", capturedMsgs[2])
	}
	if !msgsContain(capturedMsgs[2], "scenario-a") {
		t.Errorf("iteration 3 Generate call should mention the regressed scenario, got: %v", capturedMsgs[2])
	}
}

// TestOscillationSteeringInjected verifies that oscillation steering is injected into the
// Generate call once an A→B→A→B pattern is detected in the code hashes.
func TestOscillationSteeringInjected(t *testing.T) {
	// Two distinct valid LLM outputs that produce different file hashes.
	outputA := `=== FILE: main.go ===
package main

func main() { /* version A */ }
=== END FILE ===
=== FILE: Dockerfile ===
FROM scratch
CMD ["./app"]
=== END FILE ===`

	outputB := `=== FILE: main.go ===
package main

func main() { /* version B */ }
=== END FILE ===
=== FILE: Dockerfile ===
FROM scratch
CMD ["./server"]
=== END FILE ===`

	var callCount atomic.Int32
	var mu sync.Mutex
	var capturedMsgs [][]llm.Message
	client := &mockLLMClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			mu.Lock()
			capturedMsgs = append(capturedMsgs, req.Messages)
			mu.Unlock()
			n := callCount.Add(1)
			if n%2 == 1 {
				return llm.GenerateResponse{Content: outputA, CostUSD: 0.01}, nil
			}
			return llm.GenerateResponse{Content: outputB, CostUSD: 0.01}, nil
		},
	}

	// Validation always fails — drives stall without converging.
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		return 30, []string{"not good enough"}, 0.005, nil
	}

	opts := defaultOpts(t)
	// StallLimit=4: iter 1 sets best (stallCount=0); iters 2,3,4,5 stall (stallCount 1,2,3,4).
	// This produces exactly 5 Generate calls, giving oscillation detection a chance to fire on iter 5.
	opts.StallLimit = 4

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusStalled {
		t.Errorf("expected stalled status, got %q", result.Status)
	}

	if len(capturedMsgs) < 5 {
		t.Fatalf("expected at least 5 Generate calls, got %d", len(capturedMsgs))
	}

	msgsContain := func(msgs []llm.Message, sub string) bool {
		for _, m := range msgs {
			if strings.Contains(m.Content, sub) {
				return true
			}
		}
		return false
	}

	// Iterations 1–4 (indices 0–3): codeHashes has fewer than 4 ABAB entries → no oscillation.
	for i := range 4 {
		if msgsContain(capturedMsgs[i], "OSCILLATION DETECTED") {
			t.Errorf("iteration %d (index %d) should not contain OSCILLATION DETECTED", i+1, i)
		}
	}

	// Iteration 5 (index 4): codeHashes = [A,B,A,B] → oscillation detected → steering injected.
	if !msgsContain(capturedMsgs[4], "OSCILLATION DETECTED") {
		t.Errorf("iteration 5 (index 4) should contain OSCILLATION DETECTED; messages: %v", capturedMsgs[4])
	}
}

// TestBlockOnRegressionPreventsConvergence verifies that when BlockOnRegression is true,
// the loop does not converge if a per-scenario regression is detected in the same iteration
// that the aggregate score meets the threshold.
func TestBlockOnRegressionPreventsConvergence(t *testing.T) {
	// Iter 1: aggregate below threshold; scenario-a scores above threshold per-scenario.
	// Iter 2: aggregate meets threshold but scenario-a regressed → BlockOnRegression blocks.
	// Iter 3: aggregate meets threshold with no regressions → converge.
	iter1Failures := []string{"✓ scenario-a (98/100)", "✓ scenario-b (100/100)"}
	iter2Failures := []string{"✗ scenario-a (45/100)", "✓ scenario-b (100/100)"}

	var validateCount atomic.Int32
	client := &mockLLMClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		n := validateCount.Add(1)
		switch n {
		case 1:
			// aggregate 60 < threshold 95 — does not converge; scenario-a is at 98.
			return 60, iter1Failures, 0.005, nil
		case 2:
			// Aggregate meets threshold, but scenario-a regressed from 98 to 45.
			return 97, iter2Failures, 0.005, nil
		default:
			return 100, nil, 0.005, nil
		}
	}

	opts := defaultOpts(t)
	opts.BlockOnRegression = true

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should not have converged on iteration 2 due to regression blocking.
	// Iteration 3 has no regression and aggregate 100 → converged.
	if result.Status != StatusConverged {
		t.Errorf("expected converged (on iteration 3), got %q", result.Status)
	}
	if result.Iterations < 3 {
		t.Errorf("expected at least 3 iterations (regression blocked iter 2), got %d", result.Iterations)
	}
}

func TestTestCommandEmpty_SkipsMechanicalTest(t *testing.T) {
	var runTestCalled bool
	client := &mockLLMClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	mgr := &mockContainerMgr{
		runTestFn: func(_ context.Context, _, _ string) (container.ExecResult, error) {
			runTestCalled = true
			return container.ExecResult{ExitCode: 0}, nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		return 100, nil, 0.005, nil
	}

	opts := defaultOpts(t)
	// TestCommand is empty — RunTest should never be called.
	opts.TestCommand = ""

	a := New(client, mgr, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected converged, got %q", result.Status)
	}
	if runTestCalled {
		t.Error("RunTest should not be called when TestCommand is empty")
	}
}

func TestTestCommandExitZero_ProceedsToJudge(t *testing.T) {
	var validateCalled bool
	client := &mockLLMClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	mgr := &mockContainerMgr{
		runTestFn: func(_ context.Context, _, _ string) (container.ExecResult, error) {
			return container.ExecResult{ExitCode: 0, Stdout: "ok\n"}, nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		validateCalled = true
		return 100, nil, 0.005, nil
	}

	opts := defaultOpts(t)
	opts.TestCommand = "go test ./..."

	a := New(client, mgr, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected converged, got %q", result.Status)
	}
	if !validateCalled {
		t.Error("validate should be called when test command exits 0")
	}
}

func TestTestCommandExitNonZero_SkipsJudge(t *testing.T) {
	var validateCalled bool
	var lastUserMsg string
	var lastOutcome IterationOutcome
	client := &mockLLMClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			if len(req.Messages) > 0 {
				lastUserMsg = req.Messages[0].Content
			}
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	mgr := &mockContainerMgr{
		runTestFn: func(_ context.Context, _, _ string) (container.ExecResult, error) {
			return container.ExecResult{ExitCode: 1, Stderr: "FAIL: test_foo\n"}, nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		validateCalled = true
		return 100, nil, 0.005, nil
	}

	opts := defaultOpts(t)
	opts.TestCommand = "go test ./..."
	opts.Progress = func(p IterationProgress) {
		lastOutcome = p.Outcome
	}

	a := New(client, mgr, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusStalled {
		t.Errorf("expected stalled (all test_fail), got %q", result.Status)
	}
	if validateCalled {
		t.Error("validate should NOT be called when test command exits non-zero")
	}
	if lastOutcome != OutcomeTestFail {
		t.Errorf("expected outcome %q, got %q", OutcomeTestFail, lastOutcome)
	}
	// Second iteration prompt should contain TEST FAILURE header.
	if !strings.Contains(lastUserMsg, "TEST FAILURE") {
		t.Errorf("expected TEST FAILURE in user message, got: %s", lastUserMsg)
	}
}

func TestTestCommandOutput_IncludedInFeedback(t *testing.T) {
	const testOutput = "error: assertion failed in test_bar"
	var lastUserMsg string
	client := &mockLLMClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			if len(req.Messages) > 0 {
				lastUserMsg = req.Messages[0].Content
			}
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	mgr := &mockContainerMgr{
		runTestFn: func(_ context.Context, _, _ string) (container.ExecResult, error) {
			return container.ExecResult{ExitCode: 1, Stdout: testOutput, Stderr: ""}, nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		return 100, nil, 0.005, nil
	}

	opts := defaultOpts(t)
	opts.TestCommand = "go test ./..."

	a := New(client, mgr, testLogger(), nil)
	_, err := a.Run(context.Background(), "Build an app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(lastUserMsg, testOutput) {
		t.Errorf("expected test output %q in user message, got: %s", testOutput, lastUserMsg)
	}
}

// validDiagnosisJSON returns a JSON string suitable as wonder phase output.
func validDiagnosisJSON() string {
	return `{"hypotheses":["possible cause"],"root_causes":["root"],"suggested_approach":"Use a completely different architecture"}`
}

// stallingValidateFn returns a validate function that reports the same scenario failing
// for the first n calls, then returns satisfaction = 100.
// Failure format matches FormatScenarioFailureLine so buildSteeringText can detect the stall.
func stallingValidateFn(n int) (ValidateFn, *atomic.Int32) {
	var count atomic.Int32
	fn := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		c := int(count.Add(1))
		if c <= n {
			return 50, []string{FormatScenarioFailureLine("auth", 50)}, 0.005, nil
		}
		return 100, nil, 0.005, nil
	}
	return fn, &count
}

func TestWonderReflect_StallTriggersTwoCalls(t *testing.T) {
	type callInfo struct {
		model string
		temp  *float64
	}
	var (
		callsMu sync.Mutex
		calls   []callInfo
		callIdx atomic.Int32
	)

	client := &mockLLMClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			callsMu.Lock()
			calls = append(calls, callInfo{model: req.Model, temp: req.Temperature})
			callsMu.Unlock()
			idx := int(callIdx.Add(1))
			switch idx {
			case 3:
				// Iteration 3: wonder phase — return diagnosis JSON.
				return llm.GenerateResponse{Content: validDiagnosisJSON(), CostUSD: 0.005}, nil
			case 4:
				// Iteration 3: reflect phase — return valid files.
				return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
			default:
				return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
			}
		},
	}

	// 2 stalling iterations, then converge.
	validate, _ := stallingValidateFn(2)

	opts := defaultOpts(t)
	opts.Model = "gen-model"
	opts.JudgeModel = "judge-model"

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected converged, got %q", result.Status)
	}

	// Iter 1 + iter 2: 2 normal gen calls. Iter 3: wonder + reflect (no normal gen).
	if len(calls) != 4 {
		t.Fatalf("expected 4 generate calls, got %d: %v", len(calls), calls)
	}

	wonderCall := calls[2]
	if wonderCall.model != "judge-model" {
		t.Errorf("wonder call: expected model %q, got %q", "judge-model", wonderCall.model)
	}
	if wonderCall.temp == nil || *wonderCall.temp != wonderTemperature {
		t.Errorf("wonder call: expected temperature %.1f, got %v", wonderTemperature, wonderCall.temp)
	}

	reflectCall := calls[3]
	if reflectCall.model != "gen-model" {
		t.Errorf("reflect call: expected model %q, got %q", "gen-model", reflectCall.model)
	}
	if reflectCall.temp == nil || *reflectCall.temp != reflectTemperature {
		t.Errorf("reflect call: expected temperature %.1f, got %v", reflectTemperature, reflectCall.temp)
	}
}

func TestWonderReflect_ReflectOutputParsedAsFiles(t *testing.T) {
	var callIdx atomic.Int32
	client := &mockLLMClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			idx := int(callIdx.Add(1))
			switch idx {
			case 3:
				return llm.GenerateResponse{Content: validDiagnosisJSON(), CostUSD: 0.005}, nil
			case 4:
				// Reflect output: distinct marker to verify it was actually used.
				return llm.GenerateResponse{Content: `=== FILE: reflect_output.go ===
package main
func main() { /* reflect */ }
=== END FILE ===
=== FILE: Dockerfile ===
FROM scratch
=== END FILE ===`, CostUSD: 0.01}, nil
			default:
				return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
			}
		},
	}

	validate, _ := stallingValidateFn(2)

	var builtDir string
	mgr := &mockContainerMgr{
		buildFn: func(_ context.Context, dir, _ string) error {
			builtDir = dir
			return nil
		},
	}

	opts := defaultOpts(t)
	opts.Model = "gen-model"
	opts.JudgeModel = "judge-model"

	a := New(client, mgr, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected converged, got %q", result.Status)
	}

	// Verify the reflect output was used by checking the built directory contents.
	if builtDir == "" {
		t.Fatal("no build directory captured")
	}
	content, readErr := os.ReadFile(filepath.Join(builtDir, "reflect_output.go"))
	if readErr != nil {
		t.Fatalf("reflect_output.go not found in build dir: %v", readErr)
	}
	if !strings.Contains(string(content), "reflect") {
		t.Error("reflect output file should contain reflect marker")
	}
}

func TestWonderReflect_WonderFailsFallsBack(t *testing.T) {
	var callIdx atomic.Int32
	client := &mockLLMClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			idx := int(callIdx.Add(1))
			if idx == 3 {
				// Wonder call returns a transient error — should fall back.
				return llm.GenerateResponse{}, fmt.Errorf("transient API error")
			}
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}

	validate, _ := stallingValidateFn(2)

	opts := defaultOpts(t)
	opts.Model = "gen-model"
	opts.JudgeModel = "judge-model"

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	// Should complete without error — fell back to normal generation.
	result, err := a.Run(context.Background(), "Build an app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error (should fall back, not hard fail): %v", err)
	}
	if result == nil {
		t.Fatal("expected a result")
	}
}

func TestWonderReflect_WonderGarbageFallsBack(t *testing.T) {
	var callIdx atomic.Int32
	client := &mockLLMClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			idx := int(callIdx.Add(1))
			if idx == 3 {
				// Wonder call returns unparseable text.
				return llm.GenerateResponse{Content: "this is not JSON at all", CostUSD: 0.005}, nil
			}
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}

	validate, _ := stallingValidateFn(2)

	opts := defaultOpts(t)
	opts.JudgeModel = "judge-model"

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error (garbage diagnosis should fall back): %v", err)
	}
	if result == nil {
		t.Fatal("expected a result")
	}
}

func TestWonderReflect_OnlyOnStall(t *testing.T) {
	var generateCalls atomic.Int32
	client := &mockLLMClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			generateCalls.Add(1)
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}

	// Always succeeds — no stall, no wonder/reflect.
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		return 100, nil, 0.005, nil
	}

	opts := defaultOpts(t)
	opts.JudgeModel = "judge-model"

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected converged, got %q", result.Status)
	}
	// Only 1 generate call (iter 1 normal gen) — no wonder/reflect calls.
	if n := generateCalls.Load(); n != 1 {
		t.Errorf("expected exactly 1 generate call, got %d", n)
	}
}

func TestWonderReflect_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var callIdx atomic.Int32

	client := &mockLLMClient{
		generateFn: func(callCtx context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			idx := int(callIdx.Add(1))
			if idx == 3 {
				// Cancel on wonder call.
				cancel()
				return llm.GenerateResponse{}, callCtx.Err()
			}
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}

	validate, _ := stallingValidateFn(2)

	opts := defaultOpts(t)
	opts.JudgeModel = "judge-model"

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	_, err := a.Run(ctx, "Build an app", opts, validate, nil, nil)
	if err == nil {
		t.Fatal("expected error from context cancellation during wonder phase")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestWonderReflect_BudgetExhaustedAfterWonder(t *testing.T) {
	var callIdx atomic.Int32
	client := &mockLLMClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			idx := int(callIdx.Add(1))
			if idx == 3 {
				// Wonder call costs a lot — pushes over budget.
				return llm.GenerateResponse{Content: validDiagnosisJSON(), CostUSD: 100.0}, nil
			}
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}

	// 2 stalling iterations.
	validate, _ := stallingValidateFn(2)

	opts := defaultOpts(t)
	opts.JudgeModel = "judge-model"
	opts.BudgetUSD = 1.0 // small budget; wonder call will exceed it

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected a result")
	}
	// Budget exceeded during or after wonder — should terminate gracefully.
	if result.Status != StatusBudgetExceeded && result.Status != StatusStalled && result.Status != StatusConverged {
		t.Errorf("unexpected status %q (expected budget_exceeded, stalled, or converged)", result.Status)
	}
}

func TestModelEscalationPassedToGenerate(t *testing.T) {
	// Simulate 2 build failures then success, verifying that the model seen by
	// Generate changes from the frugal model to the primary model after escalation.
	var buildCount atomic.Int32
	capturedModels := make([]string, 0, 4)
	var mu sync.Mutex

	client := &mockLLMClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			mu.Lock()
			capturedModels = append(capturedModels, req.Model)
			mu.Unlock()
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	mgr := &mockContainerMgr{
		buildFn: func(_ context.Context, _, _ string) error {
			n := buildCount.Add(1)
			if n <= 2 {
				return fmt.Errorf("build failed iteration %d", n)
			}
			return nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		return 100, nil, 0.005, nil
	}

	opts := defaultOpts(t)
	opts.Model = "primary-model"
	opts.FrugalModel = "frugal-model"
	opts.StallLimit = 5 // allow more iterations so escalation has room to fire

	a := New(client, mgr, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected converged, got %q", result.Status)
	}

	// At least 3 generate calls: 2 before escalation (frugal) + 1 after (primary).
	mu.Lock()
	defer mu.Unlock()
	if len(capturedModels) < 3 {
		t.Fatalf("expected at least 3 generate calls, got %d", len(capturedModels))
	}
	// First 2 calls should use the frugal model.
	for i := range 2 {
		if capturedModels[i] != "frugal-model" {
			t.Errorf("generate call %d: expected model %q, got %q", i+1, "frugal-model", capturedModels[i])
		}
	}
	// The call after escalation should use the primary model.
	if capturedModels[2] != "primary-model" {
		t.Errorf("generate call 3 (post-escalation): expected model %q, got %q", "primary-model", capturedModels[2])
	}
}

func TestNoEscalationWithoutFrugalModel(t *testing.T) {
	var capturedModels []string
	var mu sync.Mutex

	client := &mockLLMClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			mu.Lock()
			capturedModels = append(capturedModels, req.Model)
			mu.Unlock()
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	var callCount atomic.Int32
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		n := callCount.Add(1)
		if n < 3 {
			return 50, []string{"not yet"}, 0.005, nil
		}
		return 100, nil, 0.005, nil
	}

	opts := defaultOpts(t)
	opts.Model = "primary-model"
	// FrugalModel intentionally left empty → escalation disabled.

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected converged, got %q", result.Status)
	}

	mu.Lock()
	defer mu.Unlock()
	for i, m := range capturedModels {
		if m != "primary-model" {
			t.Errorf("generate call %d: expected model %q, got %q", i+1, "primary-model", m)
		}
	}
}

// mockAgentClient embeds mockLLMClient and adds AgentLoop support.
type mockAgentClient struct {
	mockLLMClient
	agentLoopFn func(ctx context.Context, req llm.AgentRequest, handler llm.ToolHandler) (llm.AgentResponse, error)
}

func (m *mockAgentClient) AgentLoop(ctx context.Context, req llm.AgentRequest, handler llm.ToolHandler) (llm.AgentResponse, error) {
	if m.agentLoopFn != nil {
		return m.agentLoopFn(ctx, req, handler)
	}
	return llm.AgentResponse{}, nil
}

// agentWritesFiles is a helper that calls handler with write_file for main.go and Dockerfile.
func agentWritesFiles(ctx context.Context, handler llm.ToolHandler) error {
	calls := []struct {
		name  string
		input string
	}{
		{"write_file", `{"path":"main.go","content":"package main\n\nfunc main() {}\n"}`},
		{"write_file", `{"path":"Dockerfile","content":"FROM scratch\n"}`},
	}
	for i, c := range calls {
		_, err := handler(ctx, llm.ToolCall{ID: fmt.Sprintf("call_%d", i), Name: c.name, Input: []byte(c.input)})
		if err != nil {
			return fmt.Errorf("tool call %s: %w", c.name, err)
		}
	}
	return nil
}

func agenticOpts(t *testing.T) RunOptions {
	t.Helper()
	opts := defaultOpts(t)
	opts.Agentic = true
	return opts
}

func TestAgenticConverge(t *testing.T) {
	client := &mockAgentClient{
		agentLoopFn: func(ctx context.Context, _ llm.AgentRequest, handler llm.ToolHandler) (llm.AgentResponse, error) {
			if err := agentWritesFiles(ctx, handler); err != nil {
				return llm.AgentResponse{}, err
			}
			return llm.AgentResponse{Turns: 2, TotalCost: 0.05}, nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		return 100, nil, 0.005, nil
	}

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build a hello world app", agenticOpts(t), validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected status %q, got %q", StatusConverged, result.Status)
	}
	if result.Iterations != 1 {
		t.Errorf("expected 1 iteration, got %d", result.Iterations)
	}

	// Verify files were written to bestDir.
	mainPath := filepath.Join(result.OutputDir, "main.go")
	if _, statErr := os.Stat(mainPath); statErr != nil {
		t.Errorf("expected main.go in bestDir: %v", statErr)
	}
}

func TestAgenticCostTracking(t *testing.T) {
	const agentCost = 0.50
	client := &mockAgentClient{
		agentLoopFn: func(ctx context.Context, _ llm.AgentRequest, handler llm.ToolHandler) (llm.AgentResponse, error) {
			if err := agentWritesFiles(ctx, handler); err != nil {
				return llm.AgentResponse{}, err
			}
			return llm.AgentResponse{Turns: 1, TotalCost: agentCost}, nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		return 100, nil, 0.005, nil
	}

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", agenticOpts(t), validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.CostUSD < agentCost {
		t.Errorf("expected CostUSD >= %.2f, got %.2f", agentCost, result.CostUSD)
	}
}

func TestAgenticTurnsReported(t *testing.T) {
	const wantTurns = 5
	var capturedProgress []IterationProgress
	client := &mockAgentClient{
		agentLoopFn: func(ctx context.Context, _ llm.AgentRequest, handler llm.ToolHandler) (llm.AgentResponse, error) {
			if err := agentWritesFiles(ctx, handler); err != nil {
				return llm.AgentResponse{}, err
			}
			return llm.AgentResponse{Turns: wantTurns, TotalCost: 0.01}, nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		return 100, nil, 0.005, nil
	}

	opts := agenticOpts(t)
	opts.Progress = func(p IterationProgress) {
		capturedProgress = append(capturedProgress, p)
	}

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	_, err := a.Run(context.Background(), "Build an app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(capturedProgress) == 0 {
		t.Fatal("expected at least one progress callback")
	}
	if capturedProgress[0].Turns != wantTurns {
		t.Errorf("expected Turns=%d, got %d", wantTurns, capturedProgress[0].Turns)
	}
}

func TestAgenticPatchPreSeed(t *testing.T) {
	// First iteration writes files; second iteration verifies pre-seeded files are readable.
	var iterCount atomic.Int32
	var readResult string

	client := &mockAgentClient{
		agentLoopFn: func(ctx context.Context, req llm.AgentRequest, handler llm.ToolHandler) (llm.AgentResponse, error) {
			n := iterCount.Add(1)
			if n == 1 {
				// First iteration: write files normally.
				if err := agentWritesFiles(ctx, handler); err != nil {
					return llm.AgentResponse{}, err
				}
			} else {
				// Second iteration: read the pre-seeded main.go, then write files.
				result, err := handler(ctx, llm.ToolCall{
					ID:    "read_1",
					Name:  "read_file",
					Input: []byte(`{"path":"main.go"}`),
				})
				if err != nil {
					return llm.AgentResponse{}, fmt.Errorf("read_file: %w", err)
				}
				readResult = result
				if err := agentWritesFiles(ctx, handler); err != nil {
					return llm.AgentResponse{}, err
				}
			}
			return llm.AgentResponse{Turns: 1, TotalCost: 0.01}, nil
		},
	}

	var callCount atomic.Int32
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		n := callCount.Add(1)
		if n == 1 {
			return 60, []string{"needs work"}, 0.005, nil
		}
		return 100, nil, 0.005, nil
	}

	opts := agenticOpts(t)
	opts.PatchMode = true

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected converged, got %q", result.Status)
	}
	// The second iteration's read_file should have returned the pre-seeded content.
	if !strings.Contains(readResult, "package main") {
		t.Errorf("expected pre-seeded main.go content to be readable, got: %q", readResult)
	}
}

func TestAgenticPatchMergesUnchangedFiles(t *testing.T) {
	// In patch mode, the agent only writes files it modifies. The files map returned by
	// generateAgentic must include unchanged files from bestFiles so that hashFiles() and
	// bestFiles tracking operate on the complete file set.
	var iterCount atomic.Int32

	client := &mockAgentClient{
		agentLoopFn: func(ctx context.Context, _ llm.AgentRequest, handler llm.ToolHandler) (llm.AgentResponse, error) {
			n := iterCount.Add(1)
			if n == 1 {
				// First iteration: write both main.go and Dockerfile.
				if err := agentWritesFiles(ctx, handler); err != nil {
					return llm.AgentResponse{}, err
				}
			} else {
				// Second iteration (patch mode): agent only writes main.go, leaving Dockerfile unchanged.
				_, err := handler(ctx, llm.ToolCall{
					ID:    "write_1",
					Name:  "write_file",
					Input: []byte(`{"path":"main.go","content":"package main\n\nfunc main() { println(\"v2\") }\n"}`),
				})
				if err != nil {
					return llm.AgentResponse{}, err
				}
			}
			return llm.AgentResponse{Turns: 1, TotalCost: 0.01}, nil
		},
	}

	var callCount atomic.Int32
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		n := callCount.Add(1)
		if n == 1 {
			return 60, []string{"needs work"}, 0.005, nil
		}
		return 100, nil, 0.005, nil
	}

	opts := agenticOpts(t)
	opts.PatchMode = true

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected converged, got %q", result.Status)
	}
	// Both files should be in the output (bestFiles) even though the agent only wrote main.go
	// in the second iteration. The patch merge must carry forward unchanged files.
	for _, want := range []string{"main.go", "Dockerfile"} {
		if _, err := os.Stat(filepath.Join(result.OutputDir, want)); err != nil {
			t.Errorf("expected %s in output dir after patch convergence: %v", want, err)
		}
	}
}

func TestAgenticRequiresAgentClient(t *testing.T) {
	// Plain mockLLMClient does not implement AgentClient.
	client := &mockLLMClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: validLLMOutput()}, nil
		},
	}
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		return 100, nil, 0.005, nil
	}

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	_, err := a.Run(context.Background(), "Build an app", agenticOpts(t), validate, nil, nil)
	if err == nil {
		t.Fatal("expected error when AgentClient not implemented")
	}
	if !errors.Is(err, errAgentClientRequired) {
		t.Errorf("expected errAgentClientRequired, got %v", err)
	}
}

func TestAgenticBuildFailure(t *testing.T) {
	client := &mockAgentClient{
		agentLoopFn: func(ctx context.Context, _ llm.AgentRequest, handler llm.ToolHandler) (llm.AgentResponse, error) {
			if err := agentWritesFiles(ctx, handler); err != nil {
				return llm.AgentResponse{}, err
			}
			return llm.AgentResponse{Turns: 1, TotalCost: 0.01}, nil
		},
	}
	buildErr := fmt.Errorf("docker build failed: exit 1")
	mgr := &mockContainerMgr{
		buildFn: func(_ context.Context, _, _ string) error {
			return buildErr
		},
	}

	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		return 100, nil, 0.005, nil
	}

	opts := agenticOpts(t)
	opts.StallLimit = 1

	a := New(client, mgr, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusStalled {
		t.Errorf("expected stalled after build failure, got %q", result.Status)
	}
}

func TestAgenticWonderReflectSkipped(t *testing.T) {
	// Agentic mode should call AgentLoop and NOT call Generate even during stall history.
	var generateCalled atomic.Bool
	var agentLoopCalls atomic.Int32

	client := &mockAgentClient{
		mockLLMClient: mockLLMClient{
			generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
				generateCalled.Store(true)
				return llm.GenerateResponse{Content: validLLMOutput()}, nil
			},
		},
		agentLoopFn: func(ctx context.Context, _ llm.AgentRequest, handler llm.ToolHandler) (llm.AgentResponse, error) {
			agentLoopCalls.Add(1)
			if err := agentWritesFiles(ctx, handler); err != nil {
				return llm.AgentResponse{}, err
			}
			return llm.AgentResponse{Turns: 1, TotalCost: 0.01}, nil
		},
	}

	var callCount atomic.Int32
	validate := func(_ context.Context, _ string, _ RestartFunc) (float64, []string, float64, error) {
		n := callCount.Add(1)
		if n < 3 {
			return 50, []string{"scenario-a (50/100)"}, 0.005, nil
		}
		return 100, nil, 0.005, nil
	}

	opts := agenticOpts(t)
	opts.StallLimit = 5

	a := New(client, &mockContainerMgr{}, testLogger(), nil)
	result, err := a.Run(context.Background(), "Build an app", opts, validate, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected converged, got %q", result.Status)
	}
	if generateCalled.Load() {
		t.Error("Generate should not be called in agentic mode")
	}
	if agentLoopCalls.Load() == 0 {
		t.Error("AgentLoop should have been called at least once")
	}
}
