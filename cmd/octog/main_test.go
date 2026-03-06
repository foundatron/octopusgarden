package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
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

	agg, err := runAndScore(context.Background(), scenarios, srv.URL, mock, testLogger(), "claude-haiku-4-5-20251001", func() *container.Session { return nil }, false, "")
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
	agg, err := runAndScore(context.Background(), scenarios, "http://127.0.0.1:1", mock, testLogger(), "claude-haiku-4-5-20251001", func() *container.Session { return nil }, false, "")
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

			agg, err := runAndScore(context.Background(), scenarios, srv.URL, mock, testLogger(), "claude-haiku-4-5-20251001", func() *container.Session { return nil }, false, "")
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
	ogDir := filepath.Join(dir, ".octopusgarden")
	if err := os.MkdirAll(ogDir, 0o750); err != nil {
		t.Fatal(err)
	}
	ogConfig := filepath.Join(ogDir, "config")
	if err := os.WriteFile(ogConfig, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)

	// Ensure the env vars are unset before loading.
	// t.Setenv("", "") makes os.Getenv return "" which loadConfig treats as unset.
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

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
		name        string
		scenarios   []scenario.Scenario
		wantHTTP    bool
		wantExec    bool
		wantBrowser bool
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
		{
			name: "browser only in steps",
			scenarios: []scenario.Scenario{
				{
					ID: "browser-only",
					Steps: []scenario.Step{
						{Browser: &scenario.BrowserRequest{Action: "navigate", URL: "/"}},
					},
				},
			},
			wantBrowser: true,
		},
		{
			name: "browser and request",
			scenarios: []scenario.Scenario{
				{
					ID: "mixed",
					Steps: []scenario.Step{
						{Request: &scenario.Request{Method: "GET", Path: "/api/items"}},
						{Browser: &scenario.BrowserRequest{Action: "navigate", URL: "/"}},
					},
				},
			},
			wantHTTP:    true,
			wantBrowser: true,
		},
		{
			name: "browser in setup",
			scenarios: []scenario.Scenario{
				{
					ID: "browser-setup",
					Setup: []scenario.Step{
						{Browser: &scenario.BrowserRequest{Action: "navigate", URL: "/"}},
					},
					Steps: []scenario.Step{
						{Request: &scenario.Request{Method: "GET", Path: "/api/items"}},
					},
				},
			},
			wantHTTP:    true,
			wantBrowser: true,
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
			if caps.NeedsBrowser != tt.wantBrowser {
				t.Errorf("NeedsBrowser = %v, want %v", caps.NeedsBrowser, tt.wantBrowser)
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

func TestMaskValue(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", "****"},
		{"short", "abc", "****"},
		{"exactly 15", "123456789012345", "****"},
		{"exactly 16", "1234567890123456", "1234...3456"},
		{"long API key", "sk-ant-api03-abcdefg-hijklmnop", "sk-a...mnop"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := maskValue(tt.input)
			if got != tt.want {
				t.Errorf("maskValue(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestConfigureInteractiveNewConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, ".octopusgarden", "config")

	input := "sk-ant-test-key-1234\n\nhttp://localhost:11434\n"
	var out bytes.Buffer

	if err := configureInteractive(strings.NewReader(input), &out, cfgPath); err != nil {
		t.Fatalf("configureInteractive: %v", err)
	}

	// Verify file was created.
	content, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	got := string(content)
	if !strings.Contains(got, "ANTHROPIC_API_KEY=sk-ant-test-key-1234") {
		t.Errorf("config missing ANTHROPIC_API_KEY, got:\n%s", got)
	}
	if !strings.Contains(got, "OPENAI_BASE_URL=http://localhost:11434") {
		t.Errorf("config missing OPENAI_BASE_URL, got:\n%s", got)
	}
	// OPENAI_API_KEY was skipped (empty input) and never set — should not appear.
	if strings.Contains(got, "OPENAI_API_KEY=") {
		t.Errorf("config should not contain empty OPENAI_API_KEY, got:\n%s", got)
	}

	// Verify directory permissions.
	dirInfo, err := os.Stat(filepath.Dir(cfgPath))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir perm = %04o, want 0700", perm)
	}

	// Verify file permissions.
	fileInfo, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("file perm = %04o, want 0600", perm)
	}

	// Verify output mentions "saved".
	if !strings.Contains(out.String(), "Configuration saved") {
		t.Errorf("output missing 'Configuration saved', got:\n%s", out.String())
	}
}

func TestConfigureInteractiveUpdateExisting(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".octopusgarden")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(cfgDir, "config")

	existing := "# My settings\nANTHROPIC_API_KEY=sk-ant-existing-key-5678\nOPENAI_API_KEY=sk-openai-old\n"
	if err := os.WriteFile(cfgPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	// Keep ANTHROPIC (empty enter), update OPENAI, skip BASE_URL.
	input := "\nsk-openai-new\n\n"
	var out bytes.Buffer

	if err := configureInteractive(strings.NewReader(input), &out, cfgPath); err != nil {
		t.Fatalf("configureInteractive: %v", err)
	}

	// Output should show masked existing values.
	output := out.String()
	// sk-ant-existing-key-5678 (24 chars, >=16) → first4...last4 = sk-a...5678
	if !strings.Contains(output, "sk-a...5678") {
		t.Errorf("output should show masked ANTHROPIC key, got:\n%s", output)
	}
	// sk-openai-old (13 chars, <16) → ****
	if !strings.Contains(output, "OPENAI_API_KEY [****]") {
		t.Errorf("output should show masked OPENAI key, got:\n%s", output)
	}

	content, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	got := string(content)

	// Comment preserved.
	if !strings.Contains(got, "# My settings") {
		t.Errorf("comment not preserved, got:\n%s", got)
	}
	// ANTHROPIC unchanged.
	if !strings.Contains(got, "ANTHROPIC_API_KEY=sk-ant-existing-key-5678") {
		t.Errorf("ANTHROPIC_API_KEY should be unchanged, got:\n%s", got)
	}
	// OPENAI updated.
	if !strings.Contains(got, "OPENAI_API_KEY=sk-openai-new") {
		t.Errorf("OPENAI_API_KEY should be updated, got:\n%s", got)
	}
}

func TestConfigureInteractiveClearValue(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".octopusgarden")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(cfgDir, "config")

	existing := "ANTHROPIC_API_KEY=sk-ant-old\nOPENAI_API_KEY=sk-openai-old\n"
	if err := os.WriteFile(cfgPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	// Clear ANTHROPIC with "-", keep OPENAI, skip BASE_URL.
	input := "-\n\n\n"
	var out bytes.Buffer

	if err := configureInteractive(strings.NewReader(input), &out, cfgPath); err != nil {
		t.Fatalf("configureInteractive: %v", err)
	}

	content, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	got := string(content)

	// ANTHROPIC should be gone.
	if strings.Contains(got, "ANTHROPIC_API_KEY") {
		t.Errorf("ANTHROPIC_API_KEY should be cleared, got:\n%s", got)
	}
	// OPENAI still there.
	if !strings.Contains(got, "OPENAI_API_KEY=sk-openai-old") {
		t.Errorf("OPENAI_API_KEY should be preserved, got:\n%s", got)
	}
}

func TestConfigureInteractiveEOF(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".octopusgarden")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(cfgDir, "config")

	existing := "ANTHROPIC_API_KEY=sk-ant-keep\nOPENAI_API_KEY=sk-openai-keep\n"
	if err := os.WriteFile(cfgPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	// Piped empty input (immediate EOF).
	var out bytes.Buffer
	if err := configureInteractive(strings.NewReader(""), &out, cfgPath); err != nil {
		t.Fatalf("configureInteractive: %v", err)
	}

	content, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	got := string(content)

	// All existing values preserved.
	if !strings.Contains(got, "ANTHROPIC_API_KEY=sk-ant-keep") {
		t.Errorf("ANTHROPIC_API_KEY should be preserved on EOF, got:\n%s", got)
	}
	if !strings.Contains(got, "OPENAI_API_KEY=sk-openai-keep") {
		t.Errorf("OPENAI_API_KEY should be preserved on EOF, got:\n%s", got)
	}
}

func TestConfigureInteractiveNoKeyWarning(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		name        string
		input       string
		wantWarning bool
	}{
		{"no keys", "\n\n\n", true},
		{"anthropic set", "sk-ant-key-12345678\n\n\n", false},
		{"openai set", "\nsk-openai-key-12345678\n\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, tt.name, ".octopusgarden", "config")
			var out bytes.Buffer
			if err := configureInteractive(strings.NewReader(tt.input), &out, path); err != nil {
				t.Fatalf("configureInteractive: %v", err)
			}
			hasWarning := strings.Contains(out.String(), "Warning: no API key configured")
			if hasWarning != tt.wantWarning {
				t.Errorf("warning present = %v, want %v\noutput:\n%s", hasWarning, tt.wantWarning, out.String())
			}
		})
	}
}

func TestReadConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")

	content := "# comment\n\nANTHROPIC_API_KEY=sk-test\nUNKNOWN=value\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	values, lines, err := readConfigFile(path)
	if err != nil {
		t.Fatalf("readConfigFile: %v", err)
	}

	if values["ANTHROPIC_API_KEY"] != "sk-test" {
		t.Errorf("ANTHROPIC_API_KEY = %q, want %q", values["ANTHROPIC_API_KEY"], "sk-test")
	}
	if values["UNKNOWN"] != "value" {
		t.Errorf("UNKNOWN = %q, want %q", values["UNKNOWN"], "value")
	}
	if len(lines) != 4 {
		t.Errorf("expected 4 original lines, got %d", len(lines))
	}
	if lines[0] != "# comment" {
		t.Errorf("lines[0] = %q, want %q", lines[0], "# comment")
	}
}

