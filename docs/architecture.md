# Architecture

## System Overview

```
Spec (markdown) ──→ Attractor Loop ──→ Generated Code ──→ Docker Build
                        │                                      │
                        │         (holdout wall)               ▼
                        │                              Running Container
                        │                                      │
                        ◄──── Failure Feedback ◄──── Validator + LLM Judge
                                                              │
                                                    Satisfaction Score (0-100)
```

The attractor loop generates code from a spec, builds it in Docker, validates it against holdout scenarios using an LLM judge, and iterates on failures until satisfaction converges above threshold.

## Repository Structure

```
octopusgarden/
├── CLAUDE.md
├── README.md
├── go.mod
├── go.sum
├── cmd/
│   └── octopusgarden/
│       └── main.go             # CLI entrypoint, subcommand routing
├── internal/
│   ├── spec/
│   │   ├── parser.go           # Parse markdown specs into structured form
│   │   └── types.go            # Spec data structures
│   ├── scenario/
│   │   ├── loader.go           # Load scenarios from YAML files
│   │   ├── runner.go           # Execute scenario steps against running software
│   │   ├── judge.go            # LLM-as-judge satisfaction scoring
│   │   └── types.go            # Scenario data structures
│   ├── attractor/
│   │   ├── loop.go             # Core attractor convergence loop
│   │   ├── context.go          # Context window management
│   │   ├── convergence.go      # Stall detection, checkpoint management
│   │   └── fileparse.go        # Parse LLM output into files
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
│       └── migrations.go       # Schema migrations
├── specs/
│   └── examples/
│       ├── hello-api/
│       │   └── spec.md
│       └── todo-app/
│           └── spec.md
├── scenarios/
│   └── examples/
│       ├── hello-api/
│       │   ├── crud.yaml
│       │   ├── list.yaml
│       │   ├── pagination.yaml
│       │   ├── validation.yaml
│       │   └── not-found.yaml
│       └── todo-app/
│           └── *.yaml
└── docs/
    ├── architecture.md         # This file
    └── sessions.md             # Implementation roadmap
```

## Package Dependency DAG

```
cmd/octopusgarden
    ├── internal/attractor   (loop, convergence, context)
    │       ├── internal/llm
    │       ├── internal/spec
    │       ├── internal/container
    │       └── internal/store
    ├── internal/scenario    (loader, runner, judge)
    │       └── internal/llm
    ├── internal/llm         (client interface, anthropic, openai, models, prompts)
    ├── internal/container   (docker build/run)
    ├── internal/spec        (parser, types)
    └── internal/store       (sqlite)
```

Key constraint: `internal/attractor` never imports `internal/scenario`. The attractor receives spec content and failure feedback as strings. The validator (scenario runner + judge) is invoked by `cmd/octopusgarden`, not by the attractor.

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

Uses `github.com/sashabaranov/go-openai`. For GPT models and OpenAI-compatible endpoints (Ollama at `localhost:11434`).

## Prompt Caching Strategy

The spec content is included in every attractor iteration as a system prompt. Without caching, this is the dominant cost.

Anthropic's prompt caching:
1. First request: full cost (cache write)
2. Subsequent requests with same prefix: ~10% of input cost (cache read)
3. Cache TTL: 5 minutes (resets on each hit)

Implementation:
- Set `CacheControl: &CacheControl{Type: "ephemeral"}` on the system message block containing spec content
- The failure feedback in user messages changes each iteration (not cached — that's fine, it's small)
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

## Scenario Data Structures

```go
// internal/scenario/types.go

type Scenario struct {
    ID                    string  `yaml:"id"`
    Description           string  `yaml:"description"`
    Type                  string  `yaml:"type"`  // "api" only for MVP
    Weight                float64 `yaml:"weight"` // default 1.0
    Setup                 []Step  `yaml:"setup"`
    Steps                 []Step  `yaml:"steps"`
    SatisfactionCriteria  string  `yaml:"satisfaction_criteria"`
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
2. After each step, evaluates `capture` rules against the response body
3. Stores captured values in a variable map
4. Before executing subsequent steps, substitutes `{variable_name}` in paths, headers, and bodies

## Scenario Runner

```go
// internal/scenario/runner.go

type Runner struct {
    HTTPClient *http.Client
    BaseURL    string
}

func (r *Runner) Run(ctx context.Context, scenario Scenario) (ScenarioResult, error)

type ScenarioResult struct {
    ScenarioID string
    Steps      []StepResult
}

type StepResult struct {
    Description string
    Request     Request
    Response    HTTPResponse
    Duration    time.Duration
}

type HTTPResponse struct {
    Status  int
    Headers map[string]string
    Body    string
}
```

## LLM Judge

The judge scores each step independently, then aggregates per scenario.

### Judge Prompt

```
System: You are evaluating whether software correctly satisfies a user scenario.
Score from 0-100 based on how well observed behavior matches expected behavior.
Return JSON: {"score": N, "reasoning": "...", "failures": ["..."]}

User:
Scenario: {scenario.description}
Step: {step.description}
Expected: {step.expect}
Actual HTTP Response:
  Status: {response.status}
  Headers: {response.headers}
  Body: {response.body}

