---
name: update-docs
description: Update project documentation (architecture.md, gene-transfusion.md, README.md) to reflect recent code changes. Use after implementing features, fixing bugs, or refactoring code that affects documented behavior.
allowed-tools: Bash(git diff *), Bash(git log *), Bash(make docs*), Read, Edit, Glob, Grep
---

Update documentation to reflect recent code changes on this branch.

## Step 1 -- Gather context

Run `git diff main...HEAD` to see all changes on this branch.
If the diff is very large, also run `git diff main...HEAD --stat` for an overview.

## Step 2 -- Read current docs

Read `docs/architecture.md`, `docs/gene-transfusion.md`, and `README.md`.

## Step 3 -- Determine what needs updating

The three docs serve different audiences:

- `docs/architecture.md` is a reference for AI agents and LLMs that work on this codebase.
  It should be comprehensive and information-dense -- optimized for machine consumption,
  not human readability. Include type signatures, interface definitions, package relationships,
  and behavioral details that help an LLM understand the system quickly.
- `docs/gene-transfusion.md` is a user-facing guide for the gene transfusion feature. It covers
  extraction, gene JSON format, components, composed convergence, cross-language synthesis, and
  CLI reference. Update it when gene-related code changes (internal/gene/*, attractor component
  logic, scenario component field, CLI flags for --genes/--language).
- `README.md` is for human developers. Keep it concise, user-friendly, and focused on
  usage, quick start, and high-level concepts.

Common updates include:
- New packages, files, or interfaces added to the architecture doc
- New CLI flags or commands added to README
- Changed data structures or type definitions
- New capabilities, algorithms, or features
- Updated package dependency DAG
- Gene/component changes reflected in gene-transfusion.md

## Step 4 -- Make changes

If documentation needs updates, make the changes directly. Keep the existing style and structure.
Only update sections that are affected by the code changes -- do not rewrite unrelated sections.

## Step 5 -- Verify

Run `make docs` to sync any embedded code blocks (embedmd).
Run `make docs-check` to verify everything is in sync.

## Step 6 -- Stage

If you made any changes, stage them: `git add docs/ README.md`
Do NOT commit, push, or create a PR. Leave the changes staged for the user to review.

If the documentation is already up-to-date and nothing needs changing, say so and do nothing.
