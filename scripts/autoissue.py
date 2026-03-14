#!/usr/bin/env python3
"""Fully automated GitHub issue solver for OctopusGarden.

8-phase pipeline with information barriers between phases:
  Phase 1: Plan          -> plan.md
  Phase 2: Review Plan   -> reviewed-plan.md (+ complexity rating)
  Phase 3: Implement     -> git commit (model chosen by complexity)
  Phase 4: Simplify      -> review diff for reuse/quality/efficiency, fix in-place
  Phase 5: Review Code   -> review-findings.md
  Phase 6: Fix Findings  -> amended commit
  Phase 6.5: Update Docs -> update architecture.md + README.md, run make docs, push, PR
  Phase 7: CI Retry      -> fix CI failures (max 4 retries), merge

Usage:
  ./scripts/autoissue.py <issue-number>... [options]

Options:
  --budget <usd>           Max budget per phase (default: unlimited)
  --plan-model <model>     Model for planning phase (default: opus)
  --review-model <model>   Model for review phases (default: opus)
  --impl-model <model>     Model for implementation phase (default: sonnet)
  --no-merge               Skip auto-merge after CI passes
  --dry-run                Print what would happen without running

Prerequisites:
  - claude CLI installed and authenticated
  - gh CLI installed and authenticated
  - git configured with push access
"""

from __future__ import annotations

import argparse
import atexit
import collections.abc
import json
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time
from pathlib import Path

REPO = "foundatron/octopusgarden"
MAX_CI_WAIT = 600
CI_POLL_INTERVAL = 30
MAX_CI_RETRIES = 4
MAX_PUSH_RETRIES = 2
DIFF_SIZE_LIMIT = 100_000
DIFF_TRUNCATE_LINES = 3000


class PhaseError(Exception):
    """Raised when a phase (claude invocation) fails."""


# ---------------------------------------------------------------------------
# Utilities
# ---------------------------------------------------------------------------


def log(msg: str) -> None:
    print(f"==> {msg}", flush=True)


def run_cmd(
    args: list[str],
    *,
    check: bool = True,
    capture: bool = True,
    stderr_devnull: bool = False,
    stderr_pipe: bool = False,
) -> subprocess.CompletedProcess[str]:
    """Run a subprocess with sensible defaults."""
    stderr_target: int | None = None
    if stderr_devnull:
        stderr_target = subprocess.DEVNULL
    elif stderr_pipe:
        stderr_target = subprocess.PIPE
    return subprocess.run(
        args,
        check=check,
        capture_output=False,
        stdout=subprocess.PIPE if capture else None,
        stderr=stderr_target,
        text=True,
    )


def check_prerequisites() -> None:
    for cmd in ("claude", "gh", "git"):
        if shutil.which(cmd) is None:
            print(f"Error: {cmd} is not installed", file=sys.stderr)
            sys.exit(1)


def write_prompt(path: Path, content: str) -> None:
    path.write_text(content)


# ---------------------------------------------------------------------------
# Phase runners
# ---------------------------------------------------------------------------


def _claude_args(model: str, budget: str | None) -> list[str]:
    args = [
        "claude",
        "-p",
        "--model",
        model,
        "--effort",
        "medium",
        "--dangerously-skip-permissions",
    ]
    if budget:
        args += ["--max-budget-usd", budget]
    return args


