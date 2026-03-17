package scenario

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newHTTPRunner(baseURL string, client *http.Client, logger *slog.Logger) *Runner {
	return NewRunner(
		map[string]StepExecutor{
			"request": &HTTPExecutor{Client: client, BaseURL: baseURL},
			"exec":    &ExecExecutor{},
		},
		logger,
	)
}

func TestRunnerHappyPath(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /items", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id": 99, "name": "test item"}`)
	})
	mux.HandleFunc("GET /items/99", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id": 99, "name": "test item"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	weight := 1.0
	sc := Scenario{
		ID:          "test-crud",
		Description: "Test CRUD",
		Weight:      &weight,
		Setup: []Step{
			{
				Description: "Create item",
				Request:     &Request{Method: "POST", Path: "/items", Body: map[string]any{"name": "test item"}},
				Capture:     []Capture{{Name: "item_id", JSONPath: "$.id"}},
			},
		},
		Steps: []Step{
			{
				Description: "Read item",
				Request:     &Request{Method: "GET", Path: "/items/{item_id}"},
				Expect:      "Returns the item",
			},
		},
	}

	runner := newHTTPRunner(srv.URL, srv.Client(), newTestLogger())
	result, err := runner.Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ScenarioID != "test-crud" {
		t.Errorf("got scenario ID %q, want %q", result.ScenarioID, "test-crud")
	}

	// Setup steps should NOT appear in results.
	if len(result.Steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(result.Steps))
	}

	step := result.Steps[0]
	if step.Err != nil {
		t.Fatalf("unexpected step error: %v", step.Err)
	}
	if !strings.Contains(step.Observed, "HTTP 200") {
		t.Errorf("observed missing expected status: %s", step.Observed)
	}
	if !strings.Contains(step.CaptureBody, `"id": 99`) {
		t.Errorf("capture body missing expected content: %s", step.CaptureBody)
	}
	if step.Duration <= 0 {
		t.Error("expected non-zero duration for executed step")
	}
}

func TestRunnerVariableSubstitutionInBody(t *testing.T) {
	var gotBody string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /setup", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"token": "abc123"}`)
	})
	mux.HandleFunc("POST /action", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok": true}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	sc := Scenario{
		ID: "var-sub",
		Setup: []Step{
			{
				Description: "Get token",
				Request:     &Request{Method: "POST", Path: "/setup"},
				Capture:     []Capture{{Name: "auth_token", JSONPath: "$.token"}},
			},
		},
		Steps: []Step{
			{
				Description: "Use token in body",
				Request: &Request{
					Method: "POST",
					Path:   "/action",
					Body:   map[string]any{"token": "{auth_token}"},
				},
				Expect: "Success",
			},
		},
	}

	runner := newHTTPRunner(srv.URL, srv.Client(), newTestLogger())
	_, err := runner.Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(gotBody, "abc123") {
		t.Errorf("expected body to contain substituted token, got: %s", gotBody)
	}
}

