<!-- markdownlint-disable MD024 -->

# Architecture

> **Audience: AI agents and LLMs.** This document is optimized for machine consumption. It is
> comprehensive and information-dense by design -- type signatures, interface definitions, package
> relationships, behavioral details, and prompt templates are included so an LLM can understand the
> system quickly without reading every source file. For human-oriented documentation, see
> [README.md](../README.md).

## System Overview

```text
Spec + Scenarios ──→ Preflight ──→ Attractor Loop ──→ Generated Code ──→ Docker Build
                     (optional)        │    ▲                                  │
                                       │    │ wonder/reflect                   ▼
                                       │    │ (on stall)              Running Container
                                       │                                      │
                                       ◄──── Failure Feedback ◄──── Validator + LLM Judge
                                                                              │
                                                                   Satisfaction Score (0-100)
```

The attractor loop generates code from a spec, builds it in Docker, validates it against holdout
scenarios using an LLM judge, and iterates on failures until satisfaction converges above threshold.
On stalls, the wonder/reflect cycle diagnoses root causes and generates surgical fixes. Model
escalation starts cheap and upgrades when progress stalls.

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
│   │   ├── attractor.go          # Core loop, types, options, composed convergence
│   │   ├── convergence.go        # Trend detection
│   │   ├── diagnosis.go          # Wonder/reflect two-phase stall recovery
│   │   ├── escalation.go         # Model tier escalation (frugal ↔ primary)
│   │   ├── fileparse.go          # Parse LLM output into files, merge for patch mode
│   │   ├── languages.go          # Per-language templates (Go, Python, Node, Rust)
│   │   ├── oscillation.go        # A→B→A→B oscillation detection (SHA-256 hashing)
│   │   ├── prompts.go            # System prompt, feedback fidelity, steering text, component prompts
│   │   ├── regression.go         # Per-scenario regression tracking
│   │   ├── toposort.go           # Kahn's algorithm topological sort for component dependency graph
│   │   └── triage.go             # LLM-based file triage: filter bestFiles to failure-relevant subset
│   ├── container/docker.go       # Build and run Docker containers
│   ├── llm/                      # LLM client abstraction
│   │   ├── client.go             # Client + AgentClient interfaces, request/response types
│   │   ├── anthropic.go          # Anthropic backend (anthropic-sdk-go, includes AgentLoop)
│   │   ├── openai.go             # OpenAI/Ollama backend (openai-go/v3)
│   │   ├── json.go               # Shared JSON extraction for judge responses
│   │   ├── models.go             # Model registry, cost tracking
│   │   └── prompt.go             # Prompt templates
│   ├── gene/                     # Gene transfusion (scan, analyze, gene types)
│   ├── interview/                # Conversational spec-drafting assistant + spec-completeness scorer
│   ├── lint/                     # Spec and scenario structural linting
│   ├── preflight/                # LLM-based spec/scenario quality assessment
│   │   ├── preflight.go          # Check(): spec clarity (goal, constraint, success)
│   │   └── scenario.go           # CheckScenarios(): coverage, feasibility, isolation, chains
│   ├── observability/            # OpenTelemetry tracing (OTLP/HTTP)
│   │   └── setup.go              # InitTracer, TracingLLMClient, TracingContainerManager
│   ├── view/                     # JSON view models for CLI output
│   ├── store/                    # SQLite run history (db.go, types.go)
│   ├── testutil/                 # Test helpers
│   └── e2e/                      # End-to-end integration tests
├── examples/                     # Example specs, scenarios, and reference implementations
│   ├── <name>/
│   │   ├── spec.md               # Spec file
│   │   └── scenarios/            # Scenario YAML files
│   └── exemplars/                # Reference implementations for gene extraction
│       ├── go-rest/              # Go stdlib REST API (CRUD, pagination, in-memory store)
│       ├── python-rest/          # Python stdlib REST API (CRUD, pagination, in-memory store)
│       ├── node-rest/            # Node.js stdlib REST API (CRUD, pagination, in-memory store)
│       └── rust-rest/            # Rust/Axum REST API (CRUD, pagination, in-memory store)
└── docs/architecture.md          # This file
```

## Package Dependency DAG

```text
cmd/octog
    ├── internal/attractor      (loop, convergence, diagnosis, escalation, oscillation, regression)
    │       ├── internal/llm
    │       ├── internal/spec
    │       └── internal/container
    ├── internal/interview      (conversational spec-drafting, multi-turn LLM, scenario generation)
    │       ├── internal/llm
    │       ├── internal/attractor  (ParseFiles for scenario block extraction)
    │       └── internal/scenario   (Load for scenario YAML validation)
    ├── internal/preflight      (spec clarity, scenario quality assessment)
    │       └── internal/llm
    ├── internal/gene           (scan, analyze, gene types)
    │       ├── internal/llm
    │       └── internal/spec
    ├── internal/scenario       (loader, runner, judge)
    │       └── internal/llm
    ├── internal/lint           (spec + scenario structural linting)
    ├── internal/observability  (OpenTelemetry tracing, instrumented wrappers)
    ├── internal/llm            (client interface, anthropic, openai, models, prompts)
    ├── internal/container      (docker build/run, sessions, multi-port)
    ├── internal/spec           (parser, types, summary)
    ├── internal/view           (JSON view models for CLI output)
    └── internal/store          (sqlite)
