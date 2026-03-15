#!/usr/bin/env python3
"""Smoke tests for the octog CLI (Rounds 1-3).

Runs zero-cost, low-cost, and medium-cost tests to catch regressions
without burning significant API budget. Typical cost: ~$0.15

Usage: ./scripts/smoke-test.py
       make smoke
"""

from __future__ import annotations

import json
import os
import re
import subprocess
import sys
import tempfile
from pathlib import Path

BINARY = "./octog"


# ---------------------------------------------------------------------------
# Output helpers
# ---------------------------------------------------------------------------

_is_tty = sys.stdout.isatty()
GREEN = "\033[0;32m" if _is_tty else ""
RED = "\033[0;31m" if _is_tty else ""
YELLOW = "\033[0;33m" if _is_tty else ""
BOLD = "\033[1m" if _is_tty else ""
RESET = "\033[0m" if _is_tty else ""


class Results:
    def __init__(self) -> None:
        self.passed = 0
        self.failed = 0
        self.skipped = 0
        self.failures: list[str] = []

    def pass_(self, name: str) -> None:
        self.passed += 1
        print(f"{GREEN}PASS{RESET}  {name}")

    def fail(self, name: str, reason: str) -> None:
        self.failed += 1
        self.failures.append(f"{name}: {reason}")
        print(f"{RED}FAIL{RESET}  {name}: {reason}")

    def skip(self, name: str, reason: str) -> None:
        self.skipped += 1
        print(f"{YELLOW}SKIP{RESET}  {name}: {reason}")

    def summary(self) -> int:
        header("Summary")
        total = self.passed + self.failed + self.skipped
        print(
            f"{GREEN}{self.passed} passed{RESET}, "
            f"{RED}{self.failed} failed{RESET}, "
            f"{YELLOW}{self.skipped} skipped{RESET} out of {total}"
        )
        if self.failures:
            print("\nFailures:")
            for f in self.failures:
                print(f"  {RED}FAIL{RESET}  {f}")
        return 1 if self.failed > 0 else 0


def header(title: str) -> None:
    print(f"\n{BOLD}--- {title} ---{RESET}")


def run(
    args: list[str],
    *,
    check: bool = False,
    capture: bool = True,
) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        args,
        check=check,
        stdout=subprocess.PIPE if capture else subprocess.DEVNULL,
        stderr=subprocess.STDOUT if capture else subprocess.DEVNULL,
        text=True,
    )


# ---------------------------------------------------------------------------
# Prerequisites
# ---------------------------------------------------------------------------


def has_api_key() -> bool:
    if os.environ.get("ANTHROPIC_API_KEY") or os.environ.get("OPENAI_API_KEY"):
        return True
    config = Path.home() / ".octopusgarden" / "config"
    if config.is_file():
        text = config.read_text()
        if "ANTHROPIC_API_KEY" in text or "OPENAI_API_KEY" in text:
            return True
    print("Warning: No API key found. Rounds 2-3 will be skipped.")
    return False


# ---------------------------------------------------------------------------
# Round 1: Zero Cost
# ---------------------------------------------------------------------------


def round1(r: Results) -> bool:
    header("Round 1: Zero Cost")

    # Build
    result = run(["make", "build"])
    if result.returncode != 0:
        r.fail("make build", "compilation failed")
        return False
    r.pass_("make build")

    # Tests
    result = run(["make", "test"])
    if result.returncode == 0:
        r.pass_("make test")
    else:
        r.fail("make test", "unit tests failed")

    # Lint
    result = run(["make", "lint"])
    if result.returncode == 0:
        r.pass_("make lint")
    else:
        r.fail("make lint", "lint errors found")

    # --help exit code
    result = run([BINARY, "--help"])
    if result.returncode == 0:
        r.pass_("--help exit 0")
    else:
        r.fail("--help exit 0", f"exit code {result.returncode}")

    # Subcommand help
    subcmds = [
        "run",
        "validate",
        "status",
        "extract",
        "lint",
        "models",
        "interview",
        "preflight",
        "configure",
    ]
    help_fail = 0
    for cmd in subcmds:
        result = run([BINARY, cmd, "--help"])
        if result.returncode != 0:
            help_fail += 1
    if help_fail == 0:
        r.pass_(f"subcommand --help ({len(subcmds)}/{len(subcmds)})")
    else:
        r.fail("subcommand --help", f"{help_fail}/{len(subcmds)} failed")

    # Graceful errors on missing args
    result = run([BINARY, "run"])
    if result.stdout and any(
        w in result.stdout.lower() for w in ("required", "error", "usage")
    ):
        r.pass_("run (no args) graceful error")
    else:
        r.fail("run (no args)", "no error message")

    result = run([BINARY, "validate"])
    if result.stdout and any(
        w in result.stdout.lower() for w in ("required", "error", "usage")
    ):
        r.pass_("validate (no args) graceful error")
    else:
        r.fail("validate (no args)", "no error message")

    # Lint on bundled examples
    result = run(
        [
            BINARY,
            "lint",
            "-spec",
            "examples/hello-api/spec.md",
            "-scenarios",
            "examples/hello-api/scenarios/",
        ]
    )
    if result.stdout and "No issues" in result.stdout:
        r.pass_("lint hello-api example")
    else:
        r.fail("lint hello-api", "issues found in bundled example")

    # Status command
    result = run([BINARY, "status"])
    if result.returncode == 0:
        r.pass_("status")
    else:
        r.fail("status", "command failed")

    # Status JSON
    result = run([BINARY, "status", "--format", "json"])
    try:
        # stderr is merged into stdout via STDOUT redirect, so parse carefully
        json.loads(result.stdout)
        r.pass_("status --format json (valid JSON)")
    except (json.JSONDecodeError, TypeError):
        # Retry with stderr separated
        result2 = subprocess.run(
            [BINARY, "status", "--format", "json"],
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            text=True,
        )
        try:
            json.loads(result2.stdout)
            r.pass_("status --format json (valid JSON)")
        except (json.JSONDecodeError, TypeError):
            r.fail("status --format json", "invalid JSON output")

    return True


