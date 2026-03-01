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
	buildFn       func(ctx context.Context, dir, tag string) error
	runFn         func(ctx context.Context, tag string) (string, container.StopFunc, error)
	waitHealthyFn func(ctx context.Context, url string, timeout time.Duration) error
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

func (m *mockContainerMgr) WaitHealthy(ctx context.Context, url string, timeout time.Duration) error {
	if m.waitHealthyFn != nil {
		return m.waitHealthyFn(ctx, url, timeout)
	}
	return nil
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

	a := New(client, &mockContainerMgr{}, testLogger())
	result, err := a.Run(context.Background(), "Build a hello world app", defaultOpts(t), validate)
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

	a := New(client, &mockContainerMgr{}, testLogger())
	result, err := a.Run(context.Background(), "Build an app", defaultOpts(t), validate)
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

	a := New(client, &mockContainerMgr{}, testLogger())
	result, err := a.Run(context.Background(), "Build an app", opts, validate)
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

	a := New(client, &mockContainerMgr{}, testLogger())
	result, err := a.Run(context.Background(), "Build an app", opts, validate)
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

	a := New(client, &mockContainerMgr{}, testLogger())
	result, err := a.Run(context.Background(), "Build an app", opts, validate)
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

	a := New(client, mgr, testLogger())
	result, err := a.Run(context.Background(), "Build an app", defaultOpts(t), validate)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected status %q, got %q", StatusConverged, result.Status)
	}
	if !strings.Contains(lastUserMsg, "build_error") {
		t.Errorf("expected build error feedback in prompt, got: %s", lastUserMsg)
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

	a := New(client, mgr, testLogger())
	result, err := a.Run(context.Background(), "Build an app", defaultOpts(t), validate)
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

	a := New(client, &mockContainerMgr{}, testLogger())
	result, err := a.Run(context.Background(), "Build an app", opts, validate)
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

	a := New(client, &mockContainerMgr{}, testLogger())
	_, err := a.Run(context.Background(), "", defaultOpts(t), nil)
	if !errors.Is(err, errEmptySpec) {
		t.Fatalf("expected errEmptySpec, got %v", err)
	}

	// Also test whitespace-only spec.
	_, err = a.Run(context.Background(), "   \n\t  ", defaultOpts(t), nil)
	if !errors.Is(err, errEmptySpec) {
		t.Fatalf("expected errEmptySpec for whitespace spec, got %v", err)
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

	a := New(client, &mockContainerMgr{}, testLogger())
	_, err := a.Run(ctx, "Build an app", defaultOpts(t), nil)
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

	a := New(client, &mockContainerMgr{}, testLogger())
	_, err := a.Run(context.Background(), "Build an app", defaultOpts(t), validate)
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
	a := New(client, &mockContainerMgr{}, testLogger())
	result, err := a.Run(context.Background(), "Build an app", opts, validate)
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

	a := New(client, mgr, testLogger())
	result, err := a.Run(context.Background(), "Build an app", defaultOpts(t), validate)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusConverged {
		t.Errorf("expected status %q, got %q", StatusConverged, result.Status)
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

	a := New(client, &mockContainerMgr{}, testLogger())
	_, err := a.Run(context.Background(), "Build an app", defaultOpts(t), validate)
	if err == nil {
		t.Fatal("expected error from validate failure")
	}
	if !strings.Contains(err.Error(), "judge unavailable") {
		t.Errorf("expected validate error in message, got: %v", err)
	}
}