```

Key constraint: `internal/attractor` never imports `internal/scenario`. The attractor receives spec
content and failure feedback as strings. The validator (scenario runner + judge) is invoked by
`cmd/octog`, not by the attractor. Store interaction is also owned by `cmd/octog`.

## Package Scope Registry

| Package | Purpose | Key Files | Dependencies |
| ------- | ------- | --------- | ------------ |
| `attractor` | Convergence loop: generate code, build, validate, iterate | `attractor.go`, `convergence.go`, `diagnosis.go`, `escalation.go`, `oscillation.go`, `regression.go`, `prompts.go`, `fileparse.go`, `languages.go`, `triage.go`, `toposort.go` | `llm`, `spec`, `container`, `gene` |
| `container` | Docker image build, container run, health check, exec sessions | `docker.go` | docker SDK |
| `scenario` | Load YAML scenarios, execute steps, LLM-judge scoring | `types.go`, `loader.go`, `runner.go`, `judge.go`, `result.go`, `jsonpath.go`, `grpc.go` | `llm` |
| `spec` | Parse markdown specs, pyramid summarization | `parser.go`, `types.go`, `summary.go` | (none) |
| `llm` | Model-agnostic LLM client, cost tracking, prompt templates | `client.go`, `anthropic.go`, `openai.go`, `models.go`, `json.go`, `prompt.go` | anthropic-sdk, openai-sdk |
| `gene` | Scan exemplar codebases, LLM pattern extraction | `gene.go`, `scan.go`, `analyze.go` | `llm`, `spec` |
| `interview` | Conversational spec-drafting, spec-completeness scoring, holdout scenario generation | `interview.go`, `prompt.go`, `scoring.go`, `scoring_prompt.go`, `scenarios.go`, `scenarios_prompt.go` | `llm`, `attractor`, `scenario` |
| `preflight` | Pre-run quality assessment of specs and scenarios | `preflight.go`, `scenario.go` | `llm` |
| `lint` | Structural linting for specs and scenario YAML | `spec.go`, `scenario.go`, `diagnostic.go`, `varcheck.go` | (none) |
| `observability` | OpenTelemetry tracing wrappers | `setup.go` | `llm`, `container`, otel SDK |
| `store` | SQLite run/iteration persistence | `db.go`, `types.go` | modernc.org/sqlite |
| `view` | JSON view models for CLI output | `*.go` | (none) |
| `limits` | Shared constants (MaxResponseBytes) | `limits.go` | (none) |
| `testutil` | Test helpers | `*.go` | (none) |
| `e2e` | End-to-end integration tests | `*.go` | multiple |

## Capabilities & Algorithms

### Convergence Detection

- **Status**: Implemented
- **Files**: `attractor/convergence.go`
- **Method**: Sliding-window trend classification (improving/plateau/regressing/converged) over satisfaction score history. Convergence = score >= threshold.
- **Limitations**: Binary threshold with no prediction of iterations remaining. No Bayesian or curve-fitting estimation of convergence probability.

### Oscillation Detection

- **Status**: Implemented
- **Files**: `attractor/oscillation.go`
- **Method**: SHA-256 hash of each iteration's file set. Detects A-B-A-B pattern over last 4 hashes. Injects steering text when detected.
- **Limitations**: Only detects period-2 oscillations. Longer cycles (period-3+) or near-miss oscillations (semantically equivalent but hash-different outputs) are invisible.

### Stall Recovery

- **Status**: Implemented
- **Files**: `attractor/diagnosis.go`
- **Method**: Two-phase LLM process: wonder (high temp, diagnose failures) then reflect (low temp, generate fix from diagnosis). Falls back to normal generation on failure.
- **Limitations**: Single diagnosis attempt per stall. No ensemble of hypotheses, no automated hypothesis testing, no causal reasoning beyond what the LLM infers from context.

### Model Escalation

- **Status**: Implemented
- **Files**: `attractor/escalation.go`
- **Method**: Start at frugal tier, escalate to primary after 2 non-improving iterations, downgrade after 5 improving. Binary tier system.
- **Limitations**: Only two tiers. No cost-aware routing based on task difficulty. No per-scenario model selection. No dynamic budget allocation across iterations.

### LLM-as-Judge Scoring

- **Status**: Implemented
- **Files**: `scenario/judge.go`, `llm/prompt.go`, `llm/json.go`
- **Method**: Each step scored independently by LLM (0-100 JSON response). Per-scenario = mean of step scores. Overall = weighted mean of scenario scores.
- **Limitations**: No judge calibration or consistency checking. No reference-based scoring. No multi-judge consensus. Score variance across runs is uncharacterized.

### Feedback Fidelity

- **Status**: Implemented
- **Files**: `attractor/prompts.go`
- **Method**: Three tiers (compact/standard/full) scaling detail and byte limits by iteration number. Stalls escalate fidelity.
- **Limitations**: Fixed tier boundaries. No adaptive selection based on failure type or information content. No ranking of which failures are most informative.

### Spec Summarization

- **Status**: Implemented
- **Files**: `spec/summary.go`
- **Method**: Multi-level pyramid (full, section summaries with expansion, outline, abstract, truncated). SelectContent picks richest representation within token budget.
- **Limitations**: Summarization is static per spec. No failure-aware dynamic summarization that emphasizes sections most relevant to current failures (expansion is coarse-grained).

### Gene Transfusion

- **Status**: Implemented
- **Files**: `gene/scan.go`, `gene/analyze.go`, `gene/gene.go`, `attractor/prompts.go`
- **Method**: Scan exemplar codebase for high-signal files within 20K token budget. LLM extracts structured pattern guide including optional named `Component` entries (interface, patterns, dependency graph). `parseComponents` scans the guide for `**COMPONENT: <name>**` headers; `validateComponents` enforces non-empty unique names, declared dependencies exist, and no cycles (DFS). Guide and components stored in `Gene` JSON. Injected into system prompt. When components are present, `cmd/octog` also constructs per-component `ValidateFn` closures (grouped by `Scenario.Component`) and passes them to the attractor via `RunOptions.ComponentValidators` — enabling composed convergence (see below).
- **Limitations**: Single exemplar only. No multi-repo synthesis. No incremental update as generated code evolves. Patterns are extracted once, not refined based on generation outcomes.

### Composed Convergence

- **Status**: Implemented
- **Files**: `attractor/attractor.go`, `attractor/toposort.go`, `attractor/prompts.go`
- **Method**: When `RunOptions.GeneComponents` and `RunOptions.ComponentValidators` are both set (and `Agentic` is false), the attractor attempts composed convergence before falling back to the monolithic loop. Steps:
  1. **Topological sort** (`topoSort`): order components dependency-first using Kahn's BFS algorithm. Returns error on cycles or unknown dependencies.
  2. **Per-component mini-loops**: for each component in topo order, run up to `componentMiniLoopMaxIter` (5) iterations. Each iteration generates files scoped to the component using `buildComponentPrompt` (spec + `COMPONENT CONTRACT` + `COMPONENT PATTERNS` + `DEPENDENCY INTERFACES` for declared deps only). Only the component's own `ValidateFn` scores it. All previously converged files are accumulated and included as a base file set (transitive dependencies are included automatically).
  3. **File merge**: after all components converge, `mergeComponentFiles` merges all component file sets in topo order (later components win on path conflicts).
  4. **Integration validation**: the merged files are built and validated with the `""` key validator (scenarios with empty `Component` field). If integration satisfaction >= threshold, the run converges. Otherwise, falls back to the monolithic loop.
  5. **Fallback**: any component stall, build failure, or integration failure triggers a fallback to the standard monolithic loop. Costs accumulated during the composed attempt are carried forward so budget accounting remains accurate.
- **Prompt structure**: `buildComponentPrompt` places the spec first (cacheable prefix) followed by component-specific content — `COMPONENT CONTRACT`, optional `COMPONENT PATTERNS`, and `DEPENDENCY INTERFACES` filtered to declared dependencies only (alphabetical order).
- **Scenario grouping**: `buildComponentValidators` groups scenarios by `Scenario.Component` in a single pass. The `""` key collects integration scenarios (empty `Component`). Each validator is wrapped with `observability.WrapValidateFn` to emit `scenario.validate` spans.
- **Limitations**: Not supported in agentic mode. No partial-merge retry (integration failure falls back entirely to monolithic). Component mini-loop stall limit (2) and max iterations (5) are compile-time constants. No cost-aware per-component budget allocation. File-level conflicts resolved by topo order, not semantic merging.

### Regression Tracking

- **Status**: Implemented
- **Files**: `attractor/regression.go`
- **Method**: Per-scenario score comparison between consecutive validated iterations. Regression = score drops below threshold after being at/above it. Injected as feedback.
- **Limitations**: Only tracks threshold crossings, not gradual degradation. No root-cause attribution for regressions. No automatic rollback of regressive changes.

### Preflight Quality Assessment

- **Status**: Implemented
- **Files**: `preflight/preflight.go`, `preflight/scenario.go`
- **Method**: LLM-based scoring of spec clarity (goal/constraint/success) and scenario quality (coverage/feasibility/isolation/chains).
- **Limitations**: Single-pass assessment. No iterative refinement suggestions. No automated spec or scenario repair.

### Scenario Execution

- **Status**: Implemented
- **Files**: `scenario/runner.go`, `scenario/grpc.go`, step executors
- **Method**: Sequential step execution with variable capture/substitution. Pluggable executors: HTTP, exec, browser (chromedp), gRPC (reflection), WebSocket. Setup steps fatal, judged steps non-fatal.
- **Parallelism**: `validate --parallel-scenarios N` runs up to N scenarios concurrently using a semaphore-bounded goroutine pool. Each goroutine owns its own `Runner`, `Judge`, and executor instances. Container restart is disabled when `N > 1` (scenarios share container state); use `--parallel-scenarios 1` (the default) when clean state between scenarios is required.
- **Limitations**: Sequential steps within a scenario only. No parallel step groups. No conditional branching. JSONPath is dot-notation only (no filters, array slicing, or recursive descent).

## Known Gaps & Improvement Opportunities

- **Convergence prediction**: Bayesian or GP-based models to estimate iterations-to-convergence and inform budget/model decisions early
- **Judge calibration**: Reference-based scoring, multi-judge voting, or calibration sets to reduce score variance and improve reliability
- **Cost-aware model routing**: Per-iteration difficulty estimation to dynamically select model tier (not just binary escalation)
- **Oscillation breaking**: Detect period-3+ cycles; use semantic similarity (embeddings or AST diff) instead of exact hash matching
- **Feedback selection**: Rank failures by information content or novelty; prioritize feedback that is most likely to drive improvement
- **Search strategies**: Beam search (maintain N candidate solutions), tree-of-thought, or MCTS-style exploration instead of single-path iteration
- **Incremental patching**: AST-level diff and merge instead of full-file regeneration to preserve working code and reduce token cost
- **Spec-failure alignment**: Dynamically weight spec sections in the prompt based on which sections are causing current failures
- **Multi-exemplar gene synthesis**: Combine patterns from multiple codebases; update gene guide based on generation outcomes
- **Automated spec repair**: Use preflight failures to suggest or auto-fix spec ambiguities before entering the attractor loop

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
`Message`, `CacheControl`, `Diagnostic`) are defined in `internal/llm/client.go`.

### Agent Loop Interface

[embedmd]:# (../internal/llm/client.go go /^\/\/ AgentClient extends/ /^}/)
```go
// AgentClient extends Client with an agentic tool-use loop.
type AgentClient interface {
	AgentLoop(ctx context.Context, req AgentRequest, handler ToolHandler) (AgentResponse, error)
}
```

`AgentClient` adds a multi-turn tool-use loop on top of `Client`. Supporting types: `ToolDef`
(tool schema), `ToolCall` (model invocation), `ToolHandler` (caller-provided callback), `AgentRequest`,
`AgentResponse`. Sentinel errors: `ErrAgentLoopNotSupported` (client lacks implementation),
`ErrMaxTurnsExceeded` (safety bound hit).

### Anthropic Backend (`internal/llm/anthropic.go`)

Uses `github.com/anthropics/anthropic-sdk-go`. Implements both `Client` and `AgentClient`.

Spec content in the system prompt gets `CacheControl{Type: "ephemeral"}` — cached across attractor
iterations for ~90% input cost reduction (cache TTL: 5 minutes, resets on hit). Failure feedback in
user messages changes each iteration and is not cached.

`AgentLoop` converts `ToolDef` schemas to Anthropic tool parameters, runs a message loop (default 10
turns max), dispatches tool calls to the provided handler, and accumulates usage across turns.

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
	TestCommand string // optional test command from "Test-Command: ..." in description
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

`weight` defaults to 1.0 in aggregate scoring when not set. `tier` is auto-inferred by `inferTier`
in `loader.go` when not set (zero): >6 judged steps or ≥3 steps with captures → 3 (complex); >3
judged steps or ≥1 step with captures → 2 (moderate); else → 1 (simple). The `octog lint` checker
warns if `tier` is explicitly set outside the 1–3 range.

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

#### Retry / Poll

Steps support an optional `retry` block for eventual-consistency scenarios (polling until a
background job completes, waiting for a resource to appear). Retries fire only when
`executor.Execute` returns a non-nil error (transport failures, timeouts). HTTP 4xx/5xx and non-zero
exit codes are NOT errors — they produce a `StepOutput` for the judge.

```yaml
steps:
  - description: "Wait for item to be processed"
    request:
      method: GET
      path: /items/{item_id}
    retry:
      attempts: 10     # max attempts (default: 3)
      interval: "2s"   # delay between retries (default: "1s")
      timeout: "30s"   # overall timeout cap (optional)
    expect: "Status 200 with status 'processed'"
