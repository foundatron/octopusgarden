package lint

import "fmt"

// Level represents the severity of a diagnostic.
type Level int

const (
	// Error indicates a problem that will cause runtime failures.
	Error Level = iota
	// Warning indicates a potential issue or style violation.
	Warning
)

func (l Level) String() string {
	switch l {
	case Error:
		return "error"
	case Warning:
		return "warning"
	default:
		return "unknown"
	}
}

// Diagnostic represents a single lint finding with file location.
type Diagnostic struct {
	File    string
	Line    int // 1-based; 0 means unknown
	Level   Level
	Message string
}

func (d Diagnostic) String() string {
	if d.Line > 0 {
		return fmt.Sprintf("%s:%d: %s: %s", d.File, d.Line, d.Level, d.Message)
	}
	return fmt.Sprintf("%s: %s: %s", d.File, d.Level, d.Message)
}

// HasErrors returns true if any diagnostic has Error level.
func HasErrors(diags []Diagnostic) bool {
	for _, d := range diags {
		if d.Level == Error {
			return true
		}
	}
	return false
}

// CountByLevel returns the number of errors and warnings in the diagnostics.
func CountByLevel(diags []Diagnostic) (errs, warns int) {
	for _, d := range diags {
		switch d.Level {
		case Error:
			errs++
		case Warning:
			warns++
		}
	}
	return errs, warns
}