def run_phase(
    phase_name: str,
    model: str,
    output_file: Path,
    prompt_file: Path,
    budget: str | None,
) -> None:
    """Run a claude phase, capturing text output to a file."""
    log(f"  Running {phase_name} (model: {model})...")
    prompt_content = prompt_file.read_text()
    args = _claude_args(model, budget) + ["--output-format", "text"]
    result = subprocess.run(
        args,
        input=prompt_content,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    if result.returncode != 0:
        log(f"  ERROR: {phase_name} failed (exit code {result.returncode})")
        if result.stderr:
            log(f"  stderr: {result.stderr.strip()}")
        raise PhaseError(phase_name)
    output_file.write_text(result.stdout or "")
    lines = len((result.stdout or "").splitlines())
    log(f"  {phase_name} complete ({lines} lines)")


def run_phase_nocapture(
    phase_name: str,
    model: str,
    prompt_file: Path,
    budget: str | None,
) -> None:
    """Run a claude phase, letting output go to terminal."""
    log(f"  Running {phase_name} (model: {model})...")
    prompt_content = prompt_file.read_text()
    args = _claude_args(model, budget)
    result = subprocess.run(args, input=prompt_content, text=True)
    if result.returncode != 0:
        log(f"  ERROR: {phase_name} failed (exit code {result.returncode})")
        raise PhaseError(phase_name)
    log(f"  {phase_name} complete")


# ---------------------------------------------------------------------------
# Artifact helpers
# ---------------------------------------------------------------------------


def validate_artifact(
    path: Path, min_lines: int, required_sections: list[str] | None = None
) -> None:
    if not path.is_file():
        log(f"  ERROR: artifact not found: {path}")
        raise PhaseError(f"artifact not found: {path}")
    text = path.read_text()
    lines = text.splitlines()
    if len(lines) < min_lines:
        log(f"  ERROR: artifact too short ({len(lines)} lines, need {min_lines}+)")
        raise PhaseError(f"artifact too short: {path}")
    for section in required_sections or []:
        if f"### {section}" not in text:
            log(f"  ERROR: missing required section in artifact: ### {section}")
            raise PhaseError(f"missing section: {section}")


def extract_section(path: Path, section_name: str) -> str:
    """Extract content between ### section_name and the next ### heading."""
    text = path.read_text()
    pattern = rf"^### {re.escape(section_name)}$"
    lines = text.splitlines()
    collecting = False
    result: list[str] = []
    for line in lines:
        if re.match(pattern, line):
            collecting = True
            continue
        if collecting and line.startswith("### "):
            break
        if collecting:
            result.append(line)
    return "\n".join(result).strip()


def parse_complexity(path: Path) -> str:
    """Parse complexity rating from reviewed plan (case-insensitive, strip asterisks)."""
    text = path.read_text()
    matches = re.findall(r"rating:\s*\**\s*(\w+)", text, re.IGNORECASE)
    if not matches:
        return "unknown"
    return matches[-1].strip().lower()


def parse_assessment(path: Path) -> str:
    """Parse assessment from review findings (case-insensitive, strip asterisks)."""
    text = path.read_text()
    matches = re.findall(r"assessment:\s*\**\s*(\w[\w\s]*)", text, re.IGNORECASE)
    if not matches:
        return "unknown"
    return matches[-1].strip().lower().replace(" ", "")


# ---------------------------------------------------------------------------
# Git helpers
# ---------------------------------------------------------------------------


def clean_working_tree() -> None:
    """Discard uncommitted changes if any."""
    diff_exit = subprocess.run(["git", "diff", "--quiet"]).returncode
    cached_exit = subprocess.run(["git", "diff", "--cached", "--quiet"]).returncode
    if diff_exit != 0 or cached_exit != 0:
        log("  Cleaning up uncommitted changes from previous issue...")
        subprocess.run(["git", "checkout", "--", "."], check=True)
        subprocess.run(["git", "clean", "-fd"], check=True)


def checkout_or_create_branch(branch: str) -> None:
    result = subprocess.run(
        ["git", "show-ref", "--verify", "--quiet", f"refs/heads/{branch}"],
    )
    if result.returncode == 0:
        log(f"Branch {branch} already exists, checking it out")
        subprocess.run(["git", "checkout", branch], check=True)
        subprocess.run(["git", "merge", "main", "--no-edit"], check=True)
    else:
        subprocess.run(["git", "checkout", "-b", branch], check=True)


def verify_commits_exist() -> int:
    result = run_cmd(["git", "rev-list", "--count", "main..HEAD"])
    return int(result.stdout.strip())


def get_diff_for_review() -> str:
    """Get diff for code review, with truncation for large diffs."""
    result = run_cmd(["git", "diff", "main...HEAD"])
    diff_full = result.stdout or ""
    if len(diff_full) > DIFF_SIZE_LIMIT:
        log(f"  Large diff ({len(diff_full)} chars), using stat + per-file strategy")
        stat_result = run_cmd(["git", "diff", "main...HEAD", "--stat"])
        numstat_result = run_cmd(["git", "diff", "main...HEAD", "--numstat"])
        # Get top 10 changed files by lines added
        numstat_lines = numstat_result.stdout.strip().splitlines()
        top_files: list[str] = []
        for line in sorted(
            numstat_lines,
            key=lambda x: int(x.split()[0]) if x.split()[0].isdigit() else 0,
            reverse=True,
        )[:10]:
            parts = line.split()
            if len(parts) >= 3:
                top_files.append(parts[2])
        if top_files:
            per_file_result = run_cmd(["git", "diff", "main...HEAD", "--"] + top_files)
            per_file_lines = per_file_result.stdout.splitlines()[:DIFF_TRUNCATE_LINES]
            per_file_diff = "\n".join(per_file_lines)
        else:
            per_file_diff = ""
        return (
            f"{stat_result.stdout}\n"
            f"--- Per-file diffs for largest changes (truncated) ---\n\n"
            f"{per_file_diff}"
        )
    return diff_full


# ---------------------------------------------------------------------------
# GitHub helpers
# ---------------------------------------------------------------------------


def snapshot_issues(issues: list[str], work_dir: Path) -> list[str]:
    """Fetch and snapshot all issue content. Returns list of titles."""
    titles: list[str] = []
    for issue_number in issues:
        log(f"Snapshotting issue #{issue_number}...")
        result = run_cmd(
            [
                "gh",
                "issue",
                "view",
                issue_number,
                "--repo",
                REPO,
                "--json",
                "title,body,comments",
            ],
            check=False,
        )
        if result.returncode != 0:
            log(f"ERROR: could not fetch issue #{issue_number} (does it exist?)")
            sys.exit(1)
        data = json.loads(result.stdout)
        title = data["title"]
        titles.append(title)
        print(f"    Title: {title}")

        body = data.get("body") or "No description provided."
        comments = data.get("comments", [])

        lines = [
            f"# Issue #{issue_number}: {title}",
            "",
            "## Description",
            "",
            body,
            "",
        ]
        if comments:
            lines += ["## Comments", ""]
            for c in comments:
                lines += [
                    f"### {c['author']['login']} ({c['createdAt']})",
                    "",
                    c["body"],
                    "",
                ]

        (work_dir / f"issue-{issue_number}.md").write_text("\n".join(lines))
    return titles


def lock_issues(issues: list[str]) -> None:
    for n in issues:
        subprocess.run(
            ["gh", "issue", "lock", n, "--repo", REPO, "--reason", "resolved"],
            check=False,
            stderr=subprocess.DEVNULL,
        )


def unlock_issue(issue_number: str) -> None:
    subprocess.run(
        ["gh", "issue", "unlock", issue_number, "--repo", REPO],
        check=False,
        stderr=subprocess.DEVNULL,
    )


def create_pr(
    branch: str, issue_number: str, issue_title: str, issue_work_dir: Path
) -> None:
    changes_summary = ""
    reviewed_plan = issue_work_dir / "reviewed-plan.md"
    if reviewed_plan.is_file():
        changes_summary = extract_section(reviewed_plan, "Changes")

    review_summary = ""
    review_findings = issue_work_dir / "review-findings.md"
    if review_findings.is_file():
        review_summary = extract_section(review_findings, "Summary")

    body = (
        f"Closes #{issue_number}\n\n"
        f"## Changes\n{changes_summary or 'See commits for details.'}\n\n"
        f"## Review Findings\n{review_summary or 'No findings.'}\n"
    )
    body_file = issue_work_dir / "pr-body.md"
    body_file.write_text(body)

    subprocess.run(
        [
            "gh",
            "pr",
            "create",
            "--repo",
            REPO,
            "--head",
            branch,
            "--title",
            issue_title,
            "--body-file",
            str(body_file),
        ],
        check=True,
    )


def get_pr_number(branch: str) -> str | None:
    result = run_cmd(
        ["gh", "pr", "list", "--repo", REPO, "--head", branch, "--json", "number"],
        check=False,
    )
    if result.returncode != 0:
        return None
    data = json.loads(result.stdout)
    if not data:
        return None
    return str(data[0]["number"])


def merge_pr(pr_number: str) -> None:
    subprocess.run(
        ["gh", "pr", "merge", pr_number, "--repo", REPO, "--squash", "--delete-branch"],
        check=True,
    )


def wait_for_ci(pr_number: str) -> int:
    """Wait for CI. Returns 0=pass, 1=fail, 2=timeout."""
    log("Waiting for CI checks to complete...")
    elapsed = 0
    while elapsed < MAX_CI_WAIT:
        result = run_cmd(
            [
                "gh",
                "pr",
                "checks",
                pr_number,
                "--repo",
                REPO,
                "--json",
                "name,state,bucket",
            ],
            check=False,
            stderr_pipe=True,
        )
        output = result.stdout or ""
        try:
            checks = json.loads(output)
        except (json.JSONDecodeError, ValueError):
            checks = []
        if checks:
            buckets = [c.get("bucket", "") for c in checks]
            if any(b == "fail" for b in buckets):
                log("CI checks failed.")
                for c in checks:
                    if c.get("bucket") == "fail":
                        log(f"  FAILED: {c.get('name', 'unknown')}")
                return 1
            if all(b in {"pass", "skipping"} for b in buckets):
                log("All CI checks passed!")
                return 0
        if elapsed == 0:
            log(
                f"Checks still running, polling every {CI_POLL_INTERVAL}s (max {MAX_CI_WAIT}s)..."
            )
        time.sleep(CI_POLL_INTERVAL)
        elapsed += CI_POLL_INTERVAL
    log("Timed out waiting for CI.")
    return 2


# ---------------------------------------------------------------------------
# Push / CI fix
# ---------------------------------------------------------------------------


def push_with_retry(
    branch: str, prompt_file: Path, impl_model: str, budget: str | None
) -> None:
    for attempt in range(MAX_PUSH_RETRIES + 1):
        result = subprocess.run(
            ["git", "push", "--force-with-lease", "-u", "origin", branch],
            capture_output=True,
            text=True,
        )
        if result.returncode == 0:
            return
        push_output = result.stderr or result.stdout or ""
        if attempt >= MAX_PUSH_RETRIES:
            log(
                f"ERROR: Push failed after {MAX_PUSH_RETRIES} retries (pre-push hooks keep failing)"
            )
            print(push_output)
            raise PhaseError("push failed")
        retry_num = attempt + 1
        log(
            f"Push failed (likely pre-push hook). Attempting fix (retry {retry_num}/{MAX_PUSH_RETRIES})..."
        )
        write_prompt(prompt_file, push_fix_prompt(push_output))
        run_phase_nocapture(
            f"Push Fix (retry {retry_num})", impl_model, prompt_file, budget
        )


def ci_retry_loop(
    pr_number: str,
    branch: str,
    prompt_file: Path,
    impl_model: str,
    budget: str | None,
    pr_url: str,
) -> bool:
    """Run CI retry loop. Returns True if CI passed, False otherwise."""
    ci_retries = 0
    while True:
        ci_result = wait_for_ci(pr_number)
        if ci_result == 0:
            return True
        if ci_result == 2:
            log(f"ERROR: Timed out waiting for CI. PR: {pr_url}")
            return False
        ci_retries += 1
        if ci_retries > MAX_CI_RETRIES:
            log(f"CI failed after {MAX_CI_RETRIES} retries. PR: {pr_url}")
            return False
        log(f"CI retry {ci_retries}/{MAX_CI_RETRIES}...")

        # Get failed run logs
        run_id_result = run_cmd(
            [
                "gh",
                "run",
                "list",
                "--repo",
                REPO,
                "--branch",
                branch,
                "--status",
                "failure",
                "--json",
                "databaseId",
            ],
            check=False,
        )
        ci_logs = ""
        if run_id_result.returncode == 0:
            data = json.loads(run_id_result.stdout)
            if data and data[0].get("databaseId"):
                log_result = run_cmd(
                    [
                        "gh",
                        "run",
                        "view",
                        str(data[0]["databaseId"]),
                        "--repo",
                        REPO,
                        "--log-failed",
                    ],
                    check=False,
                )
                if log_result.returncode == 0:
                    log_lines = (log_result.stdout or "").splitlines()
                    ci_logs = "\n".join(log_lines[-200:])

        write_prompt(prompt_file, ci_fix_prompt(ci_logs))
        run_phase_nocapture(
            f"Phase 7: CI Fix (retry {ci_retries})", impl_model, prompt_file, budget
        )
        push_with_retry(branch, prompt_file, impl_model, budget)


# ---------------------------------------------------------------------------
# Prompt templates
# ---------------------------------------------------------------------------


def plan_prompt(issue_number: str, issue_title: str, issue_snapshot: str) -> str:
    return f"""\
You are solving GitHub issue #{issue_number} for the octopusgarden project.

Here is the issue content (already fetched -- do NOT fetch it from GitHub):

<issue>
{issue_snapshot}
</issue>

Analyze the codebase to understand what needs to change. Read all relevant files.
Create a detailed implementation plan.

Output ONLY the final implementation plan with this structure:

## Issue #{issue_number}: {issue_title}

### Changes
(List each file to create/modify with a description of what changes)

### Tests
(List each test file and what test cases to add)

### Risks & Recommendations
(Bullet list of anything the implementer should watch out for)"""


def review_plan_prompt(plan_content: str) -> str:
    return f"""\
You are a senior Go architect reviewing an implementation plan for the octopusgarden project.
You are seeing ONLY the plan -- you do not have access to the original issue.
Your job is to evaluate the plan on its own merits.

<plan>
{plan_content}
</plan>

Review the plan for:
1. Completeness: Are all necessary changes listed? Any missing files or edge cases?
2. Correctness: Do the proposed changes make sense architecturally?
3. Design invariants: Does the plan respect holdout isolation, error handling conventions, etc.? (See CLAUDE.md)
4. Over-engineering: Is the approach the simplest that could work? Anything unnecessary?
5. Tests: Are tests planned for all new functionality? Any missing cases?
6. Security: Any injection risks, leaked secrets, or OWASP concerns?

Output a revised plan incorporating your feedback, using this structure:

## Revised Plan

### Review Notes
(What you changed or flagged from the original plan, or "No changes needed")

### Changes
(The final list of files to create/modify -- revised if needed)

### Tests
(The final list of tests -- revised if needed)

### Risks & Recommendations
(Revised risks)

### Complexity
- Rating: simple | moderate | complex
- Reason: (one sentence explaining the rating)"""


def implement_prompt(reviewed_plan_content: str) -> str:
    return f"""\
You are implementing a reviewed plan for the octopusgarden project.

Here is the implementation plan (already reviewed and approved by a senior architect):

<plan>
{reviewed_plan_content}
</plan>

Instructions:
1. Implement all changes described in the plan. Follow the coding standards in CLAUDE.md.
2. Write tests as specified in the plan.
3. Run `make build && make test && make lint && make docs` and fix any issues.
4. Stage and commit all changes with a conventional commit message (e.g., feat(package): description).
5. Do NOT push the branch. Do NOT create a PR. Only commit locally."""


def simplify_prompt(diff_for_review: str) -> str:
    return f"""\
You are reviewing recent changes to the octopusgarden project for simplification opportunities.

Here is the diff of all changes on this branch:

<diff>
{diff_for_review}
</diff>

Review the changed code across three dimensions:

1. **Code Reuse** -- Search the existing codebase for utilities, helpers, or patterns that overlap
   with the new code. If existing code already does what the new code does, refactor to reuse it.
   Do NOT introduce new abstractions -- only reuse what already exists.

2. **Code Quality** -- Look for redundant state, parameter sprawl, copy-paste duplication, leaky
   abstractions, or unnecessary complexity in the changed code. Simplify where possible.

3. **Efficiency** -- Identify unnecessary work, missed concurrency opportunities, or memory waste
   in the changed code. Only fix clear inefficiencies, not speculative optimizations.

Instructions:
- If you find issues, fix them directly in the code.
- Run `make build && make test && make lint` to verify your changes.
- If you made changes, stage and amend the commit: `git add -A && git commit --amend --no-edit`
- If the code is already clean and nothing needs simplifying, do nothing. Not every diff needs changes."""


def review_code_prompt(diff_for_review: str) -> str:
    return f"""\
You are a senior Go architect performing a cold code review for the octopusgarden project.
You are seeing ONLY the git diff -- you do not have the original issue or plan.
Your job is to evaluate the code changes on their own merits.

<diff>
{diff_for_review}
</diff>

Review the diff for:
1. Correctness: logic errors, off-by-one, nil dereferences, race conditions
2. Error handling: wrapped errors, sentinel errors, no swallowed errors
3. Tests: adequate coverage, table-driven, edge cases
4. Style: no stuttering, structured logging, context propagation (see CLAUDE.md)
5. Security: injection, secrets, OWASP top 10
6. Design: unnecessary complexity, missing abstractions, broken invariants

Classify each finding as: error (must fix), warning (should fix), or nit (optional).

Output your review in this structure:

### Findings
(Numbered list of findings with classification: [error], [warning], or [nit])

### Summary
- Errors: N
- Warnings: N
- Nits: N
- Assessment: PASS | NEEDS CHANGES
(PASS if 0 errors and 0 warnings. NEEDS CHANGES otherwise.)"""


def fix_findings_prompt(review_findings: str) -> str:
    return f"""\
You are fixing code review findings for the octopusgarden project.

A senior architect reviewed the current changes and found issues that need fixing:

<review-findings>
{review_findings}
</review-findings>

Instructions:
1. Fix ALL errors and warnings listed in the findings. Nits are optional but encouraged.
2. Run `make build && make test && make lint && make docs` and fix any issues.
3. Stage all fixes and amend the previous commit: `git add -A && git commit --amend --no-edit`
4. Do NOT push. Do NOT create a PR."""


def ci_fix_prompt(ci_logs: str) -> str:
    return f"""\
The CI checks failed for the octopusgarden project. Fix the failures.

<ci-logs>
{ci_logs or "No CI logs available. Check make build, make test, and make lint."}
</ci-logs>

Instructions:
1. Analyze the CI failure logs above.
2. Fix the issues in the code.
3. Run `make build && make test && make lint` locally to verify.
4. Stage all fixes and amend the commit: `git add -A && git commit --amend --no-edit`
5. Do NOT push. Do NOT create a PR."""


def docs_update_prompt(diff_for_review: str) -> str:
    return f"""\
You are updating documentation for the octopusgarden project to reflect recent code changes.

Here is the diff of all changes on this branch:

<diff>
{diff_for_review}
</diff>

Instructions:
1. Read `docs/architecture.md`, `docs/gene-transfusion.md`, and `README.md`.
2. Review the diff above and determine if the documentation needs updates to reflect the changes.
3. The three docs serve different audiences:
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
4. Common updates include:
   - New packages, files, or interfaces added to the architecture doc
   - New CLI flags or commands added to README
   - Changed data structures or type definitions
   - New capabilities, algorithms, or features
   - Updated package dependency DAG
   - Gene/component changes reflected in gene-transfusion.md
5. If documentation needs updates, make the changes directly. Keep the existing style and structure.
   Only update sections that are affected by the code changes -- do not rewrite unrelated sections.
6. Run `make docs` to sync any embedded code blocks (embedmd).
7. If you made any changes, stage and amend the commit: `git add -A && git commit --amend --no-edit`
8. If the documentation is already up-to-date and nothing needs changing, do nothing.
9. Do NOT push. Do NOT create a PR."""


def push_fix_prompt(push_output: str) -> str:
    return f"""\
The git push failed because pre-push hooks found issues in the octopusgarden project.

<push-output>
{push_output}
</push-output>

Instructions:
1. Analyze the hook failure output above.
2. Fix the issues. Common fixes:
   - embedmd failures: run `make docs` to sync embedded code blocks, then stage the changes.
   - lint failures: run `make lint` and fix issues.
   - test failures: run `make test` and fix issues.
   - trailing whitespace / end-of-file: fix the formatting.
3. Run `make build && make test && make lint && make docs` to verify everything passes.
4. Stage all fixes and amend the commit: `git add -A && git commit --amend --no-edit`
5. Do NOT push. Do NOT create a PR."""


# ---------------------------------------------------------------------------
# Cleanup
# ---------------------------------------------------------------------------


def make_cleanup(
    work_dir: Path,
    issues: list[str],
    *,
    dry_run: bool = False,
) -> tuple[collections.abc.Callable[[], None], collections.abc.Callable[..., None]]:
    """Create cleanup and signal handler functions with a once-guard."""
    cleaned = False

    def cleanup() -> None:
        nonlocal cleaned
        if cleaned:
            return
        cleaned = True
        shutil.rmtree(work_dir, ignore_errors=True)
        if not dry_run:
            for n in issues:
                unlock_issue(n)

    def signal_handler(signum: int, _frame: object) -> None:
        cleanup()
        sys.exit(128 + signum)

    return cleanup, signal_handler


def install_signal_handlers(handler: collections.abc.Callable[..., None]) -> None:
    signal.signal(signal.SIGINT, handler)
    signal.signal(signal.SIGTERM, handler)


# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Fully automated GitHub issue solver for OctopusGarden.",
    )
    parser.add_argument(
        "issues",
        nargs="+",
        metavar="ISSUE",
        help="GitHub issue numbers to solve",
    )
    parser.add_argument("--budget", default=None, help="Max budget per phase in USD")
    parser.add_argument(
        "--plan-model", default="opus", help="Model for planning (default: opus)"
    )
    parser.add_argument(
        "--review-model", default="opus", help="Model for review (default: opus)"
    )
    parser.add_argument(
        "--impl-model", default=None, help="Model for implementation (default: sonnet)"
    )
    parser.add_argument(
        "--no-merge", action="store_true", help="Skip auto-merge after CI passes"
    )
    parser.add_argument(
        "--dry-run", action="store_true", help="Print pipeline without running"
    )
    args = parser.parse_args()

    # Validate issue numbers
    for issue in args.issues:
        if not re.fullmatch(r"\d+", issue):
            parser.error(f"not a valid issue number: {issue}")

    return args


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------


