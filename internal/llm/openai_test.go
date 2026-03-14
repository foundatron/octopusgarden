package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

// openaiResponse builds a canned OpenAI Chat Completion API JSON response.
func openaiResponse(content string, promptTokens, completionTokens, cachedTokens int, finishReason string) string {
	usage := map[string]any{
		"prompt_tokens":     promptTokens,
		"completion_tokens": completionTokens,
		"total_tokens":      promptTokens + completionTokens,
	}
	if cachedTokens > 0 {
		usage["prompt_tokens_details"] = map[string]any{
			"cached_tokens": cachedTokens,
		}
	}

	resp := map[string]any{
		"id":      "chatcmpl-test",
		"object":  "chat.completion",
		"created": 1700000000,
		"model":   "gpt-4o",
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": finishReason,
			},
		},
		"usage": usage,
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

func newOpenAITestClient(serverURL string, zeroCost bool) *OpenAIClient {
	return NewOpenAIClient("test-key", serverURL, zeroCost, newTestLogger())
}

func TestOpenAIGenerate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		content          string
		model            string
		promptTokens     int
		completionTokens int
		cachedTokens     int
		finishReason     string
		zeroCost         bool
		wantContent      string
		wantInputTokens  int
		wantOutputTokens int
		wantCacheHit     bool
		wantCost         float64
		wantFinishReason string
	}{
		{
			name:             "basic generation",
			content:          "generated code",
			model:            "gpt-4o",
			promptTokens:     100,
			completionTokens: 50,
			finishReason:     "stop",
			wantContent:      "generated code",
			wantInputTokens:  100,
			wantOutputTokens: 50,
			wantCacheHit:     false,
			// gpt-4o: (100 * 2.50 + 50 * 10.00) / 1_000_000
			wantCost:         100*2.50/1_000_000 + 50*10.00/1_000_000,
			wantFinishReason: "stop",
		},
		{
			name:             "finish reason length normalized to max_tokens",
			content:          "truncated output",
			model:            "gpt-4o",
			promptTokens:     100,
			completionTokens: 8192,
			finishReason:     "length",
			wantContent:      "truncated output",
			wantInputTokens:  100,
			wantOutputTokens: 8192,
			wantCacheHit:     false,
			wantCost:         (100*2.50 + 8192*10.00) / 1_000_000,
			wantFinishReason: "max_tokens",
		},
		{
			name:             "cache hit",
			content:          "cached response",
			model:            "gpt-4o",
			promptTokens:     100,
			completionTokens: 50,
			cachedTokens:     80,
			finishReason:     "stop",
			wantContent:      "cached response",
			wantInputTokens:  100,
			wantOutputTokens: 50,
			wantCacheHit:     true,
			// regular input = 100-80=20, cache read = 80, output = 50
			wantCost:         (20*2.50 + 80*1.25 + 50*10.00) / 1_000_000,
			wantFinishReason: "stop",
		},
		{
			name:             "zero cost",
			content:          "local model output",
			model:            "llama3",
			promptTokens:     100,
			completionTokens: 50,
			finishReason:     "stop",
			zeroCost:         true,
			wantContent:      "local model output",
			wantInputTokens:  100,
			wantOutputTokens: 50,
			wantCacheHit:     false,
			wantCost:         0,
			wantFinishReason: "stop",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(openaiResponse(tt.content, tt.promptTokens, tt.completionTokens, tt.cachedTokens, tt.finishReason)))
			}))
			defer server.Close()

			client := newOpenAITestClient(server.URL, tt.zeroCost)

			resp, err := client.Generate(context.Background(), GenerateRequest{
				Messages: []Message{{Role: "user", Content: "generate code"}},
				Model:    tt.model,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if resp.Content != tt.wantContent {
				t.Errorf("content = %q, want %q", resp.Content, tt.wantContent)
			}
			if resp.InputTokens != tt.wantInputTokens {
				t.Errorf("input_tokens = %d, want %d", resp.InputTokens, tt.wantInputTokens)
			}
			if resp.OutputTokens != tt.wantOutputTokens {
				t.Errorf("output_tokens = %d, want %d", resp.OutputTokens, tt.wantOutputTokens)
			}
			if resp.CacheHit != tt.wantCacheHit {
				t.Errorf("cache_hit = %v, want %v", resp.CacheHit, tt.wantCacheHit)
			}
			if math.Abs(resp.CostUSD-tt.wantCost) > 0.0001 {
				t.Errorf("cost_usd = %f, want %f", resp.CostUSD, tt.wantCost)
			}
			if resp.FinishReason != tt.wantFinishReason {
				t.Errorf("finish_reason = %q, want %q", resp.FinishReason, tt.wantFinishReason)
			}
		})
	}
}

