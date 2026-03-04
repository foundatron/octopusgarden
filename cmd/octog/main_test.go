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
	"path/filepath"
	"strings"
	"testing"

	"github.com/foundatron/octopusgarden/internal/container"
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
					Request:     &scenario.Request{Method: "POST", Path: "/items", Body: map[string]any{"name": "test"}},
					Expect:      "Should return 201 with item",
				},
				{
					Description: "Read the item",
					Request:     &scenario.Request{Method: "GET", Path: "/items/1"},
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

	agg, err := runAndScore(context.Background(), scenarios, srv.URL, mock, testLogger(), "claude-haiku-4-5-20251001", func() *container.Session { return nil })
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
					Request:     &scenario.Request{Method: "POST", Path: "/setup"},
					Expect:      "Should return 200",
				},
			},
			Steps: []scenario.Step{
				{
					Description: "Check result",
					Request:     &scenario.Request{Method: "GET", Path: "/result"},
					Expect:      "Should return data",
				},
			},
		},
	}

	mock := &mockLLMClient{}
	// Use unreachable address to deterministically cause connection errors.
	agg, err := runAndScore(context.Background(), scenarios, "http://127.0.0.1:1", mock, testLogger(), "claude-haiku-4-5-20251001", func() *container.Session { return nil })
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
					Request:     &scenario.Request{Method: "GET", Path: "/status"},
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

			agg, err := runAndScore(context.Background(), scenarios, srv.URL, mock, testLogger(), "claude-haiku-4-5-20251001", func() *container.Session { return nil })
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

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	content := "# comment\n\nANTHROPIC_API_KEY=sk-test-from-config\nOPENAI_API_KEY=sk-openai-test\n"

	// Override HOME so configPath() resolves to our temp dir.
	ogHome := os.Getenv("HOME")
	// Put config at dir/.octopusgarden/config
	ogDir := filepath.Join(dir, ".octopusgarden")
	if err := os.MkdirAll(ogDir, 0o750); err != nil {
		t.Fatal(err)
	}
	ogConfig := filepath.Join(ogDir, "config")
	if err := os.WriteFile(ogConfig, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)
	defer func() { _ = os.Setenv("HOME", ogHome) }()

	// Ensure the env vars are unset before loading.
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	os.Unsetenv("ANTHROPIC_API_KEY")
	os.Unsetenv("OPENAI_API_KEY")

	logger := testLogger()
	if err := loadConfig(logger); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if got := os.Getenv("ANTHROPIC_API_KEY"); got != "sk-test-from-config" {
		t.Errorf("ANTHROPIC_API_KEY = %q, want %q", got, "sk-test-from-config")
	}
	if got := os.Getenv("OPENAI_API_KEY"); got != "sk-openai-test" {
		t.Errorf("OPENAI_API_KEY = %q, want %q", got, "sk-openai-test")
	}
}

func TestLoadConfigEnvPrecedence(t *testing.T) {
	dir := t.TempDir()
	ogDir := filepath.Join(dir, ".octopusgarden")
	if err := os.MkdirAll(ogDir, 0o750); err != nil {
		t.Fatal(err)
	}
	ogConfig := filepath.Join(ogDir, "config")
	if err := os.WriteFile(ogConfig, []byte("ANTHROPIC_API_KEY=from-config\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", dir)
	t.Setenv("ANTHROPIC_API_KEY", "from-env")

	logger := testLogger()
	if err := loadConfig(logger); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if got := os.Getenv("ANTHROPIC_API_KEY"); got != "from-env" {
		t.Errorf("env precedence failed: ANTHROPIC_API_KEY = %q, want %q", got, "from-env")
	}
}

func TestLoadConfigMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	logger := testLogger()
	if err := loadConfig(logger); err != nil {
		t.Fatalf("loadConfig should not error on missing file: %v", err)
	}
}

