# CLAUDE.md — OctopusGarden

OctopusGarden is an open-source, self-hostable software dark factory. Humans write specifications and scenarios. OctopusGarden orchestrates AI coding agents that autonomously generate, test, and converge working software — with zero human code review. The core loop: specs -> agent generates code -> validator runs holdout scenarios -> LLM judge scores satisfaction -> failures fed back -> agent iterates -> converges.

## Module & Stack

- **Module:** `github.com/foundatron/octopusgarden`
- **Language:** Go 1.22+
- **Build:** `go build ./cmd/octopusgarden`
- **Binary:** `octopusgarden` with subcommands: `run`, `validate`, `status`

## Dependencies

Minimize. Use stdlib where possible. Key exceptions:

- `github.com/anthropics/anthropic-sdk-go` — Anthropic API (Claude models, prompt caching, streaming)
- `github.com/sashabaranov/go-openai` — OpenAI and Ollama only
- `gopkg.in/yaml.v3` — scenario YAML parsing
- `github.com/mattn/go-sqlite3` — run history and metrics
- `github.com/docker/docker/client` — container orchestration

## Design Invariants

1. **Holdout isolation is sacred.** The attractor loop MUST NOT have access to scenario files during code generation. Scenarios are only used by the validator after code is generated. This prevents reward hacking. Enforce this architecturally — the attractor receives spec content as a string, never scenario content or file paths.

2. **Satisfaction is probabilistic, not boolean.** The validator produces a 0-100 score per scenario via LLM-as-judge, not pass/fail. Aggregate satisfaction determines convergence. Default threshold: 95%.

3. **Code is opaque weights.** Generated code is a build artifact. Internal structure doesn't matter — only externally observable behavior matters. Never optimize for "readable" generated code.

4. **Specs are the source of truth.** If generated code doesn't match the spec, the code is wrong. If the spec is ambiguous, improve the spec.

5. **Cost-aware by default.** Every LLM call is logged with token counts and estimated cost. Budget caps are configurable. Cheap models for judging, expensive models for generation.

## Prompt Caching

Spec content repeats every attractor iteration. Use Anthropic's prompt caching (`cache_control: {type: "ephemeral"}`) on the system prompt containing the spec. This gives ~90% cost reduction on cache reads after the first write. The `anthropic-sdk-go` client supports this natively via `CacheControl` on message blocks.

## Coding Standards

- Use Go idioms: error returns (not panics), interfaces for testability, table-driven tests
- Package names: short, lowercase, no underscores
- Error handling: wrap with `fmt.Errorf("operation: %w", err)` for context
- Logging: `log/slog` structured logging (not `log.Println`)
- Context: pass `context.Context` through all operations for cancellation/timeouts
- Tests: `_test.go` files alongside source, use `testing.T` not testify
- No global state — pass dependencies via struct fields or function parameters

## Architecture Reference

See [docs/architecture.md](docs/architecture.md) for:
- Repository structure and package dependency DAG
- LLM client interface design
- Attractor loop pseudocode and context window management
- Scenario format, runner, and LLM judge prompts
- Docker container strategy
- CLI interface specification
- SQLite schema
- Prompt templates

## Session Plan

See [docs/sessions.md](docs/sessions.md) for the implementation roadmap: 10 sessions across 2 phases, with exact types, test criteria, and done conditions.
