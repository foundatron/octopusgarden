package scenario

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
			Command: "sleep 1",
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
		Exec: &ExecRequest{Command: "sleep 1"},
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

func TestExecExecutorFilesLocal(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "hello.txt")

	executor := &ExecExecutor{}
	step := Step{
		Exec: &ExecRequest{
			Command: "cat " + filePath,
			Files:   map[string]string{filePath: "file content"},
		},
	}

	output, err := executor.Execute(context.Background(), step, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(output.CaptureSources[ExecSourceStdout], "file content") {
		t.Errorf("expected file content in stdout, got: %s", output.CaptureSources[ExecSourceStdout])
	}

	// Verify the file has the expected permissions.
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("expected file mode 0600, got %o", info.Mode().Perm())
	}
}

func TestExecExecutorFilesLocalVarSubstitution(t *testing.T) {
	dir := t.TempDir()

	executor := &ExecExecutor{}
	vars := map[string]string{
		"dir":  dir,
		"name": "world",
	}
	step := Step{
		Exec: &ExecRequest{
			Command: "cat {dir}/greeting.txt",
			Files:   map[string]string{"{dir}/greeting.txt": "hello {name}"},
		},
	}

	output, err := executor.Execute(context.Background(), step, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(output.CaptureSources[ExecSourceStdout], "hello world") {
		t.Errorf("expected substituted content in stdout, got: %s", output.CaptureSources[ExecSourceStdout])
	}
}

func TestExecExecutorFilesContainer(t *testing.T) {
	type call struct {
		command string
		stdin   string
	}
	var calls []call

	session := &mockContainerSession{
		execFn: func(_ context.Context, command string, opts container.ExecOptions) (container.ExecResult, error) {
			calls = append(calls, call{command: command, stdin: opts.Stdin})
			return container.ExecResult{ExitCode: 0, Stdout: "ok"}, nil
		},
	}

	executor := &ExecExecutor{Session: session}
	step := Step{
		Exec: &ExecRequest{
			Command: "echo done",
			Files:   map[string]string{"/tmp/config.yaml": "key: value"},
		},
	}

	_, err := executor.Execute(context.Background(), step, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Expect 2 calls: one for the file write, one for the command.
	if len(calls) != 2 {
		t.Fatalf("expected 2 exec calls, got %d", len(calls))
	}
	// First call should be the mkdir+cat command.
	if !strings.Contains(calls[0].command, "mkdir -p") || !strings.Contains(calls[0].command, "cat >") {
		t.Errorf("expected mkdir+cat command, got: %s", calls[0].command)
	}
	if calls[0].stdin != "key: value" {
		t.Errorf("expected stdin 'key: value', got %q", calls[0].stdin)
	}
	// Second call is the actual command.
	if calls[1].command != "echo done" {
		t.Errorf("expected command 'echo done', got %q", calls[1].command)
	}
}

func TestExecExecutorFilesContainerWriteError(t *testing.T) {
	callCount := 0
	session := &mockContainerSession{
		execFn: func(_ context.Context, _ string, _ container.ExecOptions) (container.ExecResult, error) {
			callCount++
			// File write fails with non-zero exit code.
			return container.ExecResult{ExitCode: 1}, nil
		},
	}

	executor := &ExecExecutor{Session: session}
	step := Step{
		Exec: &ExecRequest{
			Command: "echo done",
			Files:   map[string]string{"/tmp/config.yaml": "content"},
		},
	}

	_, err := executor.Execute(context.Background(), step, nil)
	if err == nil {
		t.Fatal("expected error from failed file write")
	}
	if !errors.Is(err, errWriteFileFailed) {
		t.Errorf("expected errWriteFileFailed, got: %v", err)
	}
	// Command must not have been executed.
	if callCount != 1 {
		t.Errorf("expected 1 exec call (file write only), got %d", callCount)
	}
}

func TestExecExecutorFilesEmpty(t *testing.T) {
	executor := &ExecExecutor{}

	// nil files map — no-op.
	step := Step{Exec: &ExecRequest{Command: "echo hi"}}
	output, err := executor.Execute(context.Background(), step, nil)
	if err != nil {
		t.Fatalf("nil files: unexpected error: %v", err)
	}
	if output.CaptureSources[ExecSourceExitCode] != "0" {
		t.Errorf("nil files: expected exit code 0, got %s", output.CaptureSources[ExecSourceExitCode])
	}

	// Empty files map — no-op.
	step.Exec.Files = map[string]string{}
	output, err = executor.Execute(context.Background(), step, nil)
	if err != nil {
		t.Fatalf("empty files: unexpected error: %v", err)
	}
	if output.CaptureSources[ExecSourceExitCode] != "0" {
		t.Errorf("empty files: expected exit code 0, got %s", output.CaptureSources[ExecSourceExitCode])
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
