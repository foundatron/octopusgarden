package interview

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/foundatron/octopusgarden/internal/llm"
)

type mockClient struct {
	generateFn func(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error)
}

func (m *mockClient) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	return m.generateFn(ctx, req)
}

func (m *mockClient) Judge(_ context.Context, _ llm.JudgeRequest) (llm.JudgeResponse, error) {
	return llm.JudgeResponse{}, nil
}

func TestInterviewHappyPath(t *testing.T) {
	t.Parallel()
	calls := 0
	// 3 Generate calls: initial question, response to "Go", final spec on "done".
	responses := []llm.GenerateResponse{
		{Content: "What language?", CostUSD: 0.01},
		{Content: "Got it. Any other requirements?", CostUSD: 0.01},
		{Content: "# Spec\n\n## Purpose\nA todo app.", CostUSD: 0.02},
	}

	client := &mockClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			resp := responses[calls]
			calls++
			return resp, nil
		},
	}

	in := strings.NewReader("Go\n\ndone\n")
	var out bytes.Buffer

	iv := New(client, in, &out, "test-model")
	spec, cost, err := iv.Run(context.Background(), "I want to build a CLI app.")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if spec != "# Spec\n\n## Purpose\nA todo app." {
		t.Errorf("unexpected spec: %q", spec)
	}

	const wantCost = 0.01 + 0.01 + 0.02
	if cost != wantCost {
		t.Errorf("cost = %v, want %v", cost, wantCost)
	}
}

func TestInterviewEmptyInput(t *testing.T) {
	t.Parallel()
	calls := 0
	client := &mockClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			calls++
			return llm.GenerateResponse{Content: "Tell me more.", CostUSD: 0.01}, nil
		},
	}

	in := strings.NewReader("\n\nanswer\n\ndone\n")
	var out bytes.Buffer

	iv := New(client, in, &out, "test-model")
	_, _, err := iv.Run(context.Background(), "I want to build something.")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out.String(), rePromptMsg) {
		t.Error("expected rePromptMsg written for empty lines")
	}

	// Only 1 initial call + 1 for "answer" + 1 final = 3 calls; empty lines don't add calls
	if calls != 3 {
		t.Errorf("expected 3 generate calls, got %d", calls)
	}
}

func TestInterviewMaxRounds(t *testing.T) {
	t.Parallel()
	var lastReq llm.GenerateRequest
	calls := 0
	client := &mockClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			lastReq = req
			calls++
			return llm.GenerateResponse{Content: "Next question.", CostUSD: 0.01}, nil
		},
	}

	// 21 non-"done" answers to exceed maxRounds=20.
	// Each answer is followed by a blank line to submit it.
	lines := make([]string, 21)
	for j := range lines {
		lines[j] = "answer"
	}
	in := strings.NewReader(strings.Join(lines, "\n\n") + "\n\n")
	var out bytes.Buffer

	iv := New(client, in, &out, "test-model")
	spec, _, err := iv.Run(context.Background(), "Start.")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out.String(), "Maximum rounds reached") {
		t.Error("expected max-rounds warning in output")
	}

	// Verify last Generate call included the final generation instruction.
	found := false
	for _, msg := range lastReq.Messages {
		if strings.Contains(msg.Content, finalInstruction) {
			found = true
			break
		}
	}
	if !found {
		t.Error("final Generate call should include finalInstruction")
	}

	if spec == "" {
		t.Error("expected non-empty spec")
	}
}

func TestInterviewDoneCaseInsensitive(t *testing.T) {
	t.Parallel()
	tests := []string{"DONE", "Done", " done "}
	for _, input := range tests {
		t.Run(fmt.Sprintf("%q", input), func(t *testing.T) {
			t.Parallel()
			client := &mockClient{
				generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
					return llm.GenerateResponse{Content: "spec content"}, nil
				},
			}
			in := strings.NewReader(input + "\n")
			var out bytes.Buffer
			iv := New(client, in, &out, "test-model")
			spec, _, err := iv.Run(context.Background(), "Start.")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if spec == "" {
				t.Error("expected non-empty spec for input", input)
			}
		})
	}
}

func TestInterviewContextCancellation(t *testing.T) {
	t.Parallel()
	client := &mockClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: "question"}, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	in := strings.NewReader("answer\n")
	var out bytes.Buffer
	iv := New(client, in, &out, "test-model")
	_, _, err := iv.Run(ctx, "Start.")
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestInterviewLLMError(t *testing.T) {
	t.Parallel()
	errLLM := errors.New("api failure")
	client := &mockClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{}, errLLM
		},
	}

	in := strings.NewReader("")
	var out bytes.Buffer
	iv := New(client, in, &out, "test-model")
	_, _, err := iv.Run(context.Background(), "Start.")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.HasPrefix(err.Error(), "interview: generate:") {
		t.Errorf("expected 'interview: generate:' prefix, got %q", err.Error())
	}
	if !errors.Is(err, errLLM) {
		t.Errorf("expected wrapped errLLM, got %v", err)
	}
}

