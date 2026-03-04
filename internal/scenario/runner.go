package scenario

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

var (
	errSetupFailed          = errors.New("runner: setup step failed")
	errNoExecutorRegistered = errors.New("no executor registered for step type")
)

// Runner executes scenario steps by dispatching to registered StepExecutors.
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
		executor, err := r.resolveExecutor(step)
		if err != nil {
			return Result{}, fmt.Errorf("%w: step %d (%s): %w", errSetupFailed, i, step.Description, err)
		}

		output, err := executor.Execute(ctx, step, vars)
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
		executor, err := r.resolveExecutor(step)
		if err != nil {
			results = append(results, StepResult{
				Description: step.Description,
				StepType:    step.StepType(),
				Err:         err,
			})
			r.Logger.Warn("judged step executor error", "step", i, "description", step.Description, "error", err)
			continue
		}

		start := time.Now()
		output, err := executor.Execute(ctx, step, vars)
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

func (r *Runner) resolveExecutor(step Step) (StepExecutor, error) {
	st := step.StepType()
	if st == "" {
		return nil, errUnknownStepType
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
