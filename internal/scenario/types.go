package scenario

import (
	"context"
	"errors"
)

var errUnknownStepType = errors.New("step has no recognized step type (need request or exec)")

// StepExecutor executes a single scenario step and returns its output.
type StepExecutor interface {
	Execute(ctx context.Context, step Step, vars map[string]string) (StepOutput, error)
}

// StepOutput is the result of executing a step, independent of step type.
type StepOutput struct {
	Observed    string // formatted description for the judge
	CaptureBody string // raw body for JSONPath capture extraction
}

// Scenario represents a holdout validation scenario loaded from YAML.
type Scenario struct {
	ID                   string   `yaml:"id"`
	Description          string   `yaml:"description"`
	Type                 string   `yaml:"type"`   // "api" only for MVP
	Weight               *float64 `yaml:"weight"` // nil means not set, defaults to 1.0
	Setup                []Step   `yaml:"setup"`
	Steps                []Step   `yaml:"steps"`
	SatisfactionCriteria string   `yaml:"satisfaction_criteria"`
}

// Step represents a single action within a scenario.
type Step struct {
	Description string       `yaml:"description"`
	Request     *Request     `yaml:"request"`
	Exec        *ExecRequest `yaml:"exec"`
	Expect      string       `yaml:"expect"` // natural language, judged by LLM
	Capture     []Capture    `yaml:"capture"`
}

// StepType returns the step type key: "request", "exec", or "" if unknown.
func (s Step) StepType() string {
	if s.Request != nil {
		return "request"
	}
	if s.Exec != nil {
		return "exec"
	}
	return ""
}

// Request describes an HTTP request to execute.
type Request struct {
	Method  string            `yaml:"method"`
	Path    string            `yaml:"path"`
	Headers map[string]string `yaml:"headers"`
	Body    any               `yaml:"body"`
}

// ExecRequest describes a CLI command to execute.
type ExecRequest struct {
	Command string `yaml:"command"`
}

// Capture defines a variable to extract from a response.
type Capture struct {
	Name     string `yaml:"name"`     // variable name
	JSONPath string `yaml:"jsonpath"` // path into response body
}
