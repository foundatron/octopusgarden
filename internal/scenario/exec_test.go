package scenario

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/foundatron/octopusgarden/internal/container"
)

// mockContainerSession implements containerSession for testing.
type mockContainerSession struct {
	execFn func(ctx context.Context, command string, opts container.ExecOptions) (container.ExecResult, error)
}

func (m *mockContainerSession) Exec(ctx context.Context, command string, opts container.ExecOptions) (container.ExecResult, error) {
	return m.execFn(ctx, command, opts)
}

func TestExecExecutorBasic(t *testing.T) {
	executor := &ExecExecutor{}
	step := Step{
		Exec: &ExecRequest{Command: "echo hello"},
	}

	output, err := executor.Execute(context.Background(), step, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(output.Observed, "Exit code: 0") {
		t.Errorf("observed missing exit code: %s", output.Observed)
	}
	if !strings.Contains(output.CaptureBody, "hello") {
		t.Errorf("capture body missing output: %s", output.CaptureBody)
	}
	if output.CaptureSources[ExecSourceStdout] == "" {
		t.Error("expected stdout in capture sources")
	}
	if output.CaptureSources[ExecSourceExitCode] != "0" {
		t.Errorf("expected exitcode '0', got %q", output.CaptureSources[ExecSourceExitCode])
	}
}

func TestExecExecutorVariableSubstitution(t *testing.T) {
	executor := &ExecExecutor{}
	step := Step{
		Exec: &ExecRequest{
			Command: "echo {greeting} {name}",
			Stdin:   "{greeting}",
			Env:     map[string]string{"GREET": "{greeting}"},
		},
	}
	vars := map[string]string{
		"greeting": "hello",
		"name":     "world",
	}

	output, err := executor.Execute(context.Background(), step, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(output.CaptureBody, "hello world") {
		t.Errorf("capture body missing substituted values: %s", output.CaptureBody)
	}
}

func TestExecExecutorNonZeroExit(t *testing.T) {
	executor := &ExecExecutor{}
	step := Step{
		Exec: &ExecRequest{Command: "sh -c 'echo oops >&2; exit 1'"},
	}

	output, err := executor.Execute(context.Background(), step, nil)
	// Non-zero exit is NOT a transport error.
	if err != nil {
		t.Fatalf("expected no error for non-zero exit, got: %v", err)
	}

	if !strings.Contains(output.Observed, "Exit code: 1") {
		t.Errorf("observed missing exit code 1: %s", output.Observed)
	}
	if !strings.Contains(output.Observed, "oops") {
		t.Errorf("observed missing stderr content: %s", output.Observed)
	}
	if output.CaptureSources[ExecSourceExitCode] != "1" {
		t.Errorf("expected exitcode '1', got %q", output.CaptureSources[ExecSourceExitCode])
	}
}

func TestExecExecutorStdin(t *testing.T) {
	executor := &ExecExecutor{}
	step := Step{
		Exec: &ExecRequest{
			Command: "cat",
			Stdin:   "hello from stdin",
		},
	}
	output, err := executor.Execute(context.Background(), step, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(output.CaptureSources[ExecSourceStdout], "hello from stdin") {
		t.Errorf("expected stdin content in stdout, got: %s", output.CaptureSources[ExecSourceStdout])
	}
}

func TestExecExecutorEnv(t *testing.T) {
	executor := &ExecExecutor{}
	step := Step{
		Exec: &ExecRequest{
			Command: "echo $TEST_OG_VAR",
			Env:     map[string]string{"TEST_OG_VAR": "myvalue"},
		},
	}
	output, err := executor.Execute(context.Background(), step, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(output.CaptureSources[ExecSourceStdout], "myvalue") {
		t.Errorf("expected env var in stdout, got: %s", output.CaptureSources[ExecSourceStdout])
	}
}

func TestExecExecutorTimeout(t *testing.T) {
	executor := &ExecExecutor{}
	step := Step{
		Exec: &ExecRequest{
			Command: "sleep 60",
			Timeout: "100ms",
		},
	}
	output, err := executor.Execute(context.Background(), step, nil)
	// The process gets killed by context timeout — this is a non-zero exit, not a transport error.
	// On macOS, killed processes produce exit code -1 (signal); on Linux, 137 (128+9).
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if output.CaptureSources[ExecSourceExitCode] == "0" {
		t.Error("expected non-zero exit code from timed-out process")
	}
}

func TestExecExecutorInvalidTimeout(t *testing.T) {
	executor := &ExecExecutor{}
	step := Step{
		Exec: &ExecRequest{
			Command: "echo hi",
			Timeout: "notaduration",
		},
	}
	_, err := executor.Execute(context.Background(), step, nil)
	if err == nil {
		t.Fatal("expected error for invalid timeout")
	}
}

func TestExecExecutorStderr(t *testing.T) {
	executor := &ExecExecutor{}
	step := Step{
		Exec: &ExecRequest{Command: "echo errout >&2"},
	}
	output, err := executor.Execute(context.Background(), step, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(output.CaptureSources[ExecSourceStderr], "errout") {
		t.Errorf("expected 'errout' in stderr source, got: %s", output.CaptureSources[ExecSourceStderr])
	}
}

func TestExecExecutorCommandNotFound(t *testing.T) {
	executor := &ExecExecutor{}
	step := Step{
		Exec: &ExecRequest{Command: "/nonexistent/binary/that/does/not/exist"},
	}

	// sh -c handles the missing binary and returns exit code 127.
	output, err := executor.Execute(context.Background(), step, nil)
	if err != nil {
		t.Fatalf("expected no error (sh handles missing command), got: %v", err)
	}
	if !strings.Contains(output.Observed, "Exit code: 127") {
		t.Errorf("expected exit code 127, got: %s", output.Observed)
	}
}

func TestExecExecutorContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	executor := &ExecExecutor{}
	step := Step{
		Exec: &ExecRequest{Command: "sleep 60"},
	}

	_, err := executor.Execute(ctx, step, nil)
	if err == nil {
		t.Fatal("expected error from canceled context")
	}
}

func TestExecExecutorPipeCommand(t *testing.T) {
	executor := &ExecExecutor{}
	step := Step{
		Exec: &ExecRequest{Command: "echo 'hello world' | tr 'h' 'H'"},
	}

	output, err := executor.Execute(context.Background(), step, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(output.CaptureBody, "Hello world") {
		t.Errorf("capture body missing piped output: %s", output.CaptureBody)
	}
}

func TestExecContainerSession(t *testing.T) {
	session := &mockContainerSession{
		execFn: func(_ context.Context, command string, _ container.ExecOptions) (container.ExecResult, error) {
			if command != "echo hello" {
				t.Errorf("unexpected command: %s", command)
			}
			return container.ExecResult{Stdout: "hello\n", ExitCode: 0}, nil
		},
	}

	executor := &ExecExecutor{Session: session}
	step := Step{
		Exec: &ExecRequest{Command: "echo hello"},
	}
	output, err := executor.Execute(context.Background(), step, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(output.Observed, "Exit code: 0") {
		t.Errorf("expected exit code 0 in observed, got: %s", output.Observed)
	}
	if output.CaptureSources[ExecSourceStdout] != "hello\n" {
		t.Errorf("expected stdout source 'hello\\n', got %q", output.CaptureSources[ExecSourceStdout])
	}
}

func TestExecContainerSessionError(t *testing.T) {
	session := &mockContainerSession{
		execFn: func(_ context.Context, _ string, _ container.ExecOptions) (container.ExecResult, error) {
			return container.ExecResult{}, errors.New("container unavailable")
		},
	}

	executor := &ExecExecutor{Session: session}
	step := Step{
		Exec: &ExecRequest{Command: "echo hello"},
	}
	_, err := executor.Execute(context.Background(), step, nil)
	if err == nil {
		t.Fatal("expected error from container session")
	}
}

func TestExecContainerSessionPassesOptions(t *testing.T) {
	var gotOpts container.ExecOptions
	var gotCmd string
	session := &mockContainerSession{
		execFn: func(_ context.Context, command string, opts container.ExecOptions) (container.ExecResult, error) {
			gotCmd = command
			gotOpts = opts
			return container.ExecResult{Stdout: "ok", ExitCode: 0}, nil
		},
	}

	executor := &ExecExecutor{Session: session}
	step := Step{
		Exec: &ExecRequest{
			Command: "myapp {arg}",
			Stdin:   "input {arg}",
			Env:     map[string]string{"KEY": "{arg}"},
			Timeout: "5s",
		},
	}
	vars := map[string]string{"arg": "val"}
	_, err := executor.Execute(context.Background(), step, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotCmd != "myapp val" {
		t.Errorf("expected command 'myapp val', got %q", gotCmd)
	}
	if gotOpts.Stdin != "input val" {
		t.Errorf("expected stdin 'input val', got %q", gotOpts.Stdin)
	}
	if gotOpts.Env["KEY"] != "val" {
		t.Errorf("expected env KEY=val, got %q", gotOpts.Env["KEY"])
	}
	if gotOpts.Timeout != 5*time.Second {
		t.Errorf("expected timeout 5s, got %v", gotOpts.Timeout)
	}
}

func TestExecValidCaptureSources(t *testing.T) {
	executor := &ExecExecutor{}
	sources := executor.ValidCaptureSources()
	expected := map[string]bool{
		ExecSourceStdout:   true,
		ExecSourceStderr:   true,
		ExecSourceExitCode: true,
	}
	if len(sources) != len(expected) {
		t.Fatalf("expected %d sources, got %d", len(expected), len(sources))
	}
	for _, s := range sources {
		if !expected[s] {
			t.Errorf("unexpected source: %s", s)
		}
	}
}
