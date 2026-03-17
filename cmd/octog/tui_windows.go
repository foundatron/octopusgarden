//go:build windows

package main

import (
	"log/slog"

	"github.com/foundatron/octopusgarden/internal/scenario"
)

// registerTUIExecutor is a no-op on Windows; TUI steps are Unix-only.
func registerTUIExecutor(opts executorOpts, _ map[string]scenario.StepExecutor, _ *[]func()) {
	if opts.needsTUI {
		slog.Warn("tui steps are not supported on Windows; tui steps will be skipped")
	}
}