func TestLoadConfigUnknownKey(t *testing.T) {
	dir := t.TempDir()
	ogDir := filepath.Join(dir, ".octopusgarden")
	if err := os.MkdirAll(ogDir, 0o750); err != nil {
		t.Fatal(err)
	}
	ogConfig := filepath.Join(ogDir, "config")
	if err := os.WriteFile(ogConfig, []byte("UNKNOWN_KEY=value\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", dir)

	// Should not error, just warn.
	logger := testLogger()
	if err := loadConfig(logger); err != nil {
		t.Fatalf("loadConfig should not error on unknown key: %v", err)
	}

	// Unknown key should not be set.
	if got := os.Getenv("UNKNOWN_KEY"); got == "value" {
		t.Error("unknown key should not be set in environment")
	}
}

func TestDetectCapabilities(t *testing.T) {
	tests := []struct {
		name      string
		scenarios []scenario.Scenario
		wantHTTP  bool
		wantExec  bool
	}{
		{
			name:      "empty scenarios",
			scenarios: nil,
			wantHTTP:  false,
			wantExec:  false,
		},
		{
			name: "HTTP only in steps",
			scenarios: []scenario.Scenario{
				{
					ID: "http-only",
					Steps: []scenario.Step{
						{Request: &scenario.Request{Method: "GET", Path: "/items"}},
					},
				},
			},
			wantHTTP: true,
			wantExec: false,
		},
		{
			name: "exec only in steps",
			scenarios: []scenario.Scenario{
				{
					ID: "exec-only",
					Steps: []scenario.Step{
						{Exec: &scenario.ExecRequest{Command: "echo hello"}},
					},
				},
			},
			wantHTTP: false,
			wantExec: true,
		},
		{
			name: "both HTTP and exec",
			scenarios: []scenario.Scenario{
				{
					ID: "mixed",
					Steps: []scenario.Step{
						{Request: &scenario.Request{Method: "GET", Path: "/items"}},
						{Exec: &scenario.ExecRequest{Command: "echo hello"}},
					},
				},
			},
			wantHTTP: true,
			wantExec: true,
		},
		{
			name: "HTTP in setup exec in steps",
			scenarios: []scenario.Scenario{
				{
					ID: "setup-http",
					Setup: []scenario.Step{
						{Request: &scenario.Request{Method: "POST", Path: "/setup"}},
					},
					Steps: []scenario.Step{
						{Exec: &scenario.ExecRequest{Command: "check"}},
					},
				},
			},
			wantHTTP: true,
			wantExec: true,
		},
		{
			name: "multiple scenarios aggregate",
			scenarios: []scenario.Scenario{
				{
					ID: "http-scenario",
					Steps: []scenario.Step{
						{Request: &scenario.Request{Method: "GET", Path: "/a"}},
					},
				},
				{
					ID: "exec-scenario",
					Steps: []scenario.Step{
						{Exec: &scenario.ExecRequest{Command: "test"}},
					},
				},
			},
			wantHTTP: true,
			wantExec: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caps := detectCapabilities(tt.scenarios)
			if caps.NeedsHTTP != tt.wantHTTP {
				t.Errorf("NeedsHTTP = %v, want %v", caps.NeedsHTTP, tt.wantHTTP)
			}
			if caps.NeedsExec != tt.wantExec {
				t.Errorf("NeedsExec = %v, want %v", caps.NeedsExec, tt.wantExec)
			}
		})
	}
}

func TestValidateJudgeFlags(t *testing.T) {
	tests := []struct {
		name       string
		threshold  float64
		judgeModel string
		wantErr    error
	}{
		{"valid", 95, "claude-haiku-4-5", nil},
		{"threshold too high", 200, "claude-haiku-4-5", errInvalidThreshold},
		{"threshold negative", -1, "claude-haiku-4-5", errInvalidThreshold},
		{"unknown judge model", 95, "nonexistent-model", errNoJudgeModelPricing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateJudgeFlags(tt.threshold, tt.judgeModel)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("got %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateCmdInvalidFormat(t *testing.T) {
	logger := testLogger()
	ctx := context.Background()
	err := validateCmd(ctx, logger, []string{
		"--scenarios", "s/", "--target", "http://localhost:1", "--format", "xml",
	})
	if !errors.Is(err, errInvalidFormat) {
		t.Errorf("validateCmd(--format xml) = %v, want %v", err, errInvalidFormat)
	}
}

func TestStatusCmdInvalidFormat(t *testing.T) {
	logger := testLogger()
	ctx := context.Background()
	err := statusCmd(ctx, logger, []string{"--format", "yaml"})
	if !errors.Is(err, errInvalidFormat) {
		t.Errorf("statusCmd(--format yaml) = %v, want %v", err, errInvalidFormat)
	}
}

func TestRunCmdInvalidThreshold(t *testing.T) {
	logger := testLogger()
	ctx := context.Background()

	tests := []struct {
		name string
		args []string
	}{
		{"above 100", []string{"--spec", "s.md", "--scenarios", "s/", "--threshold", "200"}},
		{"negative", []string{"--spec", "s.md", "--scenarios", "s/", "--threshold", "-1"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runCmd(ctx, logger, tt.args)
			if !errors.Is(err, errInvalidThreshold) {
				t.Errorf("runCmd(%v) = %v, want %v", tt.args, err, errInvalidThreshold)
			}
		})
	}
}
