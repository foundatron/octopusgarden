//go:build !windows

package main

import (
	"github.com/foundatron/octopusgarden/internal/scenario"
)

// registerTUIExecutor adds a TUIExecutor to executors when opts.needsTUI is set.
// When a session container is available, its ID is passed to the executor so
// that TUI commands run inside the container via docker exec -it.
func registerTUIExecutor(opts executorOpts, executors map[string]scenario.StepExecutor, closers *[]func()) {
	if opts.needsTUI {
		var containerID string
		if session := opts.sessionGetter(); session != nil {
			containerID = session.ContainerID()
		}
		tuiExec := &scenario.TUIExecutor{
			Logger:      opts.logger,
			ContainerID: containerID,
		}
		executors["tui"] = tuiExec
		*closers = append(*closers, tuiExec.Close)
	}
}
