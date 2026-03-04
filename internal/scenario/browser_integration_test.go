//go:build integration

package scenario

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func testBrowserLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestBrowserIntegrationNavigate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body><h1>Hello Browser</h1><p>Welcome to the test page.</p></body></html>`)
	}))
	defer srv.Close()

	exec := &BrowserExecutor{BaseURL: srv.URL, Logger: testBrowserLogger()}
	defer exec.Close()

	step := Step{Browser: &BrowserRequest{Action: "navigate", URL: "/"}}
	output, err := exec.Execute(context.Background(), step, nil)
	if err != nil {
		t.Fatalf("navigate failed: %v", err)
	}

	if !strings.Contains(output.Observed, "Hello Browser") {
		t.Errorf("Observed should contain page content, got:\n%s", output.Observed)
	}
	if output.CaptureSources["text"] == "" {
		t.Error("text capture source should not be empty")
	}
	if output.CaptureSources["html"] == "" {
		t.Error("html capture source should not be empty")
	}
	if output.CaptureSources["location"] == "" {
		t.Error("location capture source should not be empty")
	}
}

func TestBrowserIntegrationClickAndFill(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<input id="name" type="text" data-testid="name-input" />
			<button id="btn" onclick="document.getElementById('result').textContent='Clicked!'">Click me</button>
			<div id="result"></div>
		</body></html>`)
	}))
	defer srv.Close()

	exec := &BrowserExecutor{BaseURL: srv.URL, Logger: testBrowserLogger()}
	defer exec.Close()

	ctx := context.Background()

	// Navigate first.
	navStep := Step{Browser: &BrowserRequest{Action: "navigate", URL: "/"}}
	if _, err := exec.Execute(ctx, navStep, nil); err != nil {
		t.Fatalf("navigate failed: %v", err)
	}

	// Fill input.
	fillStep := Step{Browser: &BrowserRequest{Action: "fill", Selector: "#name", Value: "TestValue"}}
	fillOut, err := exec.Execute(ctx, fillStep, nil)
	if err != nil {
		t.Fatalf("fill failed: %v", err)
	}
	if fillOut.Observed == "" {
		t.Error("fill should produce observed output")
	}

	// Click button.
	clickStep := Step{Browser: &BrowserRequest{Action: "click", Selector: "#btn"}}
	clickOut, err := exec.Execute(ctx, clickStep, nil)
	if err != nil {
		t.Fatalf("click failed: %v", err)
	}
	if !strings.Contains(clickOut.CaptureSources["text"], "Clicked!") {
		t.Errorf("after click, page text should contain 'Clicked!', got: %s", clickOut.CaptureSources["text"])
	}
}

func TestBrowserIntegrationAssert(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<div class="card" data-testid="card">Card 1</div>
			<div class="card" data-testid="card">Card 2</div>
			<div class="card" data-testid="card">Card 3</div>
		</body></html>`)
	}))
	defer srv.Close()

	exec := &BrowserExecutor{BaseURL: srv.URL, Logger: testBrowserLogger()}
	defer exec.Close()

	ctx := context.Background()

	// Navigate.
	navStep := Step{Browser: &BrowserRequest{Action: "navigate", URL: "/"}}
	if _, err := exec.Execute(ctx, navStep, nil); err != nil {
		t.Fatalf("navigate failed: %v", err)
	}

	// Assert text presence.
	count := 3
	assertStep := Step{Browser: &BrowserRequest{
		Action:   "assert",
		Selector: ".card",
		Text:     "Card 1",
		Count:    &count,
	}}
	output, err := exec.Execute(ctx, assertStep, nil)
	if err != nil {
		t.Fatalf("assert failed: %v", err)
	}

	if !strings.Contains(output.Observed, "PASS") {
		t.Errorf("assertions should pass, got:\n%s", output.Observed)
	}
	if output.CaptureSources["count"] != "3" {
		t.Errorf("count should be 3, got: %s", output.CaptureSources["count"])
	}
}
