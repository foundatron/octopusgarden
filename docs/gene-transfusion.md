# Gene Transfusion

Gene transfusion extracts coding patterns from an exemplar codebase and injects them into the
attractor loop, bootstrapping code generation with proven architectural decisions.

## Quick Start

```bash
# 1. Extract patterns from your best project
octog extract --source-dir /path/to/exemplar --output genes.json

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
                      │            │
               select files   extract patterns
               detect lang    produce guide
```

1. **Scan** (`internal/gene/scan.go`) — walks the source directory and selects high-signal files:
   project markers (`go.mod`, `package.json`), README (truncated to 100 lines), Dockerfile,
   entrypoint, largest handler file, and largest model file. Skips test files, generated files, lock
   files, and binary assets. Enforces a ~20,000 token budget by dropping lower-priority files.

1. **Analyze** (`internal/gene/analyze.go`) — sends the selected files to an LLM with a structured
   extraction prompt. The LLM produces a concise guide covering: architectural pattern, invariants,
   edge case handling, stack, directory structure, boot sequence, and build strategy. Uses the
   judge-tier model (e.g. `claude-haiku-4-5`) since extraction is a summarization task.

1. **Gene JSON** (`internal/gene/gene.go`) — the guide is stored as a versioned JSON file with
   metadata: source directory, detected language, extraction timestamp, guide text, and token count.

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
  "token_count": 2320
}
```

| Field          | Type   | Description                                          |
| -------------- | ------ | ---------------------------------------------------- |
| `version`      | int    | Schema version (currently 1)                         |
| `source`       | string | Path to the source directory that was scanned        |
| `language`     | string | Detected language: `go`, `python`, `node`, or `rust` |
| `extracted_at` | string | ISO 8601 timestamp of extraction                     |
| `guide`        | string | The extracted pattern guide (LLM-generated text)     |
| `token_count`  | int    | Estimated token count of the guide                   |

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
equivalent in another — the architectural patterns transfer even when the idioms differ.

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

## CLI Reference

### `extract`

| Flag           | Default      | Description                                             |
| -------------- | ------------ | ------------------------------------------------------- |
| `--source-dir` | *(required)* | Path to the source directory to extract patterns from   |
| `--output`     | `genes.json` | Output file path (use `-` for stdout)                   |
| `--model`      | *(auto)*     | LLM model for extraction (defaults to judge-tier model) |
| `--provider`   | *(auto)*     | LLM provider: `anthropic` or `openai`                   |

### `run --genes`

| Flag         | Default  | Description                                                 |
| ------------ | -------- | ----------------------------------------------------------- |
| `--genes`    | *(none)* | Path to `genes.json` file produced by `octog extract`       |
| `--language` | `go`     | Target language (`go`, `python`, `node`, `rust`, or `auto`) |

When `--genes` is provided and `--language` is not explicitly set, the target language is inferred
from the gene file.

## File Selection

The scanner selects files by role, in priority order:

| Role         | Selection strategy                       | Example                    |
| ------------ | ---------------------------------------- | -------------------------- |
| `marker`     | All project markers found                | `go.mod`, `package.json`   |
| `readme`     | First README found (truncated 100 lines) | `README.md`                |
| `dockerfile` | First Dockerfile found                   | `Dockerfile`               |
| `entrypoint` | First recognized entrypoint              | `main.go`, `cmd/*/main.go` |
| `handler`    | Largest file in handler-like directories | `routes/`, `handlers/`     |
| `model`      | Largest file in model-like directories   | `models/`, `types/`        |

When the total exceeds the ~20,000 token budget, files are dropped in reverse priority: model first,
then handler, then readme.

Skipped: `.git`, `vendor`, `node_modules`, `__pycache__`, test files (`*_test.go`, `*.test.ts`),
generated files (`*.pb.go`, `*_generated.*`), lock files (`go.sum`, `package-lock.json`), and binary
assets (`.exe`, `.png`, `.woff`).

## Best Practices

- **Extract from your best project.** The gene captures patterns from the source directory — pick a
  well-structured exemplar that represents the conventions you want.
- **Re-extract after major refactors.** Gene files are snapshots. If the exemplar's architecture
  evolves, re-run `octog extract` to update the gene.
- **Commit gene files.** They're small JSON files (~2-5 KB) that encode team conventions. Version
  them alongside your specs and scenarios.
- **Share across projects.** The same gene file can bootstrap multiple specs targeting the same
  language and stack.
- **Use cross-language for migrations.** Extract from a Go service, generate a Python equivalent —
  the structural patterns transfer.
- **Stdout mode for pipelines.** Use `--output -` to pipe gene JSON into other tools or inspect
  without writing a file.
