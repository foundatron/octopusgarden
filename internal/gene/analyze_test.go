package gene

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/foundatron/octopusgarden/internal/llm"
	"github.com/foundatron/octopusgarden/internal/spec"
)

type mockClient struct {
	captured *llm.GenerateRequest
	resp     llm.GenerateResponse
	err      error
}

func (m *mockClient) Generate(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	m.captured = &req
	return m.resp, m.err
}

func (m *mockClient) Judge(_ context.Context, _ llm.JudgeRequest) (llm.JudgeResponse, error) {
	return llm.JudgeResponse{}, nil
}

func testAnalyzeLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func testScanResult() ScanResult {
	return ScanResult{
		Language: "go",
		Files: []SelectedFile{
			{Path: "go.mod", Content: "module example\n", Role: "marker"},
			{Path: "main.go", Content: "package main\nfunc main() {}\n", Role: "entrypoint"},
		},
	}
}

const cannedGuide = `**PATTERN** — Layered HTTP server.
**INVARIANTS** — Errors wrapped with fmt.Errorf.
**EDGE CASES** — Timeouts via context.
**STACK** — Go 1.24, net/http.
**STRUCTURE** — cmd/ for entrypoints.
**BOOT** — main.go creates server.
**BUILD** — go build ./cmd/...`

func TestAnalyzeBasic(t *testing.T) {
	mock := &mockClient{resp: llm.GenerateResponse{Content: cannedGuide, InputTokens: 100, OutputTokens: 50, CostUSD: 0.001}}
	g, err := Analyze(context.Background(), testAnalyzeLogger(), mock, "claude-haiku-4-5", "/src/example", testScanResult())
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if g.Guide != cannedGuide {
		t.Errorf("Guide = %q, want %q", g.Guide, cannedGuide)
	}
	if g.Language != "go" {
		t.Errorf("Language = %q, want %q", g.Language, "go")
	}
	if g.Source != "/src/example" {
		t.Errorf("Source = %q, want %q", g.Source, "/src/example")
	}
}

func TestAnalyzePromptStructure(t *testing.T) {
	mock := &mockClient{resp: llm.GenerateResponse{Content: cannedGuide}}
	_, err := Analyze(context.Background(), testAnalyzeLogger(), mock, "claude-haiku-4-5", "/src", testScanResult())
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	sections := []string{"PATTERN", "INVARIANTS", "EDGE CASES", "STACK", "STRUCTURE", "BOOT", "BUILD"}
	for _, s := range sections {
		if !strings.Contains(mock.captured.SystemPrompt, s) {
			t.Errorf("system prompt missing section %q", s)
		}
	}
}

func TestAnalyzePromptContainsFiles(t *testing.T) {
	mock := &mockClient{resp: llm.GenerateResponse{Content: cannedGuide}}
	_, err := Analyze(context.Background(), testAnalyzeLogger(), mock, "claude-haiku-4-5", "/src", testScanResult())
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	msg := mock.captured.Messages[0].Content
	if !strings.Contains(msg, "=== FILE: go.mod (marker) ===") {
		t.Error("user message missing go.mod file delimiter")
	}
	if !strings.Contains(msg, "module example") {
		t.Error("user message missing go.mod content")
	}
	if !strings.Contains(msg, "=== FILE: main.go (entrypoint) ===") {
		t.Error("user message missing main.go file delimiter")
	}
	if !strings.Contains(msg, "=== END FILE ===") {
		t.Error("user message missing END FILE delimiter")
	}
}

func TestAnalyzePromptContainsLanguage(t *testing.T) {
	mock := &mockClient{resp: llm.GenerateResponse{Content: cannedGuide}}
	_, err := Analyze(context.Background(), testAnalyzeLogger(), mock, "claude-haiku-4-5", "/src", testScanResult())
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	msg := mock.captured.Messages[0].Content
	if !strings.Contains(msg, "Detected language: go") {
		t.Error("user message missing detected language")
	}
}