func TestReadConfigFileMissing(t *testing.T) {
	values, lines, err := readConfigFile(filepath.Join(t.TempDir(), "nonexistent"))
	if err != nil {
		t.Fatalf("readConfigFile on missing file: %v", err)
	}
	if len(values) != 0 {
		t.Errorf("expected empty map, got %v", values)
	}
	if lines != nil {
		t.Errorf("expected nil lines, got %v", lines)
	}
}

func TestWriteConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "config")

	originalLines := []string{
		"# API keys",
		"ANTHROPIC_API_KEY=old-key",
		"",
		"CUSTOM_THING=preserved",
	}
	values := map[string]string{
		"ANTHROPIC_API_KEY": "new-key",
		"OPENAI_API_KEY":    "sk-openai",
		"CUSTOM_THING":      "preserved",
	}

	if err := writeConfigFile(path, values, originalLines); err != nil {
		t.Fatalf("writeConfigFile: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(content)

	// Comment preserved.
	if !strings.Contains(got, "# API keys") {
		t.Errorf("comment not preserved, got:\n%s", got)
	}
	// ANTHROPIC updated in-place.
	if !strings.Contains(got, "ANTHROPIC_API_KEY=new-key") {
		t.Errorf("ANTHROPIC_API_KEY not updated, got:\n%s", got)
	}
	// Unknown key preserved.
	if !strings.Contains(got, "CUSTOM_THING=preserved") {
		t.Errorf("CUSTOM_THING not preserved, got:\n%s", got)
	}
	// New OPENAI appended.
	if !strings.Contains(got, "OPENAI_API_KEY=sk-openai") {
		t.Errorf("OPENAI_API_KEY not appended, got:\n%s", got)
	}

	// Verify file permissions.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %04o, want 0600", perm)
	}

	// Verify ANTHROPIC comes before OPENAI (in-place before appended).
	anthIdx := strings.Index(got, "ANTHROPIC_API_KEY")
	openIdx := strings.Index(got, "OPENAI_API_KEY")
	if anthIdx > openIdx {
		t.Errorf("ANTHROPIC_API_KEY should come before OPENAI_API_KEY in output")
	}
}

