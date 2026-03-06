//go:build integration

package scenario

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/foundatron/octopusgarden/internal/container"
)

// startExecSession starts a long-lived container session using the shared testservice image.
// The container stays alive via "sleep infinity" for the duration of the test.
func startExecSession(t *testing.T, svc *sharedService) (*container.Session, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	session, stop, err := svc.manager.StartSession(ctx, svc.imageTag)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	return session, stop
}

func TestIntegrationExecContainer(t *testing.T) {
	svc := getSharedService(t)
	session, stop := startExecSession(t, svc)
	defer stop()

	execExec := &ExecExecutor{Session: session}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	step := Step{
		Exec: &ExecRequest{Command: "echo hello"},
	}
	out, err := execExec.Execute(ctx, step, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if out.CaptureSources[ExecSourceExitCode] != "0" {
		t.Errorf("exitcode = %q, want %q", out.CaptureSources[ExecSourceExitCode], "0")
	}
	if strings.TrimSpace(out.CaptureSources[ExecSourceStdout]) != "hello" {
		t.Errorf("stdout = %q, want %q", out.CaptureSources[ExecSourceStdout], "hello")
	}
}

func TestIntegrationExecContainerExitCode(t *testing.T) {
	svc := getSharedService(t)
	session, stop := startExecSession(t, svc)
	defer stop()

	execExec := &ExecExecutor{Session: session}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	step := Step{
		Exec: &ExecRequest{Command: "exit 42"},
	}
	out, err := execExec.Execute(ctx, step, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if out.CaptureSources[ExecSourceExitCode] != "42" {
		t.Errorf("exitcode = %q, want %q", out.CaptureSources[ExecSourceExitCode], "42")
	}
}

func TestIntegrationExecContainerEnvVars(t *testing.T) {
	svc := getSharedService(t)
	session, stop := startExecSession(t, svc)
	defer stop()

	execExec := &ExecExecutor{Session: session}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	step := Step{
		Exec: &ExecRequest{
			Command: "echo $GREETING",
			Env:     map[string]string{"GREETING": "hello-world"},
		},
	}
	out, err := execExec.Execute(ctx, step, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if out.CaptureSources[ExecSourceExitCode] != "0" {
		t.Errorf("exitcode = %q, want %q", out.CaptureSources[ExecSourceExitCode], "0")
	}
	if strings.TrimSpace(out.CaptureSources[ExecSourceStdout]) != "hello-world" {
		t.Errorf("stdout = %q, want %q", out.CaptureSources[ExecSourceStdout], "hello-world")
	}
}

func TestIntegrationExecContainerStdin(t *testing.T) {
	svc := getSharedService(t)
	session, stop := startExecSession(t, svc)
	defer stop()

	execExec := &ExecExecutor{Session: session}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	step := Step{
		Exec: &ExecRequest{
			Command: "cat",
			Stdin:   "stdin-input",
		},
	}
	out, err := execExec.Execute(ctx, step, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if out.CaptureSources[ExecSourceExitCode] != "0" {
		t.Errorf("exitcode = %q, want %q", out.CaptureSources[ExecSourceExitCode], "0")
	}
	if out.CaptureSources[ExecSourceStdout] != "stdin-input" {
		t.Errorf("stdout = %q, want %q", out.CaptureSources[ExecSourceStdout], "stdin-input")
	}
}