func TestInterviewCostTracking(t *testing.T) {
	t.Parallel()
	calls := 0
	costs := []float64{0.10, 0.20, 0.30}
	client := &mockClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			cost := costs[calls]
			calls++
			return llm.GenerateResponse{Content: "response", CostUSD: cost}, nil
		},
	}

	// initial call + 1 round + done → 3 calls total
	in := strings.NewReader("answer\n\ndone\n")
	var out bytes.Buffer
	iv := New(client, in, &out, "test-model")
	_, total, err := iv.Run(context.Background(), "Start.")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	const want = 0.60
	const epsilon = 1e-9
	if total < want-epsilon || total > want+epsilon {
		t.Errorf("total cost = %v, want %v", total, want)
	}
}

func TestInterviewEOF(t *testing.T) {
	t.Parallel()
	calls := 0
	client := &mockClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			calls++
			return llm.GenerateResponse{Content: "spec from eof"}, nil
		},
	}

	// Reader exhausts without "done"
	in := strings.NewReader("partial answer")
	var out bytes.Buffer
	iv := New(client, in, &out, "test-model")
	spec, _, err := iv.Run(context.Background(), "Start.")
	if err != nil {
		t.Fatalf("unexpected error on EOF: %v", err)
	}
	if spec == "" {
		t.Error("expected auto-generated spec on EOF")
	}
}

func TestInterviewMultiLineInput(t *testing.T) {
	t.Parallel()
	var capturedReqs []llm.GenerateRequest
	calls := 0
	responses := []llm.GenerateResponse{
		{Content: "Tell me more.", CostUSD: 0.01},
		{Content: "Got it!", CostUSD: 0.01},
		{Content: "# Spec", CostUSD: 0.02},
	}

	client := &mockClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			capturedReqs = append(capturedReqs, req)
			resp := responses[calls]
			calls++
			return resp, nil
		},
	}

	// Multi-line paste followed by blank line to submit, then done.
	in := strings.NewReader("line one\nline two\nline three\n\ndone\n")
	var out bytes.Buffer

	iv := New(client, in, &out, "test-model")
	_, _, err := iv.Run(context.Background(), "Start.")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 3 calls: initial + one multi-line response + final
	if calls != 3 {
		t.Errorf("expected 3 generate calls, got %d", calls)
	}

	// The user message should contain all three lines joined together.
	if len(capturedReqs) < 2 {
		t.Fatal("expected at least 2 captured requests")
	}
	userMsg := capturedReqs[1].Messages[len(capturedReqs[1].Messages)-1]
	if userMsg.Role != "user" {
		t.Fatalf("expected user message, got %q", userMsg.Role)
	}
	if !strings.Contains(userMsg.Content, "line one") ||
		!strings.Contains(userMsg.Content, "line two") ||
		!strings.Contains(userMsg.Content, "line three") {
		t.Errorf("multi-line input not preserved: %q", userMsg.Content)
	}
}

func TestRunWithSeed(t *testing.T) {
	t.Parallel()
	const seedSpec = "## Purpose\nA todo app."
	var capturedReqs []llm.GenerateRequest
	calls := 0
	responses := []llm.GenerateResponse{
		{Content: "What persistence mechanism do you need?", CostUSD: 0.01},
		{Content: "# Spec\n\n## Purpose\nAn improved todo app.", CostUSD: 0.02},
	}

	client := &mockClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			capturedReqs = append(capturedReqs, req)
			resp := responses[calls]
			calls++
			return resp, nil
		},
	}

	in := strings.NewReader("done\n")
	var out bytes.Buffer

	iv := New(client, in, &out, "test-model")
	spec, cost, err := iv.RunWithSeed(context.Background(), seedSpec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if spec != "# Spec\n\n## Purpose\nAn improved todo app." {
		t.Errorf("unexpected spec: %q", spec)
	}

	const wantCost = 0.01 + 0.02
	if cost != wantCost {
		t.Errorf("cost = %v, want %v", cost, wantCost)
	}

	// Verify system prompt is the seed variant.
	if len(capturedReqs) == 0 {
		t.Fatal("no generate calls captured")
	}
	if capturedReqs[0].SystemPrompt != seedSystemPrompt {
		t.Errorf("expected seedSystemPrompt, got %q", capturedReqs[0].SystemPrompt)
	}

	// Verify first user message contains the seed spec content.
	if len(capturedReqs[0].Messages) == 0 {
		t.Fatal("no messages in first request")
	}
	if !strings.Contains(capturedReqs[0].Messages[0].Content, seedSpec) {
		t.Errorf("first user message should contain seed spec, got %q", capturedReqs[0].Messages[0].Content)
	}
}

func TestRunWithSeedPreservesConversationLoop(t *testing.T) {
	t.Parallel()
	calls := 0
	responses := []llm.GenerateResponse{
		{Content: "What language?", CostUSD: 0.01},
		{Content: "Any constraints?", CostUSD: 0.01},
		{Content: "# Final Spec", CostUSD: 0.02},
	}

	client := &mockClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			resp := responses[calls]
			calls++
			return resp, nil
		},
	}

	// Multi-round: answer one question then "done"
	in := strings.NewReader("Go\n\ndone\n")
	var out bytes.Buffer

	iv := New(client, in, &out, "test-model")
	spec, _, err := iv.RunWithSeed(context.Background(), "## Purpose\nA CLI tool.")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if spec != "# Final Spec" {
		t.Errorf("unexpected spec: %q", spec)
	}

	// 3 calls: initial + answer to "Go" + final generation on "done"
	if calls != 3 {
		t.Errorf("expected 3 generate calls, got %d", calls)
	}
}
