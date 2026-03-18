package interview

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/foundatron/octopusgarden/internal/llm"
)

func TestScorerHappyPath(t *testing.T) {
	t.Parallel()
	var capturedReq llm.GenerateRequest
	client := &mockClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			capturedReq = req
			return llm.GenerateResponse{
				Content: `{"dimensions":[` +
					`{"name":"behavioral_completeness","score":85,"gaps":["Missing error for expired token"]},` +
					`{"name":"interface_precision","score":70,"gaps":["Type of created_at unspecified"]},` +
					`{"name":"defaults_and_boundaries","score":60,"gaps":["Max payload size missing"]},` +
					`{"name":"acceptance_criteria","score":90,"gaps":[]},` +
					`{"name":"economy","score":75,"gaps":[]}` +
					`]}`,
				CostUSD: 0.05,
			}, nil
		},
	}

	scorer := newScorer(client, "test-model")
	result, err := scorer.Score(context.Background(), "# My Spec\n\nSome content.")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Cache control should be set.
	if capturedReq.CacheControl == nil || capturedReq.CacheControl.Type != "ephemeral" {
		t.Errorf("expected CacheControl ephemeral, got %+v", capturedReq.CacheControl)
	}

	if result.CostUSD != 0.05 {
		t.Errorf("CostUSD = %v, want 0.05", result.CostUSD)
	}

	if len(result.Dimensions) != 5 {
		t.Fatalf("expected 5 dimensions, got %d", len(result.Dimensions))
	}

	// Find behavioral_completeness and verify gaps.
	var found bool
	for _, d := range result.Dimensions {
		if d.Name == "behavioral_completeness" {
			found = true
			if d.Score != 85 {
				t.Errorf("behavioral_completeness score = %d, want 85", d.Score)
			}
			if d.Weight != 0.25 {
				t.Errorf("behavioral_completeness weight = %v, want 0.25", d.Weight)
			}
			if len(d.Gaps) != 1 || d.Gaps[0] != "Missing error for expired token" {
				t.Errorf("unexpected gaps: %v", d.Gaps)
			}
		}
	}
	if !found {
		t.Error("behavioral_completeness dimension missing from result")
	}

	// Overall: 85*0.25 + 70*0.25 + 60*0.20 + 90*0.20 + 75*0.10
	// = 21.25 + 17.5 + 12 + 18 + 7.5 = 76.25 → 76
	if result.Overall != 76 {
		t.Errorf("Overall = %d, want 76", result.Overall)
	}
}

func TestScorerEmptySpec(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"whitespace", "   "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := tc.input
			t.Parallel()
			calls := 0
			client := &mockClient{
				generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
					calls++
					return llm.GenerateResponse{}, nil
				},
			}
			scorer := newScorer(client, "test-model")
			_, err := scorer.Score(context.Background(), input)
			if !errors.Is(err, errEmptySpec) {
				t.Errorf("expected errEmptySpec, got %v", err)
			}
			if calls != 0 {
				t.Errorf("expected 0 LLM calls, got %d", calls)
			}
		})
	}
}

func TestScorerLLMError(t *testing.T) {
	t.Parallel()
	errAPI := errors.New("api failure")
	client := &mockClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{}, errAPI
		},
	}
	scorer := newScorer(client, "test-model")
	_, err := scorer.Score(context.Background(), "some spec")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.HasPrefix(err.Error(), "scorer:") {
		t.Errorf("expected 'scorer:' prefix, got %q", err.Error())
	}
	if !errors.Is(err, errAPI) {
		t.Errorf("expected wrapped errAPI, got %v", err)
	}
}

func TestScorerMalformedJSON(t *testing.T) {
	t.Parallel()
	client := &mockClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: "not json"}, nil
		},
	}
	scorer := newScorer(client, "test-model")
	_, err := scorer.Score(context.Background(), "some spec")
	if !errors.Is(err, errMalformedResponse) {
		t.Errorf("expected errMalformedResponse, got %v", err)
	}
}

