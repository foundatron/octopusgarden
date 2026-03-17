# Gene Transfusion

Gene transfusion extracts coding patterns from an exemplar codebase and injects them into the
attractor loop, bootstrapping code generation with proven architectural decisions.

## Quick Start

```bash
# 1. Extract patterns from your best project (or use a bundled exemplar)
octog extract --source-dir /path/to/exemplar --output genes.json

# Bundled Go REST exemplar — good starting point for Go HTTP services
octog extract --source-dir examples/exemplars/go-rest --output genes.json

# Bundled Python REST exemplar — good starting point for Python HTTP services
octog extract --source-dir examples/exemplars/python-rest --output genes.json

# Bundled Rust REST exemplar — good starting point for Rust/Axum HTTP services
octog extract --source-dir examples/exemplars/rust-rest --output genes.json

# 2. Run the factory with extracted genes
octog run \
  --spec spec.md \
  --scenarios scenarios/ \
  --genes genes.json
```

## How It Works

Gene transfusion is a three-stage pipeline:

```text
Source Directory ──→ Scan ──→ Analyze (LLM) ──→ Gene JSON
                      │            │                  │
               select files   extract patterns   extract components
               detect lang    produce guide      (if multi-layer)
```

1. **Scan** (`internal/gene/scan.go`) -- walks the source directory and selects high-signal files:
   project markers (`go.mod`, `package.json`), README (truncated to 100 lines), Dockerfile,
   entrypoint, largest handler file, and largest model file. Skips test files, generated files, lock
   files, and binary assets. Enforces a ~20,000 token budget by dropping lower-priority files. With
   `--max-files N`, additional source files are backfilled (largest-first) up to that limit; source
   files are trimmed first if the budget is exceeded.

1. **Analyze** (`internal/gene/analyze.go`) -- sends the selected files to an LLM with a structured
   extraction prompt. The LLM produces a concise guide covering: architectural pattern, invariants,
   edge case handling, stack, directory structure, boot sequence, and build strategy. For
   multi-layer codebases, the LLM also identifies **components** with their interfaces, patterns,
   and dependency relationships. Uses the judge-tier model (e.g. `claude-haiku-4-5`) since
   extraction is a summarization task.

1. **Gene JSON** (`internal/gene/gene.go`) -- the guide and optional components are stored as a
   versioned JSON file with metadata: source directory, detected language, extraction timestamp,
   guide text, token count, and component definitions.

### Prompt Injection

When `--genes` is provided to `octog run`, the gene guide is injected into the system prompt between
the spec and the instructions:

```text
SPECIFICATION:
<spec content>

PROVEN PATTERNS (extracted from a working exemplar — synthesize equivalent behavior
adapted to the specification above. Preserve the structural approach and invariants.
The SPECIFICATION always takes precedence over these patterns on any conflict):

<gene guide>

INSTRUCTIONS:
<capability-specific instructions>
```

The spec always takes precedence over gene patterns on any conflict, preserving holdout isolation
semantics.

## Gene JSON Format

```json
{
  "version": 1,
  "source": "/path/to/exemplar",
  "language": "go",
  "extracted_at": "2026-03-06T14:30:00Z",
  "guide": "**PATTERN** — Layered architecture with...",
  "token_count": 2320,
  "components": [
    {
      "name": "models",
      "interface": "Data model types and validation",
      "patterns": "Struct tags for JSON/DB mapping, builder pattern for complex types",
      "depends_on": []
    },
    {
      "name": "routes",
      "interface": "HTTP handlers and middleware",
      "patterns": "Chi router, middleware chain, request/response DTOs",
      "depends_on": ["models"]
    }
  ]
}
```

| Field          | Type        | Description                                                    |
| -------------- | ----------- | -------------------------------------------------------------- |
| `version`      | int         | Schema version (currently 1)                                   |
| `source`       | string      | Path to the source directory that was scanned                  |
| `language`     | string      | Detected language: `go`, `python`, `node`, or `rust`           |
| `extracted_at` | string      | ISO 8601 timestamp of extraction                               |
| `guide`        | string      | The extracted pattern guide (LLM-generated text)               |
| `guidance`     | string      | Extraction guidance passed via `--guidance` (omitted if empty) |
| `token_count`  | int         | Estimated token count of the guide                             |
| `components`   | Component[] | Optional array of architectural components                     |

### Component Fields

| Field        | Type     | Description                                                |
| ------------ | -------- | ---------------------------------------------------------- |
| `name`       | string   | Unique component name (e.g. `models`, `routes`, `storage`) |
| `interface`  | string   | What this component exposes to callers                     |
| `patterns`   | string   | Key implementation patterns for this component             |
| `depends_on` | string[] | Names of components this one depends on (must form a DAG)  |

Components are optional. Simple single-package applications omit them entirely, and the gene file
works exactly as before. Components are extracted automatically when the LLM detects clear module
boundaries in the exemplar.

