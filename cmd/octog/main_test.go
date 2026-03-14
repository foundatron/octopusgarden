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
	"sync/atomic"
	"testing"
	"time"

	"github.com/foundatron/octopusgarden/internal/attractor"
	"github.com/foundatron/octopusgarden/internal/container"
	"github.com/foundatron/octopusgarden/internal/llm"
	"github.com/foundatron/octopusgarden/internal/scenario"
	"github.com/foundatron/octopusgarden/internal/store"
)

// mockLLMClient implements llm.Client for testing.
type mockLLMClient struct {
	generateFn func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error)
	judgeFn    func(ctx context.Context, req llm.JudgeRequest) (llm.JudgeResponse, error)
}

func (m *mockLLMClient) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	if m.generateFn != nil {
		return m.generateFn(ctx, req)
	}
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

	agg, err := runAndScore(context.Background(), scenarios, executorOpts{
		targetURL:     srv.URL,
		logger:        testLogger(),
		sessionGetter: func() *container.Session { return nil },
	}, mock, "claude-haiku-4-5-20251001", nil, 1)
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
	agg, err := runAndScore(context.Background(), scenarios, executorOpts{
		targetURL:     "http://127.0.0.1:1",
		logger:        testLogger(),
		sessionGetter: func() *container.Session { return nil },
	}, mock, "claude-haiku-4-5-20251001", nil, 1)
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

			agg, err := runAndScore(context.Background(), scenarios, executorOpts{
				targetURL:     srv.URL,
				logger:        testLogger(),
				sessionGetter: func() *container.Session { return nil },
			}, mock, "claude-haiku-4-5-20251001", nil, 1)
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

func TestValidateCmdCodeAndTargetConflict(t *testing.T) {
	logger := testLogger()
	ctx := context.Background()
	err := validateCmd(ctx, logger, []string{
		"--scenarios", "s/", "--target", "http://localhost:1", "--code", "/some/dir",
	})
	if !errors.Is(err, errCodeAndTargetConflict) {
		t.Errorf("validateCmd(--code + --target) = %v, want %v", err, errCodeAndTargetConflict)
	}
}

func TestValidateCmdMissingCodeAndTarget(t *testing.T) {
	logger := testLogger()
	ctx := context.Background()
	err := validateCmd(ctx, logger, []string{
		"--scenarios", "s/",
	})
	if !errors.Is(err, errScenariosAndTargetRequired) {
		t.Errorf("validateCmd(no --target, no --code) = %v, want %v", err, errScenariosAndTargetRequired)
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

func TestInterviewRun(t *testing.T) {
	tests := []struct {
		name       string
		responses  []string  // Generate responses in order: opening question, then final spec
		costs      []float64 // CostUSD per Generate call
		userInput  string    // lines typed by the user
		wantSpec   string    // expected content written to output file
		wantErrOut string    // expected fragment in errOut
		wantErr    bool
	}{
		{
			name:       "happy path writes spec and reports cost",
			responses:  []string{"What are you building?", "## My Spec\n\nA task manager."},
			costs:      []float64{0.001, 0.002},
			userInput:  "done\n",
			wantSpec:   "## My Spec\n\nA task manager.",
			wantErrOut: "$0.0030",
		},
		{
			name:       "zero cost prints free",
			responses:  []string{"What are you building?", "## Spec\n\nFree spec."},
			costs:      []float64{0, 0},
			userInput:  "done\n",
			wantSpec:   "## Spec\n\nFree spec.",
			wantErrOut: "free",
		},
		{
			name:      "LLM error propagates",
			responses: nil,
			costs:     nil,
			userInput: "",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			callIdx := 0
			client := &mockLLMClient{
				generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
					if tt.responses == nil {
						return llm.GenerateResponse{}, errors.New("LLM unavailable")
					}
					if callIdx >= len(tt.responses) {
						t.Fatalf("unexpected Generate call %d", callIdx)
					}
					resp := llm.GenerateResponse{
						Content: tt.responses[callIdx],
						CostUSD: tt.costs[callIdx],
					}
					callIdx++
					return resp, nil
				},
			}

			dir := t.TempDir()
			outputPath := filepath.Join(dir, "spec.md")
			in := strings.NewReader(tt.userInput)
			var out, errOut bytes.Buffer

			err := interviewRun(context.Background(), client, "test-model", "What would you like to build?", outputPath, "", in, &out, &errOut)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("interviewRun: %v", err)
			}

			content, readErr := os.ReadFile(outputPath)
			if readErr != nil {
				t.Fatalf("read output file: %v", readErr)
			}
			if string(content) != tt.wantSpec {
				t.Errorf("spec content = %q, want %q", string(content), tt.wantSpec)
			}

			if tt.wantErrOut != "" && !strings.Contains(errOut.String(), tt.wantErrOut) {
				t.Errorf("errOut = %q, want it to contain %q", errOut.String(), tt.wantErrOut)
			}
		})
	}

	t.Run("file write error returns error", func(t *testing.T) {
		callIdx := 0
		responses := []string{"Question?", "## Spec"}
		client := &mockLLMClient{
			generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
				resp := llm.GenerateResponse{Content: responses[callIdx]}
				callIdx++
				return resp, nil
			},
		}
		// Use a path under a non-existent nested directory to force write failure.
		badPath := filepath.Join(t.TempDir(), "does-not-exist", "sub", "spec.md")
		in := strings.NewReader("done\n")
		var out, errOut bytes.Buffer

		err := interviewRun(context.Background(), client, "model", "start", badPath, "", in, &out, &errOut)
		if err == nil {
			t.Fatal("expected write error, got nil")
		}
	})
}

