# Contributing to OctopusGarden

## Development Setup

```bash
git clone https://github.com/foundatron/octopusgarden.git
cd octopusgarden
```

Prerequisites:

- Go 1.24+
- Docker (for integration tests and running the factory)
- [golangci-lint](https://golangci-lint.run/) v2
- [pre-commit](https://pre-commit.com/)

Install git hooks:

```bash
pre-commit install --hook-type pre-commit --hook-type pre-push --hook-type commit-msg
```

## Build & Test

```bash
make build   # compile octog binary
make test    # run unit tests
make lint    # golangci-lint
make fmt     # gci + gofumpt
```

Integration tests require a running Docker daemon:

```bash
go test -tags=integration ./internal/container/...
```

## Commit Messages

Commits must follow [Conventional Commits](https://www.conventionalcommits.org/) — enforced by a
commit-msg hook.

Types: `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `chore`, `build`, `ci`, `revert`

```text
feat(attractor): add stall detection
fix(scenario): handle empty response body
docs: update CLI reference in README
```

## Coding Standards

### Errors

- Return errors, never panic
- Wrap with context: `fmt.Errorf("operation: %w", err)`
- Sentinel errors as package-level vars: `var errFoo = errors.New("...")`

### Style

- Logging: `log/slog` structured logging (never `log.Println`)
- Context: `context.Context` through all operations
- No global state — dependencies via struct fields or function parameters
- No type-name stuttering: `scenario.Result` not `scenario.ScenarioResult`

### Tests

- Same-package `_test.go` files (e.g., `package llm`, not `package llm_test`)
- Table-driven tests with `testing.T` only (no testify)

### Dependencies

Minimize — stdlib first. See [CLAUDE.md](CLAUDE.md) for the list of allowed exceptions.

## Project Structure

```text
cmd/octog/            CLI entrypoint and subcommands
internal/
  spec/               Parse markdown specs
  scenario/           Load, run, and judge YAML scenarios
  attractor/          Convergence loop and file parsing
  container/          Docker build and run
  llm/                LLM client interface (Anthropic + OpenAI backends)
  store/              SQLite run history
examples/             Example specs and scenarios (holdout sets)
```

## Design Invariants

These are non-negotiable — PRs that violate them will be rejected:

1. **Holdout isolation is sacred.** The attractor must never access scenario files during code
   generation. It receives spec content as a string — never scenario content or file paths.

1. **Satisfaction is probabilistic.** The validator produces a 0-100 score per scenario via
   LLM-as-judge. Never reduce this to boolean pass/fail.

1. **Code is opaque weights.** Generated code is a build artifact. Only externally observable
   behavior matters.

1. **Specs are the source of truth.** Generated code wrong → fix code. Spec ambiguous → fix spec.
