# Architecture

## System Overview

```text
Spec (markdown) ──→ Attractor Loop ──→ Generated Code ──→ Docker Build
                        │                                      │
                        │         (holdout wall)               ▼
                        │                              Running Container
                        │                                      │
                        ◄──── Failure Feedback ◄──── Validator + LLM Judge
                                                               │
                                                    Satisfaction Score (0-100)
```

The attractor loop generates code from a spec, builds it in Docker, validates it against holdout
scenarios using an LLM judge, and iterates on failures until satisfaction converges above threshold.

## Repository Structure

```text
octopusgarden/
├── cmd/octog/main.go             # CLI entrypoint, subcommand routing
├── internal/
│   ├── spec/                     # Parse markdown specs (parser.go, types.go, summary.go)
│   ├── scenario/                 # Load/run/judge YAML scenarios
│   │   ├── types.go              # Scenario, Step, Request, Capture
│   │   ├── loader.go             # Load, LoadFile, LoadDir
│   │   ├── runner.go             # Execute scenario steps against live server
│   │   ├── judge.go              # LLM-as-judge satisfaction scoring
│   │   ├── result.go             # Result, StepScore, ScoredStep, ScoredScenario, AggregateResult
│   │   ├── jsonpath.go           # Dot-notation JSONPath evaluator ($.field.sub)
│   │   └── grpc.go              # gRPC step executor (reflection-based, streaming)
│   ├── attractor/                # Convergence loop
│   │   ├── attractor.go          # Core loop, types, options
│   │   ├── convergence.go        # Trend detection
│   │   └── fileparse.go          # Parse LLM output into files, merge for patch mode
│   ├── container/docker.go       # Build and run Docker containers
│   ├── llm/                      # LLM client abstraction
│   │   ├── client.go             # Client interface, request/response types
│   │   ├── anthropic.go          # Anthropic backend (anthropic-sdk-go)
│   │   ├── openai.go             # OpenAI/Ollama backend (openai-go/v3)
│   │   ├── json.go               # Shared JSON extraction for judge responses
│   │   ├── models.go             # Model registry, cost tracking
│   │   └── prompt.go             # Prompt templates
│   ├── lint/                     # Spec and scenario linting
│   └── store/                    # SQLite run history (db.go, types.go)
├── examples/                     # Example specs and scenarios
│   └── <name>/
│       ├── spec.md               # Spec file
│       └── scenarios/            # Scenario YAML files
└── docs/architecture.md          # This file
```

## Package Dependency DAG

```text
cmd/octog
    ├── internal/attractor   (loop, convergence, fileparse)
    │       ├── internal/llm
    │       ├── internal/spec
    │       └── internal/container
    ├── internal/scenario    (loader, runner, judge)
    │       └── internal/llm
    ├── internal/llm         (client interface, anthropic, openai, models, prompts)
    ├── internal/container   (docker build/run)
    ├── internal/spec        (parser, types, summary)
    └── internal/store       (sqlite)
```

Key constraint: `internal/attractor` never imports `internal/scenario`. The attractor receives spec
content and failure feedback as strings. The validator (scenario runner + judge) is invoked by
`cmd/octog`, not by the attractor. Store interaction is also owned by `cmd/octog`.

## LLM Client Interface

[embedmd]:# (../internal/llm/client.go go /^\/\/ Client is/ /^}/)
```go
// Client is the model-agnostic LLM interface used by the attractor loop and judge.
type Client interface {
	Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error)
	Judge(ctx context.Context, req JudgeRequest) (JudgeResponse, error)
}
```

Request/response types (`GenerateRequest`, `GenerateResponse`, `JudgeRequest`, `JudgeResponse`,
`Message`, `CacheControl`) are defined in `internal/llm/client.go`.

### Anthropic Backend (`internal/llm/anthropic.go`)

Uses `github.com/anthropics/anthropic-sdk-go`. Spec content in the system prompt gets
`CacheControl{Type: "ephemeral"}` — cached across attractor iterations for ~90% input cost reduction
(cache TTL: 5 minutes, resets on hit). Failure feedback in user messages changes each iteration and
is not cached.

### OpenAI Backend (`internal/llm/openai.go`)

Uses `github.com/openai/openai-go/v3`. For GPT models and OpenAI-compatible endpoints (Ollama via
`OPENAI_BASE_URL`). Implements `Generate`, `Judge`, and `ListModels`.

