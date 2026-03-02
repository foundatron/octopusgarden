package spec

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/foundatron/octopusgarden/internal/llm"
)

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
	}{
		{name: "empty", text: "", want: 0},
		{name: "short", text: "hello world", want: 2},
		{name: "100 chars", text: strings.Repeat("abcd", 25), want: 25},
		{name: "1000 chars", text: strings.Repeat("x", 1000), want: 250},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateTokens(tt.text)
			if got != tt.want {
				t.Errorf("EstimateTokens() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSelectContent(t *testing.T) {
	spec := &Spec{
		RawContent: strings.Repeat("x", 4000), // 1000 tokens
		Sections: []Section{
			{Heading: "Authentication", Level: 2, Content: strings.Repeat("a", 800)},
			{Heading: "Database", Level: 2, Content: strings.Repeat("b", 800)},
			{Heading: "API Endpoints", Level: 2, Content: strings.Repeat("c", 800)},
		},
	}

	ss := &SummarizedSpec{
		Spec: spec,
		Sections: []SectionSummary{
			{Heading: "Authentication", Summary: "Auth summary."},
			{Heading: "Database", Summary: "DB summary."},
			{Heading: "API Endpoints", Summary: "API summary."},
		},
		Outline:  "Short outline of spec.",
		Abstract: "Brief abstract.",
	}

	tests := []struct {
		name     string
		budget   int
		failures []string
		check    func(t *testing.T, content string)
	}{
		{
			name:   "full spec fits",
			budget: 2000,
			check: func(t *testing.T, content string) {
				t.Helper()
				if content != spec.RawContent {
					t.Error("expected full raw content when budget is large enough")
				}
			},
		},
		{
			name:     "section summaries with expansion",
			budget:   500,
			failures: []string{"authentication error"},
			check: func(t *testing.T, content string) {
				t.Helper()
				// Should contain full auth content and summaries for others.
				if !strings.Contains(content, strings.Repeat("a", 800)) {
					t.Error("expected full Authentication content for matched failure")
				}
				if !strings.Contains(content, "DB summary.") {
					t.Error("expected Database summary")
				}
			},
		},
		{
			name:     "outline with failure expansion",
			budget:   215,
			failures: []string{"authentication error"},
			check: func(t *testing.T, content string) {
				t.Helper()
				// Section summaries + expanded auth (218 tokens) won't fit in 215,
				// but outline + expanded auth (210 tokens) does.
				if !strings.Contains(content, "Short outline") {
					t.Error("expected outline content")
				}
				if !strings.Contains(content, strings.Repeat("a", 800)) {
					t.Error("expected full Authentication section expanded from failure match")
				}
			},
		},
		{
			name:   "abstract fallback",
			budget: 4,
			check: func(t *testing.T, content string) {
				t.Helper()
				if content != "Brief abstract." {
					t.Errorf("expected abstract fallback, got %q", content)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := SelectContent(ss, tt.budget, tt.failures)
			tt.check(t, content)
		})
	}
}

func TestExpandFailureSections(t *testing.T) {
	spec := &Spec{
		Sections: []Section{
			{Heading: "Authentication", Level: 2, Content: "Full auth content here."},
			{Heading: "Database", Level: 2, Content: "Full DB content here."},
			{Heading: "Pagination", Level: 2, Content: "Full pagination content."},
		},
	}
	summaries := []SectionSummary{
		{Heading: "Authentication", Summary: "Auth summary."},
		{Heading: "Database", Summary: "DB summary."},
		{Heading: "Pagination", Summary: "Pagination summary."},
	}

	tests := []struct {
		name     string
		failures []string
		wantFull []string // headings that should have full content
		wantSum  []string // headings that should have summary content
	}{
		{
			name:     "exact match",
			failures: []string{"authentication"},
			wantFull: []string{"Full auth content here."},
			wantSum:  []string{"DB summary.", "Pagination summary."},
		},
		{
			name:     "partial match in failure string",
			failures: []string{"the database connection failed"},
			wantFull: []string{"Full DB content here."},
			wantSum:  []string{"Auth summary.", "Pagination summary."},
		},
		{
			name:     "no match",
			failures: []string{"network timeout"},
			wantFull: nil,
			wantSum:  []string{"Auth summary.", "DB summary.", "Pagination summary."},
		},
		{
			name:     "multiple matches",
			failures: []string{"authentication error", "pagination broke"},
			wantFull: []string{"Full auth content here.", "Full pagination content."},
			wantSum:  []string{"DB summary."},
		},
		{
			name:     "case insensitive",
			failures: []string{"DATABASE error"},
			wantFull: []string{"Full DB content here."},
			wantSum:  []string{"Auth summary."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := expandFailureSections(spec, summaries, tt.failures)
			for _, want := range tt.wantFull {
				if !strings.Contains(result, want) {
					t.Errorf("expected full content %q in result", want)
				}
			}
			for _, want := range tt.wantSum {
				if !strings.Contains(result, want) {
					t.Errorf("expected summary %q in result", want)
				}
			}
		})
	}
}

type mockLLMClient struct {
	t          *testing.T
	generateFn func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error)
}

func (m *mockLLMClient) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	return m.generateFn(ctx, req)
}

func (m *mockLLMClient) Judge(_ context.Context, _ llm.JudgeRequest) (llm.JudgeResponse, error) {
	if m.t != nil {
		m.t.Error("Judge should not be called in spec summary tests")
	}
	return llm.JudgeResponse{}, nil
}

func TestSummarize(t *testing.T) {
	spec := &Spec{
		Title:       "Test API",
		Description: "A test API spec.",
		RawContent:  "# Test API\n\nA test API spec.\n\n## Auth\n\nAuth details.\n\n## DB\n\nDB details.",
		Sections: []Section{
			{Heading: "Test API", Level: 1, Content: "A test API spec."},
			{Heading: "Auth", Level: 2, Content: "Auth details."},
			{Heading: "DB", Level: 2, Content: "DB details."},
		},
	}

	llmResponse := `=== SECTION SUMMARIES ===
### Test API
This is a test API specification.

### Auth
Authentication is handled via tokens.

### DB
Database uses PostgreSQL.

=== OUTLINE ===
- Test API: Main specification
- Auth: Token-based authentication
- DB: PostgreSQL database

=== ABSTRACT ===
A test API that uses token authentication and PostgreSQL.`

	var capturedReq llm.GenerateRequest
	client := &mockLLMClient{
		t: t,
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			capturedReq = req
			return llm.GenerateResponse{Content: llmResponse, CostUSD: 0.001}, nil
		},
	}

	result, err := Summarize(context.Background(), spec, client, "claude-haiku-4-5-20251001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify prompt includes spec content.
	if !strings.Contains(capturedReq.Messages[0].Content, spec.RawContent) {
		t.Error("prompt should contain spec raw content")
	}

	// Verify model is passed through.
	if capturedReq.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("expected model claude-haiku-4-5-20251001, got %q", capturedReq.Model)
	}

	// Verify cost is returned.
	if result.CostUSD != 0.001 {
		t.Errorf("expected CostUSD 0.001, got %f", result.CostUSD)
	}

	ss := result.Summary

	// Verify section summaries parsed.
	if len(ss.Sections) != 3 {
		t.Fatalf("expected 3 section summaries, got %d", len(ss.Sections))
	}
	if ss.Sections[0].Heading != "Test API" {
		t.Errorf("expected heading 'Test API', got %q", ss.Sections[0].Heading)
	}
	if !strings.Contains(ss.Sections[1].Summary, "tokens") {
		t.Errorf("expected Auth summary to mention tokens, got %q", ss.Sections[1].Summary)
	}

	// Verify outline parsed.
	if !strings.Contains(ss.Outline, "Token-based") {
		t.Errorf("expected outline to mention Token-based, got %q", ss.Outline)
	}

	// Verify abstract parsed.
	if !strings.Contains(ss.Abstract, "token authentication") {
		t.Errorf("expected abstract to mention token authentication, got %q", ss.Abstract)
	}
}

func TestSummarizeNoSections(t *testing.T) {
	spec := &Spec{
		RawContent: "Just some raw content with no headings.",
	}

	client := &mockLLMClient{
		t: t,
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			t.Error("LLM should not be called for spec with no sections")
			return llm.GenerateResponse{}, nil
		},
	}

	result, err := Summarize(context.Background(), spec, client, "test-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Summary.Abstract != spec.RawContent {
		t.Errorf("expected abstract to be raw content for no-section spec")
	}
	if result.CostUSD != 0 {
		t.Errorf("expected zero cost for no-section spec, got %f", result.CostUSD)
	}
}

func TestSummarizeLLMError(t *testing.T) {
	spec := &Spec{
		RawContent: "# Title\n\nContent.",
		Sections:   []Section{{Heading: "Title", Level: 1, Content: "Content."}},
	}

	client := &mockLLMClient{
		t: t,
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{}, fmt.Errorf("API rate limit exceeded")
		},
	}

	_, err := Summarize(context.Background(), spec, client, "test-model")
	if err == nil {
		t.Fatal("expected error from LLM failure")
	}
	if !strings.Contains(err.Error(), "API rate limit exceeded") {
		t.Errorf("expected rate limit error, got: %v", err)
	}
}
