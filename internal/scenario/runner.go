package scenario

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

var errSetupFailed = errors.New("runner: setup step failed")

// Runner executes scenario steps as HTTP requests against a live server.
type Runner struct {
	HTTPClient *http.Client
	BaseURL    string
	Logger     *slog.Logger
}

// NewRunner creates a Runner for the given base URL.
func NewRunner(baseURL string, httpClient *http.Client, logger *slog.Logger) *Runner {
	return &Runner{
		HTTPClient: httpClient,
		BaseURL:    baseURL,
		Logger:     logger,
	}
}

// Run executes a scenario: setup steps first (fatal on failure), then judged steps.
// Returns a Result containing only the judged step results.
func (r *Runner) Run(ctx context.Context, scenario Scenario) (Result, error) {
	vars := make(map[string]string)

	// Execute setup steps — fatal on failure.
	for i, step := range scenario.Setup {
		resp, _, err := r.executeStep(ctx, step, vars)
		if err != nil {
			return Result{}, fmt.Errorf("%w: step %d (%s): %w", errSetupFailed, i, step.Description, err)
		}
		if err := applyCaptures(step.Capture, resp.Body, vars); err != nil {
			return Result{}, fmt.Errorf("%w: step %d capture: %w", errSetupFailed, i, err)
		}
		r.Logger.Debug("setup step completed", "step", i, "description", step.Description, "status", resp.Status)
	}

	// Execute judged steps — non-fatal on failure.
	results := make([]StepResult, 0, len(scenario.Steps))
	for i, step := range scenario.Steps {
		req := substituteRequest(step.Request, vars)
		resp, dur, err := r.executeStep(ctx, step, vars)
		result := StepResult{
			Description: step.Description,
			Request:     req,
			Response:    resp,
			Duration:    dur,
			Err:         err,
		}
		results = append(results, result)

		if err != nil {
			r.Logger.Warn("judged step transport error", "step", i, "description", step.Description, "error", err)
			continue
		}

		if err := applyCaptures(step.Capture, resp.Body, vars); err != nil {
			r.Logger.Warn("judged step capture error", "step", i, "error", err)
		}
		r.Logger.Debug("judged step completed", "step", i, "description", step.Description, "status", resp.Status)
	}

	return Result{
		ScenarioID: scenario.ID,
		Steps:      results,
	}, nil
}

func (r *Runner) executeStep(ctx context.Context, step Step, vars map[string]string) (HTTPResponse, time.Duration, error) {
	req := substituteRequest(step.Request, vars)

	body, err := buildRequestBody(req.Body, vars)
	if err != nil {
		return HTTPResponse{}, 0, fmt.Errorf("build request body: %w", err)
	}

	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	url := r.BaseURL + req.Path
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, url, bodyReader)
	if err != nil {
		return HTTPResponse{}, 0, fmt.Errorf("create request: %w", err)
	}

	// Default Content-Type for requests with a body.
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	// Apply step headers (can override defaults).
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}

	start := time.Now()
	resp, err := r.HTTPClient.Do(httpReq) //nolint:gosec // URL is constructed from configured BaseURL + scenario path, not user input
	dur := time.Since(start)
	if err != nil {
		return HTTPResponse{}, dur, err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return HTTPResponse{}, dur, fmt.Errorf("read response body: %w", err)
	}

	headers := make(map[string]string, len(resp.Header))
	for k := range resp.Header {
		headers[k] = resp.Header.Get(k)
	}

	return HTTPResponse{
		Status:  resp.StatusCode,
		Headers: headers,
		Body:    string(respBody),
	}, dur, nil
}

func substituteVars(s string, vars map[string]string) string {
	for name, val := range vars {
		s = strings.ReplaceAll(s, "{"+name+"}", val)
	}
	return s
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
		// map or slice from YAML — marshal to JSON, then substitute.
		data, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		return []byte(substituteVars(string(data), vars)), nil
	}
}

func applyCaptures(captures []Capture, responseBody string, vars map[string]string) error {
	for _, c := range captures {
		val, err := evalJSONPath(responseBody, c.JSONPath)
		if err != nil {
			return fmt.Errorf("capture %q: %w", c.Name, err)
		}
		vars[c.Name] = val
	}
	return nil
}
