//go:build !windows

package scenario

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/ActiveState/vt10x"
	"github.com/creack/pty"
)

const (
	defaultTUITimeout = 5 * time.Second
	tuiTermCols       = 80
	tuiTermRows       = 24
	tuiPollInterval   = 10 * time.Millisecond
	tuiGraceTimeout   = 500 * time.Millisecond

	// TUISourceScreen is the capture source for the current terminal screen contents.
	TUISourceScreen = "screen"
	// TUISourceExitCode is the capture source for the process exit code (populated only after process exits).
	TUISourceExitCode = "exit_code"
)

var (
	errTUINotStarted  = errors.New("tui: no process started; use command to launch first")
	errTUIWaitTimeout = errors.New("tui: timed out waiting for text")
	errTUIUnknownKey  = errors.New("tui: unknown key name")

	_ StepExecutor = (*TUIExecutor)(nil)
)

// keyMap maps named keys to their ANSI byte sequences.
var keyMap = map[string][]byte{
	"enter":     {'\r'},
	"escape":    {'\x1b'},
	"tab":       {'\t'},
	"backspace": {'\x7f'},
	"space":     {' '},
	"up":        {'\x1b', '[', 'A'},
	"down":      {'\x1b', '[', 'B'},
	"right":     {'\x1b', '[', 'C'},
	"left":      {'\x1b', '[', 'D'},
	"home":      {'\x1b', '[', 'H'},
	"end":       {'\x1b', '[', 'F'},
	"pageup":    {'\x1b', '[', '5', '~'},
	"pagedown":  {'\x1b', '[', '6', '~'},
	"delete":    {'\x1b', '[', '3', '~'},
}

// TUIExecutor executes terminal UI interaction steps.
// A single process persists across steps within a scenario.
// TUIExecutor is NOT safe for concurrent use from multiple goroutines.
type TUIExecutor struct {
	Logger *slog.Logger

	cmd     *exec.Cmd
	ptyFile *os.File
	state   *vt10x.State
	vt      *vt10x.VT
	done    chan struct{}
}

// ValidCaptureSources returns the valid capture source names for TUI steps.
func (e *TUIExecutor) ValidCaptureSources() []string {
	return []string{TUISourceScreen, TUISourceExitCode}
}

// Execute dispatches a TUI step: launch, send keys/text, wait, and assert.
func (e *TUIExecutor) Execute(ctx context.Context, step Step, vars map[string]string) (StepOutput, error) {
	req := substituteTUIRequest(*step.TUI, vars)

	timeout, err := parseStepTimeout(req.Timeout, defaultTUITimeout)
	if err != nil {
		return StepOutput{}, fmt.Errorf("tui: parse timeout: %w", err)
	}

	if req.Command != "" {
		if err := e.launchProcess(req.Command); err != nil {
			return StepOutput{}, err
		}
	}

	if req.SendKey != "" {
		if err := e.doSendKey(req.SendKey); err != nil {
			return StepOutput{}, err
		}
	}

	if req.SendText != "" {
		if err := e.doSendText(req.SendText); err != nil {
			return StepOutput{}, err
		}
	}

	if req.WaitFor != "" {
		if err := e.doWaitFor(ctx, req.WaitFor, timeout); err != nil {
			return StepOutput{}, err
		}
	}

	screen := e.readScreen()

	var assertions []string
	if req.AssertScreen != "" {
		assertions = append(assertions, doAssertScreenContains(screen, req.AssertScreen))
	}
	if req.AssertAbsent != "" {
		assertions = append(assertions, doAssertScreenAbsent(screen, req.AssertAbsent))
	}

	return buildTUIOutput(screen, assertions, e.exitCode()), nil
}

// Close terminates the process (SIGTERM then SIGKILL) and releases resources.
// Signals the entire process group so child processes (e.g., TUI apps launched via sh -c)
// are also terminated. Safe to call on a never-initialized executor.
func (e *TUIExecutor) Close() {
	if e.cmd == nil || e.cmd.Process == nil {
		return
	}

	// SIGTERM the process group with grace period, then SIGKILL.
	pgid := e.cmd.Process.Pid
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	timer := time.NewTimer(tuiGraceTimeout)
	select {
	case <-e.done:
		// Process exited; pty read goroutine already exited.
		timer.Stop()
	case <-timer.C:
		// Force kill the process group and close pty to unblock the background reader goroutine.
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		_ = e.ptyFile.Close()
		e.ptyFile = nil
		<-e.done
	}

	if e.ptyFile != nil {
		_ = e.ptyFile.Close()
		e.ptyFile = nil
	}

	e.cmd = nil
	e.state = nil
	e.vt = nil
	e.done = nil
}

