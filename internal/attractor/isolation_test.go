package attractor

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/foundatron/octopusgarden/internal/llm"
)

func TestAttractorDoesNotImportScenario(t *testing.T) {
	ctx := context.Background()
	cmd := exec.CommandContext(ctx, "go", "list", "-deps", "github.com/foundatron/octopusgarden/internal/attractor")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			t.Fatalf("go list failed: %v\nstderr: %s", err, exitErr.Stderr)
		}
		t.Fatalf("go list failed: %v", err)
	}

	for line := range strings.SplitSeq(string(out), "\n") {
		if strings.Contains(line, "internal/scenario") {
			t.Fatalf("attractor package must not depend on internal/scenario, found dependency: %s", line)
		}
	}
}

func TestSystemPromptContainsOnlySpec(t *testing.T) {
	specContent := "Build a REST API that manages widgets with CRUD endpoints"

	var mu sync.Mutex
	var captured []llm.GenerateRequest

	client := &mockLLMClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			mu.Lock()
			captured = append(captured, req)
			mu.Unlock()
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}
	validate := func(_ context.Context, _ string) (float64, []string, float64, error) {
		return 100, nil, 0.005, nil
	}

	a := New(client, &mockContainerMgr{}, testLogger())
	_, err := a.Run(context.Background(), specContent, defaultOpts(t), validate)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(captured) == 0 {
		t.Fatal("expected at least one LLM request to be captured")
	}

	expectedPrompt := buildSystemPrompt(specContent)
	for i, req := range captured {
		if req.SystemPrompt != expectedPrompt {
			t.Errorf("request %d: system prompt does not match buildSystemPrompt(spec)\ngot:  %s\nwant: %s", i, req.SystemPrompt, expectedPrompt)
		}
		if !strings.Contains(req.SystemPrompt, specContent) {
			t.Errorf("request %d: system prompt does not contain spec content", i)
		}
	}
}

func TestScenarioContentNeverInSystemPrompt(t *testing.T) {
	specContent := "Build a REST API that manages items"

	sentinels := []string{
		"HOLDOUT_SENTINEL_criteria_abc123",
		"HOLDOUT_SENTINEL_endpoint_def456",
		"HOLDOUT_SENTINEL_validation_ghi789",
	}

	var mu sync.Mutex
	var captured []llm.GenerateRequest
	var callCount atomic.Int32

	client := &mockLLMClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			mu.Lock()
			captured = append(captured, req)
			mu.Unlock()
			return llm.GenerateResponse{Content: validLLMOutput(), CostUSD: 0.01}, nil
		},
	}

	validate := func(_ context.Context, _ string) (float64, []string, float64, error) {
		n := callCount.Add(1)
		// Return failures containing sentinels — these should appear in user
		// messages (feedback channel) but never in system prompts.
		failures := make([]string, 0, len(sentinels))
		for _, s := range sentinels {
			failures = append(failures, fmt.Sprintf("Failed: %s (iteration %d)", s, n))
		}
		return 60, failures, 0.005, nil
	}

	opts := defaultOpts(t)
	opts.MaxIterations = 5
	opts.StallLimit = 100 // prevent stall exit

	a := New(client, &mockContainerMgr{}, testLogger())
	result, err := a.Run(context.Background(), specContent, opts, validate)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusMaxIterations {
		t.Fatalf("expected status %q, got %q", StatusMaxIterations, result.Status)
	}
	if result.Iterations != 5 {
		t.Fatalf("expected 5 iterations, got %d", result.Iterations)
	}

	// Verify no sentinel appears in any system prompt.
	for i, req := range captured {
		for _, sentinel := range sentinels {
			if strings.Contains(req.SystemPrompt, sentinel) {
				t.Errorf("request %d: system prompt contains sentinel %q — scenario content leaked into system prompt", i, sentinel)
			}
		}
	}

	// Verify sentinels DO appear in user messages (the feedback channel) to
	// confirm the test is actually exercising the feedback path.
	foundInUserMsg := false
	for _, req := range captured {
		for _, msg := range req.Messages {
			for _, sentinel := range sentinels {
				if strings.Contains(msg.Content, sentinel) {
					foundInUserMsg = true
					break
				}
			}
			if foundInUserMsg {
				break
			}
		}
		if foundInUserMsg {
			break
		}
	}
	if !foundInUserMsg {
		t.Error("expected sentinels to appear in user messages (feedback channel), but none found — test may not be exercising the feedback path")
	}
}