func TestInterviewSeedAndPromptConflict(t *testing.T) {
	t.Parallel()
	logger := testLogger()
	ctx := context.Background()
	err := interviewCmd(ctx, logger, []string{"--seed", "somefile.md", "--prompt", "hello"})
	if !errors.Is(err, errSeedAndPromptConflict) {
		t.Errorf("expected errSeedAndPromptConflict, got %v", err)
	}
}

func TestInterviewSeedFileNotFound(t *testing.T) {
	t.Parallel()
	logger := testLogger()
	ctx := context.Background()
	err := interviewCmd(ctx, logger, []string{"--seed", "/nonexistent-spec-abc123.md"})
	if err == nil {
		t.Fatal("expected error for missing seed file, got nil")
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

func TestBuildDetailedFailures(t *testing.T) {
	tests := []struct {
		name      string
		agg       scenario.AggregateResult
		wantLen   int
		wantCheck func(t *testing.T, out []string)
	}{
		{
			name:    "zero scenarios",
			agg:     scenario.AggregateResult{},
			wantLen: 0,
		},
		{
			name: "all passing",
			agg: scenario.AggregateResult{
				Scenarios: []scenario.ScoredScenario{
					{ScenarioID: "crud", Score: 95, Steps: []scenario.ScoredStep{
						{StepScore: scenario.StepScore{Score: 95}, StepResult: scenario.StepResult{Description: "list items"}},
					}},
					{ScenarioID: "auth", Score: 100, Steps: []scenario.ScoredStep{
						{StepScore: scenario.StepScore{Score: 100}, StepResult: scenario.StepResult{Description: "login"}},
					}},
				},
			},
			wantLen: 2,
			wantCheck: func(t *testing.T, out []string) {
				t.Helper()
				if !strings.Contains(out[0], "✓") || !strings.Contains(out[0], "crud") {
					t.Errorf("passing scenario should start with ✓ and contain name, got %q", out[0])
				}
				if !strings.Contains(out[1], "✓") || !strings.Contains(out[1], "auth") {
					t.Errorf("passing scenario should start with ✓ and contain name, got %q", out[1])
				}
				// Passing scenarios: no step detail
				if strings.Contains(out[0], "list items") {
					t.Error("passing scenario should not include step detail")
				}
			},
		},
		{
			name: "all failing",
			agg: scenario.AggregateResult{
				Scenarios: []scenario.ScoredScenario{
					{ScenarioID: "crud", Score: 30, Steps: []scenario.ScoredStep{
						{StepScore: scenario.StepScore{Score: 90}, StepResult: scenario.StepResult{Description: "list items"}},
						{StepScore: scenario.StepScore{Score: 20, Reasoning: "wrong status"}, StepResult: scenario.StepResult{Description: "delete item", Observed: "got 500"}},
					}},
				},
			},
			wantLen: 1,
			wantCheck: func(t *testing.T, out []string) {
				t.Helper()
				if !strings.Contains(out[0], "✗") || !strings.Contains(out[0], "crud") {
					t.Errorf("failing scenario should contain ✗ and name, got %q", out[0])
				}
				// Passing step in failing scenario: single-line summary
				if !strings.Contains(out[0], "✓") || !strings.Contains(out[0], "list items") {
					t.Errorf("passing step should appear as single-line summary, got %q", out[0])
				}
				// Failing step: expanded detail
				if !strings.Contains(out[0], "delete item") {
					t.Errorf("failing step should include description, got %q", out[0])
				}
				if !strings.Contains(out[0], "wrong status") {
					t.Errorf("failing step should include reasoning, got %q", out[0])
				}
				if !strings.Contains(out[0], "got 500") {
					t.Errorf("failing step should include observed, got %q", out[0])
				}
			},
		},
		{
			name: "mixed passing and failing",
			agg: scenario.AggregateResult{
				Scenarios: []scenario.ScoredScenario{
					{ScenarioID: "ok", Score: 85},
					{ScenarioID: "bad", Score: 50, Steps: []scenario.ScoredStep{
						{StepScore: scenario.StepScore{Score: 50, Reasoning: "timeout"}, StepResult: scenario.StepResult{Description: "check health"}},
					}},
				},
			},
			wantLen: 2,
			wantCheck: func(t *testing.T, out []string) {
				t.Helper()
				if !strings.Contains(out[0], "✓ ok") {
					t.Errorf("passing scenario should be ✓, got %q", out[0])
				}
				if !strings.Contains(out[1], "✗ bad") {
					t.Errorf("failing scenario should be ✗, got %q", out[1])
				}
				if !strings.Contains(out[1], "timeout") {
					t.Errorf("failing step reasoning should appear, got %q", out[1])
				}
			},
		},
		{
			name: "observed truncation in failing step",
			agg: scenario.AggregateResult{
				Scenarios: []scenario.ScoredScenario{
					{ScenarioID: "trunc", Score: 10, Steps: []scenario.ScoredStep{
						{StepScore: scenario.StepScore{Score: 10}, StepResult: scenario.StepResult{
							Description: "big output",
							Observed:    strings.Repeat("x", attractor.MaxObservedBytes+100),
						}},
					}},
				},
			},
			wantLen: 1,
			wantCheck: func(t *testing.T, out []string) {
				t.Helper()
				if !strings.Contains(out[0], "Observed (2000B)") {
					t.Errorf("truncated observed should use byte-count label, got %q", out[0])
				}
				if !strings.Contains(out[0], "…") {
					t.Errorf("truncated observed should end with ellipsis, got %q", out[0])
				}
			},
		},
		{
			name: "no observed when empty",
			agg: scenario.AggregateResult{
				Scenarios: []scenario.ScoredScenario{
					{ScenarioID: "noobs", Score: 10, Steps: []scenario.ScoredStep{
						{StepScore: scenario.StepScore{Score: 10, Reasoning: "missing"}, StepResult: scenario.StepResult{
							Description: "check",
							Observed:    "",
						}},
					}},
				},
			},
			wantLen: 1,
			wantCheck: func(t *testing.T, out []string) {
				t.Helper()
				if strings.Contains(out[0], "Observed") {
					t.Errorf("empty observed should not appear in output, got %q", out[0])
				}
				if !strings.Contains(out[0], "missing") {
					t.Errorf("reasoning should still appear, got %q", out[0])
				}
			},
		},
		{
			name: "failing step with diagnostics",
			agg: scenario.AggregateResult{
				Scenarios: []scenario.ScoredScenario{
					{ScenarioID: "diag", Score: 20, Steps: []scenario.ScoredStep{
						{
							StepScore: scenario.StepScore{
								Score:     20,
								Reasoning: "bad status",
								Diagnostics: []llm.Diagnostic{
									{Category: "missing_endpoint", Detail: "POST /users returned 404"},
									{Category: "wrong_shape", Detail: "id field missing"},
								},
							},
							StepResult: scenario.StepResult{Description: "create user", Observed: "got 404"},
						},
					}},
				},
			},
			wantLen: 1,
			wantCheck: func(t *testing.T, out []string) {
				t.Helper()
				if !strings.Contains(out[0], "[missing_endpoint] POST /users returned 404") {
					t.Errorf("diagnostic line missing, got %q", out[0])
				}
				if !strings.Contains(out[0], "[wrong_shape] id field missing") {
					t.Errorf("second diagnostic line missing, got %q", out[0])
				}
				// Diagnostics must appear after Observed.
				obsIdx := strings.Index(out[0], "Observed:")
				diagIdx := strings.Index(out[0], "[missing_endpoint]")
				if obsIdx < 0 || diagIdx < 0 || diagIdx <= obsIdx {
					t.Errorf("diagnostics should appear after Observed, got %q", out[0])
				}
			},
		},
		{
			name: "passing step with diagnostics emits no diagnostic lines",
			agg: scenario.AggregateResult{
				Scenarios: []scenario.ScoredScenario{
					{ScenarioID: "passdiag", Score: 95, Steps: []scenario.ScoredStep{
						{
							StepScore: scenario.StepScore{
								Score:       95,
								Diagnostics: []llm.Diagnostic{{Category: "latency", Detail: "slow"}},
							},
							StepResult: scenario.StepResult{Description: "ping"},
						},
					}},
				},
			},
			wantLen: 1,
			wantCheck: func(t *testing.T, out []string) {
				t.Helper()
				if strings.Contains(out[0], "[latency]") {
					t.Errorf("passing step diagnostics must not appear in output, got %q", out[0])
				}
			},
		},
		{
			name: "empty diagnostics slice produces no extra output",
			agg: scenario.AggregateResult{
				Scenarios: []scenario.ScoredScenario{
					{ScenarioID: "nodiag", Score: 10, Steps: []scenario.ScoredStep{
						{
							StepScore:  scenario.StepScore{Score: 10, Reasoning: "fail", Diagnostics: []llm.Diagnostic{}},
							StepResult: scenario.StepResult{Description: "check"},
						},
					}},
				},
			},
			wantLen: 1,
			wantCheck: func(t *testing.T, out []string) {
				t.Helper()
				if strings.Contains(out[0], "[]") {
					t.Errorf("empty diagnostics must not produce bracket output, got %q", out[0])
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := buildDetailedFailures(tt.agg)
			if len(out) != tt.wantLen {
				t.Fatalf("len = %d, want %d; out = %v", len(out), tt.wantLen, out)
			}
			if tt.wantCheck != nil {
				tt.wantCheck(t, out)
			}
		})
	}
}

func TestTruncateObserved(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		max        int
		wantSuffix string
		wantLen    int // expected len of result, 0 = not checked
		unchanged  bool
	}{
		{
			name:      "empty string",
			input:     "",
			max:       500,
			unchanged: true,
		},
		{
			name:      "under limit",
			input:     "short",
			max:       500,
			unchanged: true,
		},
		{
			name:      "exact boundary",
			input:     strings.Repeat("x", 500),
			max:       500,
			unchanged: true,
		},
		{
			name:       "over limit",
			input:      strings.Repeat("x", 600),
			max:        500,
			wantSuffix: "…",
		},
		{
			name:       "multi-byte UTF-8 at boundary",
			input:      strings.Repeat("x", 499) + "\u2603" + "extra", // snowman = 3 bytes
			max:        500,
			wantSuffix: "…",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateObserved(tt.input, tt.max)
			if tt.unchanged {
				if got != tt.input {
					t.Errorf("expected unchanged input, got %q", got)
				}
				return
			}
			if !strings.HasSuffix(got, tt.wantSuffix) {
				t.Errorf("result should end with %q, got %q", tt.wantSuffix, got)
			}
			// Result before the suffix must be valid UTF-8.
			body := strings.TrimSuffix(got, "…")
			for i, r := range body {
				if r == '\uFFFD' {
					t.Errorf("invalid UTF-8 at position %d in truncated output", i)
					break
				}
			}
		})
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

func TestProgressFnVerbosity(t *testing.T) {
	ctx := context.Background()
	logger := testLogger()
	st, err := store.NewStore(ctx, ":memory:")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = st.Close() }()

	progress := attractor.IterationProgress{
		RunID:         "test-run",
		Iteration:     1,
		MaxIterations: 5,
		Outcome:       attractor.OutcomeValidated,
		Satisfaction:  72.5,
		Threshold:     95.0,
		TotalCostUSD:  0.01,
		Elapsed:       2 * time.Second,
		Failures: []string{
			"✗ scenario-a (45/100)\n  ✗ step one (40/100)\n    Reasoning: not working\n    Observed: got 404",
			"✓ scenario-b (95/100)",
		},
	}

	tests := []struct {
		name      string
		verbosity int
		wantLines []string
		noLines   []string
	}{
		{
			name:      "v0 shows summary only",
			verbosity: 0,
			wantLines: []string{"iter 1/5  satisfaction: 72.5/95.0"},
			noLines:   []string{"scenario-a", "scenario-b"},
		},
		{
			name:      "v1 shows first line of each failure",
			verbosity: 1,
			wantLines: []string{"iter 1/5  satisfaction: 72.5/95.0", "✗ scenario-a (45/100)", "✓ scenario-b (95/100)"},
			noLines:   []string{"Reasoning: not working"},
		},
		{
			name:      "v2 shows full failure detail",
			verbosity: 2,
			wantLines: []string{"iter 1/5  satisfaction: 72.5/95.0", "✗ scenario-a (45/100)", "Reasoning: not working", "✓ scenario-b (95/100)"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			fn := progressFn(ctx, logger, st, &buf, tt.verbosity)
			fn(progress)
			out := buf.String()
			for _, want := range tt.wantLines {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q\nfull output:\n%s", want, out)
				}
			}
			for _, noWant := range tt.noLines {
				if strings.Contains(out, noWant) {
					t.Errorf("output should not contain %q\nfull output:\n%s", noWant, out)
				}
			}
		})
	}
}

func TestOutputValidationVerbosity(t *testing.T) {
	agg := scenario.AggregateResult{
		Satisfaction: 55.0,
		TotalCostUSD: 0.001,
		Scenarios: []scenario.ScoredScenario{
			{
				ScenarioID: "failing-scenario",
				Score:      55,
				Weight:     1.0,
				Steps: []scenario.ScoredStep{
					{
						StepResult: scenario.StepResult{Description: "get items", Observed: "HTTP 404"},
						StepScore:  scenario.StepScore{Score: 55, Reasoning: "expected 200 got 404"},
					},
				},
			},
		},
	}

	tests := []struct {
		name      string
		verbosity int
		wantLines []string
		noLines   []string
	}{
		{
			name:      "v0 shows summary with reasoning",
			verbosity: 0,
			wantLines: []string{"failing-scenario", "Aggregate satisfaction: 55.0/100", "Reasoning: expected 200 got 404"},
			noLines:   []string{"Step detail:"},
		},
		{
			name:      "v1 shows per-scenario summary line",
			verbosity: 1,
			wantLines: []string{"Aggregate satisfaction: 55.0/100", "Step detail:", "failing-scenario", "Reasoning: expected 200 got 404"},
		},
		{
			name:      "v2 shows full step detail with reasoning",
			verbosity: 2,
			wantLines: []string{"Aggregate satisfaction: 55.0/100", "Step detail:", "expected 200 got 404"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := outputValidation(agg, "http://localhost", 0, "text", tt.verbosity, &buf)
			if err != nil {
				t.Fatalf("outputValidation: %v", err)
			}
			out := buf.String()
			for _, want := range tt.wantLines {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q\nfull output:\n%s", want, out)
				}
			}
			for _, noWant := range tt.noLines {
				if strings.Contains(out, noWant) {
					t.Errorf("output should not contain %q\nfull output:\n%s", noWant, out)
				}
			}
		})
	}
}

