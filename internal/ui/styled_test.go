package ui

import (
	"bytes"
	"strings"
	"testing"
)

func TestPlainAssistantMessage(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	d := NewPlain(&buf)
	d.AssistantMessage("hello world")
	if got := buf.String(); got != "hello world\n" {
		t.Errorf("PlainDisplay.AssistantMessage = %q, want %q", got, "hello world\n")
	}
}

func TestPlainSystemMessage(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	d := NewPlain(&buf)
	d.SystemMessage("hint text")
	if got := buf.String(); got != "hint text\n" {
		t.Errorf("PlainDisplay.SystemMessage = %q, want %q", got, "hint text\n")
	}
}

func TestPlainNoOps(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	d := NewPlain(&buf)
	d.InputPrompt()
	d.InputSummary(10)
	d.Separator()
	if buf.Len() != 0 {
		t.Errorf("PlainDisplay no-ops wrote %d bytes, want 0", buf.Len())
	}
}

func TestStyledAssistantMessageRendersContent(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	d := NewStyled(&buf, 0) // fd=0 won't resolve terminal width; falls back to 80
	d.AssistantMessage("## Heading\n\nBody text.")
	out := buf.String()
	if !strings.Contains(out, "Heading") || !strings.Contains(out, "Body text.") {
		t.Errorf("StyledDisplay.AssistantMessage missing content: %q", out)
	}
}

func TestStyledInputPrompt(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	d := NewStyled(&buf, 0)
	d.InputPrompt()
	out := buf.String()
	if !strings.Contains(out, "❯") {
		t.Errorf("StyledDisplay.InputPrompt missing prompt indicator: %q", out)
	}
}

func TestStyledInputSummaryThreshold(t *testing.T) {
	t.Parallel()

	t.Run("below threshold", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		d := NewStyled(&buf, 0)
		d.InputSummary(3)
		if buf.Len() != 0 {
			t.Errorf("InputSummary(3) should be no-op, got %q", buf.String())
		}
	})

	t.Run("above threshold", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		d := NewStyled(&buf, 0)
		d.InputSummary(10)
		out := buf.String()
		if !strings.Contains(out, "10 lines") {
			t.Errorf("InputSummary(10) should mention line count, got %q", out)
		}
	})
}

func TestStyledSeparator(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	d := NewStyled(&buf, 0)
	d.Separator()
	out := buf.String()
	if !strings.Contains(out, "─") {
		t.Errorf("StyledDisplay.Separator missing box-drawing char: %q", out)
	}
}

func TestStyledSystemMessage(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	d := NewStyled(&buf, 0)
	d.SystemMessage("Thinking...")
	out := buf.String()
	if !strings.Contains(out, "Thinking...") {
		t.Errorf("StyledDisplay.SystemMessage missing text: %q", out)
	}
}