```

`StepResult.Duration` reflects total wall time including retries and sleeps. Captures are applied
only from the final successful attempt.

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
	Run(ctx context.Context, tag string) (container.RunResult, container.StopFunc, error)
	RunMultiPort(ctx context.Context, tag string, extraPorts []string) (container.RunResult, container.StopFunc, error)
	RunTest(ctx context.Context, containerID, command string) (container.ExecResult, error)
	WaitHealthy(ctx context.Context, url string, timeout time.Duration) error
	WaitPort(ctx context.Context, addr string, timeout time.Duration) error
	StartSession(ctx context.Context, tag string) (session *container.Session, stop container.StopFunc, err error)
}
```

[embedmd]:# (../internal/attractor/attractor.go go /^\/\/ ValidateFn runs/ /err error\)/)
```go
// ValidateFn runs holdout scenarios against a running container and returns results.
// The attractor never imports internal/scenario — the CLI provides this closure.
// restart may be called to stop the current container and start a fresh one between scenarios.
// restart is nil for gRPC and exec-only paths that do not support container restart.
// maxTier, when > 0, restricts validation to scenarios with Tier <= maxTier (stratified mode).
// maxTier == 0 means run all scenarios (non-stratified or component validators).
type ValidateFn func(ctx context.Context, url string, restart RestartFunc, maxTier int) (satisfaction float64, failures []string, cost float64, err error)
```

