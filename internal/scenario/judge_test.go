package scenario

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/foundatron/octopusgarden/internal/llm"
)

// mockClient implements llm.Client for testing.
type mockClient struct {
	judgeFunc func(ctx context.Context, req llm.JudgeRequest) (llm.JudgeResponse, error)
}

func (m *mockClient) Generate(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
	return llm.GenerateResponse{}, nil
}

func (m *mockClient) Judge(ctx context.Context, req llm.JudgeRequest) (llm.JudgeResponse, error) {
	return m.judgeFunc(ctx, req)
}

func TestJudgeScorePerfect(t *testing.T) {
	client := &mockClient{
		judgeFunc: func(_ context.Context, _ llm.JudgeRequest) (llm.JudgeResponse, error) {
			return llm.JudgeResponse{
				Score:     100,
				Reasoning: "Perfect match",
				CostUSD:   0.001,
			}, nil
		},
	}

	judge := NewJudge(client, "test-model", newTestLogger())
	score, err := judge.Score(context.Background(), Scenario{Description: "Test"}, Step{Description: "Step 1", Expect: "200 OK"}, HTTPResponse{Status: 200, Body: "ok"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if score.Score != 100 {
		t.Errorf("got score %d, want 100", score.Score)
	}
	if score.CostUSD != 0.001 {
		t.Errorf("got cost %f, want 0.001", score.CostUSD)
	}
}

func TestJudgeScorePartialWithFailures(t *testing.T) {
	client := &mockClient{
		judgeFunc: func(_ context.Context, _ llm.JudgeRequest) (llm.JudgeResponse, error) {
			return llm.JudgeResponse{
				Score:     60,
				Reasoning: "Partial match",
				Failures:  []string{"missing field", "wrong status"},
				CostUSD:   0.002,
			}, nil
		},
	}

	judge := NewJudge(client, "test-model", newTestLogger())
	score, err := judge.Score(context.Background(), Scenario{Description: "Test"}, Step{Description: "Step 1", Expect: "full response"}, HTTPResponse{Status: 404, Body: "not found"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if score.Score != 60 {
		t.Errorf("got score %d, want 60", score.Score)
	}
	if len(score.Failures) != 2 {
		t.Errorf("got %d failures, want 2", len(score.Failures))
	}
}

func TestJudgeScoreLLMError(t *testing.T) {
	client := &mockClient{
		judgeFunc: func(_ context.Context, _ llm.JudgeRequest) (llm.JudgeResponse, error) {
			return llm.JudgeResponse{}, errors.New("LLM unavailable")
		},
	}

	judge := NewJudge(client, "test-model", newTestLogger())
	_, err := judge.Score(context.Background(), Scenario{Description: "Test"}, Step{Description: "Step 1", Expect: "ok"}, HTTPResponse{Status: 200, Body: "ok"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestJudgeScorePromptContainsExpected(t *testing.T) {
	var gotUserPrompt string
	client := &mockClient{
		judgeFunc: func(_ context.Context, req llm.JudgeRequest) (llm.JudgeResponse, error) {
			gotUserPrompt = req.UserPrompt
			return llm.JudgeResponse{Score: 100}, nil
		},
	}

	judge := NewJudge(client, "test-model", newTestLogger())
	_, err := judge.Score(context.Background(),
		Scenario{Description: "My scenario"},
		Step{Description: "My step", Expect: "Returns 200 with data"},
		HTTPResponse{Status: 200, Body: `{"data": true}`},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(gotUserPrompt, "My scenario") {
		t.Error("user prompt missing scenario description")
	}
	if !strings.Contains(gotUserPrompt, "My step") {
		t.Error("user prompt missing step description")
	}
	if !strings.Contains(gotUserPrompt, "Returns 200 with data") {
		t.Error("user prompt missing expected behavior")
	}
	if !strings.Contains(gotUserPrompt, "200") {
		t.Error("user prompt missing observed status")
	}
}

func TestJudgeScoreScenarioAveraging(t *testing.T) {
	callIdx := 0
	scores := []int{80, 60}
	client := &mockClient{
		judgeFunc: func(_ context.Context, _ llm.JudgeRequest) (llm.JudgeResponse, error) {
			s := scores[callIdx]
			callIdx++
			return llm.JudgeResponse{Score: s, CostUSD: 0.001}, nil
		},
	}

	weight := 2.0
	sc := Scenario{
		ID:     "avg-test",
		Weight: &weight,
		Steps: []Step{
			{Description: "Step 1", Expect: "ok"},
			{Description: "Step 2", Expect: "ok"},
		},
	}
	result := Result{
		ScenarioID: "avg-test",
		Steps: []StepResult{
			{Description: "Step 1", Response: HTTPResponse{Status: 200, Body: "ok"}},
			{Description: "Step 2", Response: HTTPResponse{Status: 200, Body: "ok"}},
		},
	}

	judge := NewJudge(client, "test-model", newTestLogger())
	scored, err := judge.ScoreScenario(context.Background(), sc, result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if scored.Weight != 2.0 {
		t.Errorf("got weight %f, want 2.0", scored.Weight)
	}

	// Average of 80 and 60 = 70
	if scored.Score != 70.0 {
		t.Errorf("got score %f, want 70.0", scored.Score)
	}
}

func TestJudgeScoreScenarioTransportError(t *testing.T) {
	client := &mockClient{
		judgeFunc: func(_ context.Context, _ llm.JudgeRequest) (llm.JudgeResponse, error) {
			return llm.JudgeResponse{Score: 100, CostUSD: 0.001}, nil
		},
	}

	sc := Scenario{
		ID: "err-test",
		Steps: []Step{
			{Description: "Failing step", Expect: "ok"},
			{Description: "Good step", Expect: "ok"},
		},
	}
	result := Result{
		ScenarioID: "err-test",
		Steps: []StepResult{
			{Description: "Failing step", Err: errors.New("connection refused")},
			{Description: "Good step", Response: HTTPResponse{Status: 200, Body: "ok"}},
		},
	}

	judge := NewJudge(client, "test-model", newTestLogger())
	scored, err := judge.ScoreScenario(context.Background(), sc, result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if scored.Steps[0].StepScore.Score != 0 {
		t.Errorf("transport error step score = %d, want 0", scored.Steps[0].StepScore.Score)
	}
	if scored.Steps[1].StepScore.Score != 100 {
		t.Errorf("good step score = %d, want 100", scored.Steps[1].StepScore.Score)
	}
}

func TestJudgeScoreScenarioStepCountMismatch(t *testing.T) {
	client := &mockClient{
		judgeFunc: func(_ context.Context, _ llm.JudgeRequest) (llm.JudgeResponse, error) {
			return llm.JudgeResponse{}, nil
		},
	}

	sc := Scenario{
		ID: "mismatch",
		Steps: []Step{
			{Description: "Step 1", Expect: "ok"},
		},
	}
	result := Result{
		ScenarioID: "mismatch",
		Steps: []StepResult{
			{Description: "Step 1"},
			{Description: "Step 2"},
		},
	}

	judge := NewJudge(client, "test-model", newTestLogger())
	_, err := judge.ScoreScenario(context.Background(), sc, result)
	if err == nil {
		t.Fatal("expected error for step count mismatch")
	}
	if !errors.Is(err, errStepCountMismatch) {
		t.Errorf("expected errStepCountMismatch, got: %v", err)
	}
}

func TestAggregateSingleScenario(t *testing.T) {
	scenarios := []ScoredScenario{
		{
			ScenarioID: "s1",
			Weight:     1.0,
			Score:      80.0,
			Steps: []ScoredStep{
				{StepScore: StepScore{Score: 80, CostUSD: 0.001}},
			},
		},
	}

	result := Aggregate(scenarios)
	if result.Satisfaction != 80.0 {
		t.Errorf("got satisfaction %f, want 80.0", result.Satisfaction)
	}
	if result.TotalCostUSD != 0.001 {
		t.Errorf("got cost %f, want 0.001", result.TotalCostUSD)
	}
}

func TestAggregateUnequalWeights(t *testing.T) {
	scenarios := []ScoredScenario{
		{ScenarioID: "s1", Weight: 3.0, Score: 100.0, Steps: []ScoredStep{{StepScore: StepScore{Score: 100, CostUSD: 0.001}}}},
		{ScenarioID: "s2", Weight: 1.0, Score: 0.0, Steps: []ScoredStep{{StepScore: StepScore{Score: 0, CostUSD: 0.001}}}},
	}

	result := Aggregate(scenarios)
	// Weighted: (100*3 + 0*1) / (3+1) = 75
	if result.Satisfaction != 75.0 {
		t.Errorf("got satisfaction %f, want 75.0", result.Satisfaction)
	}
}

func TestAggregateZeroWeight(t *testing.T) {
	scenarios := []ScoredScenario{
		{ScenarioID: "s1", Weight: 0.0, Score: 100.0},
	}

	result := Aggregate(scenarios)
	if result.Satisfaction != 0.0 {
		t.Errorf("got satisfaction %f, want 0.0 for zero weight", result.Satisfaction)
	}
}

func TestAggregateFailureDedup(t *testing.T) {
	scenarios := []ScoredScenario{
		{
			ScenarioID: "s1",
			Weight:     1.0,
			Score:      50.0,
			Steps: []ScoredStep{
				{StepScore: StepScore{Failures: []string{"missing field", "wrong status"}}},
			},
		},
		{
			ScenarioID: "s2",
			Weight:     1.0,
			Score:      50.0,
			Steps: []ScoredStep{
				{StepScore: StepScore{Failures: []string{"wrong status", "bad format"}}},
			},
		},
	}

	result := Aggregate(scenarios)
	if len(result.Failures) != 3 {
		t.Fatalf("got %d failures, want 3 (deduplicated)", len(result.Failures))
	}
	// Should be sorted.
	expected := []string{"bad format", "missing field", "wrong status"}
	for i, f := range expected {
		if result.Failures[i] != f {
			t.Errorf("failure[%d] = %q, want %q", i, result.Failures[i], f)
		}
	}
}

func TestAggregateEmpty(t *testing.T) {
	result := Aggregate(nil)
	if result.Satisfaction != 0 {
		t.Errorf("got satisfaction %f, want 0", result.Satisfaction)
	}
	if len(result.Scenarios) != 0 {
		t.Errorf("got %d scenarios, want 0", len(result.Scenarios))
	}
}
