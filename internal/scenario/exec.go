package scenario

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
)

// ExecExecutor executes CLI command steps.
type ExecExecutor struct{}

// Execute runs a shell command and returns the step output.
func (e *ExecExecutor) Execute(ctx context.Context, step Step, vars map[string]string) (StepOutput, error) {
	command := substituteVars(step.Exec.Command, vars)

	cmd := exec.CommandContext(ctx, "sh", "-c", command) //nolint:gosec // command is from scenario YAML, not user input
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	// Non-zero exit code is a valid observation, not a transport error.
	// Only actual execution failures (binary not found, context canceled) are errors.
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return StepOutput{}, err
		}

		// Non-zero exit — include stderr in observed output.
		observed := fmt.Sprintf("Exit code: %d\nStdout:\n%s\nStderr:\n%s",
			exitErr.ExitCode(), stdout.String(), stderr.String())
		return StepOutput{
			Observed:    observed,
			CaptureBody: stdout.String(),
		}, nil
	}

	observed := fmt.Sprintf("Exit code: 0\nOutput:\n%s", stdout.String())
	return StepOutput{
		Observed:    observed,
		CaptureBody: stdout.String(),
	}, nil
}
