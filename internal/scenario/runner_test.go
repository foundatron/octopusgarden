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
)

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

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
				Request:     Request{Method: "POST", Path: "/items", Body: map[string]any{"name": "test item"}},
				Capture:     []Capture{{Name: "item_id", JSONPath: "$.id"}},
			},
		},
		Steps: []Step{
			{
				Description: "Read item",
				Request:     Request{Method: "GET", Path: "/items/{item_id}"},
				Expect:      "Returns the item",
			},
		},
	}

	runner := NewRunner(srv.URL, srv.Client(), newTestLogger())
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
	if step.Response.Status != http.StatusOK {
		t.Errorf("got status %d, want %d", step.Response.Status, http.StatusOK)
	}
	if !strings.Contains(step.Response.Body, `"id": 99`) {
		t.Errorf("response body missing expected content: %s", step.Response.Body)
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
				Request:     Request{Method: "POST", Path: "/setup"},
				Capture:     []Capture{{Name: "auth_token", JSONPath: "$.token"}},
			},
		},
		Steps: []Step{
			{
				Description: "Use token in body",
				Request: Request{
					Method: "POST",
					Path:   "/action",
					Body:   map[string]any{"token": "{auth_token}"},
				},
				Expect: "Success",
			},
		},
	}

	runner := NewRunner(srv.URL, srv.Client(), newTestLogger())
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
				Request:     Request{Method: "POST", Path: "/login"},
				Capture:     []Capture{{Name: "token", JSONPath: "$.token"}},
			},
		},
		Steps: []Step{
			{
				Description: "Access protected",
				Request: Request{
					Method:  "GET",
					Path:    "/protected",
					Headers: map[string]string{"Authorization": "Bearer {token}"},
				},
				Expect: "Success",
			},
		},
	}

	runner := NewRunner(srv.URL, srv.Client(), newTestLogger())
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
				Request:     Request{Method: "GET", Path: "/fail"},
			},
		},
		Steps: []Step{
			{
				Description: "Should not run",
				Request:     Request{Method: "GET", Path: "/ok"},
				Expect:      "Never reached",
			},
		},
	}

	runner := NewRunner("http://localhost:0", client, newTestLogger())
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
				Request:     Request{Method: "GET", Path: "/fail"},
				Expect:      "Should fail",
			},
			{
				Description: "Succeeding step",
				Request:     Request{Method: "GET", Path: "/ok"},
				Expect:      "Should succeed",
			},
		},
	}

	runner := NewRunner("http://localhost:0", client, newTestLogger())
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
	if result.Steps[1].Err != nil {
		t.Errorf("unexpected error on second step: %v", result.Steps[1].Err)
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
				Request:     Request{Method: "GET", Path: "/no-body"},
				Expect:      "ok",
			},
		},
	}

	runner := NewRunner(srv.URL, srv.Client(), newTestLogger())
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
				Request:     Request{Method: "GET", Path: "/slow"},
				Expect:      "Never",
			},
		},
	}

	runner := NewRunner(srv.URL, srv.Client(), newTestLogger())
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
