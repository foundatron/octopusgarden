package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/foundatron/octopusgarden/internal/llm"
	"github.com/foundatron/octopusgarden/internal/scenario"
)

// mockLLMClient implements llm.Client for testing.
type mockLLMClient struct {
	judgeFn func(ctx context.Context, req llm.JudgeRequest) (llm.JudgeResponse, error)
}

func (m *mockLLMClient) Generate(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
	return llm.GenerateResponse{}, nil
}

func (m *mockLLMClient) Judge(ctx context.Context, req llm.JudgeRequest) (llm.JudgeResponse, error) {
	if m.judgeFn != nil {
		return m.judgeFn(ctx, req)
	}
	return llm.JudgeResponse{Score: 90}, nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestRunAndScore(t *testing.T) {
	// httptest server that returns items.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && r.URL.Path == "/items":
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"1","name":"test"}`)
		case r.Method == "GET" && r.URL.Path == "/items/1":
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"id":"1","name":"test"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	scenarios := []scenario.Scenario{
		{
			ID:          "create-read",
			Description: "Create and read an item",
			Steps: []scenario.Step{
				{
					Description: "Create an item",
					Request:     scenario.Request{Method: "POST", Path: "/items", Body: map[string]any{"name": "test"}},
					Expect:      "Should return 201 with item",
				},
				{
					Description: "Read the item",
					Request:     scenario.Request{Method: "GET", Path: "/items/1"},
					Expect:      "Should return 200 with item",
				},
			},
		},
	}

	callCount := 0
	mock := &mockLLMClient{
		judgeFn: func(_ context.Context, _ llm.JudgeRequest) (llm.JudgeResponse, error) {
			callCount++
			return llm.JudgeResponse{Score: 85, CostUSD: 0.001}, nil
		},
	}

	agg, err := runAndScore(context.Background(), scenarios, srv.URL, mock, testLogger())
	if err != nil {
		t.Fatalf("runAndScore: %v", err)
	}

	if callCount != 2 {
		t.Errorf("expected 2 judge calls, got %d", callCount)
	}
	if len(agg.Scenarios) != 1 {
		t.Fatalf("expected 1 scenario, got %d", len(agg.Scenarios))
	}
	if agg.Scenarios[0].ScenarioID != "create-read" {
		t.Errorf("expected scenario ID %q, got %q", "create-read", agg.Scenarios[0].ScenarioID)
	}
	if agg.Satisfaction != 85 {
		t.Errorf("expected satisfaction 85, got %.1f", agg.Satisfaction)
	}
	if len(agg.Scenarios[0].Steps) != 2 {
		t.Errorf("expected 2 scored steps, got %d", len(agg.Scenarios[0].Steps))
	}
}

func TestRunAndScoreSetupFailure(t *testing.T) {
	weight := 2.0
	scenarios := []scenario.Scenario{
		{
			ID:          "with-setup",
			Description: "Scenario with failing setup",
			Weight:      &weight,
			Setup: []scenario.Step{
				{
					Description: "Create prerequisite",
					Request:     scenario.Request{Method: "POST", Path: "/setup"},
					Expect:      "Should return 200",
				},
			},
			Steps: []scenario.Step{
				{
					Description: "Check result",
					Request:     scenario.Request{Method: "GET", Path: "/result"},
					Expect:      "Should return data",
				},
			},
		},
	}

	mock := &mockLLMClient{}
	// Use unreachable address to deterministically cause connection errors.
	agg, err := runAndScore(context.Background(), scenarios, "http://127.0.0.1:1", mock, testLogger())
	if err != nil {
		t.Fatalf("runAndScore: %v", err)
	}

	if len(agg.Scenarios) != 1 {
		t.Fatalf("expected 1 scenario, got %d", len(agg.Scenarios))
	}
	sc := agg.Scenarios[0]
	if sc.Score != 0 {
		t.Errorf("expected score 0 for setup failure, got %.1f", sc.Score)
	}
	if sc.Weight != 2.0 {
		t.Errorf("expected weight 2.0, got %.1f", sc.Weight)
	}
	if len(sc.Steps) != 0 {
		t.Errorf("expected no scored steps for setup failure, got %d", len(sc.Steps))
	}
}

func TestFprintValidationResult(t *testing.T) {
	agg := scenario.AggregateResult{
		Scenarios: []scenario.ScoredScenario{
			{
				ScenarioID: "crud",
				Weight:     1.0,
				Score:      92.5,
				Steps: []scenario.ScoredStep{
					{
						StepResult: scenario.StepResult{Description: "Create an item"},
						StepScore:  scenario.StepScore{Score: 95},
					},
					{
						StepResult: scenario.StepResult{Description: "Read the created item"},
						StepScore:  scenario.StepScore{Score: 90},
					},
				},
			},
			{
				ScenarioID: "validation",
				Weight:     1.0,
				Score:      45.0,
				Steps: []scenario.ScoredStep{
					{
						StepResult: scenario.StepResult{Description: "Create item with missing name"},
						StepScore:  scenario.StepScore{Score: 40},
					},
				},
			},
		},
		Satisfaction: 68.8,
		TotalCostUSD: 0.0042,
		Failures:     []string{"Missing field validation not returning 400"},
	}

	var buf bytes.Buffer
	fprintValidationResult(&buf, agg)
	out := buf.String()

	checks := []struct {
		name string
		want string
	}{
		{"header", "Scenarios:\n"},
		{"crud scenario", "crud"},
		{"crud score", "92.5/100"},
		{"crud weight", "(weight 1.0)"},
		{"pass step", "[PASS]   95  Create an item"},
		{"pass step 2", "[PASS]   90  Read the created item"},
		{"validation scenario", "validation"},
		{"validation score", "45.0/100"},
		{"fail step", "[FAIL]   40  Create item with missing name"},
		{"aggregate", "Aggregate satisfaction: 68.8/100"},
		{"cost", "Cost: $0.0042"},
		{"failures header", "Failures:\n"},
		{"failure detail", "  - Missing field validation not returning 400"},
	}

	for _, tc := range checks {
		if !strings.Contains(out, tc.want) {
			t.Errorf("%s: output missing %q\nfull output:\n%s", tc.name, tc.want, out)
		}
	}
}

func TestFprintValidationResultSetupFailure(t *testing.T) {
	agg := scenario.AggregateResult{
		Scenarios: []scenario.ScoredScenario{
			{
				ScenarioID: "broken",
				Weight:     1.0,
				Score:      0,
				// No steps — setup failed.
			},
		},
		Satisfaction: 0,
		TotalCostUSD: 0,
	}

	var buf bytes.Buffer
	fprintValidationResult(&buf, agg)
	out := buf.String()

	if !strings.Contains(out, "0.0/100") {
		t.Errorf("expected 0.0/100 for setup failure scenario, got:\n%s", out)
	}
}

func TestValidateThreshold(t *testing.T) {
	// httptest server that always returns 200.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case "GET":
			w.WriteHeader(http.StatusOK)
			resp, _ := json.Marshal(map[string]string{"status": "ok"})
			w.Write(resp)
		default:
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	scenarios := []scenario.Scenario{
		{
			ID:          "basic",
			Description: "Basic check",
			Steps: []scenario.Step{
				{
					Description: "Get status",
					Request:     scenario.Request{Method: "GET", Path: "/status"},
					Expect:      "Should return 200",
				},
			},
		},
	}

	tests := []struct {
		name      string
		score     int
		threshold float64
		wantErr   bool
	}{
		{
			name:      "above threshold",
			score:     95,
			threshold: 90,
			wantErr:   false,
		},
		{
			name:      "below threshold",
			score:     60,
			threshold: 90,
			wantErr:   true,
		},
		{
			name:      "zero threshold disables check",
			score:     10,
			threshold: 0,
			wantErr:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockLLMClient{
				judgeFn: func(_ context.Context, _ llm.JudgeRequest) (llm.JudgeResponse, error) {
					return llm.JudgeResponse{Score: tc.score, CostUSD: 0.001}, nil
				},
			}

			agg, err := runAndScore(context.Background(), scenarios, srv.URL, mock, testLogger())
			if err != nil {
				t.Fatalf("runAndScore: %v", err)
			}

			// Simulate validateCmd threshold logic.
			var thresholdErr error
			if tc.threshold > 0 && agg.Satisfaction < tc.threshold {
				thresholdErr = fmt.Errorf("%w: %.1f < %.1f", errBelowThreshold, agg.Satisfaction, tc.threshold)
			}

			if tc.wantErr && !errors.Is(thresholdErr, errBelowThreshold) {
				t.Errorf("expected errBelowThreshold, got %v", thresholdErr)
			}
			if !tc.wantErr && thresholdErr != nil {
				t.Errorf("unexpected error: %v", thresholdErr)
			}
		})
	}
}
