package interview

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/foundatron/octopusgarden/internal/llm"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// validScenarioBlock returns a well-formed scenario file block for use in tests.
func validScenarioBlock(name, id string) string {
	return "=== FILE: " + name + " ===\n" +
		"id: " + id + "\n" +
		"description: Test scenario " + id + "\n" +
		"type: api\n" +
		"satisfaction_criteria: |\n" +
		"  Returns 200 with expected payload.\n" +
		"steps:\n" +
		"  - description: Call the endpoint\n" +
		"    request:\n" +
		"      method: GET\n" +
		"      path: /items\n" +
		"    expect: Returns 200 OK\n" +
		"=== END FILE ===\n"
}

func TestGenerateHappyPath(t *testing.T) {
	t.Parallel()
	var capturedReq llm.GenerateRequest
	body := validScenarioBlock("happy-path.yaml", "happy-path") +
		validScenarioBlock("error-case.yaml", "error-case")

	client := &mockClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			capturedReq = req
			return llm.GenerateResponse{Content: body, CostUSD: 0.03}, nil
		},
	}

	gen := NewScenarioGenerator(client, "test-model", discardLogger())
	files, cost, err := gen.Generate(context.Background(), "# My Spec\n\nSome content.")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(files) != 2 {
		t.Errorf("expected 2 files, got %d", len(files))
	}
	if _, ok := files["happy-path.yaml"]; !ok {
		t.Error("expected happy-path.yaml in result")
	}
	if _, ok := files["error-case.yaml"]; !ok {
		t.Error("expected error-case.yaml in result")
	}
	if cost != 0.03 {
		t.Errorf("CostUSD = %v, want 0.03", cost)
	}

	// Verify cache control and temperature were set.
	if capturedReq.CacheControl == nil || capturedReq.CacheControl.Type != "ephemeral" {
		t.Errorf("expected CacheControl ephemeral, got %+v", capturedReq.CacheControl)
	}
	if capturedReq.Temperature == nil || *capturedReq.Temperature != 0.7 {
		t.Errorf("expected Temperature 0.7, got %v", capturedReq.Temperature)
	}

	// Each file must round-trip through scenario.Load (validated inside Generate already,
	// but verify content is non-empty as a sanity check).
	for name, content := range files {
		if strings.TrimSpace(content) == "" {
			t.Errorf("file %s has empty content", name)
		}
	}
}

func TestGeneratePartialInvalid(t *testing.T) {
	t.Parallel()
	// One valid scenario, one without an id (invalid).
	validBlock := validScenarioBlock("good.yaml", "good-scenario")
	invalidBlock := "=== FILE: bad.yaml ===\n" +
		"description: Missing id field\n" +
		"type: api\n" +
		"=== END FILE ===\n"

	client := &mockClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: validBlock + invalidBlock, CostUSD: 0.01}, nil
		},
	}

	gen := NewScenarioGenerator(client, "test-model", discardLogger())
	files, _, err := gen.Generate(context.Background(), "# Spec")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(files) != 1 {
		t.Errorf("expected 1 valid file, got %d", len(files))
	}
	if _, ok := files["good.yaml"]; !ok {
		t.Error("expected good.yaml in result")
	}
	if _, ok := files["bad.yaml"]; ok {
		t.Error("bad.yaml should have been excluded")
	}
}

func TestGenerateLLMError(t *testing.T) {
	t.Parallel()
	errAPI := errors.New("api failure")
	client := &mockClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{}, errAPI
		},
	}

	gen := NewScenarioGenerator(client, "test-model", discardLogger())
	_, _, err := gen.Generate(context.Background(), "# Spec")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, errAPI) {
		t.Errorf("expected errAPI in chain, got %v", err)
	}
}

func TestGenerateNoValidOutput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		wantErr error
	}{
		{
			name:    "no blocks at all",
			content: "Here are your scenarios: (none)",
			wantErr: errParseScenarioOutput,
		},
		{
			name: "all blocks missing END FILE",
			content: "=== FILE: foo.yaml ===\n" +
				"id: foo\n" +
				"description: unclosed block\n",
			wantErr: errParseScenarioOutput,
		},
		{
			name: "blocks present but all invalid YAML",
			content: "=== FILE: invalid.yaml ===\n" +
				"description: no id field\n" +
				"type: api\n" +
				"=== END FILE ===\n",
			wantErr: errNoValidScenarios,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := &mockClient{
				generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
					return llm.GenerateResponse{Content: tt.content}, nil
				},
			}

			gen := NewScenarioGenerator(client, "test-model", discardLogger())
			_, _, err := gen.Generate(context.Background(), "# Spec")
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("expected %v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestGenerateEmptySpec(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client := &mockClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			calls.Add(1)
			return llm.GenerateResponse{}, nil
		},
	}

	gen := NewScenarioGenerator(client, "test-model", discardLogger())
	tests := []struct {
		name string
		spec string
	}{
		{"empty string", ""},
		{"whitespace only", "   \n\t  "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := gen.Generate(context.Background(), tt.spec)
			if !errors.Is(err, errEmptySpec) {
				t.Errorf("expected errEmptySpec, got %v", err)
			}
		})
	}

	if c := calls.Load(); c != 0 {
		t.Errorf("LLM should not be called for empty spec, got %d calls", c)
	}
}