func TestAnalyzeSetsVersion(t *testing.T) {
	mock := &mockClient{resp: llm.GenerateResponse{Content: cannedGuide}}
	g, err := Analyze(context.Background(), testAnalyzeLogger(), mock, "claude-haiku-4-5", "/src", testScanResult())
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if g.Version != 1 {
		t.Errorf("Version = %d, want 1", g.Version)
	}
}

func TestAnalyzeSetsExtractedAt(t *testing.T) {
	before := time.Now()
	mock := &mockClient{resp: llm.GenerateResponse{Content: cannedGuide}}
	g, err := Analyze(context.Background(), testAnalyzeLogger(), mock, "claude-haiku-4-5", "/src", testScanResult())
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	after := time.Now()

	if g.ExtractedAt.Before(before) || g.ExtractedAt.After(after.Add(2*time.Second)) {
		t.Errorf("ExtractedAt = %v, want between %v and %v", g.ExtractedAt, before, after)
	}
}

func TestAnalyzeSetsTokenCount(t *testing.T) {
	mock := &mockClient{resp: llm.GenerateResponse{Content: cannedGuide}}
	g, err := Analyze(context.Background(), testAnalyzeLogger(), mock, "claude-haiku-4-5", "/src", testScanResult())
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	want := spec.EstimateTokens(cannedGuide)
	if g.TokenCount != want {
		t.Errorf("TokenCount = %d, want %d", g.TokenCount, want)
	}
}

const cannedGuideWithComponents = `**PATTERN** — Layered HTTP server.
**INVARIANTS** — Errors wrapped with fmt.Errorf.
**EDGE CASES** — Timeouts via context.
**STACK** — Go 1.24, net/http.
**STRUCTURE** — cmd/ for entrypoints.
**BOOT** — main.go creates server.
**BUILD** — go build ./cmd/...
**COMPONENT: Handler**
Interface: Accepts HTTP requests
Patterns: Uses net/http ServeHTTP
DependsOn: Service
**COMPONENT: Service**
Interface: Business logic layer
Patterns: Pure functions, no global state
DependsOn: none`

