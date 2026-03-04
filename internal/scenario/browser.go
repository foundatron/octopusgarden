package scenario

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
)

const defaultBrowserTimeout = 10 * time.Second

var (
	errInvalidBrowserAction = errors.New("invalid browser action")
	errNavigateRequiresURL  = errors.New("navigate action requires url")
	errClickRequiresSelect  = errors.New("click action requires selector")
	errFillRequiresSelect   = errors.New("fill action requires selector")
	errFillRequiresValue    = errors.New("fill action requires value")
	errAssertRequiresSelect = errors.New("assert action requires selector")
)

// BrowserExecutor executes browser automation steps using chromedp.
// A single Chrome process persists across steps within a scenario
// (navigation state, cookies carry over).
// BrowserExecutor is NOT safe for concurrent use from multiple goroutines.
type BrowserExecutor struct {
	BaseURL   string
	Logger    *slog.Logger
	parentCtx context.Context

	allocCancel context.CancelFunc
	ctxCancel   context.CancelFunc
	browserCtx  context.Context
}

// NewBrowserExecutor creates a BrowserExecutor with the given parent context,
// base URL, and logger.
func NewBrowserExecutor(ctx context.Context, baseURL string, logger *slog.Logger) *BrowserExecutor {
	return &BrowserExecutor{
		BaseURL:   baseURL,
		Logger:    logger,
		parentCtx: ctx,
	}
}

// ValidCaptureSources returns the valid capture source names for browser steps.
func (e *BrowserExecutor) ValidCaptureSources() []string {
	return []string{BrowserSourceText, BrowserSourceHTML, BrowserSourceCount, BrowserSourceLocation}
}

// Execute dispatches a browser action and returns the step output.
func (e *BrowserExecutor) Execute(ctx context.Context, step Step, vars map[string]string) (StepOutput, error) {
	req := substituteBrowserRequest(*step.Browser, vars)

	timeout, err := parseBrowserTimeout(req.Timeout)
	if err != nil {
		return StepOutput{}, fmt.Errorf("browser: parse timeout: %w", err)
	}

	if err := validateBrowserRequest(req); err != nil {
		return StepOutput{}, err
	}

	if err := e.ensureBrowser(); err != nil {
		return StepOutput{}, fmt.Errorf("browser: init: %w", err)
	}

	start := time.Now()

	// Create a timeout context derived from the browser context so chromedp
	// respects the deadline. We also monitor the caller's ctx for cancellation.
	timeoutCtx, cancel := context.WithTimeout(e.browserCtx, timeout)
	defer cancel()

	done := make(chan struct{})
	defer close(done)

	// If the caller's context is canceled, propagate to the browser context.
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-timeoutCtx.Done():
		case <-done:
		}
	}()

	var out StepOutput
	switch req.Action {
	case "navigate":
		out, err = e.doNavigate(timeoutCtx, req)
	case "click":
		out, err = e.doClick(timeoutCtx, req)
	case "fill":
		out, err = e.doFill(timeoutCtx, req)
	case "assert":
		out, err = e.doAssert(timeoutCtx, req)
	default:
		return StepOutput{}, fmt.Errorf("%w: %q", errInvalidBrowserAction, req.Action)
	}

	if err != nil {
		return StepOutput{}, err
	}

	e.Logger.Debug("browser action completed", "action", req.Action, "duration", time.Since(start))

	return out, nil
}

// Close releases the Chrome process. Safe to call on a never-initialized executor.
func (e *BrowserExecutor) Close() {
	if e.ctxCancel != nil {
		e.ctxCancel()
		e.ctxCancel = nil
	}
	if e.allocCancel != nil {
		e.allocCancel()
		e.allocCancel = nil
	}
	e.browserCtx = nil
}