### Component Validation

Gene files with components are validated on load:

- Component names must be non-empty and unique (comparison is case-insensitive and
  whitespace-normalized: `"HTTP Handler"` and `"http handler"` are the same name; whitespace-only
  names are rejected as empty)
- All entries in `depends_on` must reference a component that exists in the array (also
  case-insensitive and whitespace-normalized: `"data store"` matches `"Data Store"`)
- The dependency graph must be a DAG (no cycles)

## Composed Convergence

When a gene file contains components and scenarios declare a `component` field, the attractor loop
uses **composed convergence** instead of the monolithic loop. Each component converges independently
in dependency order, then the composed output is validated with integration scenarios.

```text
Gene Components ──→ Topological Sort ──→ Per-Component Mini-Loops ──→ File Merge ──→ Integration Validation
                                               │    ▲                                        │
                                               │    │ feedback                               ▼
                                               │    └───────────────── LLM Judge        Converged / Fallback
```

### Convergence Algorithm

1. **Topological sort** -- components are ordered so dependencies converge first (Kahn's algorithm).
   For chain A -> B -> C, A converges first, then B (with A's files available), then C.

1. **Per-component mini-loops** -- each component runs a mini convergence loop (max 5 iterations,
   stall limit 2). The component's system prompt includes:

   - The full spec (cacheable prefix via `cache_control: ephemeral`)
   - `COMPONENT CONTRACT` -- the component's interface description
   - `COMPONENT PATTERNS` -- implementation patterns (if provided)
   - `DEPENDENCY INTERFACES` -- interfaces of declared dependencies only

1. **Transitive dependency files** -- each component's build context includes all files from
   previously converged components (not just direct dependencies). This ensures transitive
   dependencies are available for compilation.

1. **File merge** -- after all components converge, their files are merged in topological order.
   Later components win on file path conflicts (logged as debug).

1. **Integration validation** -- the composed output is validated against integration scenarios
   (scenarios with empty `component` field). If integration fails, the entire composed attempt is
   abandoned and the attractor falls back to the monolithic loop.

1. **Fallback** -- if any component fails to converge, the budget is exceeded, or integration
   validation fails, the attractor falls back to a standard monolithic convergence loop. The cost
   from the composed attempt is carried forward.

### Scenario Component Field

Scenarios can declare which component they validate using the `component` field:

```yaml
id: models-crud
description: Verify model CRUD operations
component: models          # validates only the "models" component
steps:
  - method: POST
    path: /api/models
    # ...

---

id: full-integration
description: End-to-end workflow
# component: (empty or omitted) = integration scenario
steps:
  - method: POST
    path: /api/workflow
    # ...
```

- Scenarios with `component: <name>` are used for per-component validation during mini-loops
- Scenarios with empty or omitted `component` are integration scenarios, used for the final composed
  validation gate
- The `""` key in `ComponentValidators` maps to integration scenarios

### Limitations

- **Not supported in agentic mode.** When `--agentic` is set, composed convergence is skipped and
  the attractor falls back to the monolithic loop. Agentic mode uses multi-turn tool-use generation
  which doesn't fit the sequential component model.
- **No partial retry.** If one component fails, the entire composed attempt is abandoned. There is
  no mechanism to retry individual components while keeping others.
- **Compile-time limits.** The max iterations (5) and stall limit (2) for component mini-loops are
  compile-time constants, not configurable via CLI flags.

## Cross-Language Synthesis

Genes extracted from one language can be used to generate code in another. When the gene's language
differs from the target language, a cross-language note is automatically appended to the system
prompt:

```text
CROSS-LANGUAGE NOTE: The exemplar above is written in Go. You are generating Python.
Preserve the invariants, structural patterns, and architectural approach while using
idiomatic Python constructs, libraries, and conventions.
```

This is useful when you have a well-structured project in one language and want to generate an
equivalent in another -- the architectural patterns transfer even when the idioms differ.

## Language Auto-Detection

When `--genes` is provided without an explicit `--language` flag, the target language is
automatically set to the gene's language. This means:

```bash
# Gene was extracted from a Go project → generates Go code
octog run --spec spec.md --scenarios scenarios/ --genes go-genes.json

# Override with explicit --language to generate in a different language
octog run --spec spec.md --scenarios scenarios/ --genes go-genes.json --language python
```

The auto-detection is logged:

```text
INFO loaded genes source=. language=go tokens=2320
INFO auto-detected language from genes (override with --language) language=go
```

## Benchmarking

`scripts/gene-benchmark.sh` measures the impact of gene transfusion by running the full attractor
loop with and without genes, then printing a comparison table:

```bash
# Single run per configuration (quick sanity check)
scripts/gene-benchmark.sh examples/hello-api examples/exemplars/go-rest

# Three runs each for statistical reliability
scripts/gene-benchmark.sh examples/hello-api examples/exemplars/go-rest --runs 3

# Skip confirmation prompt (CI/non-interactive)
scripts/gene-benchmark.sh examples/hello-api examples/exemplars/go-rest --yes
```

Output:

```text
================================================================
Results
================================================================
Metric                Baseline      With Gene     Delta %
--------------------  ------------  ------------  ----------
Iterations            4             2             -50.0
Cost (USD)            0.82          0.41          -50.0
Satisfaction          97            99            +2.1
Wall time             3m12s         1m44s         -45.8
================================================================
```

Dependencies: `jq`, `octog` in `$PATH` (`make build && export PATH=$PWD/bin:$PATH`).

**Warning:** each run invokes real LLM APIs. With `--runs 3`, expect 6 full convergence loops.

## CLI Reference

### `extract`

| Flag           | Default      | Description                                                                                                                    |
| -------------- | ------------ | ------------------------------------------------------------------------------------------------------------------------------ |
| `--source-dir` | *(required)* | Path to the source directory to extract patterns from                                                                          |
| `--output`     | `genes.json` | Output file path (use `-` for stdout)                                                                                          |
| `--model`      | *(auto)*     | LLM model for extraction (defaults to judge-tier model)                                                                        |
| `--provider`   | *(auto)*     | LLM provider: `anthropic` or `openai`                                                                                          |
| `--guidance`   | *(none)*     | Extraction guidance for the LLM (use `@file.txt` to read from a file)                                                          |
| `--max-files`  | `0`          | Maximum source files to scan (0 = role-based only; positive = backfill additional source files largest-first up to this limit) |

### `run --genes`

| Flag         | Default  | Description                                                 |
| ------------ | -------- | ----------------------------------------------------------- |
| `--genes`    | *(none)* | Path to `genes.json` file produced by `octog extract`       |
| `--language` | `go`     | Target language (`go`, `python`, `node`, `rust`, or `auto`) |

When `--genes` is provided and `--language` is not explicitly set, the target language is inferred
from the gene file. If the gene contains components and scenarios declare `component` fields,
composed convergence activates automatically.

## File Selection

The scanner selects files by role, in priority order:

| Role         | Selection strategy                                                                                       | Example                    |
| ------------ | -------------------------------------------------------------------------------------------------------- | -------------------------- |
| `marker`     | All project markers found                                                                                | `go.mod`, `package.json`   |
| `readme`     | First README found (truncated 100 lines)                                                                 | `README.md`                |
| `dockerfile` | First Dockerfile found                                                                                   | `Dockerfile`               |
| `entrypoint` | First recognized entrypoint                                                                              | `main.go`, `cmd/*/main.go` |
| `handler`    | Largest file in handler-like directories                                                                 | `routes/`, `handlers/`     |
| `model`      | Largest file in model-like directories                                                                   | `models/`, `types/`        |
| `source`     | Backfilled files when `--max-files N` is set; all other source files sorted largest-first, up to N total | `pkg/alpha.go`             |

When the total exceeds the ~20,000 token budget, `source`-role files are trimmed first (smallest
first), then role-based files are dropped in reverse priority: model first, then handler, then
readme.

Skipped: `.git`, `vendor`, `node_modules`, `__pycache__`, test files (`*_test.go`, `*.test.ts`),
generated files (`*.pb.go`, `*_generated.*`), lock files (`go.sum`, `package-lock.json`), and binary
assets (`.exe`, `.png`, `.woff`).

## Best Practices

- **Extract from your best project.** The gene captures patterns from the source directory -- pick a
  well-structured exemplar that represents the conventions you want. The bundled
  `examples/exemplars/go-rest` exemplar is a ready-made starting point for Go HTTP services;
  `examples/exemplars/python-rest` covers Python stdlib HTTP services;
  `examples/exemplars/rust-rest` covers Rust/Axum HTTP services.
- **Re-extract after major refactors.** Gene files are snapshots. If the exemplar's architecture
  evolves, re-run `octog extract` to update the gene.
- **Commit gene files.** They're small JSON files (~2-5 KB) that encode team conventions. Version
  them alongside your specs and scenarios.
- **Share across projects.** The same gene file can bootstrap multiple specs targeting the same
  language and stack.
- **Use cross-language for migrations.** Extract from a Go service, generate a Python equivalent --
  the structural patterns transfer.
- **Stdout mode for pipelines.** Use `--output -` to pipe gene JSON into other tools or inspect
  without writing a file.
- **Use components for multi-layer apps.** When your exemplar has distinct service layers (models,
  routes, storage), the extracted components enable composed convergence -- each layer converges
  independently with faster feedback loops.
- **Tag scenarios with components.** Add `component: <name>` to scenarios that test a specific
  layer. Leave integration scenarios untagged to serve as the final gate.
