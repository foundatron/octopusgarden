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
├── CLAUDE.md
├── README.md
├── go.mod
├── go.sum
├── cmd/
│   └── octog/
│       └── main.go             # CLI entrypoint, subcommand routing
├── internal/
│   ├── spec/
│   │   ├── parser.go           # Parse markdown specs into structured form
│   │   ├── types.go            # Spec data structures
│   │   └── summary.go          # Pyramid summaries for large specs
│   ├── scenario/
│   │   ├── loader.go           # Load scenarios from YAML files
│   │   ├── runner.go           # Execute scenario steps against running software
│   │   ├── judge.go            # LLM-as-judge satisfaction scoring
│   │   ├── types.go            # Scenario data structures
│   │   ├── result.go           # Result, StepScore, ScoredStep, ScoredScenario, AggregateResult
│   │   └── jsonpath.go         # Dot-notation JSONPath evaluator ($.field.sub)
│   ├── attractor/
│   │   ├── attractor.go        # Core attractor convergence loop
│   │   ├── convergence.go      # Trend detection, checkpoint management
│   │   └── fileparse.go        # Parse LLM output into files, merge for patch mode
│   ├── container/
│   │   └── docker.go           # Build and run Docker containers
│   ├── llm/
│   │   ├── client.go           # Model-agnostic LLM client interface
│   │   ├── anthropic.go        # Anthropic API backend (anthropic-sdk-go)
│   │   ├── openai.go           # OpenAI/Ollama backend (go-openai)
│   │   ├── models.go           # Model registry, cost tracking
│   │   └── prompt.go           # Prompt templates
│   └── store/
│       ├── db.go               # SQLite: run history, satisfaction scores, costs
│       └── types.go            # Run, Iteration structs
├── specs/
│   └── examples/
│       ├── hello-api/
│       │   └── spec.md
│       ├── todo-app/
│       │   └── spec.md
│       └── expense-tracker/
│           └── spec.md
├── scenarios/
│   └── examples/
│       ├── hello-api/
│       │   ├── crud.yaml
│       │   ├── list.yaml
│       │   ├── pagination.yaml
│       │   ├── validation.yaml
│       │   └── not-found.yaml
│       ├── todo-app/
│       │   ├── crud.yaml
│       │   ├── list.yaml
│       │   ├── pagination.yaml
│       │   ├── validation.yaml
│       │   ├── not-found.yaml
│       │   ├── register.yaml
│       │   ├── register-duplicate.yaml
│       │   ├── register-validation.yaml
│       │   ├── auth-required.yaml
│       │   ├── auth-invalid.yaml
│       │   ├── ownership.yaml
│       │   ├── mark-completed.yaml
│       │   └── filter-completed.yaml
│       └── expense-tracker/
│           ├── expense-crud.yaml
│           ├── expense-list.yaml
│           ├── expense-filter.yaml
│           ├── expense-summary.yaml
│           ├── expense-validation.yaml
│           ├── expense-no-category.yaml
│           ├── category-crud.yaml
│           ├── category-duplicate.yaml
│           ├── category-validation.yaml
│           ├── register.yaml
│           ├── register-duplicate.yaml
│           ├── register-validation.yaml
│           ├── auth-required.yaml
│           ├── auth-invalid.yaml
│           ├── ownership.yaml
│           └── not-found.yaml
└── docs/
    ├── architecture.md         # This file
    └── sessions.md             # Implementation roadmap
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
`cmd/octog`, not by the attractor. Store interaction is also owned by `cmd/octog` — the attractor
returns a `RunResult` and the CLI records it post-hoc.

## LLM Client Interface