def main() -> None:
    args = parse_args()

    impl_model = args.impl_model or "sonnet"
    impl_model_override = args.impl_model is not None

    check_prerequisites()

    work_dir = Path(tempfile.mkdtemp())
    cleanup, sig_handler = make_cleanup(work_dir, args.issues, dry_run=args.dry_run)
    atexit.register(cleanup)
    install_signal_handlers(sig_handler)

    # Snapshot all issues upfront (prompt injection defense)
    titles = snapshot_issues(args.issues, work_dir)
    if not args.dry_run:
        lock_issues(args.issues)
    log(
        f"All {len(args.issues)} issues snapshotted{'' if args.dry_run else ' and locked'}. No further network fetches for issue content."
    )

    total = len(args.issues)
    for idx, issue_number in enumerate(args.issues):
        issue_title = titles[idx]
        issue_snapshot = (work_dir / f"issue-{issue_number}.md").read_text()
        issue_work_dir = work_dir / issue_number
        issue_work_dir.mkdir(parents=True, exist_ok=True)
        current = idx + 1

        log(f"===== Issue #{issue_number}: {issue_title} ({current}/{total}) =====")

        # Git: checkout main, pull, create branch
        log("Preparing branch...")
        clean_working_tree()
        subprocess.run(["git", "checkout", "main"], check=True)
        subprocess.run(["git", "pull", "--ff-only"], check=True)

        branch = f"issue-{issue_number}"
        checkout_or_create_branch(branch)

        if args.dry_run:
            budget_display = args.budget or "unlimited"
            log(f"[dry-run] 7-phase pipeline for issue #{issue_number}:")
            log(
                f"[dry-run]   Phase 1: Plan            model={args.plan_model}    budget={budget_display}"
            )
            log(
                f"[dry-run]   Phase 2: Review Plan     model={args.review_model}  budget={budget_display}"
            )
            log(
                f"[dry-run]   Phase 3: Implement       model=adaptive        budget={budget_display}"
            )
            log(
                f"[dry-run]     (simple/moderate -> {impl_model}, complex -> {args.plan_model})"
            )
            log(
                f"[dry-run]   Phase 4: Simplify        model={impl_model}    budget={budget_display}"
            )
            log(
                f"[dry-run]   Phase 5: Review Code     model={args.review_model}  budget={budget_display}"
            )
            log(
                f"[dry-run]   Phase 6: Fix Findings    model={impl_model}    budget={budget_display}"
            )
            log(
                f"[dry-run]   Phase 6.5: Update Docs   model={impl_model}    budget={budget_display}"
            )
            log(
                f"[dry-run]   Phase 7: CI Retry        model={impl_model}    budget={budget_display} (max {MAX_CI_RETRIES} retries)"
            )
            continue

        prompt_file = issue_work_dir / "prompt.tmp"

        # Phase 1: Plan
        log(f"Phase 1: Plan (model: {args.plan_model})...")
        write_prompt(
            prompt_file, plan_prompt(issue_number, issue_title, issue_snapshot)
        )
        run_phase(
            "Phase 1: Plan",
            args.plan_model,
            issue_work_dir / "plan.md",
            prompt_file,
            args.budget,
        )
        validate_artifact(issue_work_dir / "plan.md", 10)

        # Phase 2: Review Plan
        log(f"Phase 2: Review Plan (model: {args.review_model})...")
        plan_content = (issue_work_dir / "plan.md").read_text()
        write_prompt(prompt_file, review_plan_prompt(plan_content))
        run_phase(
            "Phase 2: Review Plan",
            args.review_model,
            issue_work_dir / "reviewed-plan.md",
            prompt_file,
            args.budget,
        )
        validate_artifact(issue_work_dir / "reviewed-plan.md", 10, ["Complexity"])

        # Parse complexity, select implementation model
        complexity = parse_complexity(issue_work_dir / "reviewed-plan.md")
        log(f"  Complexity rating: {complexity}")

        phase3_model = impl_model
        if not impl_model_override and complexity == "complex":
            phase3_model = args.plan_model
            log(f"  Complex task: upgrading implementation model to {phase3_model}")

        # Phase 3: Implement
        log(f"Phase 3: Implement (model: {phase3_model})...")
        reviewed_plan_content = (issue_work_dir / "reviewed-plan.md").read_text()
        write_prompt(prompt_file, implement_prompt(reviewed_plan_content))
        run_phase_nocapture(
            "Phase 3: Implement", phase3_model, prompt_file, args.budget
        )

        commit_count = verify_commits_exist()
        if commit_count == 0:
            log("ERROR: Phase 3 produced no commits")
            sys.exit(1)
        log(f"  Phase 3 produced {commit_count} commit(s)")

        # Phase 4: Simplify
        log(f"Phase 4: Simplify (model: {impl_model})...")
        diff_for_simplify = get_diff_for_review()
        write_prompt(prompt_file, simplify_prompt(diff_for_simplify))
        try:
            run_phase_nocapture(
                "Phase 4: Simplify", impl_model, prompt_file, args.budget
            )
        except PhaseError:
            log("  WARNING: Phase 4 failed, discarding partial changes")
            subprocess.run(["git", "checkout", "--", "."], check=True)
            subprocess.run(["git", "clean", "-fd"], check=True)

        # Phase 5: Review Code
        log(f"Phase 5: Review Code (model: {args.review_model})...")
        diff_for_review = get_diff_for_review()
        write_prompt(prompt_file, review_code_prompt(diff_for_review))
        run_phase(
            "Phase 5: Review Code",
            args.review_model,
            issue_work_dir / "review-findings.md",
            prompt_file,
            args.budget,
        )
        validate_artifact(issue_work_dir / "review-findings.md", 3, ["Summary"])

        # Display review summary
        print("--- Review Summary ---")
        summary = extract_section(issue_work_dir / "review-findings.md", "Summary")
        for line in summary.splitlines()[:10]:
            print(line)
        print("---")

        # Phase 6: Fix Findings
        assessment = parse_assessment(issue_work_dir / "review-findings.md")
        if assessment == "pass":
            log("Phase 6: Skipped (review assessment: PASS)")
        else:
            log(f"Phase 6: Fix Findings (model: {impl_model})...")
            review_findings_content = (
                issue_work_dir / "review-findings.md"
            ).read_text()
            write_prompt(prompt_file, fix_findings_prompt(review_findings_content))
            try:
                run_phase_nocapture(
                    "Phase 6: Fix Findings", impl_model, prompt_file, args.budget
                )
            except PhaseError:
                log("  WARNING: Phase 6 failed, discarding partial changes")
                subprocess.run(["git", "checkout", "--", "."], check=True)
                subprocess.run(["git", "clean", "-fd"], check=True)

        # Phase 6.5: Update Docs
        log(f"Phase 6.5: Update Docs (model: {impl_model})...")
        diff_for_docs = get_diff_for_review()
        write_prompt(prompt_file, docs_update_prompt(diff_for_docs))
        try:
            run_phase_nocapture(
                "Phase 6.5: Update Docs", impl_model, prompt_file, args.budget
            )
        except PhaseError:
            log("  WARNING: Phase 6.5 failed, discarding partial changes")
            subprocess.run(["git", "checkout", "--", "."], check=True)
            subprocess.run(["git", "clean", "-fd"], check=True)

        # Push and create PR
        log("Pushing branch and creating PR...")
        push_with_retry(branch, prompt_file, impl_model, args.budget)
        create_pr(branch, issue_number, issue_title, issue_work_dir)

        pr_number = get_pr_number(branch)
        if not pr_number:
            log(f"ERROR: No PR found for branch {branch} after creation")
            sys.exit(1)

        pr_url = f"https://github.com/{REPO}/pull/{pr_number}"
        log(f"PR created: {pr_url}")

        if args.no_merge:
            log(f"Skipping merge (--no-merge). PR is ready for review: {pr_url}")
            unlock_issue(issue_number)
            continue

        # Phase 7: CI Retry Loop
        log("Phase 7: CI check and retry...")
        ci_passed = ci_retry_loop(
            pr_number, branch, prompt_file, impl_model, args.budget, pr_url
        )

        if not ci_passed:
            log(f"CI did not pass. PR left open for manual review: {pr_url}")
            unlock_issue(issue_number)
            continue

        # Merge
        log(f"Merging PR #{pr_number}...")
        merge_pr(pr_number)
        unlock_issue(issue_number)

        log(f"Done! Issue #{issue_number} resolved and merged.")
        print(f"    PR: {pr_url}")

    if total > 1:
        log(f"===== All {total} issues processed =====")

    # Return to main
    log("Checking out main and pulling latest...")
    subprocess.run(["git", "checkout", "main"], check=True)
    subprocess.run(["git", "pull", "--ff-only"], check=True)
    log("Ready on main.")


if __name__ == "__main__":
    main()
