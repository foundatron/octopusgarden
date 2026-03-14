package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/option"
)

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// anthropicResponse builds a canned Anthropic API JSON response.
func anthropicResponse(text string, inputTokens, cacheCreation, cacheRead, outputTokens int) string {
	resp := map[string]any{
		"id":   "msg_test",
		"type": "message",
		"role": "assistant",
		"content": []map[string]any{
			{"type": "text", "text": text},
		},
		"model":         "claude-sonnet-4-20250514",
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":                inputTokens,
			"cache_creation_input_tokens": cacheCreation,
			"cache_read_input_tokens":     cacheRead,
			"output_tokens":               outputTokens,
		},
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

func TestAnthropicGenerate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		cacheCreation   int
		cacheRead       int
		wantCacheHit    bool
		wantInputTokens int
	}{
		{
			name:            "no cache",
			cacheCreation:   0,
			cacheRead:       0,
			wantCacheHit:    false,
			wantInputTokens: 100,
		},
		{
			name:            "cache hit",
			cacheCreation:   0,
			cacheRead:       80,
			wantCacheHit:    true,
			wantInputTokens: 180,
		},
		{
			name:            "cache write",
			cacheCreation:   90,
			cacheRead:       0,
			wantCacheHit:    false,
			wantInputTokens: 190,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(anthropicResponse("generated code", 100, tt.cacheCreation, tt.cacheRead, 50)))
			}))
			defer server.Close()

			client := NewAnthropicClient("test-key", newTestLogger(),
				option.WithBaseURL(server.URL),
			)

			resp, err := client.Generate(context.Background(), GenerateRequest{
				SystemPrompt: "test system prompt",
				Messages:     []Message{{Role: "user", Content: "generate code"}},
				Model:        "claude-sonnet-4-20250514",
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if resp.Content != "generated code" {
				t.Errorf("content = %q, want %q", resp.Content, "generated code")
			}
			if resp.CacheHit != tt.wantCacheHit {
				t.Errorf("cache_hit = %v, want %v", resp.CacheHit, tt.wantCacheHit)
			}
			if resp.InputTokens != tt.wantInputTokens {
				t.Errorf("input_tokens = %d, want %d", resp.InputTokens, tt.wantInputTokens)
			}
			if resp.OutputTokens != 50 {
				t.Errorf("output_tokens = %d, want 50", resp.OutputTokens)
			}
			if resp.CostUSD <= 0 {
				t.Errorf("cost_usd = %f, want > 0", resp.CostUSD)
			}
			if resp.FinishReason != "end_turn" {
				t.Errorf("finish_reason = %q, want %q", resp.FinishReason, "end_turn")
			}
		})
	}
}