Does this response satisfy the expectation?
```

### Scoring Guide

- 100: Perfect match to expected behavior
- 80-99: Works correctly with minor deviations (different wording, extra fields)
- 50-79: Partially correct (some aspects work, others don't)
- 1-49: Mostly broken but shows some correct behavior
- 0: Complete failure or error

### Aggregation

Per-scenario score = average of step scores. Overall satisfaction = weighted average of scenario scores (using scenario `weight` field).

Use a cheap model for judging (Claude Haiku, GPT-4o-mini).

## Attractor Loop

```go
// internal/attractor/loop.go

type Attractor struct {
    LLM          llm.Client
    ContainerMgr *container.Manager
    Store        *store.Store
}

type RunOptions struct {
    Model         string
    BudgetUSD     float64
    Threshold     float64 // 0-100, default 95
    MaxIterations int     // default 10
    StallLimit    int     // default 3
}

type RunResult struct {
    RunID        string
    Iterations   int
    Satisfaction float64
    CostUSD      float64
    OutputDir    string
    Status       string // "converged", "stalled", "budget_exceeded"
}

func (a *Attractor) Run(ctx context.Context, spec string, opts RunOptions) (*RunResult, error)
```

### Loop Pseudocode

```
1. Build context: spec content (cached) + failure history (last 3)
2. Call LLM: "Generate a complete implementation matching this spec"
   - Iteration 1: spec only
   - Iteration N>1: spec + "Previous attempt failed: {feedback}"
3. Parse LLM output into files (=== FILE: path === ... === END FILE ===)
4. Write files to workspace/{run_id}/iter_{n}/
5. docker build in that directory
   - Build failure → satisfaction = 0, record error, goto 1
6. docker run with port 0 (random available port)
7. Wait for health check (GET / returns non-5xx, 30s timeout)
   - Health check failure → satisfaction = 0, stop container, goto 1
8. Return (container_url, stop_func) to caller
   Caller runs validator, feeds back satisfaction + failures
9. If satisfaction >= threshold → save checkpoint, return "converged"
10. If satisfaction improved → save checkpoint, update best
11. If stalled for N iterations → return "stalled" with failure report
12. If cost > budget → save checkpoint, return "budget_exceeded"
13. Collect failure details, add to context, goto 1
```

### Context Window Management

Priority order for context (drop from bottom first):
1. Spec content (always included, cached)
2. Latest failure feedback
3. Previous failure feedback (up to 3 total)

If context exceeds model limit, drop oldest failures first.

### File Block Parser

The LLM outputs files in this format:

```
=== FILE: path/to/file.ext ===
file contents here
=== END FILE ===
```

`internal/attractor/fileparse.go` extracts these into a map of `path -> content`.

## Docker Container Strategy

```go
// internal/container/docker.go

type Manager struct {
    Client *client.Client // Docker API client
}

func (m *Manager) Build(ctx context.Context, dir string, tag string) error
func (m *Manager) Run(ctx context.Context, tag string) (url string, stopFn func(), err error)
func (m *Manager) WaitHealthy(ctx context.Context, url string, timeout time.Duration) error
```

### Port Allocation

Use port 0 — Docker assigns a random available host port. Read back the assigned port from container inspect.

### Health Check

After `docker run`, poll `GET http://localhost:{port}/` every 1s for up to 30s. Any non-5xx response means healthy.

### Cleanup

`stopFn` returned by `Run` stops and removes the container. Always defer cleanup.

### Image Tagging

On convergence, tag the successful image as `octopusgarden/{project}:latest`.

## CLI Interface

```
octopusgarden run --spec <path> --scenarios <dir> [--model claude-sonnet-4-20250514] [--budget 5.00] [--threshold 95]
octopusgarden validate --scenarios <dir> --target http://localhost:8080
octopusgarden status  # show recent runs, scores, costs
```

MVP does not include `twin`, `dashboard`, or `transfuse` subcommands.

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

```
You are building software to match this specification exactly.

SPECIFICATION:
{spec_content}

{if iteration > 1}
PREVIOUS ATTEMPT FEEDBACK:
The previous version failed these validations:
{failure_details}

Fix these issues while maintaining all previously passing behavior.
{endif}

Generate a complete, working implementation. Include:
- All source code files
- A Dockerfile that builds and runs the application
- Any configuration files needed

Output each file in this format:
=== FILE: path/to/file.ext ===
file contents here
=== END FILE ===
```

### Satisfaction Judge Prompt

```
You are a QA evaluator. Score how well this software behavior matches the expected behavior.

Scenario: {scenario_description}
Step: {step_description}

Expected behavior: {expected}

Actual observed behavior:
{observed}

Respond with JSON only:
{
  "score": <0-100>,
  "reasoning": "<brief explanation>",
  "failures": ["<specific failure 1>", "<specific failure 2>"]
}

Scoring guide:
- 100: Perfect match to expected behavior
- 80-99: Works correctly with minor deviations (different wording, extra fields)
- 50-79: Partially correct (some aspects work, others don't)
- 1-49: Mostly broken but shows some correct behavior
- 0: Complete failure or error
```
