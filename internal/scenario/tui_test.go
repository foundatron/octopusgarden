//go:build !windows

package scenario

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestMapKey(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		want    []byte
		wantErr bool
	}{
		{name: "enter", key: "enter", want: []byte{'\r'}},
		{name: "escape", key: "escape", want: []byte{'\x1b'}},
		{name: "up", key: "up", want: []byte{'\x1b', '[', 'A'}},
		{name: "down", key: "down", want: []byte{'\x1b', '[', 'B'}},
		{name: "left", key: "left", want: []byte{'\x1b', '[', 'D'}},
		{name: "right", key: "right", want: []byte{'\x1b', '[', 'C'}},
		{name: "ctrl+c", key: "ctrl+c", want: []byte{0x03}},
		{name: "ctrl+t", key: "ctrl+t", want: []byte{0x14}},
		{name: "ctrl+a", key: "ctrl+a", want: []byte{0x01}},
		{name: "digit 1", key: "1", want: []byte{'1'}},
		{name: "digit 9", key: "9", want: []byte{'9'}},
		{name: "letter q", key: "q", want: []byte{'q'}},
		{name: "letter Q (uppercase)", key: "Q", want: []byte{'Q'}},
		{name: "letter j", key: "j", want: []byte{'j'}},
		{name: "punctuation ?", key: "?", want: []byte{'?'}},
		{name: "unknown", key: "F99", wantErr: true},
		{name: "ctrl+1 (not a-z)", key: "ctrl+1", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mapKey(tt.key)
			if tt.wantErr {
				if err == nil {
					t.Errorf("mapKey(%q) expected error, got nil", tt.key)
				}
				return
			}
			if err != nil {
				t.Fatalf("mapKey(%q) unexpected error: %v", tt.key, err)
			}
			if string(got) != string(tt.want) {
				t.Errorf("mapKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestTUIExecutor_ValidCaptureSources(t *testing.T) {
	e := &TUIExecutor{}
	srcs := e.ValidCaptureSources()
	if len(srcs) != 2 {
		t.Fatalf("expected 2 capture sources, got %d: %v", len(srcs), srcs)
	}
	found := make(map[string]bool)
	for _, s := range srcs {
		found[s] = true
	}
	if !found[TUISourceScreen] {
		t.Errorf("expected %q in ValidCaptureSources", TUISourceScreen)
	}
	if !found[TUISourceExitCode] {
		t.Errorf("expected %q in ValidCaptureSources", TUISourceExitCode)
	}
}

func TestTUIExecutor_LaunchAndRead(t *testing.T) {
	e := &TUIExecutor{Logger: slog.Default()}
	defer e.Close()

	ctx := context.Background()
	step := Step{TUI: &TUIRequest{
		Command: "echo hello",
		WaitFor: "hello",
		Timeout: "2s",
	}}
	out, err := e.Execute(ctx, step, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out.Observed, "hello") {
		t.Errorf("expected 'hello' in observed output, got: %s", out.Observed)
	}
	if !strings.Contains(out.CaptureSources[TUISourceScreen], "hello") {
		t.Errorf("expected 'hello' in screen capture source, got: %s", out.CaptureSources[TUISourceScreen])
	}
}

func TestTUIExecutor_SendText(t *testing.T) {
	e := &TUIExecutor{Logger: slog.Default()}
	defer e.Close()

	ctx := context.Background()

	// Launch cat which echoes stdin to stdout.
	launchStep := Step{TUI: &TUIRequest{Command: "cat"}}
	if _, err := e.Execute(ctx, launchStep, nil); err != nil {
		t.Fatalf("launch: %v", err)
	}

	// Send text followed by newline; cat echoes it back.
	sendStep := Step{TUI: &TUIRequest{
		SendText: "hello\n",
		WaitFor:  "hello",
		Timeout:  "2s",
	}}
	out, err := e.Execute(ctx, sendStep, nil)
	if err != nil {
		t.Fatalf("send text: %v", err)
	}
	if !strings.Contains(out.CaptureSources[TUISourceScreen], "hello") {
		t.Errorf("expected 'hello' in screen, got: %s", out.CaptureSources[TUISourceScreen])
	}
}

func TestTUIExecutor_SendKey(t *testing.T) {
	e := &TUIExecutor{Logger: slog.Default()}
	defer e.Close()

	ctx := context.Background()

	// Launch cat, then send enter. No error expected.
	launchStep := Step{TUI: &TUIRequest{Command: "cat"}}
	if _, err := e.Execute(ctx, launchStep, nil); err != nil {
		t.Fatalf("launch: %v", err)
	}

	sendKeyStep := Step{TUI: &TUIRequest{SendKey: "enter"}}
	if _, err := e.Execute(ctx, sendKeyStep, nil); err != nil {
		t.Errorf("send key: %v", err)
	}
}

func TestTUIExecutor_WaitForTimeout(t *testing.T) {
	e := &TUIExecutor{Logger: slog.Default()}
	defer e.Close()

	ctx := context.Background()

	launchStep := Step{TUI: &TUIRequest{Command: "echo hello", WaitFor: "hello", Timeout: "2s"}}
	if _, err := e.Execute(ctx, launchStep, nil); err != nil {
		t.Fatalf("launch: %v", err)
	}

	waitStep := Step{TUI: &TUIRequest{
		WaitFor: "nonexistent_text_that_will_never_appear",
		Timeout: "200ms",
	}}
	_, err := e.Execute(ctx, waitStep, nil)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("expected timeout error, got: %v", err)
	}
}

func TestTUIExecutor_AssertScreen(t *testing.T) {
	e := &TUIExecutor{Logger: slog.Default()}
	defer e.Close()

	ctx := context.Background()

	launchStep := Step{TUI: &TUIRequest{Command: "echo hello", WaitFor: "hello", Timeout: "2s"}}
	if _, err := e.Execute(ctx, launchStep, nil); err != nil {
		t.Fatalf("launch: %v", err)
	}

	tests := []struct {
		assertScreen string
		wantPass     bool
	}{
		{"hello", true},
		{"missing_text_xyz", false},
	}
	for _, tt := range tests {
		assertStep := Step{TUI: &TUIRequest{AssertScreen: tt.assertScreen}}
		out, err := e.Execute(ctx, assertStep, nil)
		if err != nil {
			t.Errorf("assert_screen %q: unexpected error: %v", tt.assertScreen, err)
			continue
		}
		if tt.wantPass && !strings.Contains(out.Observed, "PASS") {
			t.Errorf("assert_screen %q: expected PASS in observed, got: %s", tt.assertScreen, out.Observed)
		}
		if !tt.wantPass && !strings.Contains(out.Observed, "FAIL") {
			t.Errorf("assert_screen %q: expected FAIL in observed, got: %s", tt.assertScreen, out.Observed)
		}
	}
}

func TestTUIExecutor_AssertAbsent(t *testing.T) {
	e := &TUIExecutor{Logger: slog.Default()}
	defer e.Close()

	ctx := context.Background()

	launchStep := Step{TUI: &TUIRequest{Command: "echo hello", WaitFor: "hello", Timeout: "2s"}}
	if _, err := e.Execute(ctx, launchStep, nil); err != nil {
		t.Fatalf("launch: %v", err)
	}

	tests := []struct {
		assertAbsent string
		wantPass     bool
	}{
		{"missing_text_xyz", true},
		{"hello", false},
	}
	for _, tt := range tests {
		absentStep := Step{TUI: &TUIRequest{AssertAbsent: tt.assertAbsent}}
		out, err := e.Execute(ctx, absentStep, nil)
		if err != nil {
			t.Errorf("assert_absent %q: unexpected error: %v", tt.assertAbsent, err)
			continue
		}
		if tt.wantPass && !strings.Contains(out.Observed, "PASS") {
			t.Errorf("assert_absent %q: expected PASS in observed, got: %s", tt.assertAbsent, out.Observed)
		}
		if !tt.wantPass && !strings.Contains(out.Observed, "FAIL") {
			t.Errorf("assert_absent %q: expected FAIL in observed, got: %s", tt.assertAbsent, out.Observed)
		}
	}
}

func TestTUIExecutor_Close(t *testing.T) {
	e := &TUIExecutor{Logger: slog.Default()}

	ctx := context.Background()
	step := Step{TUI: &TUIRequest{Command: "sleep 60"}}
	if _, err := e.Execute(ctx, step, nil); err != nil {
		t.Fatalf("launch: %v", err)
	}

	if e.cmd == nil {
		t.Fatal("expected cmd to be set after launch")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		e.Close()
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close() did not return within 2 seconds")
	}

	if e.cmd != nil {
		t.Error("expected cmd to be nil after Close()")
	}
}

func TestTUIExecutor_NotStarted(t *testing.T) {
	e := &TUIExecutor{Logger: slog.Default()}
	ctx := context.Background()

	// send_key without a prior command should return errTUINotStarted.
	step := Step{TUI: &TUIRequest{SendKey: "enter"}}
	_, err := e.Execute(ctx, step, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no process started") {
		t.Errorf("expected 'no process started' error, got: %v", err)
	}
}

func TestSubstituteTUIRequest(t *testing.T) {
	vars := map[string]string{
		"cmd":  "echo hi",
		"key":  "enter",
		"text": "hello",
		"wait": "hi",
		"asc":  "hi",
		"aabs": "bye",
	}
	req := TUIRequest{
		Command:      "{cmd}",
		SendKey:      "{key}",
		SendText:     "{text}",
		WaitFor:      "{wait}",
		AssertScreen: "{asc}",
		AssertAbsent: "{aabs}",
		Timeout:      "5s",
	}
	got := substituteTUIRequest(req, vars)

	if got.Command != "echo hi" {
		t.Errorf("Command: got %q, want %q", got.Command, "echo hi")
	}
	if got.SendKey != "enter" {
		t.Errorf("SendKey: got %q, want %q", got.SendKey, "enter")
	}
	if got.SendText != "hello" {
		t.Errorf("SendText: got %q, want %q", got.SendText, "hello")
	}
	if got.WaitFor != "hi" {
		t.Errorf("WaitFor: got %q, want %q", got.WaitFor, "hi")
	}
	if got.AssertScreen != "hi" {
		t.Errorf("AssertScreen: got %q, want %q", got.AssertScreen, "hi")
	}
	if got.AssertAbsent != "bye" {
		t.Errorf("AssertAbsent: got %q, want %q", got.AssertAbsent, "bye")
	}
	if got.Timeout != "5s" {
		t.Errorf("Timeout: got %q, want %q (should not be substituted)", got.Timeout, "5s")
	}
}