func TestAnthropicJudge(t *testing.T) {
	t.Parallel()
	judgeJSON := `{"score": 85, "reasoning": "mostly correct", "failures": ["minor issue"]}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(anthropicResponse(judgeJSON, 50, 0, 0, 30)))
	}))
	defer server.Close()

	client := NewAnthropicClient("test-key", newTestLogger(),
		option.WithBaseURL(server.URL),
	)

	resp, err := client.Judge(context.Background(), JudgeRequest{
		SystemPrompt: "judge prompt",
		UserPrompt:   "evaluate this",
		Model:        "claude-haiku-4-5-20251001",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Score != 85 {
		t.Errorf("score = %d, want 85", resp.Score)
	}
	if resp.Reasoning != "mostly correct" {
		t.Errorf("reasoning = %q, want %q", resp.Reasoning, "mostly correct")
	}
	if len(resp.Failures) != 1 || resp.Failures[0] != "minor issue" {
		t.Errorf("failures = %v, want [minor issue]", resp.Failures)
	}
	if resp.CostUSD <= 0 {
		t.Errorf("cost_usd = %f, want > 0", resp.CostUSD)
	}
}

func TestAnthropicJudge_MalformedJSON(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(anthropicResponse("this is not valid json at all", 50, 0, 0, 30)))
	}))
	defer server.Close()

	client := NewAnthropicClient("test-key", newTestLogger(),
		option.WithBaseURL(server.URL),
	)

	resp, err := client.Judge(context.Background(), JudgeRequest{
		SystemPrompt: "judge prompt",
		UserPrompt:   "evaluate this",
		Model:        "claude-haiku-3-5-20241022",
	})
	if err != nil {
		t.Fatalf("unexpected error on malformed JSON: %v", err)
	}

	if resp.Score != 0 {
		t.Errorf("score = %d, want 0 for malformed JSON", resp.Score)
	}
	if resp.Reasoning != "this is not valid json at all" {
		t.Errorf("reasoning = %q, want raw text", resp.Reasoning)
	}
}

func TestCalculateCost(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		model         string
		regular       int
		cacheCreation int
		cacheRead     int
		output        int
		wantUSD       float64
		wantFallback  bool
	}{
		{
			name:    "sonnet no cache",
			model:   "claude-sonnet-4-20250514",
			regular: 1_000_000, output: 1_000_000,
			wantUSD: 3.00 + 15.00,
		},
		{
			name:          "sonnet with cache",
			model:         "claude-sonnet-4-20250514",
			regular:       100_000,
			cacheCreation: 500_000,
			cacheRead:     400_000,
			output:        50_000,
			wantUSD:       0.30 + 1.875 + 0.12 + 0.75,
		},
		{
			name:    "haiku 4.5 no cache",
			model:   "claude-haiku-4-5-20251001",
			regular: 1_000_000, output: 1_000_000,
			wantUSD: 1.00 + 5.00,
		},
		{
			name:    "unknown model uses fallback",
			model:   "unknown-model",
			regular: 1_000_000, output: 1_000_000,
			wantUSD:      15.00 + 75.00,
			wantFallback: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, fallback := CalculateCost(tt.model, tt.regular, tt.cacheCreation, tt.cacheRead, tt.output)
			if math.Abs(got-tt.wantUSD) > 0.001 {
				t.Errorf("CalculateCost() = %f, want %f", got, tt.wantUSD)
			}
			if fallback != tt.wantFallback {
				t.Errorf("CalculateCost() fallback = %v, want %v", fallback, tt.wantFallback)
			}
		})
	}
}

func TestCacheControlPropagation(t *testing.T) {
	t.Parallel()
	var capturedBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &capturedBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(anthropicResponse("ok", 100, 0, 0, 10)))
	}))
	defer server.Close()

	client := NewAnthropicClient("test-key", newTestLogger(),
		option.WithBaseURL(server.URL),
	)

	_, err := client.Generate(context.Background(), GenerateRequest{
		SystemPrompt: "cached system prompt",
		Messages:     []Message{{Role: "user", Content: "hello"}},
		Model:        "claude-sonnet-4-20250514",
		CacheControl: &CacheControl{Type: "ephemeral"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify system block has cache_control.
	system, ok := capturedBody["system"].([]any)
	if !ok || len(system) == 0 {
		t.Fatal("expected system blocks in request body")
	}

	block, ok := system[0].(map[string]any)
	if !ok {
		t.Fatal("expected system block to be an object")
	}

	cc, ok := block["cache_control"].(map[string]any)
	if !ok {
		t.Fatal("expected cache_control in system block")
	}

	if cc["type"] != "ephemeral" {
		t.Errorf("cache_control.type = %v, want ephemeral", cc["type"])
	}
}

func TestListModels(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		resp := map[string]any{
			"data": []map[string]any{
				{
					"id":           "claude-sonnet-4-6",
					"display_name": "Claude Sonnet 4.6",
					"created_at":   "2025-05-14T00:00:00Z",
					"type":         "model",
				},
				{
					"id":           "claude-haiku-4-5",
					"display_name": "Claude Haiku 4.5",
					"created_at":   "2025-10-01T00:00:00Z",
					"type":         "model",
				},
			},
			"has_more": false,
			"first_id": "claude-sonnet-4-6",
			"last_id":  "claude-haiku-4-5",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewAnthropicClient("test-key", newTestLogger(),
		option.WithBaseURL(server.URL),
	)

	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(models))
	}

	if models[0].ID != "claude-sonnet-4-6" {
		t.Errorf("models[0].ID = %q, want %q", models[0].ID, "claude-sonnet-4-6")
	}
	if models[0].DisplayName != "Claude Sonnet 4.6" {
		t.Errorf("models[0].DisplayName = %q, want %q", models[0].DisplayName, "Claude Sonnet 4.6")
	}
	wantTime := time.Date(2025, 5, 14, 0, 0, 0, 0, time.UTC)
	if !models[0].CreatedAt.Equal(wantTime) {
		t.Errorf("models[0].CreatedAt = %v, want %v", models[0].CreatedAt, wantTime)
	}

	if models[1].ID != "claude-haiku-4-5" {
		t.Errorf("models[1].ID = %q, want %q", models[1].ID, "claude-haiku-4-5")
	}
	if models[1].DisplayName != "Claude Haiku 4.5" {
		t.Errorf("models[1].DisplayName = %q, want %q", models[1].DisplayName, "Claude Haiku 4.5")
	}
}

// anthropicToolUseResponse builds a canned Anthropic API JSON response with a single tool_use block.
func anthropicToolUseResponse(id, name, inputJSON string, inputTokens, outputTokens int) string {
	return anthropicMultiToolResponse([]map[string]any{
		{"type": "tool_use", "id": id, "name": name, "input": json.RawMessage(inputJSON)},
	}, inputTokens, outputTokens)
}

// anthropicMultiToolResponse builds a canned response with multiple tool_use blocks.
func anthropicMultiToolResponse(tools []map[string]any, inputTokens, outputTokens int) string {
	resp := map[string]any{
		"id":            "msg_test",
		"type":          "message",
		"role":          "assistant",
		"content":       tools,
		"model":         "claude-sonnet-4-20250514",
		"stop_reason":   "tool_use",
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":                inputTokens,
			"cache_creation_input_tokens": 0,
			"cache_read_input_tokens":     0,
			"output_tokens":               outputTokens,
		},
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

func TestAnthropicAgentLoop_SingleTurn(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(anthropicResponse("done", 100, 0, 0, 20)))
	}))
	defer server.Close()

	client := NewAnthropicClient("test-key", newTestLogger(), option.WithBaseURL(server.URL))
	resp, err := client.AgentLoop(context.Background(), AgentRequest{
		SystemPrompt: "you are helpful",
		Messages:     []Message{{Role: "user", Content: "hello"}},
		Model:        "claude-sonnet-4-20250514",
		MaxTurns:     5,
	}, func(_ context.Context, _ ToolCall) (string, error) {
		t.Fatal("handler should not be called on end_turn")
		return "", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "done" {
		t.Errorf("content = %q, want %q", resp.Content, "done")
	}
	if resp.Turns != 1 {
		t.Errorf("turns = %d, want 1", resp.Turns)
	}
}

func TestAnthropicAgentLoop_MultiTurn(t *testing.T) {
	t.Parallel()
	var counter atomic.Int32
	var handlerCallID, handlerCallName string
	var handlerCallInput json.RawMessage

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		n := counter.Add(1)
		switch n {
		case 1:
			w.Write([]byte(anthropicToolUseResponse("call_1", "my_tool", `{"x":1}`, 100, 30)))
		default:
			w.Write([]byte(anthropicResponse("result", 120, 0, 0, 25)))
		}
	}))
	defer server.Close()

	client := NewAnthropicClient("test-key", newTestLogger(), option.WithBaseURL(server.URL))
	resp, err := client.AgentLoop(context.Background(), AgentRequest{
		Messages: []Message{{Role: "user", Content: "run tool"}},
		Model:    "claude-sonnet-4-20250514",
		MaxTurns: 5,
	}, func(_ context.Context, call ToolCall) (string, error) {
		handlerCallID = call.ID
		handlerCallName = call.Name
		handlerCallInput = call.Input
		return "tool output", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "result" {
		t.Errorf("content = %q, want %q", resp.Content, "result")
	}
	if resp.Turns != 2 {
		t.Errorf("turns = %d, want 2", resp.Turns)
	}
	if handlerCallID != "call_1" {
		t.Errorf("handler call ID = %q, want %q", handlerCallID, "call_1")
	}
	if handlerCallName != "my_tool" {
		t.Errorf("handler call name = %q, want %q", handlerCallName, "my_tool")
	}
	if string(handlerCallInput) != `{"x":1}` {
		t.Errorf("handler call input = %s, want %s", handlerCallInput, `{"x":1}`)
	}
}

func TestAnthropicAgentLoop_MultipleToolsPerTurn(t *testing.T) {
	t.Parallel()
	var counter atomic.Int32
	var mu sync.Mutex
	var handlerCalls []string // protected by mu; tools run in parallel

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		n := counter.Add(1)
		switch n {
		case 1:
			body := anthropicMultiToolResponse([]map[string]any{
				{"type": "tool_use", "id": "call_a", "name": "tool_a", "input": json.RawMessage(`{"q":"a"}`)},
				{"type": "tool_use", "id": "call_b", "name": "tool_b", "input": json.RawMessage(`{"q":"b"}`)},
			}, 100, 40)
			w.Write([]byte(body))
		default:
			w.Write([]byte(anthropicResponse("done", 150, 0, 0, 20)))
		}
	}))
	defer server.Close()

	client := NewAnthropicClient("test-key", newTestLogger(), option.WithBaseURL(server.URL))
	resp, err := client.AgentLoop(context.Background(), AgentRequest{
		Messages: []Message{{Role: "user", Content: "run two tools"}},
		Model:    "claude-sonnet-4-20250514",
		MaxTurns: 5,
	}, func(_ context.Context, call ToolCall) (string, error) {
		mu.Lock()
		handlerCalls = append(handlerCalls, call.Name)
		mu.Unlock()
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Turns != 2 {
		t.Errorf("turns = %d, want 2", resp.Turns)
	}
	if len(handlerCalls) != 2 {
		t.Fatalf("handler called %d times, want 2", len(handlerCalls))
	}
	// Tool calls run in parallel; order is non-deterministic.
	names := make(map[string]bool, len(handlerCalls))
	for _, n := range handlerCalls {
		names[n] = true
	}
	if !names["tool_a"] || !names["tool_b"] {
		t.Errorf("handler calls = %v, want both tool_a and tool_b", handlerCalls)
	}
}

func TestAnthropicAgentLoop_MaxTurnsExceeded(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(anthropicToolUseResponse("call_x", "tool_x", `{}`, 50, 20)))
	}))
	defer server.Close()

	client := NewAnthropicClient("test-key", newTestLogger(), option.WithBaseURL(server.URL))
	resp, err := client.AgentLoop(context.Background(), AgentRequest{
		Messages: []Message{{Role: "user", Content: "loop"}},
		Model:    "claude-sonnet-4-20250514",
		MaxTurns: 2,
	}, func(_ context.Context, _ ToolCall) (string, error) {
		return "ok", nil
	})
	if !errors.Is(err, ErrMaxTurnsExceeded) {
		t.Fatalf("expected ErrMaxTurnsExceeded, got %v", err)
	}
	if resp.Turns != 2 {
		t.Errorf("turns = %d, want 2", resp.Turns)
	}
}

func TestAnthropicAgentLoop_HandlerError(t *testing.T) {
	t.Parallel()
	var counter atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counter.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(anthropicToolUseResponse("call_e", "err_tool", `{}`, 50, 20)))
	}))
	defer server.Close()

	client := NewAnthropicClient("test-key", newTestLogger(), option.WithBaseURL(server.URL))
	_, err := client.AgentLoop(context.Background(), AgentRequest{
		Messages: []Message{{Role: "user", Content: "go"}},
		Model:    "claude-sonnet-4-20250514",
		MaxTurns: 5,
	}, func(_ context.Context, _ ToolCall) (string, error) {
		return "", errors.New("handler failed")
	})
	if err == nil || !strings.Contains(err.Error(), "handler failed") {
		t.Fatalf("expected error containing 'handler failed', got %v", err)
	}
	if n := counter.Load(); n != 1 {
		t.Errorf("server hit %d times, want 1 (aborts on handler error)", n)
	}
}

func TestBuildAgentToolParams_InvalidSchema(t *testing.T) {
	t.Parallel()
	tools := []ToolDef{
		{Name: "bad_tool", Description: "broken", InputSchema: json.RawMessage(`not valid json`)},
	}
	_, err := buildAgentToolParams(tools)
	if err == nil {
		t.Fatal("expected error for invalid schema JSON, got nil")
	}
	if !strings.Contains(err.Error(), "bad_tool") {
		t.Errorf("error = %q, want it to contain tool name %q", err.Error(), "bad_tool")
	}
}

func TestAnthropicAgentLoop_ContextCancellation(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(anthropicResponse("ok", 50, 0, 0, 10)))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before call

	client := NewAnthropicClient("test-key", newTestLogger(), option.WithBaseURL(server.URL))
	_, err := client.AgentLoop(ctx, AgentRequest{
		Messages: []Message{{Role: "user", Content: "hello"}},
		Model:    "claude-sonnet-4-20250514",
	}, func(_ context.Context, _ ToolCall) (string, error) { return "", nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestAnthropicAgentLoop_CostAccumulation(t *testing.T) {
	t.Parallel()
	var counter atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		n := counter.Add(1)
		switch n {
		case 1:
			w.Write([]byte(anthropicToolUseResponse("call_c", "cost_tool", `{}`, 100, 50)))
		default:
			w.Write([]byte(anthropicResponse("final", 200, 0, 0, 30)))
		}
	}))
	defer server.Close()

	client := NewAnthropicClient("test-key", newTestLogger(), option.WithBaseURL(server.URL))
	resp, err := client.AgentLoop(context.Background(), AgentRequest{
		Messages: []Message{{Role: "user", Content: "cost test"}},
		Model:    "claude-sonnet-4-20250514",
		MaxTurns: 5,
	}, func(_ context.Context, _ ToolCall) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.InputTokens != 300 {
		t.Errorf("input_tokens = %d, want 300", resp.InputTokens)
	}
	if resp.OutputTokens != 80 {
		t.Errorf("output_tokens = %d, want 80", resp.OutputTokens)
	}
	if resp.TotalCost <= 0 {
		t.Errorf("total_cost = %f, want > 0", resp.TotalCost)
	}
	if resp.Turns != 2 {
		t.Errorf("turns = %d, want 2", resp.Turns)
	}
}

func TestAnthropicAgentLoop_CacheControl(t *testing.T) {
	t.Parallel()
	var capturedBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &capturedBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(anthropicResponse("cached", 100, 0, 0, 10)))
	}))
	defer server.Close()

	client := NewAnthropicClient("test-key", newTestLogger(), option.WithBaseURL(server.URL))
	_, err := client.AgentLoop(context.Background(), AgentRequest{
		SystemPrompt: "system with cache",
		Messages:     []Message{{Role: "user", Content: "go"}},
		Model:        "claude-sonnet-4-20250514",
		CacheControl: &CacheControl{Type: "ephemeral"},
	}, func(_ context.Context, _ ToolCall) (string, error) { return "", nil })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	system, ok := capturedBody["system"].([]any)
	if !ok || len(system) == 0 {
		t.Fatal("expected system blocks in request body")
	}
	block, ok := system[0].(map[string]any)
	if !ok {
		t.Fatal("expected system block to be an object")
	}
	cc, ok := block["cache_control"].(map[string]any)
	if !ok {
		t.Fatal("expected cache_control in system block")
	}
	if cc["type"] != "ephemeral" {
		t.Errorf("cache_control.type = %v, want ephemeral", cc["type"])
	}
}

// TestJudgeRetries529 verifies that the SDK retries Anthropic's non-standard
// 529 "Overloaded" responses. With 48+ parallel judge calls during attractor
// runs, we routinely hit this under burst traffic.
func TestJudgeRetries529(t *testing.T) {
	t.Parallel()
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n <= 2 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(529)
			w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		judgeJSON := `{"score": 90, "reasoning": "good", "failures": []}`
		w.Write([]byte(anthropicResponse(judgeJSON, 50, 0, 0, 30)))
	}))
	defer server.Close()

	client := NewAnthropicClient("test-key", newTestLogger(),
		option.WithBaseURL(server.URL),
	)

	resp, err := client.Judge(context.Background(), JudgeRequest{
		SystemPrompt: "judge prompt",
		UserPrompt:   "evaluate this",
		Model:        "claude-haiku-4-5-20251001",
	})
	if err != nil {
		t.Fatalf("expected retry to succeed, got error: %v", err)
	}

	if resp.Score != 90 {
		t.Errorf("score = %d, want 90", resp.Score)
	}
	if got := attempts.Load(); got != 3 {
		t.Errorf("attempts = %d, want 3 (2 failures + 1 success)", got)
	}
}