// launchProcess starts the command under a pty. Closes any existing process first.
func (e *TUIExecutor) launchProcess(command string) error {
	if e.cmd != nil {
		e.Close()
	}

	// context.Background(): the process persists across multiple Execute calls within a scenario;
	// cleanup is handled by Close() which sends SIGTERM/SIGKILL.
	cmd := exec.CommandContext(context.Background(), "sh", "-c", command) //nolint:gosec // command comes from scenario file, not user input
	// creack/pty.StartWithSize sets Setsid on the child, placing it in a new session and
	// process group. Close() signals the process group via syscall.Kill(-pid, ...) so the
	// actual TUI app (not just the shell) receives SIGTERM/SIGKILL.
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: tuiTermRows, Cols: tuiTermCols})
	if err != nil {
		return fmt.Errorf("tui: launch %q: %w", command, err)
	}

	state := &vt10x.State{}
	// vt10x.New(state, input, output): we pass a zero-length reader as input (the internal
	// goroutine will EOF immediately -- we feed data manually via vt.Write). Output is
	// io.Discard because terminal response sequences (e.g., cursor position reports) must
	// not loop back into the pty master.
	vt, err := vt10x.New(state, strings.NewReader(""), io.Discard)
	if err != nil {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		return fmt.Errorf("tui: init terminal: %w", err)
	}
	vt.Resize(tuiTermCols, tuiTermRows)

	done := make(chan struct{})
	e.cmd = cmd
	e.ptyFile = ptmx
	e.state = state
	e.vt = vt
	e.done = done

	// Background goroutine: read pty output, feed to terminal emulator, then reap the child.
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, readErr := ptmx.Read(buf)
			if n > 0 {
				_, _ = vt.Write(buf[:n])
			}
			if readErr != nil {
				break
			}
		}
		// Reap the child process so ProcessState is populated and no zombie is left.
		_ = cmd.Wait()
	}()

	return nil
}

// doSendKey writes an ANSI key sequence for the named key to the pty.
func (e *TUIExecutor) doSendKey(keyName string) error {
	if e.ptyFile == nil {
		return errTUINotStarted
	}
	seq, err := mapKey(keyName)
	if err != nil {
		return err
	}
	_, err = e.ptyFile.Write(seq)
	return err
}

// doSendText writes raw text bytes to the pty.
func (e *TUIExecutor) doSendText(text string) error {
	if e.ptyFile == nil {
		return errTUINotStarted
	}
	_, err := e.ptyFile.Write([]byte(text))
	return err
}

// doWaitFor polls the terminal screen until text appears or timeout/ctx expires.
func (e *TUIExecutor) doWaitFor(ctx context.Context, text string, timeout time.Duration) error {
	if e.ptyFile == nil {
		return errTUINotStarted
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(tuiPollInterval)
	defer ticker.Stop()

	for {
		if strings.Contains(e.readScreen(), text) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("%w: %q", errTUIWaitTimeout, text)
		case <-ticker.C:
		}
	}
}

// readScreen returns the current terminal screen with trailing whitespace stripped per line.
// state.String() acquires the state mutex internally, so no external locking is needed.
func (e *TUIExecutor) readScreen() string {
	if e.state == nil {
		return ""
	}
	raw := e.state.String()
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.Join(lines, "\n")
}

// exitCode returns the process exit code if the process has already exited, or "" if still running.
func (e *TUIExecutor) exitCode() string {
	if e.cmd == nil || e.cmd.ProcessState == nil {
		return ""
	}
	return fmt.Sprintf("%d", e.cmd.ProcessState.ExitCode())
}

// mapKey translates a key name to its ANSI byte sequence.
// Supports named keys (enter, escape, up, etc.) and ctrl+X patterns.
func mapKey(name string) ([]byte, error) {
	lower := strings.ToLower(name)
	if seq, ok := keyMap[lower]; ok {
		return seq, nil
	}
	// Handle ctrl+X patterns: ctrl+a through ctrl+z.
	if strings.HasPrefix(lower, "ctrl+") && len(lower) == 6 {
		ch := lower[5]
		if ch >= 'a' && ch <= 'z' {
			return []byte{ch & 0x1f}, nil
		}
	}
	return nil, fmt.Errorf("%w: %q", errTUIUnknownKey, name)
}

func doAssertScreenContains(screen, text string) string {
	if strings.Contains(screen, text) {
		return fmt.Sprintf("PASS: screen contains %q", text)
	}
	return fmt.Sprintf("FAIL: screen does not contain %q", text)
}

func doAssertScreenAbsent(screen, text string) string {
	if !strings.Contains(screen, text) {
		return fmt.Sprintf("PASS: screen does not contain %q", text)
	}
	return fmt.Sprintf("FAIL: screen contains %q (should be absent)", text)
}

func buildTUIOutput(screen string, assertions []string, exitCodeVal string) StepOutput {
	var observed strings.Builder
	observed.WriteString("TUI screen:\n")
	observed.WriteString(screen)
	if len(assertions) > 0 {
		observed.WriteString("\nAssertions:\n")
		observed.WriteString(strings.Join(assertions, "\n"))
	}

	sources := map[string]string{
		TUISourceScreen: screen,
	}
	if exitCodeVal != "" {
		sources[TUISourceExitCode] = exitCodeVal
	}

	return StepOutput{
		Observed:       observed.String(),
		CaptureBody:    screen,
		CaptureSources: sources,
	}
}

func substituteTUIRequest(req TUIRequest, vars map[string]string) TUIRequest {
	return TUIRequest{
		Command:      substituteVars(req.Command, vars),
		SendKey:      substituteVars(req.SendKey, vars),
		SendText:     substituteVars(req.SendText, vars),
		WaitFor:      substituteVars(req.WaitFor, vars),
		AssertScreen: substituteVars(req.AssertScreen, vars),
		AssertAbsent: substituteVars(req.AssertAbsent, vars),
		Timeout:      req.Timeout,
	}
}
