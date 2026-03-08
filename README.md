# OctopusGarden

An open-source software dark factory. Write specs and scenarios — OctopusGarden builds the software.

> Each arm of an octopus has its own neural cluster and can operate semi-autonomously.
> OctopusGarden's agents work the same way — independent arms coordinating toward a shared goal.

## What Is This?

OctopusGarden is an autonomous software development system. You describe what you want (specs) and
how to verify it works (scenarios). OctopusGarden orchestrates AI coding agents that generate, test,
and iterate on the code until it converges on a working implementation — without any human code
review.

The key insight: scenarios are a **holdout set**. The coding agent never sees them during
generation. An LLM judge scores satisfaction probabilistically (0-100), not with boolean pass/fail.
This prevents reward hacking and produces genuinely correct software.

## Prior Art

OctopusGarden builds on ideas pioneered by others:

- **[StrongDM's Software Factory](https://factory.strongdm.ai/)** — Production system validating
  this exact pattern (holdout scenarios, LLM-as-judge, convergence loops). Demonstrated that
  AI-generated code can pass rigorous QA without human review.
- **[Dan Shapiro's Five Levels](https://www.danshapiro.com/blog/2026/01/the-five-levels-from-spicy-autocomplete-to-the-software-factory/)**
  — Framework for AI coding maturity, from autocomplete to fully autonomous factories. OctopusGarden
  targets Level 5.
- **[Simon Willison's writeup](https://simonwillison.net/2026/Feb/7/software-factory/)** — "How
  StrongDM's AI team build serious software without even looking at the code" — deep dive into the
  software factory pattern and scenario-based validation.
- **[Ouroboros](https://github.com/Q00/ouroboros)** — Specification-first AI development plugin
  using Socratic questioning and ontological analysis to expose hidden assumptions before code
  generation. Inspired OctopusGarden's preflight and wonder/reflect meta-cognitive patterns.

## How It Works

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

1. You write a **spec** in markdown describing the software
1. You write **scenarios** in YAML describing how to verify it works
1. **Preflight** assesses spec clarity and scenario quality (skip with `--skip-preflight`)
1. The **attractor loop** calls an LLM to generate code from the spec
1. The code is built and run in a **Docker container**
1. The **validator** runs scenarios against the running container
1. An **LLM judge** scores satisfaction per scenario step
1. Failures are fed back to the attractor, which iterates — on stalls, **wonder/reflect** diagnoses
   root causes and generates surgical fixes
1. Loop continues until satisfaction exceeds your threshold (default 95%)

## Quick Start

```bash
# Clone and build
git clone https://github.com/foundatron/octopusgarden.git
cd octopusgarden
make build
```

Configure your API key:

```bash
# Interactive setup (recommended)
octog configure

# Or set an env var
export ANTHROPIC_API_KEY=sk-...

# Or write the config file directly
mkdir -p ~/.octopusgarden && echo "ANTHROPIC_API_KEY=sk-..." > ~/.octopusgarden/config
```

Run the factory on the included examples:

```bash
# Items REST API (uses default model: claude-sonnet-4-6)
octog run \
  --spec examples/hello-api/spec.md \
  --scenarios examples/hello-api/scenarios/ \
  --threshold 90

# Todo app with auth
octog run \
  --spec examples/todo-app/spec.md \
  --scenarios examples/todo-app/scenarios/ \
  --model claude-sonnet-4-6 \
  --judge-model claude-haiku-4-5

# Expense tracker
octog run \
  --spec examples/expense-tracker/spec.md \
  --scenarios examples/expense-tracker/scenarios/ \
  --model claude-sonnet-4-6 \
  --judge-model claude-haiku-4-5
```

Validate a running service against scenarios independently:

```bash
octog validate \
  --scenarios examples/hello-api/scenarios/ \
  --target http://localhost:8080
```

Bootstrap generation with patterns from an existing project:

```bash
# Extract patterns from an exemplar codebase
octog extract --source-dir /path/to/exemplar --output genes.json

# Use extracted patterns to guide code generation
octog run \
  --spec examples/hello-api/spec.md \
  --scenarios examples/hello-api/scenarios/ \
  --genes genes.json
```

List available models and check past runs:

```bash
octog models
octog status
```

Requires: Go 1.24+, Docker, an Anthropic API key.

## CLI Reference

```text
octog <command> [flags]

Commands:
  run        Run the attractor loop to generate software from a spec
  validate   Validate a running service against scenarios
  preflight  Assess spec clarity before running the attractor loop
  status     Show recent runs, scores, and costs
  lint       Check spec and scenario files for errors
  extract    Extract coding patterns from a source directory into a gene file
  models     List available models
  configure  Interactively configure API keys
```

Run `octog models` to list available models.

### `run`

| Flag                    | Default             | Description                                                            |
| ----------------------- | ------------------- | ---------------------------------------------------------------------- |
| `--spec`                | *(required)*        | Path to the spec markdown file                                         |
| `--scenarios`           | *(required)*        | Path to the scenarios directory                                        |
| `--model`               | `claude-sonnet-4-6` | LLM model for code generation                                          |
| `--frugal-model`        | *(none)*            | Cheaper model; escalates to `--model` after 2 non-improving iterations |
| `--judge-model`         | `claude-haiku-4-5`  | LLM model for satisfaction judging                                     |
| `--budget`              | `5.00`              | Maximum spend in USD                                                   |
| `--threshold`           | `95`                | Satisfaction target (0-100)                                            |
| `--genes`               | *(none)*            | Path to gene file from `octog extract` (bootstraps generation)         |
| `--language`            | `go`                | Target language: `go`, `python`, `node`, `rust`, or `auto`             |
| `--patch`               | `false`             | Incremental patch mode (iteration 2+ sends only changed files)         |
| `--block-on-regression` | `false`             | Block convergence when any scenario regresses below threshold          |
| `--context-budget`      | `0`                 | Max estimated tokens for spec in system prompt; 0 = unlimited          |
| `--otel-endpoint`       | *(none)*            | OTLP/HTTP endpoint for tracing (e.g. `localhost:4318`)                 |
| `--skip-preflight`      | `false`             | Skip the spec clarity preflight check                                  |
| `--preflight-threshold` | `0.8`               | Aggregate clarity score threshold for preflight (0.0–1.0)              |
| `-v`                    | `0`                 | Verbosity: 0=quiet, 1=per-scenario summary, 2=full step detail         |

### `validate`

| Flag            | Default            | Description                                                         |
| --------------- | ------------------ | ------------------------------------------------------------------- |
| `--scenarios`   | *(required)*       | Path to the scenarios directory                                     |
| `--target`      | *(required)*       | URL of the running service to validate                              |
| `--grpc-target` | *(none)*           | gRPC host:port for gRPC scenarios                                   |
| `--judge-model` | `claude-haiku-4-5` | LLM model for satisfaction judging                                  |
| `--threshold`   | `0`                | Minimum satisfaction score; non-zero enables exit code 1 on failure |
| `--format`      | `text`             | Output format: `text` or `json`                                     |
| `-v`            | `0`                | Verbosity: 0=standard, 1=per-scenario, 2=full detail                |

### `extract`

| Flag           | Default      | Description                                             |
| -------------- | ------------ | ------------------------------------------------------- |
| `--source-dir` | *(required)* | Path to source directory to extract patterns from       |
| `--output`     | `genes.json` | Output file path (use `-` for stdout)                   |
| `--model`      | *(auto)*     | LLM model for extraction (defaults to judge-tier model) |

### `status`

| Flag       | Default | Description                     |
| ---------- | ------- | ------------------------------- |
| `--format` | `text`  | Output format: `text` or `json` |

### `preflight`

Assess spec clarity before running the attractor loop.

```text
octog preflight [flags] <spec-path>
```

| Flag            | Default            | Description                                                      |
| --------------- | ------------------ | ---------------------------------------------------------------- |
| `--judge-model` | `claude-haiku-4-5` | LLM model for clarity assessment                                 |
| `--threshold`   | `0.8`              | Aggregate clarity score threshold (0.0–1.0)                      |
| `--verbose`     | `false`            | Show per-dimension strengths and gaps                            |
| `--scenarios`   | *(none)*           | Directory of scenario YAML files to also assess against the spec |

### `lint`

Check spec and scenario files for structural errors (no LLM required).

| Flag          | Default  | Description                         |
| ------------- | -------- | ----------------------------------- |
| `--spec`      | *(none)* | Path to spec file to lint           |
| `--scenarios` | *(none)* | Path to scenarios directory to lint |

At least one of `--spec` or `--scenarios` is required.

## Key Concepts

- **Specs** — Markdown files describing what the software should do
- **Scenarios** — YAML files describing user journeys, used as a holdout set (the agent never sees
  these during code generation)
- **Attractor** — The convergence loop: generate -> test -> score -> feedback -> regenerate
- **Satisfaction** — Probabilistic scoring (0-100) via LLM-as-judge, not boolean pass/fail
- **Preflight** — LLM-based spec clarity and scenario quality assessment before running the loop
- **Wonder/Reflect** — Two-phase stall recovery: high-temperature diagnosis (wonder) then
  low-temperature surgical generation (reflect)
- **Model Escalation** — Start cheap with `--frugal-model`, escalate to `--model` after 2
  consecutive non-improving iterations, downgrade back after 5 consecutive improvements
- **Gene Transfusion** — Extract coding patterns from exemplar codebases to bootstrap generation
  (`octog extract` → `octog run --genes`)

## Documentation

- [Architecture](docs/architecture.md) — System design, data structures, LLM interfaces, Docker
  strategy
- [Gene Transfusion](docs/gene-transfusion.md) — Extract and use coding patterns from exemplar
  codebases
- [Contributing](CONTRIBUTING.md) — Development setup, coding standards, and how to contribute

## License

MIT
