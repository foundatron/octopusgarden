package scenario

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// HTTPExecutor executes HTTP request steps.
type HTTPExecutor struct {
	Client  *http.Client
	BaseURL string
}

// ValidCaptureSources returns nil — HTTP steps use JSONPath capture only.
func (e *HTTPExecutor) ValidCaptureSources() []string { return nil }

// Execute performs an HTTP request and returns the step output.
func (e *HTTPExecutor) Execute(ctx context.Context, step Step, vars map[string]string) (StepOutput, error) {
	req := substituteRequest(*step.Request, vars)

	body, err := buildRequestBody(req.Body, vars)
	if err != nil {
		return StepOutput{}, fmt.Errorf("build request body: %w", err)
	}

	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	url := e.BaseURL + req.Path
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, url, bodyReader)
	if err != nil {
		return StepOutput{}, fmt.Errorf("create request: %w", err)
	}

	// Default Content-Type for requests with a body.
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	// Apply step headers (can override defaults).
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := e.Client.Do(httpReq) //nolint:gosec // URL is constructed from configured BaseURL + scenario path, not user input
	if err != nil {
		return StepOutput{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // cap at 10MB
	if err != nil {
		return StepOutput{}, fmt.Errorf("read response body: %w", err)
	}

	headers := make(map[string]string, len(resp.Header))
	for k := range resp.Header {
		headers[k] = resp.Header.Get(k)
	}

	observed := fmt.Sprintf("HTTP %d\nHeaders: %v\nBody:\n%s", resp.StatusCode, headers, string(respBody))

	return StepOutput{
		Observed:    observed,
		CaptureBody: string(respBody),
	}, nil
}

func substituteRequest(req Request, vars map[string]string) Request {
	out := Request{
		Method:  req.Method,
		Path:    substituteVars(req.Path, vars),
		Headers: make(map[string]string, len(req.Headers)),
		Body:    req.Body,
	}
	for k, v := range req.Headers {
		out.Headers[k] = substituteVars(v, vars)
	}
	// Body substitution is handled in buildRequestBody.
	return out
}

func buildRequestBody(body any, vars map[string]string) ([]byte, error) {
	if body == nil {
		return nil, nil
	}

	switch v := body.(type) {
	case string:
		return []byte(substituteVars(v, vars)), nil
	default:
		// map or slice from YAML — marshal to JSON, then substitute with JSON-safe escaping.
		data, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		return []byte(substituteVarsJSON(string(data), vars)), nil
	}
}

// substituteVarsJSON replaces {name} placeholders in a JSON string with
// JSON-escaped values. This prevents captured values containing quotes or
// newlines from breaking the JSON structure.
func substituteVarsJSON(s string, vars map[string]string) string {
	for name, val := range vars {
		escaped, err := json.Marshal(val)
		if err != nil {
			// Fallback to raw substitution if marshaling fails (shouldn't happen for strings).
			s = strings.ReplaceAll(s, "{"+name+"}", val)
			continue
		}
		// json.Marshal wraps the value in quotes — strip them for substitution
		// since the placeholder is already inside a JSON string literal.
		s = strings.ReplaceAll(s, "{"+name+"}", string(escaped[1:len(escaped)-1]))
	}
	return s
}