[embedmd]:# (../internal/attractor/attractor.go go /^\/\/ RunOptions configures/ /^}/)
```go
// RunOptions configures the attractor loop.
type RunOptions struct {
	Model               string
	FrugalModel         string                // optional cheaper model to start with; escalates to Model after consecutive failures
	JudgeModel          string                // model used for the wonder phase diagnosis; falls back to Model when empty
	Language            string                // language hint: "go", "python", "node", "rust", or "" (auto)
	BudgetUSD           float64               // 0 = unlimited
	Threshold           float64               // default 95
	MaxIterations       int                   // default 10
	StallLimit          int                   // default 3
	WorkspaceDir        string                // default "./workspace"
	HealthTimeout       time.Duration         // default 30s
	Progress            ProgressFunc          // optional per-iteration callback
	PatchMode           bool                  // if true, iteration 2+ sends prev best files + failures
	BlockOnRegression   bool                  // if true, convergence is blocked when per-scenario regressions are detected
	ContextBudget       int                   // max estimated tokens for spec in system prompt; 0 = unlimited
	Capabilities        ScenarioCapabilities  // detected from loaded scenarios
	Genes               string                // extracted pattern guide to inject into system prompt (empty = no genes)
	GeneLanguage        string                // source language of the gene exemplar (for cross-language note)
	TestCommand         string                // optional shell command run inside HTTP container after health check; non-zero exit = test_fail
	MaxTokens           int                   // max output tokens for generation; 0 = auto-scale per model
	Agentic             bool                  // if true, use AgentLoop for code generation (tool-use mode)
	AgentMaxTurns       int                   // max turns per AgentLoop call; 0 = default (50 when Agentic is true)
	Stratified          bool                  // if true, validate by ascending difficulty tier (1→2→3), converging each before advancing
	GeneComponents      []gene.Component      // structured component decomposition from gene extraction
	ComponentValidators map[string]ValidateFn // per-component validators; "" key = integration validator
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
0. Preflight: run spec clarity check (unless --skip-preflight); abort if below threshold
1. If GeneComponents + ComponentValidators are set (and not Agentic): attempt composed convergence
   a. topoSort components (Kahn's BFS); error on cycles/unknown deps
   b. For each component in topo order: run mini-loop (max 5 iters, stall limit 2)
      - buildComponentPrompt: spec + COMPONENT CONTRACT + DEPENDENCY INTERFACES (declared only)
      - validate with per-component ValidateFn; carry dep files as base file set
      - stall/build fail → fall back to monolithic; budget exceeded → fall back
   c. mergeComponentFiles: merge all component file sets (later topo order wins conflicts)
   d. Build composed dir; validate with integration ValidateFn ("" key)
   e. satisfaction >= threshold → return "converged"; else fall back to monolithic
   f. Costs from composed attempt carried forward into monolithic budget accounting
2. If FrugalModel is set, init escalation state (start at frugal tier)
3. If ContextBudget > 0 and spec exceeds budget, summarize spec (pyramid summaries)
4. For iter = 1 to MaxIterations:
   a. Check budget
   b. Check escalation: upgrade frugal→primary after 2 non-improving, downgrade after 5 improving
   c. Select spec content (full or summarized with failure-relevant sections expanded)
   d. Build messages:
      - Normal mode: spec only (iter 1) or spec + last 3 failure summaries (iter N>1)
      - Patch mode (iter 2+ with bestFiles): triage bestFiles via LLM (keep only files relevant to
        current failures + entry points; skip triage when ≤5 files or no failures), then send
        filtered files + failures; omitted-file count appended as a note in the prompt
   e. Apply minimalism suffix when last score > 80% (discourage over-engineering)
   f. Inject oscillation steering when A→B→A→B hash pattern detected
   g. Generate code:
      - If stalling → wonder/reflect two-phase process (see below)
      - Otherwise → normal single-call generation
   h. Parse LLM output into files (=== FILE: path === ... === END FILE ===)
   i. In patch mode, MergeFiles(newFiles, bestFiles) to carry forward unchanged files
   j. Record SHA-256 hash of file set (for oscillation detection)
   k. Write files to workspace/{run_id}/iter_{n}/
   l. docker build → docker run → wait for health check
   m. Run test command if configured (non-zero exit → test_fail)
   n. call validate(ctx, url, maxTier) → satisfaction, failures
      - maxTier = activeTier in stratified mode; 0 = all scenarios (non-stratified)
   o. Detect per-scenario regressions (score dropped below threshold since last validation)
   p. If satisfaction >= threshold and no regressions blocking → converged for this tier
      - Stratified: if activeTier < 3, advance activeTier++, reset per-tier state, continue loop
      - Stratified: if activeTier == 3 (or non-stratified), return "converged"
   q. Determine feedback fidelity: compact (iter 1-2) → standard (3-4) → full (5+)
   r. Track improvement/stalls; patch mode: disable after 2 consecutive regressions
   s. If stall count >= stall limit → return "stalled"
5. Return "max_iterations"
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
	OutcomeTestFail   IterationOutcome = "test_fail"
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
	InputTokens      int
	OutputTokens     int
	Failures         []string
	Model            string // model used for generation in this iteration
	Turns            int    // number of agent turns used (0 for non-agentic iterations)
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

### File Triage (`internal/attractor/triage.go`)

`triageFiles(ctx, allFiles, failures, model)` narrows the file set sent to the LLM in patch mode.
It asks the judge-tier model to return a JSON array of paths most relevant to the current failures,
then merges in any entry-point files present in `allFiles` (Dockerfile, main.go, go.mod, etc.),
regardless of LLM output.

Skip conditions (returns `allFiles` unchanged, zero cost):

- `len(allFiles) <= 5`
- `len(failures) == 0`

On error or empty result, falls back to the full file set. Cost is accumulated into `totalCost`.
The count of omitted files is passed to `buildPatchMessages` as `omittedCount`; when > 0, a
`(N other files not relevant to current failures, not shown)` note is appended to the prompt.

Entry points always included: `Dockerfile`, `main.go`, `main.py`, `main.rs`, `app.py`, `app.js`,
`server.js`, `index.js`, `index.ts`, `go.mod`, `cargo.toml`, `package.json`, `requirements.txt`.

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

`RunMultiPort()` starts a container exposing port 8080 (optional) plus additional ports (e.g.
`50051/tcp` for gRPC). `StartSession()` creates a long-lived container running `sleep infinity` for
exec-based scenarios; `Session.Exec()` runs commands inside it via `docker exec`.

## Stall Recovery (Wonder/Reflect)

When scenarios stall across consecutive iterations, the attractor switches from normal generation to
a two-phase wonder/reflect process:

1. **Wonder phase** — Uses the judge model (or primary model as fallback) at high temperature (0.8)
   to diagnose why attempts are failing. Receives score history, recent failures, and best generated
   code. Oscillation detection data is included when the last 4 code hashes form an A→B→A→B pattern.
   Output: a structured `Diagnosis` with hypotheses, root causes, and a suggested approach.

2. **Reflect phase** — Uses the primary model at low temperature (0.4) to generate a new
   implementation based on the diagnosis. When the score is already above 80%, a minimalism
   instruction discourages over-engineering.

If either phase fails (non-context-cancellation errors), the loop falls back to normal generation
gracefully.

### Oscillation Detection

`hashFiles()` computes a deterministic SHA-256 of each iteration's file set. `detectOscillation()`
returns true when the last 4 hashes form an A→B→A→B pattern (or the degenerate A==A==A==A case),
signaling the LLM is alternating between two solutions without progress. When detected, steering text
is injected into the generation prompt and included in the wonder phase input.

## Model Escalation

When `--frugal-model` is set, the attractor starts at the frugal (cheaper) tier and manages
automatic escalation:

- **Escalate** (frugal → primary): after 2 consecutive non-improving iterations
- **Downgrade** (primary → frugal): after 5 consecutive improving iterations

The `escalationState` struct tracks consecutive failures and improvements. `recordOutcome()` is
called after each iteration with whether satisfaction strictly improved. When escalation is disabled
(no `--frugal-model`), the primary model is used throughout.

## Feedback Fidelity

Validation feedback sent to the LLM is scaled by iteration to balance cost and signal:

| Level      | Iterations | Max bytes | Detail                                                          |
| ---------- | ---------- | --------- | --------------------------------------------------------------- |
| `compact`  | 1–2        | 4 KB      | Scenario summary lines only                                     |
| `standard` | 3–4        | 12 KB     | Failing step detail, observed truncated, structured diagnostics |
| `full`     | 5+         | 24 KB     | All step detail, full observed output, structured diagnostics   |

Stalls escalate fidelity by one level (e.g. compact → standard after 2 consecutive stalls).

## Per-Scenario Regression Tracking

After each validated iteration, `detectRegressions()` compares per-scenario scores against the
previous snapshot. A regression is recorded when a scenario was at or above the convergence threshold
in the prior iteration but drops below it in the current one. Regressions are injected as feedback
entries for the next iteration.

When `--block-on-regression` is set, the attractor will not converge even if the aggregate
satisfaction exceeds the threshold, as long as any scenario has regressed.

## Preflight

`octog preflight` (and the integrated check in `octog run`) assesses spec and scenario quality
before the attractor loop begins.

### Spec Check (`preflight.Check`)

Three dimensions, each scored 0.0–1.0:

- **Goal clarity** (weight 0.4) — Does the spec define WHAT the software should do?
- **Constraint clarity** (weight 0.3) — Does it define HOW (interfaces, constraints)?
- **Success clarity** (weight 0.3) — Does it define verification criteria?

Aggregate = weighted sum. Below threshold → error with clarifying questions. Verbose mode shows
per-dimension strengths and gaps.

### Scenario Check (`preflight.CheckScenarios`)

Four dimensions, each scored 0.0–1.0 (unweighted average):

- **Coverage** — Do scenarios exercise all spec behaviors?
- **Feasibility** — Are scenarios executable as written?
- **Isolation** — Does each scenario test one coherent behavior?
- **Chains** — Are multi-step variable captures and substitutions correct?

Returns per-scenario issues with dimension and actionable detail.

## Conversational Spec-Drafting (`interview.Interviewer`)

`interview.New(client llm.Client, in io.Reader, out io.Writer, model string) *Interviewer` — creates
an interviewer that conducts a multi-turn conversation to produce a spec.

```go
func (i *Interviewer) Run(ctx context.Context, initialPrompt string) (string, float64, error)
func (i *Interviewer) RunWithSeed(ctx context.Context, seedSpec string) (string, float64, error)
```

`Run` starts from scratch with an opening question (`initialPrompt`). `RunWithSeed` starts from an
existing spec (`seedSpec`), injecting it into the first user message and switching to
`seedSystemPrompt` — a variant that instructs the LLM to identify gaps and ambiguities rather than
elicit requirements from scratch. Both methods share the same internal conversation loop (`run`),
which accumulates messages, reads user replies from `in`, and terminates on `"done"` or EOF.

Two system prompts live in `prompt.go`:

- `systemPrompt` — standard spec-drafting persona
- `seedSystemPrompt` — review/improvement persona; composed by prepending review instructions to
  `systemPrompt` so the NLSpec dimension list stays in sync automatically

`--seed` and `--prompt` are mutually exclusive (`errSeedAndPromptConflict`).

## Spec-Completeness Scoring (`interview.Scorer`)

`interview.NewScorer(client llm.Client, model string) *Scorer` — scores a spec against five
weighted dimensions using an LLM judge. Distinct from `preflight.Check` (which gates the attractor
loop); this scorer is used by the `octog interview` command to show the user how complete their
drafted spec is.

```go
type Scorer struct { client llm.Client; model string }

