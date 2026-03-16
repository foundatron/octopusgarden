package scenario

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

const (
	defaultRetryAttempts = 3
	defaultRetryInterval = time.Second
)

var (
	errSetupFailed          = errors.New("runner: setup step failed")
	errNoExecutorRegistered = errors.New("no executor registered for step type")
	errTUINotImplemented    = errors.New("tui step type is not yet implemented")
	errRetryInvalidInterval = errors.New("retry: invalid interval")
	errRetryInvalidTimeout  = errors.New("retry: invalid timeout")
	errInvalidDelay         = errors.New("step: invalid delay duration")
)

// Runner executes scenario steps by dispatching to registered StepExecutors.
// Each Runner instance is single-use and not safe for concurrent use; callers
// running scenarios in parallel must create a separate Runner per goroutine.
type Runner struct {
	Executors map[string]StepExecutor
	Logger    *slog.Logger
}

// NewRunner creates a Runner with the given executor map.
func NewRunner(executors map[string]StepExecutor, logger *slog.Logger) *Runner {
	return &Runner{
		Executors: executors,
		Logger:    logger,
	}
}

// Run executes a scenario: setup steps first (fatal on failure), then judged steps.
// Returns a Result containing only the judged step results.
func (r *Runner) Run(ctx context.Context, scenario Scenario) (Result, error) {
	vars := make(map[string]string)

	// Execute setup steps — fatal on failure.
	for i, step := range scenario.Setup {
		if err := r.applyDelay(ctx, step.Delay); err != nil {
			return Result{}, fmt.Errorf("%w: step %d (%s): %w", errSetupFailed, i, step.Description, err)
		}

		executor, err := r.resolveExecutor(step)
		if err != nil {
			return Result{}, fmt.Errorf("%w: step %d (%s): %w", errSetupFailed, i, step.Description, err)
		}

		output, err := r.executeStep(ctx, executor, step, vars)
		if err != nil {
			return Result{}, fmt.Errorf("%w: step %d (%s): %w", errSetupFailed, i, step.Description, err)
		}
		if err := applyCaptures(step.Capture, output, vars); err != nil {
			return Result{}, fmt.Errorf("%w: step %d capture: %w", errSetupFailed, i, err)
		}
		r.Logger.Debug("setup step completed", "step", i, "description", step.Description, "type", step.StepType())
	}

	// Execute judged steps — non-fatal on failure.
	results := make([]StepResult, 0, len(scenario.Steps))
	for i, step := range scenario.Steps {
		start := time.Now()

		if err := r.applyDelay(ctx, step.Delay); err != nil {
			results = append(results, StepResult{
				Description: step.Description,
				StepType:    step.StepType(),
				Duration:    time.Since(start),
				Err:         err,
			})
			r.Logger.Warn("judged step delay error", "step", i, "description", step.Description, "error", err)
			continue
		}

		executor, err := r.resolveExecutor(step)
		if err != nil {
			results = append(results, StepResult{
				Description: step.Description,
				StepType:    step.StepType(),
				Duration:    time.Since(start),
				Err:         err,
			})
			r.Logger.Warn("judged step executor error", "step", i, "description", step.Description, "error", err)
			continue
		}

		output, err := r.executeStep(ctx, executor, step, vars)
		dur := time.Since(start)

		result := StepResult{
			Description: step.Description,
			StepType:    step.StepType(),
			Observed:    output.Observed,
			CaptureBody: output.CaptureBody,
			Duration:    dur,
			Err:         err,
		}
		results = append(results, result)

		if err != nil {
			r.Logger.Warn("judged step execution error", "step", i, "description", step.Description, "error", err)
			continue
		}

		if err := applyCaptures(step.Capture, output, vars); err != nil {
			r.Logger.Warn("judged step capture error", "step", i, "error", err)
		}
		r.Logger.Debug("judged step completed", "step", i, "description", step.Description, "type", step.StepType())
	}

	return Result{
		ScenarioID: scenario.ID,
		Steps:      results,
	}, nil
}