## Spec Data Structures

[embedmd]:# (../internal/spec/types.go go)
```go
package spec

// Spec represents a parsed markdown specification.
type Spec struct {
	Title       string
	Description string
	Sections    []Section
	RawContent  string // full markdown, used for LLM prompt
}

// Section represents a single heading and its content within a spec.
type Section struct {
	Heading string
	Level   int    // 1, 2, 3...
	Content string // text content under this heading
}
```

For large specs exceeding a context budget, `internal/spec/summary.go` produces multi-level pyramid
summaries (`SummarizedSpec`). `SelectContent` picks the richest representation that fits: full spec →
section summaries with failure-relevant sections expanded → outline → abstract → truncated.

## Scenario Data Structures

[embedmd]:# (../internal/scenario/types.go go)
```go
package scenario

import (
	"context"
	"errors"
)

var (
	errUnknownStepType = errors.New("step has no recognized step type (need request, exec, browser, or grpc)")
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

// GRPC capture source constants.
const (
	GRPCSourceStatus  = "status"
	GRPCSourceHeaders = "headers"
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
	GRPC        *GRPCRequest    `yaml:"grpc"`
	Expect      string          `yaml:"expect"` // natural language, judged by LLM
	Capture     []Capture       `yaml:"capture"`
}

// StepType returns the step type key: "request", "exec", "browser", "grpc", or "" if unknown.
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
```

Result types (`HTTPResponse`, `StepResult`, `Result`, `StepScore`, `ScoredStep`, `ScoredScenario`,
`AggregateResult`) are in `internal/scenario/result.go`.

### Scenario YAML Format

```yaml
id: items-crud
description: "Create, read, update, and delete items"
type: api
weight: 1.0
setup:
  - description: "Create a test item"
    request:
      method: POST
      path: /items
      body: { "name": "test item", "description": "for testing" }
    capture:
      - name: item_id
        jsonpath: $.id
steps:
  - description: "Read the created item"
    request:
      method: GET
      path: /items/{item_id}
    expect: "Returns the item with name 'test item' and a valid ID"
  - description: "Delete the item"
    request:
      method: DELETE
      path: /items/{item_id}
    expect: "Returns 200 or 204 indicating successful deletion"
satisfaction_criteria: |
  All CRUD operations work correctly with appropriate status codes.
```

#### gRPC Step

```yaml
id: register-sensor
description: Register a sensor via gRPC
type: api
steps:
  - description: Register a temperature sensor
    grpc:
      service: telemetry.TelemetryService
      method: RegisterSensor
      body: '{"name": "warehouse-t1", "type": "temperature"}'
    expect: "Returns sensor with generated id"
    capture:
      - name: sensor_id
        jsonpath: $.id
      - name: status_code
        source: status
```

gRPC steps use server reflection for dynamic invocation (no compiled protos needed). Supported
patterns: unary, client-streaming (`stream.messages`), server-streaming (`stream.receive`), and
background persistent streams (`stream.receive.background: true`).

Client-streaming sends multiple messages:

```yaml
grpc:
  service: telemetry.TelemetryService
  method: StreamUpload
  stream:
    messages:
      - '{"sensor_id": "{sensor_id}", "value": 22.5}'
      - '{"sensor_id": "{sensor_id}", "value": 23.1}'
```

Server-streaming collects responses:

```yaml
grpc:
  service: telemetry.TelemetryService
  method: WatchSensor
  body: '{"sensor_id": "{sensor_id}"}'
  stream:
    receive:
      timeout: 5s
      count: 3
      background: true  # persist stream across steps
```

Capture sources for gRPC steps: `status` (gRPC status code) and `headers` (response metadata).

### Variable Capture and Substitution

The runner executes steps sequentially, evaluates `capture` rules against response bodies, stores
values in a variable map, and substitutes `{variable_name}` in subsequent paths, headers, bodies,
and gRPC fields. JSONPath evaluation supports dot-notation only (`$.field.sub`).

## Scenario Runner

`Runner` (`internal/scenario/runner.go`) executes scenario steps via pluggable `StepExecutor`
implementations (HTTP, exec, browser, gRPC). Setup steps are fatal — if any fails, the runner
returns an error immediately. Judged steps are non-fatal — transport
errors are recorded and the step is scored 0 without making an LLM call.

## LLM Judge

The judge scores each step independently using an LLM, then aggregates per scenario.