func TestFrugalModelFlagParsing(t *testing.T) {
	// Verify --frugal-model is a recognized flag and its value flows into runLoopParams.
	// We use a flag.FlagSet with the same definition as runCmd to avoid going through
	// the full run path (which requires Docker, API keys, etc.).
	ctx := context.Background()
	logger := testLogger()

	// Passing missing --spec and --scenarios should return errSpecAndScenariosRequired,
	// not a "flag provided but not defined" error — proving the flag is accepted.
	err := runCmd(ctx, logger, []string{"--frugal-model", "claude-haiku-4-5", "--spec", "s.md", "--scenarios", "s/"})
	if errors.Is(err, flag.ErrHelp) {
		t.Fatal("--frugal-model was not recognized as a valid flag")
	}
	// The flag is parsed successfully; the error here is from missing spec file or API keys,
	// not from an unrecognized flag.
	if err != nil && strings.Contains(err.Error(), "flag provided but not defined") {
		t.Errorf("--frugal-model flag not defined: %v", err)
	}
}

// simpleScenario returns a Scenario with a single GET request step.
func simpleScenario(id string) scenario.Scenario {
	return scenario.Scenario{
		ID: id,
		Steps: []scenario.Step{
			{Request: &scenario.Request{Method: "GET", Path: "/"}},
		},
	}
}

func TestRunAndScoreSequentialOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	scenarios := []scenario.Scenario{
		simpleScenario("s1"),
		simpleScenario("s2"),
		simpleScenario("s3"),
	}

	mock := &mockLLMClient{}
	agg, err := runAndScore(context.Background(), scenarios, executorOpts{
		targetURL:     srv.URL,
		logger:        testLogger(),
		sessionGetter: func() *container.Session { return nil },
	}, mock, "claude-haiku-4-5-20251001", nil, 1)
	if err != nil {
		t.Fatalf("runAndScore: %v", err)
	}

	if len(agg.Scenarios) != 3 {
		t.Fatalf("expected 3 scenarios, got %d", len(agg.Scenarios))
	}
	for i, want := range []string{"s1", "s2", "s3"} {
		if agg.Scenarios[i].ScenarioID != want {
			t.Errorf("scenario[%d]: got %q, want %q", i, agg.Scenarios[i].ScenarioID, want)
		}
	}
}

func TestRunAndScoreRestartCalledBetweenScenarios(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	scenarios := []scenario.Scenario{
		simpleScenario("s1"),
		simpleScenario("s2"),
	}

	restartCount := 0
	restart := attractor.RestartFunc(func(_ context.Context) (string, error) {
		restartCount++
		return srv.URL, nil
	})

	mock := &mockLLMClient{}
	agg, err := runAndScore(context.Background(), scenarios, executorOpts{
		targetURL:     srv.URL,
		logger:        testLogger(),
		sessionGetter: func() *container.Session { return nil },
	}, mock, "claude-haiku-4-5-20251001", restart, 1)
	if err != nil {
		t.Fatalf("runAndScore: %v", err)
	}

	if len(agg.Scenarios) != 2 {
		t.Fatalf("expected 2 scenarios, got %d", len(agg.Scenarios))
	}
	if restartCount != 1 {
		t.Errorf("expected restart called 1 time, got %d", restartCount)
	}
}

