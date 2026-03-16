package ui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// StyledDisplay renders interview output with ANSI styling using lipgloss and glamour.
type StyledDisplay struct {
	out       io.Writer
	width     int
	renderer  *glamour.TermRenderer
	prompt    lipgloss.Style
	system    lipgloss.Style
	summary   lipgloss.Style
	separator lipgloss.Style
}

// NewStyled creates a StyledDisplay that writes styled output to w.
// fd is the file descriptor used for terminal width detection (typically os.Stdout.Fd()).
func NewStyled(w io.Writer, fd uintptr) *StyledDisplay {
	width := 80
	if tw, _, err := term.GetSize(int(fd)); err == nil && tw > 0 { //nolint:gosec // fd comes from os.Stdout.Fd(); overflow not possible
		width = tw
	}

	renderer, _ := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width),
	)

	return &StyledDisplay{
		out:       w,
		width:     width,
		renderer:  renderer,
		prompt:    lipgloss.NewStyle().Foreground(lipgloss.Color("2")), // green
		system:    lipgloss.NewStyle().Foreground(lipgloss.Color("8")), // dim gray
		summary:   lipgloss.NewStyle().Foreground(lipgloss.Color("3")), // yellow
		separator: lipgloss.NewStyle().Foreground(lipgloss.Color("8")), // dim gray
	}
}

// AssistantMessage renders text as markdown via glamour, falling back to plain text on error.
func (d *StyledDisplay) AssistantMessage(text string) {
	if d.renderer != nil {
		if rendered, err := d.renderer.Render(text); err == nil {
			fmt.Fprint(d.out, rendered) //nolint:errcheck
			return
		}
	}
	fmt.Fprintln(d.out, text) //nolint:errcheck
}

// InputPrompt writes the green "❯ " prompt indicator.
func (d *StyledDisplay) InputPrompt() {
	fmt.Fprint(d.out, d.prompt.Render("❯ ")) //nolint:errcheck
}

// InputSummary shows a yellow pasted-line count when lineCount exceeds 3.
func (d *StyledDisplay) InputSummary(lineCount int) {
	if lineCount <= 3 {
		return
	}
	fmt.Fprintln(d.out, d.summary.Render(fmt.Sprintf("[ pasted: %d lines ]", lineCount))) //nolint:errcheck
}

// SystemMessage writes dim gray styled text.
func (d *StyledDisplay) SystemMessage(text string) {
	fmt.Fprintln(d.out, d.system.Render(text)) //nolint:errcheck
}

// Separator writes a dim gray horizontal rule.
func (d *StyledDisplay) Separator() {
	w := d.width
	if w > 60 {
		w = 60
	}
	fmt.Fprintln(d.out, d.separator.Render(strings.Repeat("─", w))) //nolint:errcheck
}
