package preflight

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foundatron/octopusgarden/internal/llm"
)

func makeScenarioJSONResponse(coverage, feasibility, isolation, chains float64, issues string) string {
	return fmt.Sprintf(`{"coverage":%g,"feasibility":%g,"isolation":%g,"chains":%g,"issues":%s}`,
		coverage, feasibility, isolation, chains, issues)
}

func TestComputeScenarioAggregate(t *testing.T) {
	tests := []struct {
		name        string
		coverage    float64
		feasibility float64
		isolation   float64
		chains      float64
		want        float64
	}{
		{"all ones", 1.0, 1.0, 1.0, 1.0, 1.0},
		{"all zeros", 0.0, 0.0, 0.0, 0.0, 0.0},
		{"mixed", 0.8, 0.6, 0.4, 0.2, (0.8 + 0.6 + 0.4 + 0.2) / 4.0},
	}
	const eps = 1e-12
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := computeScenarioAggregate(tc.coverage, tc.feasibility, tc.isolation, tc.chains)
			if math.Abs(got-tc.want) > eps {
				t.Errorf("computeScenarioAggregate(%g,%g,%g,%g) = %g, want %g",
					tc.coverage, tc.feasibility, tc.isolation, tc.chains, got, tc.want)
			}
		})
	}
}

func TestParseScenarioResponse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:  "valid JSON",
			input: makeScenarioJSONResponse(0.9, 0.8, 0.85, 0.7, `[]`),
		},
		{
			name:  "JSON in code fences",
			input: "```json\n" + makeScenarioJSONResponse(0.9, 0.8, 0.85, 0.7, `[]`) + "\n```",
		},
		{
			name:    "non-JSON",
			input:   "not json at all",
			wantErr: true,
		},
		{
			name:    "score out of range high",
			input:   makeScenarioJSONResponse(1.5, 0.8, 0.85, 0.7, `[]`),
			wantErr: true,
		},
		{
			name:    "score out of range low",
			input:   makeScenarioJSONResponse(-0.1, 0.8, 0.85, 0.7, `[]`),
			wantErr: true,
		},
		{
			name:    "invalid dimension name",
			input:   `{"coverage":0.9,"feasibility":0.8,"isolation":0.85,"chains":0.7,"issues":[{"scenario":"foo","dimension":"badname","detail":"x"}]}`,
			wantErr: true,
		},
		{
			name:  "missing issues treated as empty",
			input: `{"coverage":0.9,"feasibility":0.8,"isolation":0.85,"chains":0.7}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseScenarioResponse(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !errors.Is(err, errMalformedScenarioResponse) {
					t.Errorf("expected errMalformedScenarioResponse, got %v", err)
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

func TestBuildScenarioSystemPrompt(t *testing.T) {
	prompt := buildScenarioSystemPrompt()
	for _, keyword := range []string{"coverage", "feasibility", "isolation", "chains", "JSON", "step types", "testable"} {
		if !strings.Contains(prompt, keyword) {
			t.Errorf("system prompt missing keyword: %q", keyword)
		}
	}
}

func TestBuildScenarioUserPrompt(t *testing.T) {
	specContent := "# My Spec\nDoes things."
	scenarios := map[string]string{
		"zebra.yaml": "name: zebra\n",
		"alpha.yaml": "name: alpha\n",
	}
	prompt := buildScenarioUserPrompt(specContent, scenarios)
	if !strings.Contains(prompt, specContent) {
		t.Error("prompt does not contain spec content")
	}
	alphaIdx := strings.Index(prompt, "alpha.yaml")
	zebraIdx := strings.Index(prompt, "zebra.yaml")
	if alphaIdx == -1 || zebraIdx == -1 {
		t.Fatal("prompt missing scenario headings")
	}
	if alphaIdx > zebraIdx {
		t.Error("scenarios not in sorted order: alpha should appear before zebra")
	}
	if !strings.Contains(prompt, "```yaml") {
		t.Error("prompt missing yaml code fences")
	}
}

func TestCheckScenariosPassFail(t *testing.T) {
	tests := []struct {
		name      string
		scores    [4]float64
		threshold float64
		wantPass  bool
	}{
		{"pass above threshold", [4]float64{0.9, 0.9, 0.9, 0.9}, 0.8, true},
		{"pass at threshold exactly", [4]float64{0.8, 0.8, 0.8, 0.8}, 0.8, true},
		{"fail below threshold", [4]float64{0.5, 0.5, 0.5, 0.5}, 0.8, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockClient{
				generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
					return llm.GenerateResponse{
						Content: makeScenarioJSONResponse(tc.scores[0], tc.scores[1], tc.scores[2], tc.scores[3], `[]`),
					}, nil
				},
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "test.yaml"), []byte("name: test\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			result, err := CheckScenarios(context.Background(), mock, "test-model", "spec content", dir, tc.threshold, testLogger())
			if err != nil {
				t.Fatalf("CheckScenarios: %v", err)
			}
			if result.Pass != tc.wantPass {
				t.Errorf("Pass=%v, want %v (aggregate=%.4f)", result.Pass, tc.wantPass, result.Aggregate)
			}
		})
	}
}

func TestCheckScenariosTransportError(t *testing.T) {
	wantErr := errors.New("network failure")
	mock := &mockClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{}, wantErr
		},
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.yaml"), []byte("name: test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := CheckScenarios(context.Background(), mock, "test-model", "spec", dir, 0.8, testLogger())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("expected transport error to be wrapped, got %v", err)
	}
}

func TestCheckScenariosMalformedResponse(t *testing.T) {
	mock := &mockClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{Content: "garbage response"}, nil
		},
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.yaml"), []byte("name: test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := CheckScenarios(context.Background(), mock, "test-model", "spec", dir, 0.8, testLogger())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, errMalformedScenarioResponse) {
		t.Errorf("expected errMalformedScenarioResponse, got %v", err)
	}
}

func TestCheckScenariosEmptyDir(t *testing.T) {
	mock := &mockClient{}
	dir := t.TempDir()
	_, err := CheckScenarios(context.Background(), mock, "test-model", "spec", dir, 0.8, testLogger())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, errNoScenarios) {
		t.Errorf("expected errNoScenarios, got %v", err)
	}
}

func TestCheckScenariosSkipsNonYAML(t *testing.T) {
	var capturedContent string
	mock := &mockClient{
		generateFn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			capturedContent = req.Messages[0].Content
			return llm.GenerateResponse{
				Content: makeScenarioJSONResponse(0.9, 0.9, 0.9, 0.9, `[]`),
			}, nil
		},
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), []byte("name: scenario\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "shorthand.yml"), []byte("name: shorthand\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.txt"), []byte("do not include this"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := CheckScenarios(context.Background(), mock, "test-model", "spec", dir, 0.8, testLogger())
	if err != nil {
		t.Fatalf("CheckScenarios: %v", err)
	}
	if strings.Contains(capturedContent, "do not include this") {
		t.Error("non-YAML file content was included in the prompt")
	}
	if !strings.Contains(capturedContent, "scenario.yaml") {
		t.Error(".yaml file was not included in the prompt")
	}
	if !strings.Contains(capturedContent, "shorthand.yml") {
		t.Error(".yml file was not included in the prompt")
	}
}