```go
// internal/llm/client.go

type Client interface {
    Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error)
    Judge(ctx context.Context, req JudgeRequest) (JudgeResponse, error)
}

type GenerateRequest struct {
    SystemPrompt string
    Messages     []Message
    MaxTokens    int
    Model        string        // e.g. "claude-sonnet-4-20250514", "gpt-4o"
    CacheControl *CacheControl // nil = no caching
}

type CacheControl struct {
    Type string // "ephemeral"
}

type GenerateResponse struct {
    Content     string
    InputTokens  int
    OutputTokens int
    CacheHit     bool  // true if prompt cache was used
    CostUSD      float64
}

type JudgeRequest struct {
    SystemPrompt string
    UserPrompt   string
    Model        string
}

type JudgeResponse struct {
    Score    int      // 0-100
    Reasoning string
    Failures []string
    CostUSD  float64
}

type Message struct {
    Role    string // "user" or "assistant"
    Content string
}
```

### Anthropic Backend (`internal/llm/anthropic.go`)

Uses `github.com/anthropics/anthropic-sdk-go`. Key features:

- Prompt caching via `CacheControl` on system prompt blocks
- Native token counting from API response
- Cost estimation from model pricing table

### OpenAI Backend (`internal/llm/openai.go`)

Uses `github.com/sashabaranov/go-openai`. For GPT models and OpenAI-compatible endpoints (Ollama at
`localhost:11434`).

## Prompt Caching Strategy

The spec content is included in every attractor iteration as a system prompt. Without caching, this
is the dominant cost.

Anthropic's prompt caching:

1. First request: full cost (cache write)
1. Subsequent requests with same prefix: ~10% of input cost (cache read)
1. Cache TTL: 5 minutes (resets on each hit)

Implementation:

- Set `CacheControl: &CacheControl{Type: "ephemeral"}` on the system message block containing spec
  content
- The failure feedback in user messages changes each iteration (not cached — that's fine, it's
  small)
- Expected savings: 80-90% on input tokens for iterations 2+

## Spec Data Structures

```go
// internal/spec/types.go

type Spec struct {
    Title       string
    Description string
    Sections    []Section
    RawContent  string // full markdown, used for LLM prompt
}

type Section struct {
    Heading string
    Level   int    // 1, 2, 3...
    Content string // text content under this heading
}
```

### Pyramid Summaries (`internal/spec/summary.go`)

For large specs that exceed a context budget, the spec package produces multi-level summaries to fit
within a token limit while preserving detail for failure-relevant sections.

```go
// internal/spec/summary.go

type SummarizedSpec struct {
    Spec     *Spec
    Sections []SectionSummary // per-section 2-3 sentence summaries
    Outline  string           // headings + one-line descriptions
    Abstract string           // single paragraph
}

type SectionSummary struct {
    Heading string
    Summary string
}

type SummarizeResult struct {
    Summary *SummarizedSpec
    CostUSD float64
}

func EstimateTokens(text string) int              // len(text)/4 heuristic
func Summarize(ctx context.Context, s *Spec, client llm.Client, model string) (SummarizeResult, error)
func SelectContent(ss *SummarizedSpec, budget int, failures []string) string
```

`SelectContent` picks the richest representation that fits within the budget:

1. Full spec (if it fits)
1. Section summaries with failure-relevant sections expanded to full content
1. Outline + failure-relevant sections
1. Abstract + failure-relevant sections
1. Abstract alone
1. Truncated raw content (last resort)

## Scenario Data Structures

```go
// internal/scenario/types.go

type Scenario struct {
    ID                    string   `yaml:"id"`
    Description           string   `yaml:"description"`
    Type                  string   `yaml:"type"`   // "api" only for MVP
    Weight                *float64 `yaml:"weight"` // nil means not set, defaults to 1.0
    Setup                 []Step   `yaml:"setup"`
    Steps                 []Step   `yaml:"steps"`
    SatisfactionCriteria  string   `yaml:"satisfaction_criteria"`
}

type Step struct {
    Description string   `yaml:"description"`
    Request     Request  `yaml:"request"`
    Expect      string   `yaml:"expect"` // natural language, judged by LLM
    Capture     []Capture `yaml:"capture"`
}

type Request struct {
    Method  string            `yaml:"method"`
    Path    string            `yaml:"path"`
    Headers map[string]string `yaml:"headers"`
    Body    any               `yaml:"body"`
}

type Capture struct {
    Name     string `yaml:"name"`     // variable name
    JSONPath string `yaml:"jsonpath"` // path into response body
}
```