func TestRunAndScoreRestartErrorPropagation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	scenarios := []scenario.Scenario{
		simpleScenario("s1"),
		simpleScenario("s2"),
	}

	errRestart := errors.New("container restart failed")
	restart := attractor.RestartFunc(func(_ context.Context) (string, error) {
		return "", errRestart
	})

	mock := &mockLLMClient{}
	_, err := runAndScore(context.Background(), scenarios, executorOpts{
		targetURL:     srv.URL,
		logger:        testLogger(),
		sessionGetter: func() *container.Session { return nil },
	}, mock, "claude-haiku-4-5-20251001", restart, 1)
	if err == nil {
		t.Fatal("expected error from restart, got nil")
	}
	if !strings.Contains(err.Error(), "restart container") {
		t.Errorf("expected error to contain 'restart container', got: %v", err)
	}
}

func TestRunAndScoreNilRestart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	scenarios := []scenario.Scenario{
		simpleScenario("s1"),
		simpleScenario("s2"),
	}

	mock := &mockLLMClient{}
	agg, err := runAndScore(context.Background(), scenarios, executorOpts{
		targetURL:     srv.URL,
		logger:        testLogger(),
		sessionGetter: func() *container.Session { return nil },
	}, mock, "claude-haiku-4-5-20251001", nil, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(agg.Scenarios) != 2 {
		t.Fatalf("expected 2 scenarios, got %d", len(agg.Scenarios))
	}
}

