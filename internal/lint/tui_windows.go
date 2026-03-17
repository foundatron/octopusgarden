//go:build windows

package lint

import "gopkg.in/yaml.v3"

// tuiPlatformDiags returns a warning on Windows where TUI steps are not supported.
func tuiPlatformDiags(path string, node *yaml.Node) []Diagnostic {
	return []Diagnostic{{
		File:    path,
		Line:    node.Line,
		Level:   Warning,
		Message: "tui steps are not supported on Windows; this scenario will fail at runtime",
	}}
}
