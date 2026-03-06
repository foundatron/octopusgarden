//go:build integration

package scenario

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestIntegrationHTTPGet(t *testing.T) {
	svc := getSharedService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	exec := &HTTPExecutor{
		Client:  &http.Client{Timeout: 10 * time.Second},
		BaseURL: svc.baseURL,
	}

	step := Step{
		Request: &Request{Method: "GET", Path: "/echo?msg=hello"},
	}
	out, err := exec.Execute(ctx, step, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(out.Observed, "HTTP 200") {
		t.Errorf("Observed should contain HTTP 200, got:\n%s", out.Observed)
	}
	if out.CaptureBody == "" {
		t.Error("CaptureBody should not be empty")
	}

	// Capture $.message from response body.
	vars := make(map[string]string)
	if err := applyCaptures([]Capture{{Name: "msg", JSONPath: "$.message"}}, out, vars); err != nil {
		t.Fatalf("applyCaptures: %v", err)
	}
	if vars["msg"] != "hello" {
		t.Errorf("captured msg = %q, want %q", vars["msg"], "hello")
	}
}

func TestIntegrationHTTPPost(t *testing.T) {
	svc := getSharedService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	exec := &HTTPExecutor{
		Client:  &http.Client{Timeout: 10 * time.Second},
		BaseURL: svc.baseURL,
	}

	step := Step{
		Request: &Request{
			Method: "POST",
			Path:   "/echo",
			Body:   map[string]string{"message": "world"},
		},
	}
	out, err := exec.Execute(ctx, step, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(out.Observed, "HTTP 200") {
		t.Errorf("Observed should contain HTTP 200, got:\n%s", out.Observed)
	}

	vars := make(map[string]string)
	if err := applyCaptures([]Capture{{Name: "msg", JSONPath: "$.message"}}, out, vars); err != nil {
		t.Fatalf("applyCaptures: %v", err)
	}
	if vars["msg"] != "world" {
		t.Errorf("captured msg = %q, want %q", vars["msg"], "world")
	}
}

func TestIntegrationHTTPVariableCapture(t *testing.T) {
	svc := getSharedService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	httpClient := &http.Client{Timeout: 10 * time.Second}
	exec := &HTTPExecutor{Client: httpClient, BaseURL: svc.baseURL}
	vars := make(map[string]string)

	// Step 1: POST to echo, capture the timestamp from the response.
	step1 := Step{
		Request: &Request{
			Method: "POST",
			Path:   "/echo",
			Body:   map[string]string{"message": "capture-me"},
		},
	}
	out1, err := exec.Execute(ctx, step1, vars)
	if err != nil {
		t.Fatalf("step1 Execute: %v", err)
	}
	if err := applyCaptures([]Capture{{Name: "ts", JSONPath: "$.timestamp"}}, out1, vars); err != nil {
		t.Fatalf("step1 capture: %v", err)
	}
	if vars["ts"] == "" {
		t.Fatal("captured timestamp should not be empty")
	}

	// Step 2: GET echo with captured var in query param — verified via substitution.
	step2 := Step{
		Request: &Request{Method: "GET", Path: "/echo?msg={ts}"},
	}
	out2, err := exec.Execute(ctx, step2, vars)
	if err != nil {
		t.Fatalf("step2 Execute: %v", err)
	}

	if !strings.Contains(out2.CaptureBody, vars["ts"]) {
		t.Errorf("step2 response should echo the timestamp %q, got:\n%s", vars["ts"], out2.CaptureBody)
	}
}

func TestIntegrationHTTPHeaders(t *testing.T) {
	svc := getSharedService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	exec := &HTTPExecutor{
		Client:  &http.Client{Timeout: 10 * time.Second},
		BaseURL: svc.baseURL,
	}

	step := Step{
		Request: &Request{
			Method:  "GET",
			Path:    "/echo?msg=hdr",
			Headers: map[string]string{"X-Test-Header": "myvalue"},
		},
	}
	out, err := exec.Execute(ctx, step, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	vars := make(map[string]string)
	if err := applyCaptures([]Capture{{Name: "hdr", JSONPath: "$.echo_header"}}, out, vars); err != nil {
		t.Fatalf("applyCaptures: %v", err)
	}
	if vars["hdr"] != "myvalue" {
		t.Errorf("echo_header = %q, want %q", vars["hdr"], "myvalue")
	}
}
