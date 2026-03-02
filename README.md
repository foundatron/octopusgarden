# OctopusGarden

An open-source software dark factory. Write specs and scenarios — OctopusGarden builds the software.

> Each arm of an octopus has its own neural cluster and can operate semi-autonomously.
> OctopusGarden's agents work the same way — independent arms coordinating toward a shared goal.

## Status: Early Development

OctopusGarden is pre-alpha. The core loop (spec -> generate -> validate -> converge) is being built.
See [docs/sessions.md](docs/sessions.md) for the implementation roadmap.

## What Is This?

OctopusGarden is an autonomous software development system. You describe what you want (specs) and
how to verify it works (scenarios). OctopusGarden orchestrates AI coding agents that generate, test,
and iterate on the code until it converges on a working implementation — without any human code
review.

The key insight: scenarios are a **holdout set**. The coding agent never sees them during
generation. An LLM judge scores satisfaction probabilistically (0-100), not with boolean pass/fail.
This prevents reward hacking and produces genuinely correct software.

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
go build ./cmd/octopusgarden

# Run the factory on the hello-api example
./octopusgarden run \
  --spec specs/examples/hello-api/spec.md \
  --scenarios scenarios/examples/hello-api/ \
  --model claude-sonnet-4-20250514 \
  --threshold 90

# Or run scenarios against any running service
./octopusgarden validate \
  --scenarios scenarios/examples/hello-api/ \
  --target http://localhost:8080
```

Requires: Go 1.22+, Docker, an Anthropic API key (`ANTHROPIC_API_KEY` env var).

## Key Concepts

- **Specs** — Markdown files describing what the software should do
- **Scenarios** — YAML files describing user journeys, used as a holdout set (the agent never sees
  these during code generation)
- **Attractor** — The convergence loop: generate -> test -> score -> feedback -> regenerate
- **Satisfaction** — Probabilistic scoring (0-100) via LLM-as-judge, not boolean pass/fail

## Prior Art

- **[StrongDM's Software Factory](https://factory.strongdm.ai/)** — Production system validating
  this exact pattern (holdout scenarios, LLM-as-judge, convergence loops). Demonstrated that
  AI-generated code can pass rigorous QA without human review.
- **[Dan Shapiro's Five Levels](https://www.danshapiro.com/blog/2026/01/the-five-levels-from-spicy-autocomplete-to-the-software-factory/)**
  — Framework for AI coding maturity, from autocomplete to fully autonomous factories. OctopusGarden
  targets Level 5.

## Documentation

- [Architecture](docs/architecture.md) — System design, data structures, LLM interfaces, Docker
  strategy
- [Implementation Sessions](docs/sessions.md) — Build roadmap with exact types, tests, and done
  conditions

## License

MIT