### Result Types (`internal/scenario/result.go`)

```go
type HTTPResponse struct {
    Status  int
    Headers map[string]string
    Body    string
}

type StepResult struct {
    Description string
    Request     Request
    Response    HTTPResponse
    Duration    time.Duration
    Err         error // non-nil only for network/transport failures
}

type Result struct {
    ScenarioID string
    Steps      []StepResult // judged steps only, not setup
}

type StepScore struct {
    Score     int
    Reasoning string
    Failures  []string
    CostUSD   float64
}

type ScoredStep struct {
    StepResult StepResult
    StepScore  StepScore
}

type ScoredScenario struct {
    ScenarioID string
    Weight     float64
    Steps      []ScoredStep
    Score      float64 // average of step scores
}

type AggregateResult struct {
    Scenarios    []ScoredScenario
    Satisfaction float64 // weighted average, 0-100
    TotalCostUSD float64
    Failures     []string // deduplicated, sorted
}
```

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
  - description: "Update the item"
    request:
      method: PUT
      path: /items/{item_id}
      body: { "name": "updated item" }
    expect: "Returns the updated item with name 'updated item'"
  - description: "Delete the item"
    request:
      method: DELETE
      path: /items/{item_id}
    expect: "Returns 200 or 204 indicating successful deletion"
  - description: "Verify deletion"
    request:
      method: GET
      path: /items/{item_id}
    expect: "Returns 404 Not Found"
satisfaction_criteria: |
  All CRUD operations work correctly with appropriate status codes.
```

### Variable Capture and Substitution

The scenario runner:

1. Executes each step sequentially
1. After each step, evaluates `capture` rules against the response body
1. Stores captured values in a variable map
1. Before executing subsequent steps, substitutes `{variable_name}` in paths, headers, and bodies

JSONPath evaluation (`internal/scenario/jsonpath.go`) supports dot-notation only (`$.field.sub`) —
no library dependency.

## Scenario Runner

```go
// internal/scenario/runner.go

type Runner struct {
    HTTPClient *http.Client
    BaseURL    string
    Logger     *slog.Logger
}

func NewRunner(baseURL string, httpClient *http.Client, logger *slog.Logger) *Runner

func (r *Runner) Run(ctx context.Context, scenario Scenario) (Result, error)
```

Setup steps are fatal — if any setup step fails, the runner returns an error immediately. Judged
steps are non-fatal — transport errors are recorded in `StepResult.Err` and the step is scored 0 by
the judge without making an LLM call.

## LLM Judge

The judge scores each step independently, then aggregates per scenario.

```go
// internal/scenario/judge.go

type Judge struct {
    LLM    llm.Client
    Model  string
    Logger *slog.Logger
}

func NewJudge(client llm.Client, model string, logger *slog.Logger) *Judge

func (j *Judge) Score(ctx context.Context, scenario Scenario, step Step, response HTTPResponse) (StepScore, error)
func (j *Judge) ScoreScenario(ctx context.Context, scenario Scenario, result Result) (ScoredScenario, error)

// Package-level function.
func Aggregate(scenarios []ScoredScenario) AggregateResult
```

### Judge Prompt

The judge uses a split system/user prompt (`internal/llm/prompt.go`):

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

The combined `SatisfactionJudgePrompt` constant is deprecated.

### Scoring Guide

- 100: Perfect match to expected behavior
- 80-99: Works correctly with minor deviations (different wording, extra fields)
- 50-79: Partially correct (some aspects work, others don't)
- 1-49: Mostly broken but shows some correct behavior
- 0: Complete failure or error

### Aggregation

Per-scenario score = average of step scores. Overall satisfaction = weighted average of scenario
scores (using scenario `weight` field, defaulting to 1.0 when nil).

Use a cheap model for judging (Claude Haiku, GPT-4o-mini).

## Attractor Loop

```go
// internal/attractor/attractor.go