# ---------------------------------------------------------------------------
# Round 2: Low Cost (~$0.03)
# ---------------------------------------------------------------------------


def round2(r: Results) -> None:
    header("Round 2: Low Cost")

    with tempfile.TemporaryDirectory() as tmpdir:
        gene_file = os.path.join(tmpdir, "genes.json")

        # Gene extraction
        result = run(
            [
                BINARY,
                "extract",
                "-source-dir",
                "examples/exemplars/go-rest",
                "-output",
                gene_file,
            ]
        )
        if result.stdout and "Extracted" in result.stdout:
            try:
                with open(gene_file) as f:
                    d = json.load(f)
                assert d["version"] == 1
                assert d["language"] == "go"
                r.pass_("gene extraction (go-rest)")
            except (json.JSONDecodeError, KeyError, AssertionError):
                r.fail("gene extraction", "invalid JSON structure")
        else:
            r.fail("gene extraction", "extract command failed")

    # Preflight
    result = run([BINARY, "preflight", "examples/hello-api/spec.md"])
    if result.stdout and ("PASS" in result.stdout or "WARN" in result.stdout):
        r.pass_("preflight hello-api")
    else:
        r.fail("preflight", "no score output")

    # Preflight with scenarios
    result = run(
        [
            BINARY,
            "preflight",
            "-scenarios",
            "examples/hello-api/scenarios/",
            "examples/hello-api/spec.md",
        ]
    )
    if result.stdout and "Scenario preflight" in result.stdout:
        r.pass_("preflight --scenarios (second LLM call)")
    else:
        r.fail("preflight --scenarios", "no scenario assessment")


# ---------------------------------------------------------------------------
# Round 3: Medium Cost (~$0.05)
# ---------------------------------------------------------------------------


def round3(r: Results) -> None:
    header("Round 3: Medium Cost")

    # Validate against go-rest exemplar
    result = run(
        [
            BINARY,
            "validate",
            "-scenarios",
            "examples/hello-api/scenarios/",
            "-code",
            "examples/exemplars/go-rest",
            "-v",
            "1",
        ]
    )
    output = result.stdout or ""
    if "Aggregate satisfaction" in output:
        m = re.search(r"(\d+\.\d+)%?", output[output.index("Aggregate") :])
        if m:
            score = float(m.group(1))
            if score >= 95:
                r.pass_(f"validate hello-api text ({score}%)")
            else:
                r.fail("validate hello-api", f"score {score}% < 95%")
        else:
            r.fail("validate hello-api", "could not parse score")
    else:
        r.fail("validate hello-api", "no aggregate score in output")

    # Validate JSON format
    json_result = subprocess.run(
        [
            BINARY,
            "validate",
            "-scenarios",
            "examples/hello-api/scenarios/",
            "-code",
            "examples/exemplars/go-rest",
            "--format",
            "json",
        ],
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
        text=True,
    )
    try:
        d = json.loads(json_result.stdout)
        assert "scenarios" in d
        assert "aggregate_score" in d
        steps = [st for s in d["scenarios"] for st in s["steps"]]
        assert all("duration_ms" in st for st in steps)
        assert all("reasoning" in st for st in steps)
        r.pass_("validate --format json (structure valid)")
    except (json.JSONDecodeError, KeyError, AssertionError, TypeError):
        r.fail("validate --format json", "missing fields in JSON output")

    # Validate v2 reasoning
    result = run(
        [
            BINARY,
            "validate",
            "-scenarios",
            "examples/hello-api/scenarios/",
            "-code",
            "examples/exemplars/go-rest",
            "-v",
            "2",
        ]
    )
    if result.stdout and "reasoning" in result.stdout.lower():
        r.pass_("validate -v 2 (reasoning shown)")
    else:
        r.fail("validate -v 2", "no reasoning in output")


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------


def main() -> None:
    print(f"{BOLD}OctopusGarden Smoke Test{RESET}")
    print("================================")

    r = Results()
    build_ok = round1(r)

    api_ok = has_api_key() if build_ok else False

    if api_ok:
        round2(r)
        round3(r)
    else:
        r.skip("Round 2 (low cost)", "no API key")
        r.skip("Round 3 (medium cost)", "no API key")

    sys.exit(r.summary())


if __name__ == "__main__":
    main()