func TestRunnerVariableSubstitutionInHeaders(t *testing.T) {
	var gotAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"token": "bearer-xyz"}`)
	})
	mux.HandleFunc("GET /protected", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok": true}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	sc := Scenario{
		ID: "header-sub",
		Setup: []Step{
			{
				Description: "Login",
				Request:     &Request{Method: "POST", Path: "/login"},
				Capture:     []Capture{{Name: "token", JSONPath: "$.token"}},
			},
		},
		Steps: []Step{
			{
				Description: "Access protected",
				Request: &Request{
					Method:  "GET",
					Path:    "/protected",
					Headers: map[string]string{"Authorization": "Bearer {token}"},
				},
				Expect: "Success",
			},
		},
	}

	runner := newHTTPRunner(srv.URL, srv.Client(), newTestLogger())
	_, err := runner.Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotAuth != "Bearer bearer-xyz" {
		t.Errorf("got Authorization %q, want %q", gotAuth, "Bearer bearer-xyz")
	}
}

func TestRunnerSetupFailure(t *testing.T) {
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return nil, errors.New("connection refused")
		}),
	}

	sc := Scenario{
		ID: "setup-fail",
		Setup: []Step{
			{
				Description: "Failing setup",
				Request:     &Request{Method: "GET", Path: "/fail"},
			},
		},
		Steps: []Step{
			{
				Description: "Should not run",
				Request:     &Request{Method: "GET", Path: "/ok"},
				Expect:      "Never reached",
			},
		},
	}

	runner := newHTTPRunner("http://localhost:0", client, newTestLogger())
	_, err := runner.Run(context.Background(), sc)
	if err == nil {
		t.Fatal("expected error for setup failure")
	}
	if !errors.Is(err, errSetupFailed) {
		t.Errorf("expected errSetupFailed, got: %v", err)
	}
}

func TestRunnerTransportErrorOnJudgedStep(t *testing.T) {
	callCount := 0
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			callCount++
			if callCount == 1 {
				return nil, errors.New("connection reset")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"ok": true}`)),
			}, nil
		}),
	}

	sc := Scenario{
		ID: "transport-err",
		Steps: []Step{
			{
				Description: "Failing step",
				Request:     &Request{Method: "GET", Path: "/fail"},
				Expect:      "Should fail",
			},
			{
				Description: "Succeeding step",
				Request:     &Request{Method: "GET", Path: "/ok"},
				Expect:      "Should succeed",
			},
		},
	}

	runner := newHTTPRunner("http://localhost:0", client, newTestLogger())
	result, err := runner.Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Steps) != 2 {
		t.Fatalf("got %d steps, want 2", len(result.Steps))
	}

	if result.Steps[0].Err == nil {
		t.Error("expected error on first step")
	}
	if result.Steps[0].Duration <= 0 {
		t.Error("expected non-zero duration for failed step")
	}
	if result.Steps[1].Err != nil {
		t.Errorf("unexpected error on second step: %v", result.Steps[1].Err)
	}
	if result.Steps[1].Duration <= 0 {
		t.Error("expected non-zero duration for succeeded step")
	}
}

func TestRunnerGetNoContentType(t *testing.T) {
	var gotContentType string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /no-body", func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		fmt.Fprint(w, "ok")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	sc := Scenario{
		ID: "no-body",
		Steps: []Step{
			{
				Description: "GET with no body",
				Request:     &Request{Method: "GET", Path: "/no-body"},
				Expect:      "ok",
			},
		},
	}

	runner := newHTTPRunner(srv.URL, srv.Client(), newTestLogger())
	_, err := runner.Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotContentType != "" {
		t.Errorf("expected no Content-Type for GET, got %q", gotContentType)
	}
}

func TestRunnerContextCancellation(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	sc := Scenario{
		ID: "ctx-cancel",
		Steps: []Step{
			{
				Description: "Canceled request",
				Request:     &Request{Method: "GET", Path: "/slow"},
				Expect:      "Never",
			},
		},
	}

	runner := newHTTPRunner(srv.URL, srv.Client(), newTestLogger())
	result, err := runner.Run(ctx, sc)
	if err != nil {
		t.Fatalf("unexpected error from Run: %v", err)
	}

	if len(result.Steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(result.Steps))
	}
	if result.Steps[0].Err == nil {
		t.Error("expected transport error from canceled context")
	}
}

