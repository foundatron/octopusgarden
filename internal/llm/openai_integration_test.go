//go:build integration

package llm

import (
	"context"
	"os"
	"testing"
	"time"
)

const openaiIntegrationModel = "gpt-4.1-nano"

func newOpenAIIntegrationClient(t *testing.T) *OpenAIClient {
	t.Helper()
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		t.Skip("OPENAI_API_KEY not set")
	}
	return NewOpenAIClient(key, "", false, newTestLogger())
}

func TestIntegrationOpenAIGenerate(t *testing.T) {
	client := newOpenAIIntegrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := client.Generate(ctx, GenerateRequest{
		SystemPrompt: "You are a helpful assistant.",
		Messages:     []Message{{Role: "user", Content: "Reply with the word 'hello'"}},
		Model:        openaiIntegrationModel,
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
	if resp.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want %q", resp.FinishReason, "stop")
	}
}

func TestIntegrationOpenAIJudge(t *testing.T) {
	client := newOpenAIIntegrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	systemPrompt := `You are a code satisfaction judge. Evaluate the provided output and respond with ONLY valid JSON in this exact format:
{"score": <integer 0-100>, "reasoning": "<brief explanation>", "failures": []}`

	resp, err := client.Judge(ctx, JudgeRequest{
		SystemPrompt: systemPrompt,
		UserPrompt:   "The function returns 42 as expected. Score this 100.",
		Model:        openaiIntegrationModel,
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

// TestIntegrationOpenAIGenerateFinishReasonLength verifies that a MaxTokens=1
// constraint forces a "length" finish reason from the API.
func TestIntegrationOpenAIGenerateFinishReasonLength(t *testing.T) {
	client := newOpenAIIntegrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := client.Generate(ctx, GenerateRequest{
		Messages:  []Message{{Role: "user", Content: "Write a long story about a dragon."}},
		Model:     openaiIntegrationModel,
		MaxTokens: 1,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if resp.FinishReason != "length" {
		t.Errorf("FinishReason = %q, want %q", resp.FinishReason, "length")
	}
}