func TestParseValidateFlagsParallelScenarios(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Run("valid flag parsed", func(t *testing.T) {
		vf, err := parseValidateFlags([]string{
			"--scenarios", "testdata/scenarios",
			"--target", srv.URL,
			"--parallel-scenarios", "4",
		})
		if err != nil {
			// Flag parsing is tested here; missing testdata dir is OK as long as flag is accepted.
			if strings.Contains(err.Error(), "flag provided but not defined") {
				t.Fatalf("--parallel-scenarios flag not recognized: %v", err)
			}
		} else if vf.parallelScenarios != 4 {
			t.Errorf("expected parallelScenarios=4, got %d", vf.parallelScenarios)
		}
	})

	t.Run("zero returns errInvalidParallelScenarios", func(t *testing.T) {
		_, err := parseValidateFlags([]string{
			"--scenarios", "testdata/scenarios",
			"--target", srv.URL,
			"--parallel-scenarios", "0",
		})
		if !errors.Is(err, errInvalidParallelScenarios) {
			t.Errorf("expected errInvalidParallelScenarios, got: %v", err)
		}
	})
}

func TestRunAndScoreParallelMatchesSequential(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	scenarios := []scenario.Scenario{
		simpleScenario("s1"),
		simpleScenario("s2"),
		simpleScenario("s3"),
	}

	mock := &mockLLMClient{
		judgeFn: func(_ context.Context, _ llm.JudgeRequest) (llm.JudgeResponse, error) {
			return llm.JudgeResponse{Score: 75, CostUSD: 0.001}, nil
		},
	}
	opts := executorOpts{
		targetURL:     srv.URL,
		logger:        testLogger(),
		sessionGetter: func() *container.Session { return nil },
	}
	const judgeModel = "claude-haiku-4-5-20251001"

	seqAgg, err := runAndScore(context.Background(), scenarios, opts, mock, judgeModel, nil, 1)
	if err != nil {
		t.Fatalf("sequential runAndScore: %v", err)
	}

	parAgg, err := runAndScore(context.Background(), scenarios, opts, mock, judgeModel, nil, 3)
	if err != nil {
		t.Fatalf("parallel runAndScore: %v", err)
	}

	if len(seqAgg.Scenarios) != len(parAgg.Scenarios) {
		t.Fatalf("scenario count mismatch: seq=%d par=%d", len(seqAgg.Scenarios), len(parAgg.Scenarios))
	}
	for i := range seqAgg.Scenarios {
		if seqAgg.Scenarios[i].ScenarioID != parAgg.Scenarios[i].ScenarioID {
			t.Errorf("scenario[%d] ID: seq=%q par=%q", i, seqAgg.Scenarios[i].ScenarioID, parAgg.Scenarios[i].ScenarioID)
		}
		if seqAgg.Scenarios[i].Score != parAgg.Scenarios[i].Score {
			t.Errorf("scenario[%d] score: seq=%.1f par=%.1f", i, seqAgg.Scenarios[i].Score, parAgg.Scenarios[i].Score)
		}
	}
	if seqAgg.Satisfaction != parAgg.Satisfaction {
		t.Errorf("satisfaction: seq=%.1f par=%.1f", seqAgg.Satisfaction, parAgg.Satisfaction)
	}
}

