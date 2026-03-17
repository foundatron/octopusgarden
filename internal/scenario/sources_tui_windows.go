//go:build windows

package scenario

// tuiStepExecutor returns nil on Windows; TUI steps are not supported.
func tuiStepExecutor() StepExecutor {
	return nil
}
