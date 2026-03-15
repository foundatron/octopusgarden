# CLAUDE.md — OctopusGarden

Autonomous software dark factory: specs → attractor loop generates code → validator runs holdout
scenarios → LLM judge scores satisfaction → failures feed back → converges. Zero human code review.

## Commands

```bash
make build   # compile octog binary
make test    # run unit tests
make lint    # golangci-lint (enforced on pre-push)
make fmt     # gci + gofumpt
```

Integration tests use `//go:build integration` tag:
`go test -tags=integration ./internal/container/...`

Commits must follow [Conventional Commits](https://www.conventionalcommits.org/) — enforced by
commit-msg hook. Types: `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `chore`,
`build`, `ci`, `revert`. Example: `feat(attractor): add stall detection`

## Module & Packages

`github.com/foundatron/octopusgarden` — Go 1.24+ — binary `octog` — subcommands: `run`, `validate`,
`status`, `extract`

Internal packages: `spec` (parse markdown specs), `scenario` (load/run/judge YAML scenarios),
`attractor` (convergence loop, file parsing), `container` (Docker build/run), `llm` (client
interface, Anthropic + OpenAI backends), `gene` (scan exemplar codebases, LLM pattern extraction)

## Dependencies

Minimize — stdlib first. Allowed exceptions:

- `github.com/anthropics/anthropic-sdk-go` — Anthropic API
- `github.com/openai/openai-go/v3` — OpenAI and Ollama only
- `gopkg.in/yaml.v3` — scenario YAML
- `modernc.org/sqlite` — run history (pure-Go, no CGO)
- `github.com/docker/docker/client` — container orchestration
- `github.com/chromedp/chromedp` — browser automation (pure Go, Chrome DevTools Protocol)
- `google.golang.org/grpc` + `google.golang.org/protobuf` — gRPC client for scenario steps
- `github.com/jhump/protoreflect/v2` — dynamic gRPC via server reflection (no compiled protos)
- `go.opentelemetry.io/otel` + related packages — OpenTelemetry tracing (spans for LLM calls,
  container ops, attractor loop)
- `github.com/coder/websocket` — WebSocket client for scenario ws steps (context-native, pure Go)

## Design Invariants

1. **Holdout isolation is sacred.** The attractor MUST NOT access scenario files during code
   generation. Attractor receives spec content as a string — never scenario content or file paths.

1. **Satisfaction is probabilistic, not boolean.** Validator produces a 0-100 score per scenario via
   LLM-as-judge. Aggregate satisfaction determines convergence. Default threshold: 95%.

1. **Code is opaque weights.** Generated code is a build artifact — only externally observable
   behavior matters. Never optimize for "readable" generated code.

1. **Specs are the source of truth.** Generated code wrong → fix code. Spec ambiguous → fix spec.

1. **Cost-aware by default.** Every LLM call logs token counts and estimated cost. Cheap models for
   judging, expensive models for generation.

## Coding Standards

- Errors: return (never panic); wrap with `fmt.Errorf("operation: %w", err)`; sentinel errors as
  `var errFoo = errors.New("...")` at package level (err113 enforced)
- Logging: `log/slog` structured (never `log.Println`)
- Context: `context.Context` through all operations
- Tests: same-package `_test.go`, table-driven, `testing.T` only (no testify)
- No global state — dependencies via struct fields or function parameters
- No type-name stuttering: `scenario.Result` not `scenario.ScenarioResult`
- Prompt caching: use `cache_control: {type: "ephemeral"}` on spec content in system prompts
  (repeated per attractor iteration — ~90% cost reduction on cache reads)
- Linting: `make lint`; config in `.golangci.yaml`; gochecknoglobals disabled (pricing tables OK)

## Interfaces

Cross-package and multi-implementation interfaces:

| Interface        | Package   | Implementations                                                       |
| ---------------- | --------- | --------------------------------------------------------------------- |
| Client           | llm       | AnthropicClient, OpenAIClient, observability.TracingLLMClient         |
| AgentClient      | llm       | AnthropicClient, observability.TracingLLMClient (conditional)         |
| StepExecutor     | scenario  | HTTPExecutor, ExecExecutor, BrowserExecutor, GRPCExecutor, WSExecutor |
| ContainerManager | attractor | container.Manager, observability.TracingContainerManager              |
| containerSession | scenario  | `*container.Session`                                                  |
| dockerAPI        | container | dockerclient.Client                                                   |
| modelLister      | cmd/octog | llm.AnthropicClient, llm.OpenAIClient                                 |

## Configuration

API keys go in the platform-native config file (preferred) or environment variables. Config file
uses `KEY=VALUE` format, one per line. Env vars take precedence over config values.

Config file location (in priority order):

1. `$OCTOG_CONFIG_DIR/config` — env var override (also used for test isolation)
1. `~/Library/Application Support/octopusgarden/config` — macOS
1. `$XDG_CONFIG_HOME/octopusgarden/config` (or `~/.config/octopusgarden/config`) — Linux
1. `~/.octopusgarden/config` — legacy fallback (deprecated)

```ini
ANTHROPIC_API_KEY=sk-ant-...
OPENAI_API_KEY=sk-...
```

Provider is auto-detected from which key is present. Use `--provider openai|anthropic` to
disambiguate when both are set. `OPENAI_BASE_URL` overrides the OpenAI endpoint (for Ollama etc.).

## Docs

- [docs/architecture.md](docs/architecture.md) — package structure, interfaces, loop pseudocode,
  scenario format, Docker strategy, SQLite schema, prompt templates
