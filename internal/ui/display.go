// Package ui provides display implementations for the interview TUI.
package ui

import (
	"fmt"
	"io"
)

// PlainDisplay writes unformatted text to an io.Writer.
// It produces byte-identical output to the original fmt.Fprintln calls.
type PlainDisplay struct {
	out io.Writer
}

// NewPlain creates a PlainDisplay that writes to w.
func NewPlain(w io.Writer) *PlainDisplay {
	return &PlainDisplay{out: w}
}

// AssistantMessage writes text followed by a newline.
func (d *PlainDisplay) AssistantMessage(text string) {
	fmt.Fprintln(d.out, text) //nolint:errcheck
}

// InputPrompt is a no-op in plain mode.
func (d *PlainDisplay) InputPrompt() {}

// InputSummary is a no-op in plain mode.
func (d *PlainDisplay) InputSummary(_ int) {}

// SystemMessage writes text followed by a newline.
func (d *PlainDisplay) SystemMessage(text string) {
	fmt.Fprintln(d.out, text) //nolint:errcheck
}

// Separator is a no-op in plain mode.
func (d *PlainDisplay) Separator() {}
