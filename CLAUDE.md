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

`github.com/foundatron/octopusgarden` — Go 1.22+ — binary `octog` — subcommands: `run`, `validate`,
`status`

Internal packages: `spec` (parse markdown specs), `scenario` (load/run/judge YAML scenarios),
`attractor` (convergence loop, file parsing), `container` (Docker build/run), `llm` (client
interface, Anthropic + OpenAI backends)

## Dependencies

Minimize — stdlib first. Allowed exceptions:

- `github.com/anthropics/anthropic-sdk-go` — Anthropic API
- `github.com/sashabaranov/go-openai` — OpenAI and Ollama only
- `gopkg.in/yaml.v3` — scenario YAML
- `github.com/mattn/go-sqlite3` — run history
- `github.com/docker/docker/client` — container orchestration

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

## Docs

- [docs/architecture.md](docs/architecture.md) — package structure, interfaces, loop pseudocode,
  scenario format, Docker strategy, SQLite schema, prompt templates
- [docs/sessions.md](docs/sessions.md) — 10-session roadmap with types, test criteria, done
  conditions
