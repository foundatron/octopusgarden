package attractor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
	runFn          func(ctx context.Context, tag string) (string, container.StopFunc, error)
	runMultiPortFn func(ctx context.Context, tag string, extraPorts []string) (container.RunResult, container.StopFunc, error)
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

func (m *mockContainerMgr) Run(ctx context.Context, tag string) (string, container.StopFunc, error) {
	if m.runFn != nil {
		return m.runFn(ctx, tag)
	}
	return "http://127.0.0.1:9999", func() {}, nil
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
	validate := func(_ context.Context, _ string) (float64, []string, float64, error) {
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
	validate := func(_ context.Context, _ string) (float64, []string, float64, error) {
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
	validate := func(_ context.Context, _ string) (float64, []string, float64, error) {
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
	validate := func(_ context.Context, _ string) (float64, []string, float64, error) {
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
	validate := func(_ context.Context, _ string) (float64, []string, float64, error) {
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
	validate := func(_ context.Context, _ string) (float64, []string, float64, error) {
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
	validate := func(_ context.Context, _ string) (float64, []string, float64, error) {
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
	validate := func(_ context.Context, _ string) (float64, []string, float64, error) {
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
	validate := func(_ context.Context, _ string) (float64, []string, float64, error) {
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
	validate := func(_ context.Context, _ string) (float64, []string, float64, error) {
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
		runFn: func(_ context.Context, _ string) (string, container.StopFunc, error) {
			n := runCount.Add(1)
			if n == 1 {
				return "", nil, fmt.Errorf("port conflict")
			}
			return "http://127.0.0.1:9999", func() {}, nil
		},
	}
	validate := func(_ context.Context, _ string) (float64, []string, float64, error) {
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
		runFn: func(_ context.Context, _ string) (string, container.StopFunc, error) {
			runCalled = true
			return "http://127.0.0.1:9999", func() {}, nil
		},
		waitHealthyFn: func(_ context.Context, _ string, _ time.Duration) error {
			waitHealthyCalled = true
			return nil
		},
	}
	validate := func(_ context.Context, _ string) (float64, []string, float64, error) {
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
	validate := func(_ context.Context, _ string) (float64, []string, float64, error) {
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
	validate := func(_ context.Context, _ string) (float64, []string, float64, error) {
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
	validate := func(_ context.Context, _ string) (float64, []string, float64, error) {
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
	validate := func(_ context.Context, _ string) (float64, []string, float64, error) {
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
	validate := func(_ context.Context, _ string) (float64, []string, float64, error) {
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
	validate := func(_ context.Context, _ string) (float64, []string, float64, error) {
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
	validate := func(_ context.Context, _ string) (float64, []string, float64, error) {
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
	validate := func(_ context.Context, _ string) (float64, []string, float64, error) {
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
	validate := func(_ context.Context, _ string) (float64, []string, float64, error) {
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
	validate := func(_ context.Context, _ string) (float64, []string, float64, error) {
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
	validate := func(_ context.Context, _ string) (float64, []string, float64, error) {
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
	validate := func(_ context.Context, _ string) (float64, []string, float64, error) {
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