type Attractor struct {
    llm          llm.Client
    containerMgr ContainerManager
    logger       *slog.Logger
}

func New(client llm.Client, containerMgr ContainerManager, logger *slog.Logger) *Attractor

// ValidateFn runs holdout scenarios against a running container and returns results.
// The attractor never imports internal/scenario — the CLI provides this closure.
type ValidateFn func(ctx context.Context, url string) (satisfaction float64, failures []string, cost float64, err error)

// ContainerManager is the interface to Docker container operations.
// *container.Manager satisfies this automatically.
type ContainerManager interface {
    Build(ctx context.Context, dir, tag string) error
    Run(ctx context.Context, tag string) (url string, stop container.StopFunc, err error)
    WaitHealthy(ctx context.Context, url string, timeout time.Duration) error
}

type RunOptions struct {
    Model         string
    BudgetUSD     float64       // 0 = unlimited
    Threshold     float64       // default 95
    MaxIterations int           // default 10
    StallLimit    int           // default 3
    WorkspaceDir  string        // default "./workspace"
    HealthTimeout time.Duration // default 30s
    Progress      ProgressFunc  // optional per-iteration callback
    PatchMode     bool          // if true, iteration 2+ sends prev best files + failures
    ContextBudget int           // max estimated tokens for spec in system prompt; 0 = unlimited
}

type RunResult struct {
    RunID        string
    Iterations   int
    Satisfaction float64
    CostUSD      float64
    OutputDir    string
    Status       string // "converged", "stalled", "budget_exceeded", "max_iterations"
}

func (a *Attractor) Run(ctx context.Context, spec string, opts RunOptions, validate ValidateFn) (*RunResult, error)
```

### Progress Reporting

```go
type ProgressFunc func(IterationProgress)

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

type IterationOutcome string // "validated", "build_fail", "run_fail", "health_fail", "parse_fail"
```

The `ProgressFunc` callback is called synchronously after each iteration completes. The CLI uses
this to print real-time progress to stderr.

### Loop Pseudocode

```text
1. If ContextBudget > 0 and spec exceeds budget, summarize spec (pyramid summaries)
2. For iter = 1 to MaxIterations:
   a. Check budget
   b. Select spec content (full or summarized with failure-relevant sections expanded)
   c. Build messages:
      - Normal mode: spec only (iter 1) or spec + last 3 failure summaries (iter N>1)
      - Patch mode (iter 2+ with bestFiles): previous best files + failures, ask for only changed files
   d. Call LLM: generate code
   e. Parse LLM output into files (=== FILE: path === ... === END FILE ===)
      - Parse failure → satisfaction = 0, increment stall count, continue
   f. In patch mode, MergeFiles(newFiles, bestFiles) to carry forward unchanged files
   g. Write files to workspace/{run_id}/iter_{n}/
   h. docker build in that directory
      - Build failure → satisfaction = 0, increment stall count, continue
   i. docker run with port 0 (random available port)
      - Run failure → satisfaction = 0, increment stall count, continue
   j. Wait for health check (GET /, non-5xx, configurable timeout)
      - Health check failure → satisfaction = 0, stop container, continue
   k. Call validate(ctx, url) — caller runs scenarios + judge
   l. If satisfaction >= threshold → write best, return "converged"
   m. If satisfaction improved → write best, reset stall count
   n. If satisfaction did not improve → increment stall count
      - Patch mode: track regressions; after 2 consecutive, disable patch mode
   o. If stall count >= stall limit → return "stalled"
   p. If cost > budget → return "budget_exceeded"
   q. Call Progress callback with IterationProgress