func TestRunAndScoreParallelContextCancellation(t *testing.T) {
	// blocking channel: all goroutines will block until context is canceled.
	block := make(chan struct{})

	var callCount atomic.Int32
	mock := &mockLLMClient{
		judgeFn: func(ctx context.Context, _ llm.JudgeRequest) (llm.JudgeResponse, error) {
			callCount.Add(1)
			select {
			case <-block:
				return llm.JudgeResponse{Score: 90}, nil
			case <-ctx.Done():
				return llm.JudgeResponse{}, ctx.Err()
			}
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Cancel after judge calls have started.
		for callCount.Load() == 0 {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()

	scenarios := []scenario.Scenario{
		simpleScenario("s1"),
		simpleScenario("s2"),
	}
	_, err := runAndScore(ctx, scenarios, executorOpts{
		targetURL:     srv.URL,
		logger:        testLogger(),
		sessionGetter: func() *container.Session { return nil },
	}, mock, "claude-haiku-4-5-20251001", nil, 2)
	if err == nil {
		t.Fatal("expected error from context cancellation, got nil")
	}
}

func TestRunAndScoreParallelConcurrencyBound(t *testing.T) {
	// Use a blocking channel inside a mock executor to verify that at most
	// parallelism goroutines run concurrently. The mock LLM client blocks
	// inside Judge until we release it, so we can count concurrent calls.
	const parallelism = 2
	const numScenarios = 4

	var concurrent atomic.Int32
	var maxConcurrent atomic.Int32
	release := make(chan struct{})

	mock := &mockLLMClient{
		judgeFn: func(_ context.Context, _ llm.JudgeRequest) (llm.JudgeResponse, error) {
			cur := concurrent.Add(1)
			defer concurrent.Add(-1)

			for {
				prev := maxConcurrent.Load()
				if cur <= prev || maxConcurrent.CompareAndSwap(prev, cur) {
					break
				}
			}

			<-release
			return llm.JudgeResponse{Score: 90}, nil
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	scenarios := make([]scenario.Scenario, numScenarios)
	for i := range scenarios {
		scenarios[i] = simpleScenario(fmt.Sprintf("s%d", i+1))
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = runAndScore(context.Background(), scenarios, executorOpts{
			targetURL:     srv.URL,
			logger:        testLogger(),
			sessionGetter: func() *container.Session { return nil },
		}, mock, "claude-haiku-4-5-20251001", nil, parallelism)
	}()

	// Wait until parallelism goroutines are blocked inside Judge.
	deadline := time.Now().Add(5 * time.Second)
	for concurrent.Load() < parallelism && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if concurrent.Load() > parallelism {
		t.Errorf("concurrent goroutines exceeded parallelism: got %d, want <= %d", concurrent.Load(), parallelism)
	}

	// Unblock all goroutines.
	close(release)
	<-done

	if got := maxConcurrent.Load(); got > parallelism {
		t.Errorf("max concurrent goroutines=%d exceeded parallelism=%d", got, parallelism)
	}
}

func TestRunAndScoreParallelRestartSkipped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var restartCalls atomic.Int32
	restart := attractor.RestartFunc(func(_ context.Context) (string, error) {
		restartCalls.Add(1)
		return srv.URL, nil
	})

	scenarios := []scenario.Scenario{
		simpleScenario("s1"),
		simpleScenario("s2"),
		simpleScenario("s3"),
	}

	mock := &mockLLMClient{}
	_, err := runAndScore(context.Background(), scenarios, executorOpts{
		targetURL:     srv.URL,
		logger:        testLogger(),
		sessionGetter: func() *container.Session { return nil },
	}, mock, "claude-haiku-4-5-20251001", restart, 2)
	if err != nil {
		t.Fatalf("runAndScore: %v", err)
	}
	if got := restartCalls.Load(); got != 0 {
		t.Errorf("expected restart not called when parallelism>1, got %d calls", got)
	}
}

// TestRunCmdAgenticRequiresAgentClient verifies that --agentic is rejected when the
// resolved LLM client does not implement AgentClient (e.g. OpenAI provider).
func TestRunCmdAgenticRequiresAgentClient(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "fake-key-for-testing")
	t.Setenv("ANTHROPIC_API_KEY", "")
	logger := testLogger()
	ctx := context.Background()

	err := runCmd(ctx, logger, []string{
		"--spec", "s.md", "--scenarios", "s/",
		"--provider", "openai",
		"--agentic",
	})
	if !errors.Is(err, errAgenticRequiresAnthropic) {
		t.Errorf("expected errAgenticRequiresAnthropic, got: %v", err)
	}
}

// TestProgressFnTurns verifies that a non-zero Turns value is included in progress output.
func TestProgressFnTurns(t *testing.T) {
	ctx := context.Background()
	logger := testLogger()
	st, err := store.NewStore(ctx, ":memory:")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = st.Close() }()

	tests := []struct {
		name      string
		turns     int
		wantTurns bool
	}{
		{
			name:      "turns=0 omitted",
			turns:     0,
			wantTurns: false,
		},
		{
			name:      "turns>0 included in validated outcome",
			turns:     7,
			wantTurns: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			fn := progressFn(ctx, logger, st, &buf, 0)
			fn(attractor.IterationProgress{
				RunID:         "test-run",
				Iteration:     1,
				MaxIterations: 5,
				Outcome:       attractor.OutcomeValidated,
				Satisfaction:  80.0,
				Threshold:     95.0,
				TotalCostUSD:  0.01,
				Elapsed:       time.Second,
				Turns:         tt.turns,
			})
			out := buf.String()
			containsTurns := strings.Contains(out, fmt.Sprintf("turns=%d", tt.turns))
			if tt.wantTurns && !containsTurns {
				t.Errorf("expected turns=%d in output, got: %s", tt.turns, out)
			}
			if !tt.wantTurns && strings.Contains(out, "turns=") {
				t.Errorf("expected no turns= in output, got: %s", out)
			}
		})
	}
}