// ensureBrowser lazily initializes headless Chrome.
func (e *BrowserExecutor) ensureBrowser() error {
	if e.browserCtx != nil {
		return nil
	}

	base := make([]chromedp.ExecAllocatorOption, len(chromedp.DefaultExecAllocatorOptions))
	copy(base, chromedp.DefaultExecAllocatorOptions[:])
	opts := append(base,
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.WindowSize(1280, 720),
	)

	parentCtx := e.parentCtx
	if parentCtx == nil {
		parentCtx = context.Background()
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(parentCtx, opts...)
	e.allocCancel = allocCancel

	browserCtx, ctxCancel := chromedp.NewContext(allocCtx)
	e.ctxCancel = ctxCancel
	e.browserCtx = browserCtx

	return nil
}

func validateBrowserRequest(req BrowserRequest) error {
	switch req.Action {
	case "navigate":
		if req.URL == "" {
			return errNavigateRequiresURL
		}
	case "click":
		if req.Selector == "" {
			return errClickRequiresSelect
		}
	case "fill":
		if req.Selector == "" {
			return errFillRequiresSelect
		}
		if req.Value == "" {
			return errFillRequiresValue
		}
	case "assert":
		if req.Selector == "" {
			return errAssertRequiresSelect
		}
	default:
		return errInvalidBrowserAction
	}
	return nil
}

func (e *BrowserExecutor) doNavigate(ctx context.Context, req BrowserRequest) (StepOutput, error) {
	url := strings.TrimRight(e.BaseURL, "/") + req.URL

	var text, html, location string
	err := chromedp.Run(ctx,
		chromedp.Navigate(url),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.InnerHTML("body", &html, chromedp.ByQuery),
		chromedp.Text("body", &text, chromedp.ByQuery),
		chromedp.Location(&location),
	)
	if err != nil {
		return StepOutput{}, fmt.Errorf("browser: navigate: %w", err)
	}

	return buildBrowserOutput(location, text, html, -1), nil
}

func (e *BrowserExecutor) doClick(ctx context.Context, req BrowserRequest) (StepOutput, error) {
	var text, html, location string
	err := chromedp.Run(ctx,
		chromedp.WaitVisible(req.Selector, chromedp.ByQuery),
		chromedp.Click(req.Selector, chromedp.ByQuery),
		chromedp.InnerHTML("body", &html, chromedp.ByQuery),
		chromedp.Text("body", &text, chromedp.ByQuery),
		chromedp.Location(&location),
	)
	if err != nil {
		return StepOutput{}, fmt.Errorf("browser: click: %w", err)
	}

	return buildBrowserOutput(location, text, html, -1), nil
}

func (e *BrowserExecutor) doFill(ctx context.Context, req BrowserRequest) (StepOutput, error) {
	var text, html, location string
	err := chromedp.Run(ctx,
		chromedp.WaitVisible(req.Selector, chromedp.ByQuery),
		chromedp.Clear(req.Selector, chromedp.ByQuery),
		chromedp.SendKeys(req.Selector, req.Value, chromedp.ByQuery),
		chromedp.InnerHTML("body", &html, chromedp.ByQuery),
		chromedp.Text("body", &text, chromedp.ByQuery),
		chromedp.Location(&location),
	)
	if err != nil {
		return StepOutput{}, fmt.Errorf("browser: fill: %w", err)
	}

	return buildBrowserOutput(location, text, html, -1), nil
}

func (e *BrowserExecutor) doAssert(ctx context.Context, req BrowserRequest) (StepOutput, error) {
	// Count matching elements.
	var nodes []*cdp.Node
	err := chromedp.Run(ctx,
		chromedp.Nodes(req.Selector, &nodes, chromedp.ByQueryAll),
	)
	if err != nil {
		return StepOutput{}, fmt.Errorf("browser: assert query: %w", err)
	}
	matchCount := len(nodes)

	// Get element text (from first match, if any).
	var elemText, elemHTML, location string
	if matchCount > 0 {
		err = chromedp.Run(ctx,
			chromedp.Text(req.Selector, &elemText, chromedp.ByQuery),
			chromedp.InnerHTML(req.Selector, &elemHTML, chromedp.ByQuery),
			chromedp.Location(&location),
		)
		if err != nil {
			return StepOutput{}, fmt.Errorf("browser: assert read: %w", err)
		}
	} else {
		err = chromedp.Run(ctx,
			chromedp.Location(&location),
		)
		if err != nil {
			return StepOutput{}, fmt.Errorf("browser: assert location: %w", err)
		}
	}

	// Build assertion results as observed text (NOT errors -- let the judge score).
	var assertions []string
	if req.Text != "" {
		if strings.Contains(elemText, req.Text) {
			assertions = append(assertions, fmt.Sprintf("PASS: element text contains %q", req.Text))
		} else {
			assertions = append(assertions, fmt.Sprintf("FAIL: element text does not contain %q (got %q)", req.Text, elemText))
		}
	}
	if req.TextAbsent != "" {
		if !strings.Contains(elemText, req.TextAbsent) {
			assertions = append(assertions, fmt.Sprintf("PASS: element text does not contain %q", req.TextAbsent))
		} else {
			assertions = append(assertions, fmt.Sprintf("FAIL: element text contains %q (should be absent)", req.TextAbsent))
		}
	}
	if req.Count != nil {
		if matchCount == *req.Count {
			assertions = append(assertions, fmt.Sprintf("PASS: found %d matching elements", matchCount))
		} else {
			assertions = append(assertions, fmt.Sprintf("FAIL: expected %d matching elements, found %d", *req.Count, matchCount))
		}
	}

	observed := fmt.Sprintf("URL: %s\nSelector: %s\nMatching elements: %d\nElement text: %s\nAssertions:\n%s",
		location, req.Selector, matchCount, elemText, strings.Join(assertions, "\n"))

	return StepOutput{
		Observed:    observed,
		CaptureBody: elemText,
		CaptureSources: map[string]string{
			BrowserSourceText:     elemText,
			BrowserSourceHTML:     elemHTML,
			BrowserSourceCount:    strconv.Itoa(matchCount),
			BrowserSourceLocation: location,
		},
	}, nil
}

func buildBrowserOutput(location, text, html string, count int) StepOutput {
	observed := fmt.Sprintf("URL: %s\nPage content:\n%s", location, text)

	sources := map[string]string{
		BrowserSourceText:     text,
		BrowserSourceHTML:     html,
		BrowserSourceLocation: location,
	}
	if count >= 0 {
		sources[BrowserSourceCount] = strconv.Itoa(count)
	}

	return StepOutput{
		Observed:       observed,
		CaptureBody:    text,
		CaptureSources: sources,
	}
}

func substituteBrowserRequest(req BrowserRequest, vars map[string]string) BrowserRequest {
	return BrowserRequest{
		Action:     req.Action,
		URL:        substituteVars(req.URL, vars),
		Selector:   substituteVars(req.Selector, vars),
		Value:      substituteVars(req.Value, vars),
		Text:       substituteVars(req.Text, vars),
		TextAbsent: substituteVars(req.TextAbsent, vars),
		Count:      req.Count,
		Timeout:    req.Timeout,
	}
}

func parseBrowserTimeout(s string) (time.Duration, error) {
	if s == "" {
		return defaultBrowserTimeout, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", s, err)
	}
	return d, nil
}
