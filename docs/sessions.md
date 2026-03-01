# Implementation Sessions

## Phase 1: Walking Skeleton (Sessions 1-6)

Goal: end-to-end loop working on a trivial example. Spec in, working software out.

### Session 1 — Go Module + CLI + LLM Client

**Scope:** Initialize the Go module, build the CLI skeleton, and implement the Anthropic LLM client
with prompt caching support.

**Types/Interfaces:**

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
    Model        string
    CacheControl *CacheControl
}

type CacheControl struct {
    Type string // "ephemeral"
}

type GenerateResponse struct {
    Content      string
    InputTokens  int
    OutputTokens int
    CacheHit     bool
    CostUSD      float64
}

type JudgeRequest struct {
    SystemPrompt string
    UserPrompt   string
    Model        string
}

type JudgeResponse struct {
    Score    int
    Reasoning string
    Failures []string
    CostUSD  float64
}
```

**Tests:** Mock HTTP server returning canned Anthropic API responses. Verify token counting, cost
estimation, and cache control header propagation.

**Done when:**

- `go build ./...` succeeds
- `go test ./internal/llm/...` passes
- `./octopusgarden run --help` prints usage
- `./octopusgarden validate --help` prints usage
- `./octopusgarden status --help` prints usage

______________________________________________________________________

### Session 2 — Spec Parser + Scenario Loader

**Scope:** Parse markdown specs into structured Go types. Load YAML scenario files with variable
capture support.

**Types/Interfaces:**

```go
// internal/spec/types.go
type Spec struct {
    Title       string
    Description string
    Sections    []Section
    RawContent  string
}

type Section struct {
    Heading string
    Level   int
    Content string
}

// internal/scenario/types.go
type Scenario struct {
    ID                   string   `yaml:"id"`
    Description          string   `yaml:"description"`
    Type                 string   `yaml:"type"`
    Weight               float64  `yaml:"weight"`
    Setup                []Step   `yaml:"setup"`
    Steps                []Step   `yaml:"steps"`
    SatisfactionCriteria string   `yaml:"satisfaction_criteria"`
}

type Step struct {
    Description string    `yaml:"description"`
    Request     Request   `yaml:"request"`
    Expect      string    `yaml:"expect"`
    Capture     []Capture `yaml:"capture"`
}

type Request struct {
    Method  string            `yaml:"method"`
    Path    string            `yaml:"path"`
    Headers map[string]string `yaml:"headers"`
    Body    any               `yaml:"body"`
}

type Capture struct {
    Name     string `yaml:"name"`
    JSONPath string `yaml:"jsonpath"`
}
```

**Tests:** Embedded test fixtures — a sample spec.md and sample scenario YAML. Verify parsing
round-trips correctly, weight defaults to 1.0, capture fields parse.

**Done when:**

- `go test ./internal/spec/...` passes
- `go test ./internal/scenario/...` passes

______________________________________________________________________

### Session 3 — Scenario Runner + LLM Judge

**Scope:** Execute scenario steps as HTTP requests against a running server. Capture variables from
responses and substitute into subsequent steps. Score each step with the LLM judge.

**Functions:**

```go
// internal/scenario/runner.go
type Runner struct {
    HTTPClient *http.Client
    BaseURL    string
}

func (r *Runner) Run(ctx context.Context, scenario Scenario) (ScenarioResult, error)

// internal/scenario/judge.go
type Judge struct {
    LLM llm.Client
    Model string
}

func (j *Judge) Score(ctx context.Context, step Step, response HTTPResponse) (StepScore, error)

type StepScore struct {
    Score    int
    Reasoning string
    Failures []string
}
```

**Tests:** httptest server with canned responses. Mock LLM client returning canned judge scores.
Verify variable capture from JSON responses and substitution into paths/bodies.

**Done when:**

- `go test ./internal/scenario/...` passes
- Variable capture + substitution works end-to-end in tests

______________________________________________________________________

### Session 4 — Docker Container Management

**Scope:** Build Docker images from generated code directories and run containers with random port
allocation, health checking, and cleanup.

**Functions:**

```go
// internal/container/docker.go
type Manager struct {
    Client *client.Client
}

func (m *Manager) Build(ctx context.Context, dir string, tag string) error
func (m *Manager) Run(ctx context.Context, tag string) (url string, stopFn func(), err error)
func (m *Manager) WaitHealthy(ctx context.Context, url string, timeout time.Duration) error
```

**Implementation details:**

- Port 0 allocation (Docker assigns random available host port)
- Health check: poll GET / every 1s, 30s timeout, non-5xx = healthy
- `stopFn` stops and removes container
- Build errors surfaced with full Docker build log

**Tests:** Unit tests with mocked Docker client. Integration tests behind `//go:build integration`
tag that actually build/run a trivial container.

**Done when:**

- `go build ./internal/container/...` succeeds
- Unit tests pass: `go test ./internal/container/...`

______________________________________________________________________

### Session 5 — Attractor Loop

**Scope:** Core convergence loop. Wires together LLM generation, file parsing, container management,
and receives validation feedback.

**Functions:**

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
    Threshold     float64
    MaxIterations int
    StallLimit    int
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

