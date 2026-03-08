package scenario

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

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
	errPageContextLost      = errors.New("page context lost after retries")
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
	return []string{BrowserSourceText, BrowserSourceHTML, BrowserSourceCount, BrowserSourceLocation, BrowserSourceValue}
}

// Execute dispatches a browser action and returns the step output.
func (e *BrowserExecutor) Execute(ctx context.Context, step Step, vars map[string]string) (StepOutput, error) {
	req := substituteBrowserRequest(*step.Browser, vars)

	timeout, err := parseStepTimeout(req.Timeout, defaultBrowserTimeout)
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

	// Eagerly start the browser so its lifecycle is tied to browserCtx,
	// not a derived timeout context from the first Execute call.
	if err := chromedp.Run(e.browserCtx); err != nil {
		e.Close()
		return fmt.Errorf("start browser: %w", err)
	}

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
	err := chromedp.Run(ctx,
		chromedp.WaitVisible(req.Selector, chromedp.ByQuery),
		chromedp.Click(req.Selector, chromedp.ByQuery),
	)
	if err != nil {
		return StepOutput{}, fmt.Errorf("browser: click: %w", err)
	}

	// After a click that triggers form submission (server-rendered apps),
	// the page may navigate via redirect. The old execution context becomes
	// invalid before the new page loads. Retry the DOM read to ride out the
	// navigation gap.
	return e.readPageWithRetry(ctx, "click")
}

// isTransientCDPError returns true for Chrome DevTools Protocol errors that
// indicate the page is mid-navigation (e.g., after a form submit triggers a
// redirect). These are transient and resolve once navigation completes.
func isTransientCDPError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Cannot find context") ||
		strings.Contains(msg, "No node with given id")
}

// readPageWithRetry reads DOM state, retrying on transient CDP errors that
// occur when the page is mid-navigation (e.g. form submit → 303 redirect).
func (e *BrowserExecutor) readPageWithRetry(ctx context.Context, action string) (StepOutput, error) {
	const maxRetries = 5
	const retryDelay = 200 * time.Millisecond

	for attempt := range maxRetries {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return StepOutput{}, fmt.Errorf("browser: %s read: %w", action, ctx.Err())
			case <-time.After(retryDelay):
			}
		}

		var text, html, location string
		err := chromedp.Run(ctx,
			chromedp.WaitReady("body", chromedp.ByQuery),
			chromedp.InnerHTML("body", &html, chromedp.ByQuery),
			chromedp.Text("body", &text, chromedp.ByQuery),
			chromedp.Location(&location),
		)
		if err == nil {
			return buildBrowserOutput(location, text, html, -1), nil
		}

		if !isTransientCDPError(err) {
			return StepOutput{}, fmt.Errorf("browser: %s read: %w", action, err)
		}

		e.Logger.Debug("page mid-navigation, retrying DOM read",
			"action", action, "attempt", attempt+1, "max", maxRetries)
	}

	return StepOutput{}, fmt.Errorf("browser: %s read: %w", action, errPageContextLost)
}

func (e *BrowserExecutor) doFill(ctx context.Context, req BrowserRequest) (StepOutput, error) {
	var text, html, location, inputValue string
	err := chromedp.Run(ctx,
		chromedp.WaitVisible(req.Selector, chromedp.ByQuery),
		chromedp.Clear(req.Selector, chromedp.ByQuery),
		chromedp.SendKeys(req.Selector, req.Value, chromedp.ByQuery),
		chromedp.Value(req.Selector, &inputValue, chromedp.ByQuery),
		chromedp.InnerHTML("body", &html, chromedp.ByQuery),
		chromedp.Text("body", &text, chromedp.ByQuery),
		chromedp.Location(&location),
	)
	if err != nil {
		return StepOutput{}, fmt.Errorf("browser: fill: %w", err)
	}

	observed := fmt.Sprintf("URL: %s\nSelector: %s\nFilled value: %s\nPage content:\n%s",
		location, req.Selector, inputValue, text)

	return StepOutput{
		Observed:    observed,
		CaptureBody: text,
		CaptureSources: map[string]string{
			BrowserSourceText:     text,
			BrowserSourceHTML:     html,
			BrowserSourceLocation: location,
			BrowserSourceValue:    inputValue,
		},
	}, nil
}

func (e *BrowserExecutor) doAssert(ctx context.Context, req BrowserRequest) (StepOutput, error) {
	const maxRetries = 5
	const retryDelay = 200 * time.Millisecond

	for attempt := range maxRetries {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return StepOutput{}, fmt.Errorf("browser: assert: %w", ctx.Err())
			case <-time.After(retryDelay):
			}
		}

		out, err := e.doAssertOnce(ctx, req)
		if err == nil {
			return out, nil
		}

		if !isTransientCDPError(err) {
			return StepOutput{}, err
		}

		e.Logger.Debug("page mid-navigation, retrying assert",
			"attempt", attempt+1, "max", maxRetries)
	}

	return StepOutput{}, fmt.Errorf("browser: assert: %w", errPageContextLost)
}

func (e *BrowserExecutor) doAssertOnce(ctx context.Context, req BrowserRequest) (StepOutput, error) {
	// Count matching elements using Evaluate to avoid chromedp.Nodes blocking
	// when zero nodes match (Nodes waits for at least one, causing timeout on
	// count=0 assertions like verifying an element was deleted).
	var matchCount int
	err := chromedp.Run(ctx,
		chromedp.Evaluate(
			fmt.Sprintf(`document.querySelectorAll(%q).length`, req.Selector),
			&matchCount,
		),
	)
	if err != nil {
		return StepOutput{}, fmt.Errorf("browser: assert query: %w", err)
	}

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