3. Return "max_iterations"
```

### Context Window Management

Priority order for context (drop from bottom first):

1. Spec content (always included, cached)
1. Latest failure feedback
1. Previous failure feedback (up to 3 total)

When `ContextBudget` is set and the spec exceeds it, the spec is summarized at multiple levels (see
Pyramid Summaries above). Each iteration selects the richest representation that fits, with
failure-relevant sections expanded to full content.

### File Block Parser

The LLM outputs files in this format:

```text
=== FILE: path/to/file.ext ===
file contents here
=== END FILE ===
```

`internal/attractor/fileparse.go` extracts these into a map of `path -> content`.

In patch mode, `=== UNCHANGED: path ===` markers are recognized and skipped — carry-forward is
handled by `MergeFiles(newFiles, prevFiles)`, which copies all previous best files and overlays the
new output on top.

## Convergence (`internal/attractor/convergence.go`)

```go
type Trend string // "improving", "plateau", "regressing", "converged"

func DetectTrend(history []float64, threshold float64, stallLimit int) Trend

type CheckpointMeta struct {
    Iteration    int       `json:"iteration"`
    Satisfaction float64   `json:"satisfaction"`
    Trend        Trend     `json:"trend"`
    Timestamp    time.Time `json:"timestamp"`
}

func SaveCheckpoint(dir string, files map[string]string, meta CheckpointMeta) error
func LoadCheckpoint(dir string) (map[string]string, CheckpointMeta, error)
```

`DetectTrend` classifies the score trajectory using a sliding window of size `stallLimit`:

- `converged`: last score >= threshold
- `improving`: last score > baseline
- `regressing`: last score < peak within window
- `plateau`: all scores in window identical, or no movement

Checkpoints save generated files alongside a `checkpoint.json` metadata file.

## Docker Container Strategy

```go
// internal/container/docker.go

type Manager struct {
    docker dockerAPI     // Docker client (interface for testability)
    http   *http.Client
    logger *slog.Logger
}

type StopFunc func()

func NewManager(logger *slog.Logger) (*Manager, error)
func (m *Manager) Build(ctx context.Context, dir, tag string) error
func (m *Manager) Run(ctx context.Context, tag string) (url string, stop StopFunc, err error)
func (m *Manager) WaitHealthy(ctx context.Context, url string, timeout time.Duration) error
```

### Port Allocation

Use port 0 — Docker assigns a random available host port. Read back the assigned port from container
inspect.

### Health Check

After `docker run`, poll `GET http://localhost:{port}/` every 1s for up to the configured timeout
(default 30s). Any non-5xx response means healthy.

### Cleanup

`StopFunc` returned by `Run` stops and removes the container. Always defer cleanup. Cleanup uses
`context.Background()` to succeed even after caller context cancellation.

## CLI Interface

```text
octog run --spec <path> --scenarios <dir> [--model claude-sonnet-4-20250514] [--budget 5.00] [--threshold 95] [--patch] [--context-budget 0]
octog validate --scenarios <dir> --target http://localhost:8080 [--threshold 0]
octog status  # show recent runs, scores, costs
```

MVP does not include `twin`, `dashboard`, or `transfuse` subcommands.

### Config File

On startup, the CLI loads `~/.octopusgarden/config` (KEY=VALUE format, one per line). Currently
supports `ANTHROPIC_API_KEY` and `OPENAI_API_KEY`. Environment variables take precedence over config
values. The config file permissions are checked — a warning is logged if the file is world-readable.

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
    satisfaction  REAL,         -- final score
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

## Prompt Templates

Store as Go string constants in `internal/llm/prompt.go`.

### Code Generation Prompt

The attractor builds the system prompt inline via `buildSystemPrompt()` in
`internal/attractor/attractor.go`. The `CodeGenerationPrompt` constant in `prompt.go` exists as a
reference template but is not directly used by the attractor — the attractor's version includes
additional instructions about dependency management and port configuration.

### Satisfaction Judge Prompt

The judge uses split prompts for the `Judge` interface:

- `SatisfactionJudgeSystem` — system prompt with scoring guide and JSON response format
- `SatisfactionJudgeUser` — user prompt template with `{scenario_description}`,
  `{step_description}`, `{expected}`, and `{observed}` placeholders

The combined `SatisfactionJudgePrompt` constant is deprecated — retained for backward compatibility
but not used by the judge implementation.