// internal/attractor/fileparse.go
func ParseFiles(output string) (map[string]string, error)
```

**Key behaviors:**

- File block parser extracts `=== FILE: path === ... === END FILE ===`
- Stall detection: 3 iterations without improvement -> stop
- Budget enforcement: cumulative cost > budget -> save checkpoint, stop
- Checkpoint: save best-scoring codebase to workspace/{run_id}/best/

**Tests:** Mock all dependencies. Table-driven tests verifying:

1. Convergence: satisfaction meets threshold -> returns "converged"
1. Stall: 3 iterations flat -> returns "stalled"
1. Budget: cost exceeds limit -> returns "budget_exceeded"
1. File parser handles well-formed and malformed LLM output

**Done when:**

- `go test ./internal/attractor/...` passes

______________________________________________________________________

### Session 6 — SQLite Store + Wire CLI + Hello-API Example

**Scope:** SQLite persistence, wire the `run` subcommand end-to-end, create the hello-api example
spec and scenarios.

**Functions:**

```go
// internal/store/db.go
type Store struct {
    DB *sql.DB
}

func NewStore(path string) (*Store, error)
func (s *Store) RecordRun(ctx context.Context, run Run) error
func (s *Store) RecordIteration(ctx context.Context, iter Iteration) error
func (s *Store) UpdateRun(ctx context.Context, run Run) error
func (s *Store) ListRuns(ctx context.Context) ([]Run, error)
func (s *Store) GetRun(ctx context.Context, id string) (Run, error)
```

**Example artifacts:**

- `specs/examples/hello-api/spec.md` — Items REST API (CRUD, list, pagination, validation)
- `scenarios/examples/hello-api/crud.yaml` — Create, read, update, delete with variable capture
- `scenarios/examples/hello-api/list.yaml` — List items, verify array response
- `scenarios/examples/hello-api/pagination.yaml` — `?limit=` and `?offset=` params
- `scenarios/examples/hello-api/validation.yaml` — Missing required fields, invalid input
- `scenarios/examples/hello-api/not-found.yaml` — GET/PUT/DELETE nonexistent item

**Tests:** Store CRUD tests with in-memory SQLite.

**Done when:**

- `go build ./cmd/octopusgarden` produces a binary
- `go test ./...` — all tests pass
- Manual smoke test:
  `./octopusgarden run --spec specs/examples/hello-api/spec.md --scenarios scenarios/examples/hello-api/ --model claude-sonnet-4-20250514 --threshold 90`

______________________________________________________________________

## Phase 2: Hardened Validation (Sessions 7-10)

Goal: trustworthy validation, convergence detection, and a real demo.

### Session 7 — Holdout Isolation Enforcement

**Scope:** Ensure the attractor can never access scenario content. This is architectural, not
convention.

**Implementation:**

- Attractor receives spec content as `string`, never file paths
- The `cmd/octopusgarden` orchestrator loads scenarios separately and passes only to the validator
- `internal/attractor` does not import `internal/scenario`

**Tests:**

- Verify `internal/attractor` has no import of `internal/scenario` (go vet or build constraint)
- Test that attractor context string never contains scenario text (inject known scenario strings,
  assert they don't appear in LLM prompts)

**Done when:**

- Isolation test passes
- Import graph verified

______________________________________________________________________

### Session 8 — Convergence Detection + Checkpoints

**Scope:** Improve convergence detection with plateau/regression/improvement classification. Save
and restore checkpoints.

**Functions:**

```go
// internal/attractor/convergence.go
type Trend string
const (
    TrendImproving  Trend = "improving"
    TrendPlateau    Trend = "plateau"
    TrendRegressing Trend = "regressing"
    TrendConverged  Trend = "converged"
)

func DetectTrend(history []float64, threshold float64, stallLimit int) Trend
func SaveCheckpoint(dir string, files map[string]string, meta CheckpointMeta) error
func LoadCheckpoint(dir string) (map[string]string, CheckpointMeta, error)
```

**Tests:** Table-driven tests for all trend cases. Checkpoint save/load round-trip.

**Done when:**

- `go test ./internal/attractor/...` passes with trend detection + checkpoint tests

______________________________________________________________________

### Session 9 — Todo App Demo

**Scope:** More complex demo that exercises the full system.

**Artifacts:**

- `specs/examples/todo-app/spec.md` — Users, auth (API key), todos per user, filtering
  (completed/pending), pagination
- 12+ scenario YAML files covering: registration, login, CRUD, ownership isolation, filtering,
  pagination, validation, 404, auth failures

**Done when:**

- Factory runs against todo-app spec
- Converges with 90%+ satisfaction (or clear failures that inform spec improvements)

______________________________________________________________________

### Session 10 — Standalone Validate Command

**Scope:** `octopusgarden validate --scenarios <dir> --target <url>` runs scenarios against any
running service.

**Done when:**

- Can validate the hello-api example against a manually started server
- Outputs per-scenario satisfaction scores and aggregate

______________________________________________________________________

## Future Work (GitHub Issues, Not Sessions)

- Twins (digital twin universe for external API testing)
- Browser-based scenario support (Playwright)
- Gene transfusion (extract patterns from existing codebases)
- Web dashboard with real-time updates
- Multi-model routing (gen/judge/summary on different models)
- Pyramid summaries for large specs
- Incremental generation (patch instead of regenerate)
- OpenAI/Ollama backends