func TestSubstituteVarsJSON(t *testing.T) {
	tests := []struct {
		name string
		s    string
		vars map[string]string
		want string
	}{
		{
			name: "simple value",
			s:    `{"name":"{val}"}`,
			vars: map[string]string{"val": "hello"},
			want: `{"name":"hello"}`,
		},
		{
			name: "value with quotes",
			s:    `{"name":"{val}"}`,
			vars: map[string]string{"val": `say "hi"`},
			want: `{"name":"say \"hi\""}`,
		},
		{
			name: "value with newline",
			s:    `{"name":"{val}"}`,
			vars: map[string]string{"val": "line1\nline2"},
			want: `{"name":"line1\nline2"}`,
		},
		{
			name: "value with backslash",
			s:    `{"path":"{val}"}`,
			vars: map[string]string{"val": `C:\Users`},
			want: `{"path":"C:\\Users"}`,
		},
		{
			name: "no vars",
			s:    `{"key":"value"}`,
			vars: map[string]string{},
			want: `{"key":"value"}`,
		},
		{
			name: "multiple vars",
			s:    `{"a":"{x}","b":"{y}"}`,
			vars: map[string]string{"x": "1", "y": "2"},
			want: `{"a":"1","b":"2"}`,
		},
		{
			name: "unicode and special chars",
			s:    `{"msg":"{val}"}`,
			vars: map[string]string{"val": "tab\there & <angle>"},
			want: `{"msg":"tab\there \u0026 \u003cangle\u003e"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := substituteVarsJSON(tt.s, tt.vars)
			if got != tt.want {
				t.Errorf("substituteVarsJSON(%q, %v)\n got %q\nwant %q", tt.s, tt.vars, got, tt.want)
			}
		})
	}
}

func TestRunnerExecStep(t *testing.T) {
	sc := Scenario{
		ID: "exec-test",
		Steps: []Step{
			{
				Description: "Run echo",
				Exec:        &ExecRequest{Command: "echo hello"},
				Expect:      "Should output hello",
			},
		},
	}

	runner := newHTTPRunner("http://unused", http.DefaultClient, newTestLogger())
	result, err := runner.Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(result.Steps))
	}

	step := result.Steps[0]
	if step.Err != nil {
		t.Fatalf("unexpected step error: %v", step.Err)
	}
	if step.StepType != "exec" {
		t.Errorf("got step type %q, want %q", step.StepType, "exec")
	}
	if !strings.Contains(step.Observed, "Exit code: 0") {
		t.Errorf("observed missing exit code: %s", step.Observed)
	}
	if !strings.Contains(step.CaptureBody, "hello") {
		t.Errorf("capture body missing expected content: %s", step.CaptureBody)
	}
}

func TestRunnerMixedSteps(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /items", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"count": 0}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	sc := Scenario{
		ID: "mixed",
		Steps: []Step{
			{
				Description: "HTTP step",
				Request:     &Request{Method: "GET", Path: "/items"},
				Expect:      "Returns items",
			},
			{
				Description: "Exec step",
				Exec:        &ExecRequest{Command: "echo done"},
				Expect:      "Outputs done",
			},
		},
	}

	runner := newHTTPRunner(srv.URL, srv.Client(), newTestLogger())
	result, err := runner.Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Steps) != 2 {
		t.Fatalf("got %d steps, want 2", len(result.Steps))
	}

	if result.Steps[0].StepType != "request" {
		t.Errorf("step 0 type = %q, want %q", result.Steps[0].StepType, "request")
	}
	if result.Steps[1].StepType != "exec" {
		t.Errorf("step 1 type = %q, want %q", result.Steps[1].StepType, "exec")
	}
}

func TestResolveCapture(t *testing.T) {
	tests := []struct {
		name    string
		capture Capture
		output  StepOutput
		want    string
		wantErr bool
	}{
		{
			name:    "source only returns trimmed source content",
			capture: Capture{Name: "out", Source: "stderr"},
			output: StepOutput{
				CaptureSources: map[string]string{
					"stderr": "  error output\n",
				},
			},
			want: "error output",
		},
		{
			name:    "jsonpath only extracts from CaptureBody",
			capture: Capture{Name: "id", JSONPath: "$.id"},
			output: StepOutput{
				CaptureBody: `{"id":"42","name":"test"}`,
			},
			want: "42",
		},
		{
			name:    "source and jsonpath extracts jsonpath from source",
			capture: Capture{Name: "val", Source: "stdout", JSONPath: "$.key"},
			output: StepOutput{
				CaptureBody: `{"key":"from-body"}`,
				CaptureSources: map[string]string{
					"stdout": `{"key":"from-stdout"}`,
				},
			},
			want: "from-stdout",
		},
		{
			name:    "invalid jsonpath returns error",
			capture: Capture{Name: "bad", JSONPath: "invalid"},
			output: StepOutput{
				CaptureBody: `{"id":"1"}`,
			},
			wantErr: true,
		},
		{
			name:    "no source or jsonpath returns errNoCapture",
			capture: Capture{Name: "empty"},
			output:  StepOutput{},
			wantErr: true,
		},
		{
			name:    "source with empty content returns empty string",
			capture: Capture{Name: "out", Source: "stdout"},
			output: StepOutput{
				CaptureSources: map[string]string{
					"stdout": "",
				},
			},
			want: "",
		},
		{
			name:    "exitcode source returns raw value",
			capture: Capture{Name: "code", Source: "exitcode"},
			output: StepOutput{
				CaptureSources: map[string]string{
					"exitcode": "0",
				},
			},
			want: "0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveCapture(tt.capture, tt.output)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got value %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// mockExecutor returns an error for the first failCount calls, then succeeds.
type mockExecutor struct {
	failCount int
	callCount int
	failErr   error
	output    StepOutput
}

func (m *mockExecutor) Execute(_ context.Context, _ Step, _ map[string]string) (StepOutput, error) {
	m.callCount++
	if m.callCount <= m.failCount {
		return StepOutput{}, m.failErr
	}
	return m.output, nil
}

func (m *mockExecutor) ValidCaptureSources() []string { return nil }

func TestExecuteWithRetry(t *testing.T) {
	okOutput := StepOutput{Observed: "ok", CaptureBody: `{"id":"1"}`}
	transientErr := errors.New("connection refused")

	tests := []struct {
		name       string
		retry      *Retry
		failCount  int
		wantErr    bool
		wantCalls  int
		wantOutput StepOutput
	}{
		{
			name:       "succeeds on first attempt",
			retry:      &Retry{Attempts: 3, Interval: "10ms"},
			failCount:  0,
			wantCalls:  1,
			wantOutput: okOutput,
		},
		{
			name:       "succeeds after transient failures",
			retry:      &Retry{Attempts: 5, Interval: "10ms"},
			failCount:  2,
			wantCalls:  3,
			wantOutput: okOutput,
		},
		{
			name:      "exhausts all attempts",
			retry:     &Retry{Attempts: 3, Interval: "10ms"},
			failCount: 10,
			wantErr:   true,
			wantCalls: 3,
		},
		{
			name:       "default attempts when zero",
			retry:      &Retry{Attempts: 0, Interval: "10ms"},
			failCount:  2,
			wantCalls:  3,
			wantOutput: okOutput,
		},
		{
			name:      "invalid interval returns error",
			retry:     &Retry{Attempts: 3, Interval: "notaduration"},
			failCount: 0,
			wantErr:   true,
			wantCalls: 0,
		},
		{
			name:      "invalid timeout returns error",
			retry:     &Retry{Attempts: 3, Interval: "10ms", Timeout: "notaduration"},
			failCount: 0,
			wantErr:   true,
			wantCalls: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockExecutor{
				failCount: tt.failCount,
				failErr:   transientErr,
				output:    okOutput,
			}

			runner := NewRunner(map[string]StepExecutor{"request": mock}, newTestLogger())
			step := Step{
				Description: "test step",
				Request:     &Request{Method: "GET", Path: "/test"},
				Retry:       tt.retry,
			}

			output, err := runner.executeStep(context.Background(), mock, step, nil)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if output.Observed != tt.wantOutput.Observed {
				t.Errorf("observed = %q, want %q", output.Observed, tt.wantOutput.Observed)
			}
			if mock.callCount != tt.wantCalls {
				t.Errorf("callCount = %d, want %d", mock.callCount, tt.wantCalls)
			}
		})
	}
}

func TestRetryTimeoutCapsRetries(t *testing.T) {
	mock := &mockExecutor{
		failCount: 100,
		failErr:   errors.New("connection refused"),
	}

	runner := NewRunner(map[string]StepExecutor{"request": mock}, newTestLogger())
	step := Step{
		Description: "timeout step",
		Request:     &Request{Method: "GET", Path: "/test"},
		Retry:       &Retry{Attempts: 100, Interval: "50ms", Timeout: "120ms"},
	}

	_, err := runner.executeStep(context.Background(), mock, step, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	// Should have executed fewer than 10 attempts due to timeout.
	if mock.callCount >= 10 {
		t.Errorf("expected timeout to cap retries, got %d calls", mock.callCount)
	}
}

func TestRetryContextCancellation(t *testing.T) {
	mock := &mockExecutor{
		failCount: 100,
		failErr:   errors.New("connection refused"),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	runner := NewRunner(map[string]StepExecutor{"request": mock}, newTestLogger())
	step := Step{
		Description: "cancel step",
		Request:     &Request{Method: "GET", Path: "/test"},
		Retry:       &Retry{Attempts: 10, Interval: "1s"},
	}

	_, err := runner.executeStep(ctx, mock, step, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	// Should stop after first attempt + context check, returning context error.
	if mock.callCount > 2 {
		t.Errorf("expected early exit on cancel, got %d calls", mock.callCount)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestRetryNoRetryBehaviorUnchanged(t *testing.T) {
	mock := &mockExecutor{
		failCount: 0,
		output:    StepOutput{Observed: "ok"},
	}

	runner := NewRunner(map[string]StepExecutor{"request": mock}, newTestLogger())
	step := Step{
		Description: "no retry",
		Request:     &Request{Method: "GET", Path: "/test"},
	}

	output, err := runner.executeStep(context.Background(), mock, step, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output.Observed != "ok" {
		t.Errorf("observed = %q, want %q", output.Observed, "ok")
	}
	if mock.callCount != 1 {
		t.Errorf("callCount = %d, want 1", mock.callCount)
	}
}

func TestRetrySetupStepSuccess(t *testing.T) {
	mock := &mockExecutor{
		failCount: 1,
		failErr:   errors.New("connection refused"),
		output:    StepOutput{Observed: "ok", CaptureBody: `{"id":"42"}`},
	}

	runner := NewRunner(map[string]StepExecutor{"request": mock}, newTestLogger())
	sc := Scenario{
		ID: "retry-setup",
		Setup: []Step{
			{
				Description: "Setup with retry",
				Request:     &Request{Method: "POST", Path: "/items"},
				Retry:       &Retry{Attempts: 3, Interval: "10ms"},
				Capture:     []Capture{{Name: "item_id", JSONPath: "$.id"}},
			},
		},
		Steps: []Step{
			{
				Description: "Read item",
				Request:     &Request{Method: "GET", Path: "/items/{item_id}"},
				Expect:      "ok",
			},
		},
	}

	result, err := runner.Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.callCount < 2 {
		t.Errorf("callCount = %d, want at least 2 (1 fail + 1 success)", mock.callCount)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(result.Steps))
	}
}

func TestRetrySetupExhausted(t *testing.T) {
	mock := &mockExecutor{
		failCount: 10,
		failErr:   errors.New("connection refused"),
	}

	runner := NewRunner(map[string]StepExecutor{"request": mock}, newTestLogger())
	sc := Scenario{
		ID: "retry-setup-fail",
		Setup: []Step{
			{
				Description: "Setup with retry",
				Request:     &Request{Method: "POST", Path: "/items"},
				Retry:       &Retry{Attempts: 3, Interval: "10ms"},
			},
		},
		Steps: []Step{
			{
				Description: "Should not run",
				Request:     &Request{Method: "GET", Path: "/items/1"},
				Expect:      "never",
			},
		},
	}

	_, err := runner.Run(context.Background(), sc)
	if err == nil {
		t.Fatal("expected error for exhausted setup retry")
	}
	if !errors.Is(err, errSetupFailed) {
		t.Errorf("expected errSetupFailed, got: %v", err)
	}
}

func TestRetryCapturesFromFinalAttempt(t *testing.T) {
	callCount := 0
	exec := &countingExecutor{
		maxFails: 2,
		failErr:  errors.New("connection refused"),
		outputs: []StepOutput{
			{}, // fail
			{}, // fail
			{Observed: "ok", CaptureBody: `{"id":"final"}`},
		},
		callCount: &callCount,
	}

	runner := NewRunner(map[string]StepExecutor{"request": exec}, newTestLogger())
	step := Step{
		Description: "retry capture",
		Request:     &Request{Method: "GET", Path: "/test"},
		Retry:       &Retry{Attempts: 5, Interval: "10ms"},
		Capture:     []Capture{{Name: "result_id", JSONPath: "$.id"}},
	}

	output, err := runner.executeStep(context.Background(), exec, step, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output.CaptureBody != `{"id":"final"}` {
		t.Errorf("capture body = %q, want from final attempt", output.CaptureBody)
	}
}

type countingExecutor struct {
	maxFails  int
	failErr   error
	outputs   []StepOutput
	callCount *int
}

func (e *countingExecutor) Execute(_ context.Context, _ Step, _ map[string]string) (StepOutput, error) {
	idx := *e.callCount
	*e.callCount++
	if idx < e.maxFails {
		return StepOutput{}, e.failErr
	}
	if idx < len(e.outputs) {
		return e.outputs[idx], nil
	}
	return StepOutput{}, nil
}

func (e *countingExecutor) ValidCaptureSources() []string { return nil }

func TestRunnerStepDelay(t *testing.T) {
	mock := &mockExecutor{output: StepOutput{Observed: "ok"}}
	runner := NewRunner(map[string]StepExecutor{"request": mock}, newTestLogger())
	sc := Scenario{
		ID: "delay-judged",
		Steps: []Step{
			{
				Description: "delayed step",
				Delay:       "100ms",
				Request:     &Request{Method: "GET", Path: "/ok"},
				Expect:      "ok",
			},
		},
	}

	start := time.Now()
	result, err := runner.Run(context.Background(), sc)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(result.Steps))
	}
	if result.Steps[0].Err != nil {
		t.Fatalf("unexpected step error: %v", result.Steps[0].Err)
	}
	if elapsed < 100*time.Millisecond {
		t.Errorf("expected elapsed >= 100ms, got %s", elapsed)
	}
}

func TestRunnerSetupStepDelay(t *testing.T) {
	mock := &mockExecutor{output: StepOutput{Observed: "ok"}}
	runner := NewRunner(map[string]StepExecutor{"request": mock}, newTestLogger())
	sc := Scenario{
		ID: "delay-setup",
		Setup: []Step{
			{
				Description: "delayed setup",
				Delay:       "100ms",
				Request:     &Request{Method: "GET", Path: "/ok"},
			},
		},
		Steps: []Step{
			{
				Description: "judged step",
				Request:     &Request{Method: "GET", Path: "/ok"},
				Expect:      "ok",
			},
		},
	}

	start := time.Now()
	_, err := runner.Run(context.Background(), sc)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed < 100*time.Millisecond {
		t.Errorf("expected elapsed >= 100ms, got %s", elapsed)
	}
}

func TestRunnerStepDelayInvalidDuration(t *testing.T) {
	mock := &mockExecutor{output: StepOutput{Observed: "ok"}}
	runner := NewRunner(map[string]StepExecutor{"request": mock}, newTestLogger())
	sc := Scenario{
		ID: "delay-invalid",
		Steps: []Step{
			{
				Description: "bad delay",
				Delay:       "notaduration",
				Request:     &Request{Method: "GET", Path: "/ok"},
				Expect:      "ok",
			},
		},
	}

	result, err := runner.Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("unexpected error from Run: %v", err)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(result.Steps))
	}
	if result.Steps[0].Err == nil {
		t.Fatal("expected step error for invalid delay, got nil")
	}
	if !errors.Is(result.Steps[0].Err, errInvalidDelay) {
		t.Errorf("expected errInvalidDelay, got: %v", result.Steps[0].Err)
	}
}

func TestRunnerSetupStepDelayInvalidDuration(t *testing.T) {
	mock := &mockExecutor{output: StepOutput{Observed: "ok"}}
	runner := NewRunner(map[string]StepExecutor{"request": mock}, newTestLogger())
	sc := Scenario{
		ID: "delay-setup-invalid",
		Setup: []Step{
			{
				Description: "bad delay setup",
				Delay:       "notaduration",
				Request:     &Request{Method: "GET", Path: "/ok"},
			},
		},
		Steps: []Step{
			{
				Description: "should not run",
				Request:     &Request{Method: "GET", Path: "/ok"},
				Expect:      "never",
			},
		},
	}

	_, err := runner.Run(context.Background(), sc)
	if err == nil {
		t.Fatal("expected error for invalid setup delay")
	}
	if !errors.Is(err, errSetupFailed) {
		t.Errorf("expected errSetupFailed, got: %v", err)
	}
	if !errors.Is(err, errInvalidDelay) {
		t.Errorf("expected errInvalidDelay in chain, got: %v", err)
	}
}

func TestRunnerStepDelayContextCancellation(t *testing.T) {
	mock := &mockExecutor{output: StepOutput{Observed: "ok"}}
	runner := NewRunner(map[string]StepExecutor{"request": mock}, newTestLogger())
	sc := Scenario{
		ID: "delay-cancel",
		Steps: []Step{
			{
				Description: "long delay",
				Delay:       "10s",
				Request:     &Request{Method: "GET", Path: "/ok"},
				Expect:      "never",
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	start := time.Now()
	result, err := runner.Run(ctx, sc)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error from Run: %v", err)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(result.Steps))
	}
	if result.Steps[0].Err == nil {
		t.Fatal("expected step error from context cancellation")
	}
	if elapsed >= time.Second {
		t.Errorf("expected fast cancellation, got %s", elapsed)
	}
}

func TestRunnerUnknownStepType(t *testing.T) {
	sc := Scenario{
		ID: "unknown-type",
		Steps: []Step{
			{
				Description: "Step with no type",
				Expect:      "Should fail",
			},
		},
	}

	runner := newHTTPRunner("http://unused", http.DefaultClient, newTestLogger())
	result, err := runner.Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(result.Steps))
	}

	if !errors.Is(result.Steps[0].Err, errUnknownStepType) {
		t.Errorf("expected errUnknownStepType, got: %v", result.Steps[0].Err)
	}
	if result.Steps[0].Duration <= 0 {
		t.Error("expected non-zero duration even for executor resolution failure")
	}
}

// TestRunnerTUINoExecutorRegistered verifies that a tui step returns
// errNoExecutorRegistered (not errTUINotImplemented) when no TUI executor is wired in.
func TestRunnerTUINoExecutorRegistered(t *testing.T) {
	sc := Scenario{
		ID: "tui-unregistered",
		Steps: []Step{
			{
				Description: "TUI step without registered executor",
				TUI:         &TUIRequest{Command: "echo hello"},
				Expect:      "Should fail with no executor",
			},
		},
	}

	runner := newHTTPRunner("http://unused", http.DefaultClient, newTestLogger())
	result, err := runner.Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(result.Steps))
	}

	if !errors.Is(result.Steps[0].Err, errNoExecutorRegistered) {
		t.Errorf("expected errNoExecutorRegistered, got: %v", result.Steps[0].Err)
	}
}
