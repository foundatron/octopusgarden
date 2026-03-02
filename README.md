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

## How It Works

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

1. You write a **spec** in markdown describing the software
1. You write **scenarios** in YAML describing how to verify it works
1. The **attractor loop** calls an LLM to generate code from the spec
1. The code is built and run in a **Docker container**
1. The **validator** runs scenarios against the running container
1. An **LLM judge** scores satisfaction per scenario step
1. Failures are fed back to the attractor, which iterates
1. Loop continues until satisfaction exceeds your threshold (default 95%)

## Quick Start

```bash
# Clone and build
git clone https://github.com/foundatron/octopusgarden.git
cd octopusgarden
make build
```

Set your API key (either as an env var or in the config file):

```bash
export ANTHROPIC_API_KEY=sk-...
# or
mkdir -p ~/.octopusgarden && echo "ANTHROPIC_API_KEY=sk-..." > ~/.octopusgarden/config
```

Run the factory on the included example — an Items REST API:

```bash
octopusgarden run \
  --spec specs/examples/hello-api/spec.md \
  --scenarios scenarios/examples/hello-api/ \
  --model claude-sonnet-4-20250514 \
  --threshold 90
```

Validate a running service against scenarios independently:

```bash
octopusgarden validate \
  --scenarios scenarios/examples/hello-api/ \
  --target http://localhost:8080
```

Check past run history:

```bash
octopusgarden status
```

Requires: Go 1.22+, Docker, an Anthropic API key.

## CLI Reference

```text
octopusgarden <command> [flags]

Commands:
  run        Run the attractor loop to generate software from a spec
  validate   Validate a running service against scenarios
  status     Show recent runs, scores, and costs
```

### `run`

| Flag               | Default                    | Description                                                    |
| ------------------ | -------------------------- | -------------------------------------------------------------- |
| `--spec`           | *(required)*               | Path to the spec markdown file                                 |
| `--scenarios`      | *(required)*               | Path to the scenarios directory                                |
| `--model`          | `claude-sonnet-4-20250514` | LLM model for code generation                                  |
| `--budget`         | `5.00`                     | Maximum spend in USD                                           |
| `--threshold`      | `95`                       | Satisfaction target (0-100)                                    |
| `--patch`          | `false`                    | Incremental patch mode (iteration 2+ sends only changed files) |
| `--context-budget` | `0`                        | Max estimated tokens for spec in system prompt; 0 = unlimited  |

### `validate`

| Flag          | Default      | Description                                                         |
| ------------- | ------------ | ------------------------------------------------------------------- |
| `--scenarios` | *(required)* | Path to the scenarios directory                                     |
| `--target`    | *(required)* | URL of the running service to validate                              |
| `--threshold` | `0`          | Minimum satisfaction score; non-zero enables exit code 1 on failure |

### `status`

No flags. Shows a table of recent runs with status, model, score, iterations, cost, and timestamp.

## Key Concepts

- **Specs** — Markdown files describing what the software should do
- **Scenarios** — YAML files describing user journeys, used as a holdout set (the agent never sees
  these during code generation)
- **Attractor** — The convergence loop: generate -> test -> score -> feedback -> regenerate
- **Satisfaction** — Probabilistic scoring (0-100) via LLM-as-judge, not boolean pass/fail

## Documentation

- [Architecture](docs/architecture.md) — System design, data structures, LLM interfaces, Docker
  strategy
- [Contributing](CONTRIBUTING.md) — Development setup, coding standards, and how to contribute

## License

MIT
