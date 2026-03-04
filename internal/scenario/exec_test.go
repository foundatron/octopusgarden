package scenario

import (
	"context"
	"strings"
	"testing"
)

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
}

func TestExecExecutorVariableSubstitution(t *testing.T) {
	executor := &ExecExecutor{}
	step := Step{
		Exec: &ExecRequest{Command: "echo {greeting} {name}"},
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
