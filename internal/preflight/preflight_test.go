package preflight

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"testing"

	"github.com/foundatron/octopusgarden/internal/llm"
)

// mockClient is a minimal llm.Client for testing.
type mockClient struct {
	generateFn func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error)
	callCount  int
}

func (m *mockClient) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	m.callCount++
	if m.generateFn != nil {
		return m.generateFn(ctx, req)
	}
	return llm.GenerateResponse{}, nil
}

func (m *mockClient) Judge(_ context.Context, _ llm.JudgeRequest) (llm.JudgeResponse, error) {
	return llm.JudgeResponse{}, nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func makeJSONResponse(goal, constraint, success float64, questions string) string {
	return fmt.Sprintf(`{"goal_clarity":%g,"constraint_clarity":%g,"success_clarity":%g,"questions":%s}`,
		goal, constraint, success, questions)
}

func TestComputeAggregate(t *testing.T) {
	tests := []struct {
		name       string
		goal       float64
		constraint float64
		success    float64
		want       float64
	}{
		{"all ones", 1.0, 1.0, 1.0, 1.0},
		{"all zeros", 0.0, 0.0, 0.0, 0.0},
		{"mixed", 0.8, 0.6, 0.4, 0.8*0.4 + 0.6*0.3 + 0.4*0.3},
		{"goal heavy", 1.0, 0.0, 0.0, 0.4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := computeAggregate(tc.goal, tc.constraint, tc.success)
			if got != tc.want {
				t.Errorf("computeAggregate(%g, %g, %g) = %g, want %g",
					tc.goal, tc.constraint, tc.success, got, tc.want)
			}
		})
	}
}

func TestParseResponse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:  "valid JSON",
			input: makeJSONResponse(0.9, 0.8, 0.85, `{}`),
		},
		{
			name:  "JSON in code fences",
			input: "```json\n" + makeJSONResponse(0.9, 0.8, 0.85, `{}`) + "\n```",
		},
		{
			name:    "non-JSON",
			input:   "not json at all",
			wantErr: true,
		},
		{
			name:    "out-of-range score high",
			input:   makeJSONResponse(1.5, 0.8, 0.85, `{}`),
			wantErr: true,
		},
		{
			name:    "out-of-range score low",
			input:   makeJSONResponse(-0.1, 0.8, 0.85, `{}`),
			wantErr: true,
		},
		{
			name:  "missing questions field treated as empty",
			input: `{"goal_clarity":0.9,"constraint_clarity":0.8,"success_clarity":0.85}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseResponse(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !errors.Is(err, errMalformedResponse) {
					t.Errorf("expected errMalformedResponse, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got == nil {
				t.Fatal("expected non-nil result")
			}
		})
	}
}

func TestCheckPass(t *testing.T) {
	mock := &mockClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{
				Content: makeJSONResponse(0.95, 0.92, 0.90, `{}`),
			}, nil
		},
	}

	result, err := Check(context.Background(), mock, "test-model", "spec content", 0.8, testLogger())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !result.Pass {
		t.Errorf("expected Pass=true, got false (aggregate=%.4f)", result.AggregateScore)
	}
	if len(result.Questions) != 0 {
		t.Errorf("expected no questions, got %v", result.Questions)
	}
}

func TestCheckWarn(t *testing.T) {
	questions := `{"goal":["What is the purpose?"],"constraint":["What is the API?"],"success":["How to verify?"]}`
	mock := &mockClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{
				Content: makeJSONResponse(0.3, 0.4, 0.2, questions),
			}, nil
		},
	}

	result, err := Check(context.Background(), mock, "test-model", "vague spec", 0.8, testLogger())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if result.Pass {
		t.Errorf("expected Pass=false, got true (aggregate=%.4f)", result.AggregateScore)
	}
	if len(result.Questions) == 0 {
		t.Error("expected questions to be populated")
	}
}

func TestCheckQuestionsOnlyForLowDimensions(t *testing.T) {
	// goal is high (above threshold), constraint and success are low.
	questions := `{"goal":["Ignore me"],"constraint":["Constraint Q?"],"success":["Success Q?"]}`
	mock := &mockClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{
				Content: makeJSONResponse(0.95, 0.3, 0.4, questions),
			}, nil
		},
	}

	result, err := Check(context.Background(), mock, "test-model", "spec", 0.8, testLogger())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	for _, q := range result.Questions {
		if q == "[goal] Ignore me" {
			t.Error("questions for high-scoring dimension should not appear")
		}
	}

	foundConstraint := false
	foundSuccess := false
	for _, q := range result.Questions {
		if q == "[constraint] Constraint Q?" {
			foundConstraint = true
		}
		if q == "[success] Success Q?" {
			foundSuccess = true
		}
	}
	if !foundConstraint {
		t.Error("expected constraint question to appear")
	}
	if !foundSuccess {
		t.Error("expected success question to appear")
	}
}

func TestCheckMalformedResponse(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"non-JSON", "this is not json"},
		{"out-of-range score", makeJSONResponse(1.5, 0.8, 0.85, `{}`)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockClient{
				generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
					return llm.GenerateResponse{Content: tc.content}, nil
				},
			}
			_, err := Check(context.Background(), mock, "test-model", "spec", 0.8, testLogger())
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, errMalformedResponse) {
				t.Errorf("expected errMalformedResponse, got %v", err)
			}
		})
	}
}

func TestCheckTransportError(t *testing.T) {
	wantErr := errors.New("network failure")
	mock := &mockClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{}, wantErr
		},
	}
	_, err := Check(context.Background(), mock, "test-model", "spec", 0.8, testLogger())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("expected transport error to be wrapped, got %v", err)
	}
}

func TestCheckSingleLLMCall(t *testing.T) {
	mock := &mockClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{
				Content: makeJSONResponse(0.9, 0.9, 0.9, `{}`),
			}, nil
		},
	}

	_, err := Check(context.Background(), mock, "test-model", "spec", 0.8, testLogger())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if mock.callCount != 1 {
		t.Errorf("expected exactly 1 Generate call, got %d", mock.callCount)
	}
}

func TestCheckCustomThreshold(t *testing.T) {
	// Score that passes 0.5 but fails 0.8.
	mock := &mockClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{
				Content: makeJSONResponse(0.6, 0.6, 0.6, `{}`),
			}, nil
		},
	}

	resultLow, err := Check(context.Background(), mock, "test-model", "spec", 0.5, testLogger())
	if err != nil {
		t.Fatalf("Check (low threshold): %v", err)
	}
	if !resultLow.Pass {
		t.Errorf("expected Pass=true with threshold=0.5, aggregate=%.4f", resultLow.AggregateScore)
	}

	resultHigh, err := Check(context.Background(), mock, "test-model", "spec", 0.8, testLogger())
	if err != nil {
		t.Fatalf("Check (high threshold): %v", err)
	}
	if resultHigh.Pass {
		t.Errorf("expected Pass=false with threshold=0.8, aggregate=%.4f", resultHigh.AggregateScore)
	}
}
