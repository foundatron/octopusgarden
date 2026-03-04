package scenario

import (
	"context"
	"errors"
)

var (
	errUnknownStepType = errors.New("step has no recognized step type (need request, exec, or browser)")
	errNoCapture       = errors.New("capture has neither source nor jsonpath")
)

// Exec capture source constants.
const (
	ExecSourceStdout   = "stdout"
	ExecSourceStderr   = "stderr"
	ExecSourceExitCode = "exitcode"
)

// Browser capture source constants.
const (
	BrowserSourceText     = "text"
	BrowserSourceHTML     = "html"
	BrowserSourceCount    = "count"
	BrowserSourceLocation = "location"
)

// StepExecutor executes a single scenario step and returns its output.
type StepExecutor interface {
	Execute(ctx context.Context, step Step, vars map[string]string) (StepOutput, error)
}

// StepOutput is the result of executing a step, independent of step type.
type StepOutput struct {
	Observed       string            // formatted description for the judge
	CaptureBody    string            // raw body for JSONPath capture extraction
	CaptureSources map[string]string // source-based capture data (e.g. "stdout", "stderr", "exitcode")
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
	Description string          `yaml:"description"`
	Request     *Request        `yaml:"request"`
	Exec        *ExecRequest    `yaml:"exec"`
	Browser     *BrowserRequest `yaml:"browser"`
	Expect      string          `yaml:"expect"` // natural language, judged by LLM
	Capture     []Capture       `yaml:"capture"`
}

// StepType returns the step type key: "request", "exec", "browser", or "" if unknown.
func (s Step) StepType() string {
	if s.Request != nil {
		return "request"
	}
	if s.Exec != nil {
		return "exec"
	}
	if s.Browser != nil {
		return "browser"
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
	Command string            `yaml:"command"`
	Stdin   string            `yaml:"stdin"`
	Env     map[string]string `yaml:"env"`
	Timeout string            `yaml:"timeout"`
}

// BrowserRequest describes a browser automation action.
type BrowserRequest struct {
	Action     string `yaml:"action"`      // navigate, click, fill, assert
	URL        string `yaml:"url"`         // for navigate: path relative to BaseURL
	Selector   string `yaml:"selector"`    // CSS selector for click, fill, assert
	Value      string `yaml:"value"`       // for fill: text to type
	Text       string `yaml:"text"`        // assert: element contains text
	TextAbsent string `yaml:"text_absent"` // assert: element does NOT contain text
	Count      *int   `yaml:"count"`       // assert: number of matching elements
	Timeout    string `yaml:"timeout"`     // wait timeout (default: 10s)
}

// Capture defines a variable to extract from a response.
type Capture struct {
	Name     string `yaml:"name"`     // variable name
	JSONPath string `yaml:"jsonpath"` // path into response body
	Source   string `yaml:"source"`   // capture source (e.g. "stdout", "stderr", "exitcode")
}