func TestParseComponents(t *testing.T) {
	tests := []struct {
		name  string
		guide string
		want  []Component
	}{
		{
			name:  "no_components",
			guide: cannedGuide,
			want:  nil,
		},
		{
			name:  "multiple_components",
			guide: cannedGuideWithComponents,
			want: []Component{
				{Name: "Handler", Interface: "Accepts HTTP requests", Patterns: "Uses net/http ServeHTTP", DependsOn: []string{"Service"}},
				{Name: "Service", Interface: "Business logic layer", Patterns: "Pure functions, no global state", DependsOn: nil},
			},
		},
		{
			name:  "depends_on_comma_separated",
			guide: "**COMPONENT: C**\nInterface: foo\nPatterns: bar\nDependsOn: A, B",
			want:  []Component{{Name: "C", Interface: "foo", Patterns: "bar", DependsOn: []string{"A", "B"}}},
		},
		{
			name:  "depends_on_none",
			guide: "**COMPONENT: D**\nInterface: foo\nPatterns: bar\nDependsOn: none",
			want:  []Component{{Name: "D", Interface: "foo", Patterns: "bar", DependsOn: nil}},
		},
		{
			name:  "multiline_patterns",
			guide: "**COMPONENT: E**\nInterface: foo\nPatterns: first line\nsecond line\nthird line\nDependsOn: none",
			want:  []Component{{Name: "E", Interface: "foo", Patterns: "first line\nsecond line\nthird line", DependsOn: nil}},
		},
		{
			name:  "trailing_bold_header_does_not_leak_into_patterns",
			guide: "**COMPONENT: F**\nInterface: foo\nPatterns: pattern one\n**BUILD** — go build ./cmd/...",
			want:  []Component{{Name: "F", Interface: "foo", Patterns: "pattern one"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseComponents(tt.guide)
			if len(got) != len(tt.want) {
				t.Fatalf("parseComponents() len = %d, want %d: %+v", len(got), len(tt.want), got)
			}
			for i, c := range got {
				w := tt.want[i]
				if c.Name != w.Name {
					t.Errorf("[%d] Name = %q, want %q", i, c.Name, w.Name)
				}
				if c.Interface != w.Interface {
					t.Errorf("[%d] Interface = %q, want %q", i, c.Interface, w.Interface)
				}
				if c.Patterns != w.Patterns {
					t.Errorf("[%d] Patterns = %q, want %q", i, c.Patterns, w.Patterns)
				}
				if len(c.DependsOn) != len(w.DependsOn) {
					t.Errorf("[%d] DependsOn len = %d, want %d", i, len(c.DependsOn), len(w.DependsOn))
				} else {
					for j, dep := range c.DependsOn {
						if dep != w.DependsOn[j] {
							t.Errorf("[%d] DependsOn[%d] = %q, want %q", i, j, dep, w.DependsOn[j])
						}
					}
				}
			}
		})
	}
}

func TestAnalyzeWithComponents(t *testing.T) {
	mock := &mockClient{resp: llm.GenerateResponse{Content: cannedGuideWithComponents}}
	g, err := Analyze(context.Background(), testAnalyzeLogger(), mock, "claude-haiku-4-5", "/src", testScanResult())
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if len(g.Components) != 2 {
		t.Fatalf("Components len = %d, want 2", len(g.Components))
	}
	if g.Components[0].Name != "Handler" {
		t.Errorf("Components[0].Name = %q, want %q", g.Components[0].Name, "Handler")
	}
	if g.Components[1].Name != "Service" {
		t.Errorf("Components[1].Name = %q, want %q", g.Components[1].Name, "Service")
	}
}

func TestAnalyzeWithoutComponents(t *testing.T) {
	mock := &mockClient{resp: llm.GenerateResponse{Content: cannedGuide}}
	g, err := Analyze(context.Background(), testAnalyzeLogger(), mock, "claude-haiku-4-5", "/src", testScanResult())
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if g.Components != nil {
		t.Errorf("Components = %v, want nil", g.Components)
	}
}

func TestExtractionPromptContainsComponentSection(t *testing.T) {
	if !strings.Contains(extractionPrompt, "COMPONENT") {
		t.Error("extractionPrompt missing COMPONENT section")
	}
}

var errTestLLM = errors.New("test LLM error")

func TestAnalyzeLLMError(t *testing.T) {
	mock := &mockClient{err: errTestLLM}
	_, err := Analyze(context.Background(), testAnalyzeLogger(), mock, "claude-haiku-4-5", "/src", testScanResult())
	if !errors.Is(err, errTestLLM) {
		t.Errorf("Analyze() error = %v, want %v", err, errTestLLM)
	}
}

func TestAnalyzeEmptyResponse(t *testing.T) {
	mock := &mockClient{resp: llm.GenerateResponse{Content: ""}}
	_, err := Analyze(context.Background(), testAnalyzeLogger(), mock, "claude-haiku-4-5", "/src", testScanResult())
	if !errors.Is(err, errEmptyExtraction) {
		t.Errorf("Analyze() error = %v, want %v", err, errEmptyExtraction)
	}
}

func TestAnalyzeCacheControl(t *testing.T) {
	mock := &mockClient{resp: llm.GenerateResponse{Content: cannedGuide}}
	_, err := Analyze(context.Background(), testAnalyzeLogger(), mock, "claude-haiku-4-5", "/src", testScanResult())
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if mock.captured.CacheControl == nil {
		t.Fatal("CacheControl is nil")
	}
	if mock.captured.CacheControl.Type != "ephemeral" {
		t.Errorf("CacheControl.Type = %q, want %q", mock.captured.CacheControl.Type, "ephemeral")
	}
}

func TestAnalyzeUsesModel(t *testing.T) {
	mock := &mockClient{resp: llm.GenerateResponse{Content: cannedGuide}}
	_, err := Analyze(context.Background(), testAnalyzeLogger(), mock, "my-custom-model", "/src", testScanResult())
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if mock.captured.Model != "my-custom-model" {
		t.Errorf("Model = %q, want %q", mock.captured.Model, "my-custom-model")
	}
}
