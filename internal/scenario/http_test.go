package scenario

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPExecutorBasic(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /items", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"items": []}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	executor := &HTTPExecutor{Client: srv.Client(), BaseURL: srv.URL}
	step := Step{
		Request: &Request{Method: "GET", Path: "/items"},
	}

	output, err := executor.Execute(context.Background(), step, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(output.Observed, "HTTP 200") {
		t.Errorf("observed missing status: %s", output.Observed)
	}
	if !strings.Contains(output.CaptureBody, `"items"`) {
		t.Errorf("capture body missing content: %s", output.CaptureBody)
	}
}

func TestHTTPExecutorVariableSubstitution(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody string
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok": true}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	executor := &HTTPExecutor{Client: srv.Client(), BaseURL: srv.URL}
	step := Step{
		Request: &Request{
			Method:  "POST",
			Path:    "/items/{id}",
			Headers: map[string]string{"Authorization": "Bearer {token}"},
			Body:    map[string]any{"name": "{name}"},
		},
	}
	vars := map[string]string{
		"id":    "42",
		"token": "abc",
		"name":  "test",
	}

	_, err := executor.Execute(context.Background(), step, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotPath != "/items/42" {
		t.Errorf("got path %q, want %q", gotPath, "/items/42")
	}
	if gotAuth != "Bearer abc" {
		t.Errorf("got auth %q, want %q", gotAuth, "Bearer abc")
	}
	if !strings.Contains(gotBody, "test") {
		t.Errorf("body missing substituted value: %s", gotBody)
	}
}

func TestHTTPExecutorPost(t *testing.T) {
	var gotContentType string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /items", func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id": 1}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	executor := &HTTPExecutor{Client: srv.Client(), BaseURL: srv.URL}
	step := Step{
		Request: &Request{
			Method: "POST",
			Path:   "/items",
			Body:   map[string]any{"name": "new item"},
		},
	}

	output, err := executor.Execute(context.Background(), step, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotContentType != "application/json" {
		t.Errorf("got Content-Type %q, want %q", gotContentType, "application/json")
	}
	if !strings.Contains(output.Observed, "HTTP 201") {
		t.Errorf("observed missing 201 status: %s", output.Observed)
	}
}
