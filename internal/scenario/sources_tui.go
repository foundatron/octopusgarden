//go:build !windows

package scenario

// tuiStepExecutor returns the TUIExecutor for inclusion in the capture source map on Unix.
func tuiStepExecutor() StepExecutor {
	return &TUIExecutor{}
}