Judge prompt (`internal/llm/prompt.go`):

**System** (`SatisfactionJudgeSystem`):

```text
You are a QA evaluator. Score how well this software behavior matches the expected behavior.

Respond with JSON only:
{"score": <0-100>, "reasoning": "<brief explanation>", "failures": ["<specific failure>"]}

Scoring guide:
- 100: Perfect match to expected behavior
- 80-99: Works correctly with minor deviations
- 50-79: Partially correct
- 1-49: Mostly broken but shows some correct behavior
- 0: Complete failure or error
```

**User** (`SatisfactionJudgeUser`):

```text
Scenario: {scenario_description}
Step: {step_description}

Expected behavior: {expected}

Actual observed behavior:
{observed}
```

Per-scenario score = average of step scores. Overall satisfaction = weighted average of scenario
scores (using scenario `weight` field, default 1.0). See `Aggregate()` in
`internal/scenario/judge.go`.

## Attractor Loop

[embedmd]:# (../internal/attractor/attractor.go go /^\/\/ ContainerManager is/ /^}/)
```go
// ContainerManager is the interface to Docker container operations.
// *container.Manager satisfies this automatically.
type ContainerManager interface {
	Build(ctx context.Context, dir, tag string) error
	Run(ctx context.Context, tag string) (url string, stop container.StopFunc, err error)
	RunMultiPort(ctx context.Context, tag string, extraPorts []string) (container.RunResult, container.StopFunc, error)
	WaitHealthy(ctx context.Context, url string, timeout time.Duration) error
	WaitPort(ctx context.Context, addr string, timeout time.Duration) error
	StartSession(ctx context.Context, tag string) (session *container.Session, stop container.StopFunc, err error)
}
```

[embedmd]:# (../internal/attractor/attractor.go go /^\/\/ ValidateFn runs/ /err error\)/)
```go
// ValidateFn runs holdout scenarios against a running container and returns results.
// The attractor never imports internal/scenario — the CLI provides this closure.
type ValidateFn func(ctx context.Context, url string) (satisfaction float64, failures []string, cost float64, err error)
```

[embedmd]:# (../internal/attractor/attractor.go go /^\/\/ RunOptions configures/ /^}/)
```go
// RunOptions configures the attractor loop.
type RunOptions struct {
	Model         string
	BudgetUSD     float64              // 0 = unlimited
	Threshold     float64              // default 95
	MaxIterations int                  // default 10
	StallLimit    int                  // default 3
	WorkspaceDir  string               // default "./workspace"
	HealthTimeout time.Duration        // default 30s
	Progress      ProgressFunc         // optional per-iteration callback
	PatchMode     bool                 // if true, iteration 2+ sends prev best files + failures
	ContextBudget int                  // max estimated tokens for spec in system prompt; 0 = unlimited
	Capabilities  ScenarioCapabilities // detected from loaded scenarios
}
```

[embedmd]:# (../internal/attractor/attractor.go go /^\/\/ RunResult holds/ /^}/)
```go
// RunResult holds the outcome of an attractor run.
type RunResult struct {
	RunID        string
	Iterations   int
	Satisfaction float64
	CostUSD      float64
	OutputDir    string
	Status       string
}
```

### Loop Pseudocode

```text
1. If ContextBudget > 0 and spec exceeds budget, summarize spec (pyramid summaries)
2. For iter = 1 to MaxIterations:
   a. Check budget
   b. Select spec content (full or summarized with failure-relevant sections expanded)
   c. Build messages:
      - Normal mode: spec only (iter 1) or spec + last 3 failure summaries (iter N>1)
      - Patch mode (iter 2+ with bestFiles): previous best files + failures
   d. Call LLM: generate code
   e. Parse LLM output into files (=== FILE: path === ... === END FILE ===)
   f. In patch mode, MergeFiles(newFiles, bestFiles) to carry forward unchanged files
   g. Write files to workspace/{run_id}/iter_{n}/
   h. docker build → docker run → wait for health check → call validate(ctx, url)
   i. If satisfaction >= threshold → return "converged"
   j. Track improvement/stalls; patch mode: disable after 2 consecutive regressions
   k. If stall count >= stall limit → return "stalled"
3. Return "max_iterations"
```

### Progress Reporting