func TestExtractCmdSourceDirNotExist(t *testing.T) {
	logger := testLogger()
	ctx := context.Background()
	err := extractCmd(ctx, logger, []string{"--source-dir", "/nonexistent-dir-abc123"})
	if !errors.Is(err, errSourceDirNotExist) {
		t.Errorf("extractCmd() = %v, want %v", err, errSourceDirNotExist)
	}
}

func TestExtractCmdSourceDirIsFile(t *testing.T) {
	logger := testLogger()
	ctx := context.Background()

	f := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(f, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := extractCmd(ctx, logger, []string{"--source-dir", f})
	if !errors.Is(err, errSourceDirNotDir) {
		t.Errorf("extractCmd() = %v, want %v", err, errSourceDirNotDir)
	}
}

func TestExtractCmdNoLanguageDetected(t *testing.T) {
	logger := testLogger()
	ctx := context.Background()

	// Create a directory with a marker file that has no language mapping
	// (pom.xml is recognized as a marker but maps to no supported language).
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pom.xml"), []byte("<project/>"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := extractCmd(ctx, logger, []string{"--source-dir", dir})
	if !errors.Is(err, errNoLanguageDetected) {
		t.Errorf("extractCmd() = %v, want %v", err, errNoLanguageDetected)
	}
}

func TestExtractFlagParsing(t *testing.T) {
	logger := testLogger()
	ctx := context.Background()

	t.Run("source_dir_required", func(t *testing.T) {
		err := extractCmd(ctx, logger, []string{})
		if !errors.Is(err, errSourceDirRequired) {
			t.Errorf("got %v, want %v", err, errSourceDirRequired)
		}
	})

	t.Run("proceeds_past_flags_with_valid_dir", func(t *testing.T) {
		// With no API keys set, the command should fail at LLM setup — not at flag
		// parsing or source-dir validation. This proves flags (including the output
		// default) are parsed correctly before any LLM work begins.
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.21\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("ANTHROPIC_API_KEY", "")
		t.Setenv("OPENAI_API_KEY", "")

		err := extractCmd(ctx, logger, []string{"--source-dir", dir})
		if errors.Is(err, errSourceDirRequired) || errors.Is(err, errSourceDirNotExist) || errors.Is(err, errSourceDirNotDir) || errors.Is(err, errNoLanguageDetected) {
			t.Errorf("got unexpected early error: %v", err)
		}
		if err == nil {
			t.Error("expected error (no API key), got nil")
		}
	})
}

func TestIsFlagSet(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	_ = fs.String("language", "go", "target language")
	_ = fs.String("spec", "", "spec path")

	if err := fs.Parse([]string{"--spec", "s.md"}); err != nil {
		t.Fatal(err)
	}

	if !isFlagSet(fs, "spec") {
		t.Error("spec should be set")
	}
	if isFlagSet(fs, "language") {
		t.Error("language should not be set")
	}
	if isFlagSet(fs, "nonexistent") {
		t.Error("nonexistent should not be set")
	}
}

func TestRunCmdGenesFileNotFound(t *testing.T) {
	logger := testLogger()
	ctx := context.Background()

	err := runCmd(ctx, logger, []string{
		"--spec", "s.md", "--scenarios", "s/",
		"--genes", "/nonexistent/genes.json",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent genes file")
	}
	if !strings.Contains(err.Error(), "load genes") {
		t.Errorf("expected 'load genes' in error, got: %v", err)
	}
}

func TestRunCmdGenesInvalidJSON(t *testing.T) {
	logger := testLogger()
	ctx := context.Background()

	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("{invalid"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := runCmd(ctx, logger, []string{
		"--spec", "s.md", "--scenarios", "s/",
		"--genes", path,
	})
	if err == nil {
		t.Fatal("expected error for invalid JSON genes file")
	}
	if !strings.Contains(err.Error(), "load genes") {
		t.Errorf("expected 'load genes' in error, got: %v", err)
	}
}

func TestRunCmdGenesEmptyGuide(t *testing.T) {
	logger := testLogger()
	ctx := context.Background()

	g := map[string]any{
		"version":      1,
		"source":       "test",
		"language":     "go",
		"extracted_at": "2025-01-01T00:00:00Z",
		"guide":        "",
		"token_count":  0,
	}
	data, _ := json.Marshal(g)
	path := filepath.Join(t.TempDir(), "empty-guide.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	err := runCmd(ctx, logger, []string{
		"--spec", "s.md", "--scenarios", "s/",
		"--genes", path,
	})
	if err == nil {
		t.Fatal("expected error for empty guide")
	}
	if !strings.Contains(err.Error(), "load genes") {
		t.Errorf("expected 'load genes' in error, got: %v", err)
	}
}

// writeTestGenes creates a valid genes.json file and returns its path.
func writeTestGenes(t *testing.T, language string) string {
	t.Helper()
	g := map[string]any{
		"version":      1,
		"source":       "/test/project",
		"language":     language,
		"extracted_at": "2025-01-01T00:00:00Z",
		"guide":        "Use consistent error handling patterns.",
		"token_count":  42,
	}
	data, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "genes.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadGenesAutoLanguage(t *testing.T) {
	genesPath := writeTestGenes(t, "python")
	logger := testLogger()

	guide, geneLang, resolvedLang, err := loadGenes(genesPath, "go", false, logger)
	if err != nil {
		t.Fatalf("loadGenes: %v", err)
	}
	if guide != "Use consistent error handling patterns." {
		t.Errorf("guide = %q, want test guide text", guide)
	}
	if geneLang != "python" {
		t.Errorf("geneLang = %q, want %q", geneLang, "python")
	}
	if resolvedLang != "python" {
		t.Errorf("resolvedLang = %q, want %q (auto-detected from genes)", resolvedLang, "python")
	}
}

func TestLoadGenesExplicitLanguageWins(t *testing.T) {
	genesPath := writeTestGenes(t, "python")
	logger := testLogger()

	guide, geneLang, resolvedLang, err := loadGenes(genesPath, "go", true, logger)
	if err != nil {
		t.Fatalf("loadGenes: %v", err)
	}
	if guide != "Use consistent error handling patterns." {
		t.Errorf("guide = %q, want test guide text", guide)
	}
	if geneLang != "python" {
		t.Errorf("geneLang = %q, want %q", geneLang, "python")
	}
	if resolvedLang != "go" {
		t.Errorf("resolvedLang = %q, want %q (explicit --language should win)", resolvedLang, "go")
	}
}

func TestLoadGenesEmptyPath(t *testing.T) {
	logger := testLogger()

	guide, geneLang, resolvedLang, err := loadGenes("", "go", false, logger)
	if err != nil {
		t.Fatalf("loadGenes: %v", err)
	}
	if guide != "" || geneLang != "" {
		t.Errorf("expected empty guide/geneLang for empty path, got guide=%q geneLang=%q", guide, geneLang)
	}
	if resolvedLang != "go" {
		t.Errorf("resolvedLang = %q, want %q (unchanged)", resolvedLang, "go")
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