func TestScorerMissingDimensions(t *testing.T) {
	t.Parallel()
	// Only 3 of 5 dimensions returned.
	client := &mockClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{
				Content: `{"dimensions":[` +
					`{"name":"behavioral_completeness","score":80,"gaps":[]},` +
					`{"name":"interface_precision","score":70,"gaps":[]},` +
					`{"name":"economy","score":90,"gaps":[]}` +
					`]}`,
			}, nil
		},
	}
	scorer := newScorer(client, "test-model")
	_, err := scorer.Score(context.Background(), "some spec")
	if !errors.Is(err, errIncompleteDimensions) {
		t.Errorf("expected errIncompleteDimensions, got %v", err)
	}
}

func TestScorerScoreClamping(t *testing.T) {
	t.Parallel()
	client := &mockClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{
				Content: `{"dimensions":[` +
					`{"name":"behavioral_completeness","score":150,"gaps":[]},` +
					`{"name":"interface_precision","score":-10,"gaps":[]},` +
					`{"name":"defaults_and_boundaries","score":80,"gaps":[]},` +
					`{"name":"acceptance_criteria","score":80,"gaps":[]},` +
					`{"name":"economy","score":80,"gaps":[]}` +
					`]}`,
			}, nil
		},
	}
	scorer := newScorer(client, "test-model")
	result, err := scorer.Score(context.Background(), "some spec")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, d := range result.Dimensions {
		if d.Score < 0 || d.Score > 100 {
			t.Errorf("dimension %q has out-of-range score %d", d.Name, d.Score)
		}
		if d.Name == "behavioral_completeness" && d.Score != 100 {
			t.Errorf("expected clamped score 100, got %d", d.Score)
		}
		if d.Name == "interface_precision" && d.Score != 0 {
			t.Errorf("expected clamped score 0, got %d", d.Score)
		}
	}
}

func TestScorerWeightedAverage(t *testing.T) {
	t.Parallel()
	// behavioral_completeness=80, interface_precision=60, defaults_and_boundaries=50,
	// acceptance_criteria=70, economy=90
	// Expected: 80*0.25 + 60*0.25 + 50*0.20 + 70*0.20 + 90*0.10
	//         = 20 + 15 + 10 + 14 + 9 = 68
	client := &mockClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{
				Content: `{"dimensions":[` +
					`{"name":"behavioral_completeness","score":80,"gaps":[]},` +
					`{"name":"interface_precision","score":60,"gaps":[]},` +
					`{"name":"defaults_and_boundaries","score":50,"gaps":[]},` +
					`{"name":"acceptance_criteria","score":70,"gaps":[]},` +
					`{"name":"economy","score":90,"gaps":[]}` +
					`]}`,
			}, nil
		},
	}
	scorer := newScorer(client, "test-model")
	result, err := scorer.Score(context.Background(), "some spec")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Overall != 68 {
		t.Errorf("Overall = %d, want 68", result.Overall)
	}
}

func TestScorerUnknownDimensionName(t *testing.T) {
	t.Parallel()
	// 5 dimensions returned but one has an unrecognized name.
	client := &mockClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{
				Content: `{"dimensions":[` +
					`{"name":"behavioral_completeness","score":80,"gaps":[]},` +
					`{"name":"interface_precision","score":70,"gaps":[]},` +
					`{"name":"defaults_and_boundaries","score":60,"gaps":[]},` +
					`{"name":"acceptance_criteria","score":90,"gaps":[]},` +
					`{"name":"unknown_dimension","score":75,"gaps":[]}` +
					`]}`,
			}, nil
		},
	}
	scorer := newScorer(client, "test-model")
	_, err := scorer.Score(context.Background(), "some spec")
	if !errors.Is(err, errIncompleteDimensions) {
		t.Errorf("expected errIncompleteDimensions for unknown dimension, got %v", err)
	}
}