func (r *Runner) executeStep(ctx context.Context, executor StepExecutor, step Step, vars map[string]string) (StepOutput, error) {
	if step.Retry == nil {
		return executor.Execute(ctx, step, vars)
	}
	return r.executeWithRetry(ctx, executor, step, vars, step.Retry)
}

func (r *Runner) executeWithRetry(ctx context.Context, executor StepExecutor, step Step, vars map[string]string, retry *Retry) (StepOutput, error) {
	attempts := retry.Attempts
	if attempts <= 0 {
		attempts = defaultRetryAttempts
	}

	interval, err := parseStepTimeout(retry.Interval, defaultRetryInterval)
	if err != nil {
		return StepOutput{}, fmt.Errorf("%w: %w", errRetryInvalidInterval, err)
	}

	execCtx := ctx
	var cancel context.CancelFunc
	if retry.Timeout != "" {
		timeout, err := parseStepTimeout(retry.Timeout, 0)
		if err != nil {
			return StepOutput{}, fmt.Errorf("%w: %w", errRetryInvalidTimeout, err)
		}
		execCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	var lastOutput StepOutput
	var lastErr error
	for attempt := range attempts {
		lastOutput, lastErr = executor.Execute(execCtx, step, vars)
		if lastErr == nil {
			return lastOutput, nil
		}

		r.Logger.Debug("retry: attempt failed",
			"step", step.Description,
			"attempt", attempt+1,
			"maxAttempts", attempts,
			"error", lastErr,
		)

		// Don't sleep after the last attempt.
		if attempt < attempts-1 {
			timer := time.NewTimer(interval)
			select {
			case <-execCtx.Done():
				timer.Stop()
				return lastOutput, execCtx.Err()
			case <-timer.C:
			}
		}
	}
	return lastOutput, lastErr
}

func (r *Runner) applyDelay(ctx context.Context, delay string) error {
	if delay == "" {
		return nil
	}
	d, err := parseStepTimeout(delay, 0)
	if err != nil {
		return fmt.Errorf("%w: %w", errInvalidDelay, err)
	}
	r.Logger.Debug("step delay", "delay", delay)
	timer := time.NewTimer(d)
	select {
	case <-ctx.Done():
		timer.Stop()
		return ctx.Err()
	case <-timer.C:
	}
	return nil
}

func (r *Runner) resolveExecutor(step Step) (StepExecutor, error) {
	st := step.StepType()
	if st == "" {
		return nil, errUnknownStepType
	}
	if st == "tui" {
		return nil, errTUINotImplemented
	}
	executor, ok := r.Executors[st]
	if !ok {
		return nil, fmt.Errorf("%w: %q", errNoExecutorRegistered, st)
	}
	return executor, nil
}

func substituteVars(s string, vars map[string]string) string {
	for name, val := range vars {
		s = strings.ReplaceAll(s, "{"+name+"}", val)
	}
	return s
}

func applyCaptures(captures []Capture, output StepOutput, vars map[string]string) error {
	for _, c := range captures {
		val, err := resolveCapture(c, output)
		if err != nil {
			return fmt.Errorf("capture %q: %w", c.Name, err)
		}
		vars[c.Name] = val
	}
	return nil
}

// resolveCapture implements composable source + jsonpath capture logic:
//  1. source set → body = output.CaptureSources[source]
//     - jsonpath also set → value = evalJSONPath(body, jsonpath)
//     - only source → value = strings.TrimSpace(body)
//  2. only jsonpath → value = evalJSONPath(output.CaptureBody, jsonpath) [existing]
func resolveCapture(c Capture, output StepOutput) (string, error) {
	if c.Source != "" {
		body := output.CaptureSources[c.Source]
		if c.JSONPath != "" {
			return evalJSONPath(body, c.JSONPath)
		}
		return strings.TrimSpace(body), nil
	}
	if c.JSONPath != "" {
		return evalJSONPath(output.CaptureBody, c.JSONPath)
	}
	return "", errNoCapture
}
