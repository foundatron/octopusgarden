//go:build integration

package llm

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

const anthropicIntegrationModel = "claude-haiku-4-5-20251001"

func newAnthropicIntegrationClient(t *testing.T) *AnthropicClient {
	t.Helper()
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}
	return NewAnthropicClient(key, newTestLogger())
}

func TestIntegrationAnthropicGenerate(t *testing.T) {
	client := newAnthropicIntegrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := client.Generate(ctx, GenerateRequest{
		SystemPrompt: "You are a helpful assistant.",
		Messages:     []Message{{Role: "user", Content: "Reply with the word 'hello'"}},
		Model:        anthropicIntegrationModel,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if resp.Content == "" {
		t.Error("Content is empty")
	}
	if resp.InputTokens <= 0 {
		t.Errorf("InputTokens = %d, want > 0", resp.InputTokens)
	}
	if resp.OutputTokens <= 0 {
		t.Errorf("OutputTokens = %d, want > 0", resp.OutputTokens)
	}
	if resp.CostUSD <= 0 {
		t.Errorf("CostUSD = %f, want > 0", resp.CostUSD)
	}
	if resp.FinishReason != "end_turn" {
		t.Errorf("FinishReason = %q, want %q", resp.FinishReason, "end_turn")
	}
}

func TestIntegrationAnthropicJudge(t *testing.T) {
	client := newAnthropicIntegrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	systemPrompt := `You are a code satisfaction judge. Evaluate the provided output and respond with ONLY valid JSON in this exact format:
{"score": <integer 0-100>, "reasoning": "<brief explanation>", "failures": []}`

	resp, err := client.Judge(ctx, JudgeRequest{
		SystemPrompt: systemPrompt,
		UserPrompt:   "The function returns 42 as expected. Score this 100.",
		Model:        anthropicIntegrationModel,
	})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}

	if resp.Score < 0 || resp.Score > 100 {
		t.Errorf("Score = %d, want 0-100", resp.Score)
	}
	if resp.Reasoning == "" {
		t.Error("Reasoning is empty")
	}
	if resp.CostUSD <= 0 {
		t.Errorf("CostUSD = %f, want > 0", resp.CostUSD)
	}
}

// TestIntegrationAnthropicGenerateCacheControl verifies that prompt caching
// works end-to-end. Cache hits are best-effort (require ≥1024 tokens and TTL
// constraints), so a missing cache hit is logged but not a hard failure.
func TestIntegrationAnthropicGenerateCacheControl(t *testing.T) {
	client := newAnthropicIntegrationClient(t)

	// Build a system prompt well above Anthropic's 1024-token minimum for caching.
	// Each repetition is ~50 tokens; 30 repetitions ≈ 1500 tokens.
	chunk := "You are a highly capable software engineer assistant. Your task is to generate clean, correct, well-structured Go code that satisfies the specification provided. Follow idiomatic Go conventions, handle all errors explicitly, and write code that is easy to maintain. "
	systemPrompt := strings.Repeat(chunk, 30)

	userMsg := []Message{{Role: "user", Content: "Reply with the word 'hello'"}}

	// First call — populates the cache.
	ctx1, cancel1 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel1()

	resp1, err := client.Generate(ctx1, GenerateRequest{
		SystemPrompt: systemPrompt,
		Messages:     userMsg,
		Model:        anthropicIntegrationModel,
		CacheControl: &CacheControl{Type: "ephemeral"},
	})
	if err != nil {
		t.Fatalf("Generate (first call): %v", err)
	}
	t.Logf("first call: cache_hit=%v input_tokens=%d cost_usd=%.6f", resp1.CacheHit, resp1.InputTokens, resp1.CostUSD)

	// Second call — should hit the cache.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel2()

	resp2, err := client.Generate(ctx2, GenerateRequest{
		SystemPrompt: systemPrompt,
		Messages:     userMsg,
		Model:        anthropicIntegrationModel,
		CacheControl: &CacheControl{Type: "ephemeral"},
	})
	if err != nil {
		t.Fatalf("Generate (second call): %v", err)
	}
	t.Logf("second call: cache_hit=%v input_tokens=%d cost_usd=%.6f", resp2.CacheHit, resp2.InputTokens, resp2.CostUSD)

	// Soft assertion: cache hits are best-effort depending on API state and TTL.
	if !resp2.CacheHit {
		t.Log("WARNING: second call did not get a cache hit; this may be a transient API condition")
	}
}