func TestGenerateStripsDirectoryPrefix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		filename string
		wantKey  string
	}{
		{
			name:     "scenarios/ prefix",
			filename: "scenarios/foo.yaml",
			wantKey:  "foo.yaml",
		},
		{
			name:     "./scenarios/ prefix",
			filename: "./scenarios/foo.yaml",
			wantKey:  "foo.yaml",
		},
		{
			name:     "nested subdirectory",
			filename: "scenarios/sub/bar.yaml",
			wantKey:  "bar.yaml",
		},
		{
			name:     "no prefix",
			filename: "baz.yaml",
			wantKey:  "baz.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body := validScenarioBlock(tt.filename, "test-id")
			client := &mockClient{
				generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
					return llm.GenerateResponse{Content: body, CostUSD: 0.01}, nil
				},
			}

			gen := NewScenarioGenerator(client, "test-model", discardLogger())
			files, _, err := gen.Generate(context.Background(), "# Spec")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if _, ok := files[tt.wantKey]; !ok {
				t.Errorf("expected key %q, got keys: %v", tt.wantKey, slices.Collect(maps.Keys(files)))
			}
		})
	}
}

func TestGenerateFilenameCollision(t *testing.T) {
	t.Parallel()
	// Two files that resolve to the same base name after stripping.
	body := validScenarioBlock("scenarios/foo.yaml", "foo-1") +
		validScenarioBlock("foo.yaml", "foo-2")

	client := &mockClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: body, CostUSD: 0.01}, nil
		},
	}

	gen := NewScenarioGenerator(client, "test-model", discardLogger())
	files, _, err := gen.Generate(context.Background(), "# Spec")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Only one should survive (first one wins).
	if len(files) != 1 {
		t.Errorf("expected 1 file after collision, got %d", len(files))
	}
	if _, ok := files["foo.yaml"]; !ok {
		t.Errorf("expected key 'foo.yaml', got keys: %v", slices.Collect(maps.Keys(files)))
	}
}

func TestGenerateUnsafeFilename(t *testing.T) {
	t.Parallel()
	// LLM returns a filename with path traversal -- attractor.ParseFiles rejects
	// these before we even get to validation, so the error is errParseScenarioOutput.
	unsafeBody := "=== FILE: ../../../etc/passwd ===\n" +
		"id: evil\n" +
		"description: Path traversal attempt\n" +
		"type: api\n" +
		"satisfaction_criteria: |\n" +
		"  Returns 200.\n" +
		"steps:\n" +
		"  - description: Call endpoint\n" +
		"    request:\n" +
		"      method: GET\n" +
		"      path: /foo\n" +
		"    expect: Returns 200\n" +
		"=== END FILE ===\n"

	client := &mockClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: unsafeBody, CostUSD: 0.01}, nil
		},
	}

	gen := NewScenarioGenerator(client, "test-model", discardLogger())
	_, _, err := gen.Generate(context.Background(), "# Spec")
	if err == nil {
		t.Fatal("expected error for unsafe filename, got nil")
	}
	if !errors.Is(err, errParseScenarioOutput) {
		t.Errorf("expected errParseScenarioOutput for unsafe filename, got %v", err)
	}
}

func TestGenerateContextCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	errCanceled := errors.New("context canceled")
	client := &mockClient{
		generateFn: func(ctx context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{}, errCanceled
		},
	}

	gen := NewScenarioGenerator(client, "test-model", discardLogger())
	_, _, err := gen.Generate(ctx, "# Spec")
	if err == nil {
		t.Fatal("expected error for canceled context, got nil")
	}
}

func TestGenerateParseErrorDistinctFromNoValid(t *testing.T) {
	t.Parallel()
	// When ParseFiles returns an error (no blocks found), the error should be
	// errParseScenarioOutput, not errNoValidScenarios.
	client := &mockClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: "no file blocks here"}, nil
		},
	}

	gen := NewScenarioGenerator(client, "test-model", discardLogger())
	_, _, err := gen.Generate(context.Background(), "# Spec")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, errParseScenarioOutput) {
		t.Errorf("expected errParseScenarioOutput, got %v", err)
	}
	if errors.Is(err, errNoValidScenarios) {
		t.Error("parse failure should not wrap errNoValidScenarios")
	}
}