func TestOpenAIGenerate_EmptySystemPrompt(t *testing.T) {
	t.Parallel()
	var capturedBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &capturedBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(openaiResponse("ok", 50, 10, 0, "stop")))
	}))
	defer server.Close()

	client := newOpenAITestClient(server.URL, false)

	_, err := client.Generate(context.Background(), GenerateRequest{
		SystemPrompt: "",
		Messages:     []Message{{Role: "user", Content: "hello"}},
		Model:        "gpt-4o",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify no developer/system message was sent.
	messages, ok := capturedBody["messages"].([]any)
	if !ok {
		t.Fatal("expected messages in request body")
	}
	for _, m := range messages {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role == "developer" || role == "system" {
			t.Errorf("unexpected %s message sent despite empty SystemPrompt", role)
		}
	}
}

func TestOpenAIJudge(t *testing.T) {
	t.Parallel()
	judgeJSON := `{"score": 85, "reasoning": "mostly correct", "failures": ["minor issue"]}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(openaiResponse(judgeJSON, 50, 30, 0, "stop")))
	}))
	defer server.Close()

	client := newOpenAITestClient(server.URL, false)

	resp, err := client.Judge(context.Background(), JudgeRequest{
		SystemPrompt: "judge prompt",
		UserPrompt:   "evaluate this",
		Model:        "gpt-4o-mini",
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

func TestOpenAIJudge_MalformedJSON(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(openaiResponse("this is not valid json at all", 50, 30, 0, "stop")))
	}))
	defer server.Close()

	client := newOpenAITestClient(server.URL, false)

	resp, err := client.Judge(context.Background(), JudgeRequest{
		SystemPrompt: "judge prompt",
		UserPrompt:   "evaluate this",
		Model:        "gpt-4o-mini",
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

func TestOpenAIGenerate_NoChoices(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"created": 1700000000,
			"model":   "gpt-4o",
			"choices": []map[string]any{},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 0, "total_tokens": 10},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := newOpenAITestClient(server.URL, false)

	_, err := client.Generate(context.Background(), GenerateRequest{
		Messages: []Message{{Role: "user", Content: "hello"}},
		Model:    "gpt-4o",
	})
	if !errors.Is(err, errNoChoices) {
		t.Errorf("err = %v, want %v", err, errNoChoices)
	}
}

func TestOpenAIListModels(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		resp := map[string]any{
			"object": "list",
			"data": []map[string]any{
				{
					"id":       "gpt-4o",
					"object":   "model",
					"created":  1700000000,
					"owned_by": "openai",
				},
				{
					"id":       "gpt-4o-mini",
					"object":   "model",
					"created":  1700100000,
					"owned_by": "openai",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := newOpenAITestClient(server.URL, false)

	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(models))
	}

	if models[0].ID != "gpt-4o" {
		t.Errorf("models[0].ID = %q, want %q", models[0].ID, "gpt-4o")
	}
	if models[0].DisplayName != "gpt-4o" {
		t.Errorf("models[0].DisplayName = %q, want %q", models[0].DisplayName, "gpt-4o")
	}
	if models[1].ID != "gpt-4o-mini" {
		t.Errorf("models[1].ID = %q, want %q", models[1].ID, "gpt-4o-mini")
	}
}

func TestOpenAIGenerate_APIError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"Rate limit exceeded","type":"rate_limit_error","code":"rate_limit_exceeded"}}`))
	}))
	defer server.Close()

	client := newOpenAITestClient(server.URL, false)

	_, err := client.Generate(context.Background(), GenerateRequest{
		Messages: []Message{{Role: "user", Content: "hello"}},
		Model:    "gpt-4o",
	})
	if err == nil {
		t.Fatal("expected error for 429 response, got nil")
	}
}