[embedmd]:# (../internal/attractor/attractor.go go /^\/\/ IterationOutcome classifies/ /^\)/)
```go
// IterationOutcome classifies how a single iteration ended.
type IterationOutcome string

// IterationOutcome constants for progress reporting.
const (
	OutcomeValidated  IterationOutcome = "validated"
	OutcomeBuildFail  IterationOutcome = "build_fail"
	OutcomeRunFail    IterationOutcome = "run_fail"
	OutcomeHealthFail IterationOutcome = "health_fail"
	OutcomeParseFail  IterationOutcome = "parse_fail"
)
```

[embedmd]:# (../internal/attractor/attractor.go go /^\/\/ IterationProgress is/ /^}/)
```go
// IterationProgress is passed to the progress callback after each iteration completes.
type IterationProgress struct {
	RunID            string
	Iteration        int
	MaxIterations    int
	Outcome          IterationOutcome
	Satisfaction     float64
	BestSatisfaction float64
	Threshold        float64
	Trend            Trend
	IterationCostUSD float64
	TotalCostUSD     float64
	BudgetUSD        float64
	Elapsed          time.Duration
	StallCount       int
}
```

The `ProgressFunc` callback is called synchronously after each iteration completes.

### File Block Parser

The LLM outputs files in this format:

```text
=== FILE: path/to/file.ext ===
file contents here
=== END FILE ===
```

In patch mode, `MergeFiles(newFiles, prevFiles)` copies all previous best files and overlays new
output on top.

## Convergence (`internal/attractor/convergence.go`)

[embedmd]:# (../internal/attractor/convergence.go go /^\/\/ Trend classifies/ /^\)/)
```go
// Trend classifies the direction of score history.
type Trend string

// Trend constants for score trajectory classification.
const (
	TrendImproving  Trend = "improving"
	TrendPlateau    Trend = "plateau"
	TrendRegressing Trend = "regressing"
	TrendConverged  Trend = "converged"
)
```

`DetectTrend` classifies the score trajectory using a sliding window: `converged` (score ≥
threshold), `improving` (score > baseline), `regressing` (score < peak in window), `plateau` (no
movement).

## Docker Container Strategy

`Manager` (`internal/container/docker.go`) builds Docker images and runs containers. Uses port 0 for
random host port assignment. Health check polls `GET /` every 1s for up to the configured timeout
(default 30s) — any non-5xx response means healthy.

`StopFunc` returned by `Run` stops and removes the container using `context.Background()` to succeed
even after caller context cancellation. `Close()` closes the underlying Docker client.

## CLI Interface

```text
octog run       --spec <path> --scenarios <dir> [--model claude-sonnet-4-6] [--judge-model claude-haiku-4-5] [--budget 5.00] [--threshold 95] [--patch] [--context-budget 0] [--provider anthropic|openai]
octog validate  --scenarios <dir> --target <url> [--judge-model claude-haiku-4-5] [--threshold 0] [--format text|json] [--provider anthropic|openai]
octog status    [--format text|json]
octog lint      --spec <path> --scenarios <dir>
octog models    [--provider anthropic|openai]
octog configure
```

Subcommands: `run`, `validate`, `status`, `lint`, `models`, `configure`.

Provider is auto-detected from which API key is set. Use `--provider` to disambiguate when both are
present. Config file (`~/.octopusgarden/config`) supports `ANTHROPIC_API_KEY` and `OPENAI_API_KEY`;
env vars take precedence. `OPENAI_BASE_URL` overrides the OpenAI endpoint for Ollama etc.

## SQLite Schema

```sql
CREATE TABLE runs (
    id            TEXT PRIMARY KEY,
    spec_path     TEXT NOT NULL,
    model         TEXT NOT NULL,
    threshold     REAL NOT NULL,
    budget_usd    REAL,
    started_at    DATETIME NOT NULL,
    finished_at   DATETIME,
    satisfaction  REAL,
    iterations    INTEGER,
    total_tokens  INTEGER,
    total_cost_usd REAL,
    status        TEXT NOT NULL  -- 'running', 'converged', 'stalled', 'budget_exceeded', 'failed'
);

CREATE TABLE iterations (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id        TEXT NOT NULL REFERENCES runs(id),
    iteration     INTEGER NOT NULL,
    satisfaction  REAL,
    input_tokens  INTEGER,
    output_tokens INTEGER,
    cost_usd      REAL,
    failures      TEXT,         -- JSON array of failure descriptions
    created_at    DATETIME NOT NULL
);
```
