package scenario

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/foundatron/octopusgarden/internal/container"
)

const (
	defaultExecTimeout = 30 * time.Second
	// defaultMaxOutputBytes is the maximum bytes captured from command output.
	// Keep in sync with the constant of the same name in internal/container/docker.go.
	defaultMaxOutputBytes = 10 << 20 // 10MB
)

// containerSession provides command execution inside a running container.
// Satisfied by *container.Session via structural typing.
type containerSession interface {
	Exec(ctx context.Context, command string, opts container.ExecOptions) (container.ExecResult, error)
}

// ExecExecutor executes CLI command steps.
type ExecExecutor struct {
	Session containerSession // if non-nil, commands run inside the container
}

// ValidCaptureSources returns the valid capture source names for exec steps.
func (e *ExecExecutor) ValidCaptureSources() []string {
	return []string{ExecSourceStdout, ExecSourceStderr, ExecSourceExitCode}
}

// Execute runs a shell command and returns the step output.
func (e *ExecExecutor) Execute(ctx context.Context, step Step, vars map[string]string) (StepOutput, error) {
	command := substituteVars(step.Exec.Command, vars)
	stdin := substituteVars(step.Exec.Stdin, vars)
	env := substituteEnv(step.Exec.Env, vars)

	timeout, err := parseStepTimeout(step.Exec.Timeout, defaultExecTimeout)
	if err != nil {
		return StepOutput{}, fmt.Errorf("exec: parse timeout: %w", err)
	}

	if e.Session != nil {
		return e.runContainer(ctx, command, stdin, env, timeout)
	}
	return e.runLocal(ctx, command, stdin, env, timeout)
}

func (e *ExecExecutor) runContainer(ctx context.Context, command, stdin string, env map[string]string, timeout time.Duration) (StepOutput, error) {
	result, err := e.Session.Exec(ctx, command, container.ExecOptions{
		Stdin:          stdin,
		Env:            env,
		Timeout:        timeout,
		MaxOutputBytes: defaultMaxOutputBytes,
	})
	if err != nil {
		return StepOutput{}, fmt.Errorf("exec: container exec: %w", err)
	}
	return buildExecOutput(result.ExitCode, result.Stdout, result.Stderr), nil
}

func (e *ExecExecutor) runLocal(ctx context.Context, command, stdin string, env map[string]string, timeout time.Duration) (StepOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command) //nolint:gosec // command is from scenario YAML, not user input
	cmd.WaitDelay = 3 * time.Second                      // don't block if child processes keep pipes open after kill
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &stdout, remaining: defaultMaxOutputBytes}
	cmd.Stderr = &limitedWriter{w: &stderr, remaining: defaultMaxOutputBytes}

	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}

	if len(env) > 0 {
		cmd.Env = mergeEnv(os.Environ(), env)
	}

	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return StepOutput{}, err
		}
		return buildExecOutput(exitErr.ExitCode(), stdout.String(), stderr.String()), nil
	}

	return buildExecOutput(0, stdout.String(), stderr.String()), nil
}

func buildExecOutput(exitCode int, stdout, stderr string) StepOutput {
	var observed string
	if exitCode == 0 {
		observed = fmt.Sprintf("Exit code: 0\nStdout:\n%s", stdout)
		if stderr != "" {
			observed += fmt.Sprintf("\nStderr:\n%s", stderr)
		}
	} else {
		observed = fmt.Sprintf("Exit code: %d\nStdout:\n%s\nStderr:\n%s", exitCode, stdout, stderr)
	}

	return StepOutput{
		Observed:    observed,
		CaptureBody: stdout,
		CaptureSources: map[string]string{
			ExecSourceStdout:   stdout,
			ExecSourceStderr:   stderr,
			ExecSourceExitCode: strconv.Itoa(exitCode),
		},
	}
}

func substituteEnv(env map[string]string, vars map[string]string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = substituteVars(v, vars)
	}
	return out
}

func mergeEnv(base []string, extra map[string]string) []string {
	merged := make([]string, 0, len(base)+len(extra))
	for _, e := range base {
		k, _, _ := strings.Cut(e, "=")
		if _, ok := extra[k]; ok {
			continue // override from extra
		}
		merged = append(merged, e)
	}
	for k, v := range extra {
		merged = append(merged, k+"="+v)
	}
	return merged
}

// limitedWriter wraps an io.Writer and stops writing after a byte limit.
type limitedWriter struct {
	w         *bytes.Buffer
	remaining int64
}

func (lw *limitedWriter) Write(p []byte) (int, error) {
	if lw.remaining <= 0 {
		return len(p), nil // discard silently
	}
	n := len(p)
	if int64(n) > lw.remaining {
		p = p[:lw.remaining]
	}
	written, err := lw.w.Write(p)
	lw.remaining -= int64(written)
	if err != nil {
		return written, err
	}
	return n, nil
}
