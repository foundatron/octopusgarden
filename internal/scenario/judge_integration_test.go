//go:build integration

package scenario

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/foundatron/octopusgarden/internal/llm"
)

// TestIntegrationJudgeScore calls a real LLM judge to score an observed vs expected pair.
// Skips if no API key is configured.
func TestIntegrationJudgeScore(t *testing.T) {
	client, model := resolveJudgeClient(t)

	judge := NewJudge(client, model, newTestLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	scenario := Scenario{
		ID:          "judge-integration",
		Description: "Simple echo check",
	}
	step := Step{
		Description: "GET /echo returns message",
		Expect:      "The response contains the word 'hello' and status code 200",
	}
	observed := "HTTP 200\nHeaders: {Content-Type: application/json}\nBody:\n{\"message\":\"hello\"}"

	score, err := judge.Score(ctx, scenario, step, observed)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}

	if score.Score < 0 || score.Score > 100 {
		t.Errorf("Score = %d, want 0-100", score.Score)
	}
	if score.Reasoning == "" {
		t.Error("Reasoning should not be empty")
	}
	t.Logf("score=%d reasoning=%s cost_usd=%.6f", score.Score, score.Reasoning, score.CostUSD)
}

// resolveJudgeClient returns an LLM client and a cheap model for judging.
// Skips the test if neither ANTHROPIC_API_KEY nor OPENAI_API_KEY is set.
func resolveJudgeClient(t *testing.T) (llm.Client, string) {
	t.Helper()
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		return llm.NewAnthropicClient(key, newTestLogger()), "claude-haiku-4-5-20251001"
	}
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		return llm.NewOpenAIClient(key, "", false, newTestLogger()), "gpt-4o-mini"
	}
	t.Skip("ANTHROPIC_API_KEY and OPENAI_API_KEY are both unset")
	return nil, ""
}
