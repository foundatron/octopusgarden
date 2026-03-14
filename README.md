# OctopusGarden

An open-source software dark factory. Write specs and scenarios -- OctopusGarden builds the
software.

> Each arm of an octopus has its own neural cluster and can operate semi-autonomously.
> OctopusGarden's agents work the same way -- independent arms coordinating toward a shared goal.

## What Is This?

OctopusGarden is an autonomous software development system. You describe what you want (specs) and
how to verify it works (scenarios). OctopusGarden orchestrates AI coding agents that generate, test,
and iterate on the code until it converges on a working implementation -- without any human code
review.

The key insight: scenarios are a **holdout set**. The coding agent never sees them during
generation. An LLM judge scores satisfaction probabilistically (0-100), not with boolean pass/fail.
This prevents reward hacking and produces genuinely correct software.

## Prior Art

OctopusGarden builds on ideas pioneered by others:

- **[StrongDM's Software Factory](https://factory.strongdm.ai/)** -- Production system validating
  this exact pattern (holdout scenarios, LLM-as-judge, convergence loops). Demonstrated that
  AI-generated code can pass rigorous QA without human review.
- **[Dan Shapiro's Five Levels](https://www.danshapiro.com/blog/2026/01/the-five-levels-from-spicy-autocomplete-to-the-software-factory/)**
  -- Framework for AI coding maturity, from autocomplete to fully autonomous factories.
  OctopusGarden targets Level 5.
- **[Simon Willison's writeup](https://simonwillison.net/2026/Feb/7/software-factory/)** -- "How
  StrongDM's AI team build serious software without even looking at the code" -- deep dive into the
  software factory pattern and scenario-based validation.
- **[Ouroboros](https://github.com/Q00/ouroboros)** -- Specification-first AI development plugin
  using Socratic questioning and ontological analysis to expose hidden assumptions before code
  generation. Inspired OctopusGarden's preflight and wonder/reflect meta-cognitive patterns.

## How It Works

```text
Spec + Scenarios --> Preflight --> Attractor Loop --> Generated Code --> Docker Build
                     (optional)        |    ^                                  |
                                       |    | wonder/reflect                   v
                                       |    | (on stall)              Running Container
                                       |                                      |
                                       <---- Failure Feedback <---- Validator + LLM Judge
                                                                              |
                                                                   Satisfaction Score (0-100)
```

1. You write a **spec** in markdown describing the software
1. You write **scenarios** in YAML describing how to verify it works
1. **Preflight** assesses spec clarity and scenario quality
1. The **attractor loop** calls an LLM to generate code from the spec
1. The code is built and run in a **Docker container**
1. The **validator** runs scenarios against the running container
1. An **LLM judge** scores satisfaction per scenario step
1. Failures are fed back to the attractor, which iterates -- on stalls, **wonder/reflect** diagnoses
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
```

Draft a spec interactively:

```bash
# Conversational spec-drafting (outputs spec.md by default)
octog interview --output my-spec.md

# Also generate holdout scenario YAML files alongside the spec
octog interview --output my-spec.md --scenarios

# Improve an existing spec through targeted questions
octog interview --seed my-spec.md --output my-spec.md
```

Run the factory on the included examples:

```bash
# Items REST API
octog run \
  --spec examples/hello-api/spec.md \
  --scenarios examples/hello-api/scenarios/ \
  --threshold 90

# Todo app with auth
octog run \
  --spec examples/todo-app/spec.md \
  --scenarios examples/todo-app/scenarios/

# Validate a running service independently
octog validate \
  --scenarios examples/hello-api/scenarios/ \
  --target http://localhost:8080

# Extract patterns from an exemplar codebase
octog extract --source-dir /path/to/exemplar --output genes.json
```

Run `octog <command> --help` for full flag details on any command.

Requires: Go 1.24+, Docker, an Anthropic or OpenAI API key.

## Key Concepts

- **Specs** -- Markdown files describing what the software should do
- **Scenarios** -- YAML files describing user journeys, used as a holdout set (the agent never sees
  these during code generation)
- **Attractor** -- The convergence loop: generate -> test -> score -> feedback -> regenerate
- **Satisfaction** -- Probabilistic scoring (0-100) via LLM-as-judge, not boolean pass/fail
- **Preflight** -- LLM-based spec clarity and scenario quality assessment before running the loop
- **Wonder/Reflect** -- Two-phase stall recovery: high-temperature diagnosis (wonder) then
  low-temperature surgical generation (reflect)
- **Model Escalation** -- Start cheap with `--frugal-model`, escalate to `--model` after 2
  consecutive non-improving iterations, downgrade back after 5 consecutive improvements
- **Gene Transfusion** -- Extract coding patterns from exemplar codebases to bootstrap generation
  (`octog extract` -> `octog run --genes`)

## Documentation

- [Architecture](docs/architecture.md) -- System design, data structures, LLM interfaces, Docker
  strategy (optimized for AI agents working on this codebase)
- [Gene Transfusion](docs/gene-transfusion.md) -- Extract and use coding patterns from exemplar
  codebases
- [Contributing](CONTRIBUTING.md) -- Development setup, coding standards, and how to contribute

## License

MIT