func (s *Scorer) Score(ctx context.Context, specContent string) (CompletenessResult, error)

type CompletenessResult struct {
    Dimensions []DimensionScore
    Overall    int     // weighted average, rounded
    CostUSD    float64
}

type DimensionScore struct {
    Name   string
    Score  int     // 0–100, clamped
    Weight float64
    Gaps   []string
}
```

Five dimensions (weights sum to 1.0), iterated in canonical order (stable output):

| Dimension                  | Weight | Description                                                         |
| -------------------------- | ------ | ------------------------------------------------------------------- |
| `behavioral_completeness`  | 0.25   | Happy paths, error paths, edge cases, state transitions             |
| `interface_precision`      | 0.25   | API endpoints, CLI flags, config, data formats, field types         |
| `defaults_and_boundaries`  | 0.20   | Default values, limits, ranges, boundary conditions                 |
| `acceptance_criteria`      | 0.20   | Testable criteria a QA engineer can automate without assumptions    |
| `economy`                  | 0.10   | Free of redundancy, contradictions, and misleading noise            |

Scoring uses the two-implementer test (would two engineers produce interoperable implementations?)
and the recreatability test (could a new engineer recreate equivalent behavior from the spec alone?).

LLM is called with `Temperature=0`, `MaxTokens=4096`, and `CacheControl: ephemeral` on the system
prompt. Response must be a JSON object with exactly five named dimensions; scores are clamped to
[0, 100]. Sentinel errors: `errEmptySpec`, `errMalformedResponse`, `errIncompleteDimensions`.

## Holdout Scenario Generation (`interview.ScenarioGenerator`)

`interview.NewScenarioGenerator(client llm.Client, model string, log *slog.Logger) *ScenarioGenerator`
— generates holdout validation scenario YAML files from a spec using an LLM. Invoked by
`octog interview --scenarios`.

```go
func (g *ScenarioGenerator) Generate(ctx context.Context, specContent string) (map[string]string, float64, error)
```

Returns a map of filename → YAML content, LLM cost in USD, and any error.

Pipeline:

1. LLM is called with `scenarioSystemPrompt` (system) + spec content (user message), `Temperature=0.7`,
   `MaxTokens=8192`, `CacheControl: ephemeral`.
2. Response is parsed with `attractor.ParseFiles` to extract `=== FILE: name === … === END FILE ===`
   blocks. If no blocks are found, returns `errParseScenarioOutput`.
3. Each filename is sanitized with `filepath.Base` (strips any directory prefix LLM may emit).
   Filenames containing `..` or equal to `.` are dropped with a warning log.
4. Each YAML block is validated with `scenario.Load`; invalid blocks are dropped with a warning log.
5. Filename collisions (after base-name normalization) keep the first occurrence.
6. If no valid scenarios survive, returns `errNoValidScenarios`.

Sentinel errors: `errEmptySpec` (shared with `Scorer`), `errParseScenarioOutput`,
`errNoValidScenarios`.

`cmd/octog.writeGeneratedScenarios` wraps `ScenarioGenerator.Generate`, writes files to a
`scenarios/` directory adjacent to the spec output path, and returns the LLM cost for
aggregated cost reporting.

## Observability

OpenTelemetry tracing is enabled via `--otel-endpoint` (or `OTEL_EXPORTER_OTLP_ENDPOINT` env var).
`observability.InitTracer()` creates an OTLP/HTTP exporter with a batch span processor. Empty
endpoint returns a noop provider (zero overhead).

Instrumented wrappers (`TracingLLMClient`, `TracingContainerManager`) create spans around LLM calls,
container operations, and the attractor loop. `TracingLLMClient` conditionally delegates `AgentLoop`
via type assertion to `AgentClient` (returns `ErrAgentLoopNotSupported` if the inner client does not
implement it). The service name is `octog`.

## CLI Interface

```text
octog interview  [--output spec.md] [--model ...] [--provider anthropic|openai] [--prompt "What would you like to build?"] [--seed <existing-spec.md>] [--scenarios]
octog run        --spec <path> --scenarios <dir> [--model claude-sonnet-4-6] [--frugal-model ...] [--judge-model claude-haiku-4-5] [--budget 5.00] [--threshold 95] [--genes genes.json] [--language go] [--patch] [--block-on-regression] [--context-budget 0] [--stratified] [--agentic] [--agent-max-turns N] [--otel-endpoint ...] [--skip-preflight] [--preflight-threshold 0.8] [-v 0|1|2] [--provider anthropic|openai]
octog validate   --scenarios <dir> --target <url> [--grpc-target host:port] [--judge-model claude-haiku-4-5] [--threshold 0] [--format text|json] [-v 0|1|2] [--provider anthropic|openai]
octog preflight  [--judge-model claude-haiku-4-5] [--threshold 0.8] [--verbose] [--scenarios <dir>] <spec-path>
octog status     [--format text|json]
octog lint       [--spec <path>] [--scenarios <dir>]
octog extract    --source-dir <path> [--output genes.json] [--model ...] [--provider anthropic|openai]
octog models     [--provider anthropic|openai]
octog configure
```

Subcommands: `interview`, `run`, `validate`, `preflight`, `status`, `lint`, `extract`, `models`, `configure`.

Provider is auto-detected from which API key is set. Use `--provider` to disambiguate when both are
present. Config file (`~/.octopusgarden/config`) supports `ANTHROPIC_API_KEY` and `OPENAI_API_KEY`;
env vars take precedence. `OPENAI_BASE_URL` overrides the OpenAI endpoint for Ollama etc.

## Gene Transfusion

Gene transfusion bootstraps code generation by extracting patterns from an exemplar codebase. The
pipeline: `gene.Scan` selects high-signal files (markers, README, Dockerfile, entrypoint, handlers,
models) within a ~20K token budget → `gene.Analyze` sends them to an LLM to produce a structured
guide → the guide is stored as a `Gene` JSON file.

When the LLM response includes `**COMPONENT: <name>**` headers, `parseComponents` extracts each
component's `Interface`, `Patterns`, and `DependsOn` fields into `Component` structs stored on the
`Gene`. `Validate` (via `validateComponents` + `detectComponentCycles`) enforces non-empty unique
names, all declared dependencies exist, and the dependency graph is acyclic (DFS gray/black coloring).

[embedmd]:# (../internal/gene/gene.go go /^\/\/ Component represents/ /^}/)
```go
// Component represents a named architectural component within a Gene,
// with its interface description, patterns, and declared dependencies.
type Component struct {
	Name      string   `json:"name"`
	Interface string   `json:"interface"`
	Patterns  string   `json:"patterns"`
	DependsOn []string `json:"depends_on,omitempty"`
}
```

[embedmd]:# (../internal/gene/gene.go go /^\/\/ Gene represents/ /^}/)
```go
// Gene represents an extracted coding guide for a specific language,
// derived from a source repository's patterns and conventions.
type Gene struct {
	Version     int         `json:"version"`
	Source      string      `json:"source"`
	Language    string      `json:"language"`
	ExtractedAt time.Time   `json:"extracted_at"`
	Guide       string      `json:"guide"`
	TokenCount  int         `json:"token_count"`
	Components  []Component `json:"components,omitempty"`
}
```

At runtime, `--genes genes.json` loads the guide into `RunOptions.Genes`. The attractor's
`buildSystemPrompt` injects it between the spec and instructions with a "PROVEN PATTERNS" header.
The spec always takes precedence on conflicts.

Cross-language synthesis: when `Gene.Language` differs from the target `--language`, a
`CROSS-LANGUAGE NOTE` is appended instructing the LLM to preserve structural patterns while using
idiomatic target-language constructs.

See [Gene Transfusion](gene-transfusion.md) for the full user guide.

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
