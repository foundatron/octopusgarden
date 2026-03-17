//go:build !windows

package lint

import "gopkg.in/yaml.v3"

// tuiPlatformDiags returns nil on non-Windows platforms where TUI steps are supported.
func tuiPlatformDiags(_ string, _ *yaml.Node) []Diagnostic {
	return nil
}
