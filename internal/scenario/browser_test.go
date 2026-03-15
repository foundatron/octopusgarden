package scenario

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestIsTransientCDPError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unrelated", errors.New("connection refused"), false},
		{"context lost", errors.New("Cannot find context with specified id"), true},
		{"node lost", errors.New("No node with given id found"), true},
		{"wrapped context lost", fmt.Errorf("run: %w", errors.New("Cannot find context")), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransientCDPError(tt.err); got != tt.want {
				t.Errorf("isTransientCDPError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestBuildBrowserOutputTruncation(t *testing.T) {
	// Build HTML that exceeds maxObservedHTML.
	longHTML := strings.Repeat("a", maxObservedHTML+500)
	out := buildBrowserOutput("http://localhost/", "text", longHTML, -1)

	if !strings.Contains(out.Observed, "... (truncated)") {
		t.Error("expected truncation marker in observed output")
	}
	// Full HTML should still be in capture sources.
	if out.CaptureSources[BrowserSourceHTML] != longHTML {
		t.Error("capture source HTML should contain full (untruncated) HTML")
	}
}

func TestBuildBrowserOutputUTF8Truncation(t *testing.T) {
	// Build HTML that has a multi-byte rune right at the truncation boundary.
	// '€' is 3 bytes (0xE2 0x82 0xAC). Place it so the byte boundary splits it.
	prefix := strings.Repeat("a", maxObservedHTML-1)
	longHTML := prefix + "€" + strings.Repeat("b", 500)
	out := buildBrowserOutput("http://localhost/", "text", longHTML, -1)

	if !strings.Contains(out.Observed, "... (truncated)") {
		t.Error("expected truncation marker")
	}
	// The observed output should be valid UTF-8.
	observedHTML := strings.SplitN(out.Observed, "Page HTML:\n", 2)
	if len(observedHTML) < 2 {
		t.Fatal("could not find HTML section in observed output")
	}
	htmlPart := strings.TrimSuffix(observedHTML[1], "\n... (truncated)")
	for _, r := range htmlPart {
		if r == '\uFFFD' {
			t.Error("found replacement character — truncation split a multi-byte rune")
		}
	}
}

func TestBrowserStepType(t *testing.T) {
	step := Step{Browser: &BrowserRequest{Action: "navigate", URL: "/"}}
	if got := step.StepType(); got != "browser" {
		t.Errorf("StepType() = %q, want %q", got, "browser")
	}
}

func TestSubstituteBrowserRequest(t *testing.T) {
	count := 3
	req := BrowserRequest{
		Action:     "assert",
		URL:        "/items/{item_id}",
		Selector:   "[data-testid={component}]",
		Value:      "Hello {name}",
		Text:       "Welcome {name}",
		TextAbsent: "Error {code}",
		Count:      &count,
		Timeout:    "5s",
	}
	vars := map[string]string{
		"item_id":   "42",
		"component": "card",
		"name":      "World",
		"code":      "404",
	}

	got := substituteBrowserRequest(req, vars)

	checks := []struct {
		name string
		got  string
		want string
	}{
		{"Action", got.Action, "assert"},
		{"URL", got.URL, "/items/42"},
		{"Selector", got.Selector, "[data-testid=card]"},
		{"Value", got.Value, "Hello World"},
		{"Text", got.Text, "Welcome World"},
		{"TextAbsent", got.TextAbsent, "Error 404"},
		{"Timeout", got.Timeout, "5s"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
	if got.Count == nil || *got.Count != 3 {
		t.Errorf("Count should be preserved as 3")
	}
}

func TestParseBrowserTimeout(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
	}{
		{name: "default", input: "", want: defaultBrowserTimeout},
		{name: "valid", input: "5s", want: 5 * time.Second},
		{name: "valid ms", input: "500ms", want: 500 * time.Millisecond},
		{name: "invalid", input: "not-a-duration", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseStepTimeout(tt.input, defaultBrowserTimeout)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBrowserValidCaptureSources(t *testing.T) {
	exec := &BrowserExecutor{}
	sources := exec.ValidCaptureSources()
	want := map[string]bool{
		"text": true, "html": true, "count": true, "location": true, "value": true,
	}
	if len(sources) != len(want) {
		t.Fatalf("got %d sources, want %d", len(sources), len(want))
	}
	for _, s := range sources {
		if !want[s] {
			t.Errorf("unexpected source %q", s)
		}
	}
}

func TestBrowserExecutorCloseIdempotent(t *testing.T) {
	// Close on a never-initialized executor should not panic.
	exec := &BrowserExecutor{}
	exec.Close()
	exec.Close() // double-close

	// After close, fields should be nil.
	if exec.ctxCancel != nil {
		t.Error("ctxCancel should be nil after Close")
	}
	if exec.allocCancel != nil {
		t.Error("allocCancel should be nil after Close")
	}
	if exec.browserCtx != nil {
		t.Error("browserCtx should be nil after Close")
	}
}

func TestBrowserExecuteValidationErrors(t *testing.T) {
	exec := &BrowserExecutor{
		BaseURL: "http://localhost",
		Logger:  slog.Default(),
	}
	tests := []struct {
		name    string
		req     BrowserRequest
		wantErr error
	}{
		{"navigate no URL", BrowserRequest{Action: "navigate"}, errNavigateRequiresURL},
		{"click no selector", BrowserRequest{Action: "click"}, errClickRequiresSelect},
		{"fill no selector", BrowserRequest{Action: "fill", Value: "x"}, errFillRequiresSelect},
		{"fill no value", BrowserRequest{Action: "fill", Selector: "#x"}, errFillRequiresValue},
		{"assert no selector", BrowserRequest{Action: "assert"}, errAssertRequiresSelect},
		{"invalid action", BrowserRequest{Action: "hover"}, errInvalidBrowserAction},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			step := Step{Browser: &tt.req}
			_, err := exec.Execute(context.Background(), step, nil)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("got %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestNewBrowserExecutor(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	exec := NewBrowserExecutor(ctx, "http://localhost:8080", logger)

	if exec.BaseURL != "http://localhost:8080" {
		t.Errorf("BaseURL = %q, want %q", exec.BaseURL, "http://localhost:8080")
	}
	if exec.Logger != logger {
		t.Error("Logger not set correctly")
	}
	if exec.parentCtx != ctx {
		t.Error("parentCtx not set correctly")
	}
}

func TestRetryOnTransient(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	const maxRetries = 3
	const retryDelay = 1 * time.Millisecond

	transientErr := errors.New("Cannot find context with specified id")
	nonTransientErr := errors.New("connection refused")

	tests := []struct {
		name      string
		mkFn      func(calls *int) func() error
		ctxFn     func() context.Context
		wantErr   error
		wantCalls int
	}{
		{
			name: "success on first attempt",
			mkFn: func(calls *int) func() error {
				return func() error {
					*calls++
					return nil
				}
			},
			ctxFn:     context.Background,
			wantErr:   nil,
			wantCalls: 1,
		},
		{
			name: "transient then success",
			mkFn: func(calls *int) func() error {
				return func() error {
					*calls++
					if *calls < 3 {
						return transientErr
					}
					return nil
				}
			},
			ctxFn:     context.Background,
			wantErr:   nil,
			wantCalls: 3,
		},
		{
			name: "all transient exhausted",
			mkFn: func(calls *int) func() error {
				return func() error {
					*calls++
					return transientErr
				}
			},
			ctxFn:     context.Background,
			wantErr:   errPageContextLost,
			wantCalls: maxRetries,
		},
		{
			name: "non-transient fails immediately",
			mkFn: func(calls *int) func() error {
				return func() error {
					*calls++
					return nonTransientErr
				}
			},
			ctxFn:     context.Background,
			wantErr:   nonTransientErr,
			wantCalls: 1,
		},
		{
			name: "context canceled during delay",
			mkFn: func(calls *int) func() error {
				return func() error {
					*calls++
					return transientErr
				}
			},
			ctxFn: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			wantErr:   context.Canceled,
			wantCalls: 1, // attempt 0 runs, sleep on attempt 1 returns immediately
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			fn := tt.mkFn(&calls)
			ctx := tt.ctxFn()
			err := retryOnTransient(ctx, maxRetries, retryDelay, logger, "test", fn)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("got error %v, want %v", err, tt.wantErr)
			}
			if calls != tt.wantCalls {
				t.Errorf("fn called %d times, want %d", calls, tt.wantCalls)
			}
		})
	}
}

func TestEvaluateAssertions(t *testing.T) {
	intPtr := func(n int) *int { return &n }

	tests := []struct {
		name       string
		elemText   string
		matchCount int
		req        BrowserRequest
		wantLines  []string
	}{
		{
			name:     "text contains PASS",
			elemText: "say hello world",
			req:      BrowserRequest{Text: "hello"},
			wantLines: []string{
				`PASS: element text contains "hello"`,
			},
		},
		{
			name:     "text contains FAIL",
			elemText: "hello",
			req:      BrowserRequest{Text: "goodbye"},
			wantLines: []string{
				`FAIL: element text does not contain "goodbye" (got "hello")`,
			},
		},
		{
			name:     "text absent PASS",
			elemText: "success",
			req:      BrowserRequest{TextAbsent: "error"},
			wantLines: []string{
				`PASS: element text does not contain "error"`,
			},
		},
		{
			name:     "text absent FAIL",
			elemText: "error occurred",
			req:      BrowserRequest{TextAbsent: "error"},
			wantLines: []string{
				`FAIL: element text contains "error" (should be absent)`,
			},
		},
		{
			name:       "count match PASS",
			matchCount: 3,
			req:        BrowserRequest{Count: intPtr(3)},
			wantLines: []string{
				"PASS: found 3 matching elements",
			},
		},
		{
			name:       "count mismatch FAIL",
			matchCount: 5,
			req:        BrowserRequest{Count: intPtr(2)},
			wantLines: []string{
				"FAIL: expected 2 matching elements, found 5",
			},
		},
		{
			name:       "multiple assertions",
			elemText:   "hello world",
			matchCount: 2,
			req:        BrowserRequest{Text: "hello", TextAbsent: "error", Count: intPtr(2)},
			wantLines: []string{
				`PASS: element text contains "hello"`,
				`PASS: element text does not contain "error"`,
				"PASS: found 2 matching elements",
			},
		},
		{
			name:      "no assertions configured",
			req:       BrowserRequest{},
			wantLines: nil,
		},
		{
			name:      "nil count skipped",
			elemText:  "",
			req:       BrowserRequest{Count: nil},
			wantLines: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := evaluateAssertions(tt.elemText, tt.matchCount, tt.req)
			if len(got) != len(tt.wantLines) {
				t.Fatalf("got %d assertions, want %d: %v", len(got), len(tt.wantLines), got)
			}
			for i, line := range tt.wantLines {
				if got[i] != line {
					t.Errorf("assertion[%d] = %q, want %q", i, got[i], line)
				}
			}
		})
	}
}

func TestValidateBrowserRequest(t *testing.T) {
	tests := []struct {
		name    string
		req     BrowserRequest
		wantErr error
	}{
		{"valid navigate", BrowserRequest{Action: "navigate", URL: "/"}, nil},
		{"valid click", BrowserRequest{Action: "click", Selector: "#btn"}, nil},
		{"valid fill", BrowserRequest{Action: "fill", Selector: "#input", Value: "hello"}, nil},
		{"valid assert", BrowserRequest{Action: "assert", Selector: ".item"}, nil},
		{"navigate no url", BrowserRequest{Action: "navigate"}, errNavigateRequiresURL},
		{"click no selector", BrowserRequest{Action: "click"}, errClickRequiresSelect},
		{"fill no selector", BrowserRequest{Action: "fill", Value: "x"}, errFillRequiresSelect},
		{"fill no value", BrowserRequest{Action: "fill", Selector: "#x"}, errFillRequiresValue},
		{"assert no selector", BrowserRequest{Action: "assert"}, errAssertRequiresSelect},
		{"unknown action", BrowserRequest{Action: "drag"}, errInvalidBrowserAction},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBrowserRequest(tt.req)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("got %v, want %v", err, tt.wantErr)
			}
		})
	}
}
