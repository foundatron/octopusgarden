//go:build !windows

package main

import (
	"github.com/foundatron/octopusgarden/internal/scenario"
)

// registerTUIExecutor adds a TUIExecutor to executors when opts.needsTUI is set.
func registerTUIExecutor(opts executorOpts, executors map[string]scenario.StepExecutor, closers *[]func()) {
	if opts.needsTUI {
		tuiExec := &scenario.TUIExecutor{Logger: opts.logger}
		executors["tui"] = tuiExec
		*closers = append(*closers, tuiExec.Close)
	}
}
