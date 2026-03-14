package scenario

import (
	"context"
	"errors"

	"github.com/foundatron/octopusgarden/internal/limits"
)

var (
	errUnknownStepType = errors.New("step has no recognized step type (need request, exec, browser, grpc, or ws)")
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
	BrowserSourceValue    = "value"
)

// GRPC capture source constants.
const (
	GRPCSourceStatus  = "status"
	GRPCSourceHeaders = "headers"
)

// MaxResponseBytes is the maximum bytes captured from response bodies and command output.
// Re-exported from internal/limits for use within the scenario package.
const MaxResponseBytes = limits.MaxResponseBytes

// StepExecutor executes a single scenario step and returns its output.
type StepExecutor interface {
	Execute(ctx context.Context, step Step, vars map[string]string) (StepOutput, error)
	// ValidCaptureSources returns the capture source names valid for this step type,
	// or nil if the step type does not support source-based capture.
	ValidCaptureSources() []string
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
	Type                 string   `yaml:"type"`      // "api" only for MVP
	Weight               *float64 `yaml:"weight"`    // nil means not set, defaults to 1.0
	Component            string   `yaml:"component"` // component name for composed convergence; empty = integration scenario
	Tier                 int      `yaml:"tier"`      // difficulty tier (1=simple, 2=moderate, 3=complex); auto-inferred when 0
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
	GRPC        *GRPCRequest    `yaml:"grpc"`
	WS          *WSRequest      `yaml:"ws"`
	Retry       *Retry          `yaml:"retry"`
	Expect      string          `yaml:"expect"` // natural language, judged by LLM
	Capture     []Capture       `yaml:"capture"`
}

// Retry configures retry/poll behavior for a step.
type Retry struct {
	Attempts int    `yaml:"attempts"` // max attempts (default: 3)
	Interval string `yaml:"interval"` // delay between retries (default: "1s")
	Timeout  string `yaml:"timeout"`  // overall timeout cap (optional)
}

// StepType returns the step type key: "request", "exec", "browser", "grpc", "ws", or "" if unknown.
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
	if s.GRPC != nil {
		return "grpc"
	}
	if s.WS != nil {
		return "ws"
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
	Files   map[string]string `yaml:"files"` // absolute path → content; written before command execution
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

// GRPCRequest describes a gRPC call to execute.
type GRPCRequest struct {
	Service string            `yaml:"service"` // e.g. "telemetry.TelemetryService"
	Method  string            `yaml:"method"`  // e.g. "RegisterSensor"
	Body    string            `yaml:"body"`    // JSON request message (unary/server-streaming)
	Headers map[string]string `yaml:"headers"` // gRPC metadata
	Timeout string            `yaml:"timeout"` // call timeout (default: 30s)
	Stream  *GRPCStream       `yaml:"stream"`  // streaming config (nil for unary)
}

// GRPCStream configures streaming behavior for a gRPC step.
type GRPCStream struct {
	Messages []string     `yaml:"messages"` // client-streaming: list of JSON messages to send
	Receive  *GRPCReceive `yaml:"receive"`  // server-streaming: receive config
	ID       string       `yaml:"id"`       // reference a named background stream
}

// GRPCReceive configures how to receive server-streaming messages.
type GRPCReceive struct {
	Timeout    string `yaml:"timeout"`
	Count      int    `yaml:"count"`
	Background bool   `yaml:"background"`
}

// Capture defines a variable to extract from a response.
type Capture struct {
	Name     string `yaml:"name"`     // variable name
	JSONPath string `yaml:"jsonpath"` // path into response body
	Source   string `yaml:"source"`   // capture source (e.g. "stdout", "stderr", "exitcode")
}

// WSRequest describes a WebSocket step: connect, send, and/or receive.
type WSRequest struct {
	URL     string     `yaml:"url"`     // path to connect (e.g. /ws/bids); omit to reuse existing conn
	ID      string     `yaml:"id"`      // connection ID for multi-conn scenarios; defaults to "default"
	Send    string     `yaml:"send"`    // message to send (optional)
	Receive *WSReceive `yaml:"receive"` // receive config (optional; nil = send-only)
}

// WSReceive configures how to receive WebSocket messages.
type WSReceive struct {
	Timeout string `yaml:"timeout"` // receive timeout (default: 5s)
	Count   int    `yaml:"count"`   // number of messages to collect (default: 1)
}
