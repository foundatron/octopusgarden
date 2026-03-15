#!/usr/bin/env python3
"""Full end-to-end convergence testing for octog.

Runs attractor loop convergence across multiple specs, languages, and modes.
Finds real issues that cheaper tests miss (TTL gaps, build failures, etc.).

Tests within each suite run in parallel (controlled by E2E_PARALLEL, default 4).

Typical cost: $1-3 depending on configuration
Typical time: 2-5 minutes with parallelism

Usage: ./scripts/e2e-test.py              # run all suites
       ./scripts/e2e-test.py basic         # run only basic suite
       ./scripts/e2e-test.py multilang     # run only multi-language suite
       make e2e                            # run all via make
"""

from __future__ import annotations

import json
import os
import re
import subprocess
import sys
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass
from pathlib import Path

BINARY = "./octog"
BUDGET = os.environ.get("E2E_BUDGET", "3")
THRESHOLD = os.environ.get("E2E_THRESHOLD", "95")
RESULTS_DIR = Path(os.environ.get("E2E_RESULTS_DIR", "e2e-results"))
PARALLEL = int(os.environ.get("E2E_PARALLEL", "4"))

AVAILABLE_SUITES = ("validate", "basic", "stratified", "modes", "multilang")

# ---------------------------------------------------------------------------
# Output helpers
# ---------------------------------------------------------------------------

_is_tty = sys.stdout.isatty()
GREEN = "\033[0;32m" if _is_tty else ""
RED = "\033[0;31m" if _is_tty else ""
BOLD = "\033[1m" if _is_tty else ""
RESET = "\033[0m" if _is_tty else ""


@dataclass
class TestResult:
    name: str
    kind: str  # "convergence" or "validate"
    status: str
    iterations: str
    satisfaction: str
    cost: str
    score: float = 0.0
    cost_f: float = 0.0


class Results:
    def __init__(self) -> None:
        self.passed = 0
        self.failed = 0
        self.total_cost = 0.0
        self.failed_runs: list[TestResult] = []

    def record(self, tr: TestResult) -> None:
        tr.cost_f = _safe_float(tr.cost)
        self.total_cost += tr.cost_f

        if tr.kind == "convergence":
            self._print_convergence(tr)
        else:
            self._print_validate(tr)

    def _print_convergence(self, tr: TestResult) -> None:
        if tr.status == "converged":
            self.passed += 1
            print(
                f"  {tr.name:<40s} "
                f"{GREEN}converged{RESET}  {tr.satisfaction}%  "
                f"{tr.iterations} iters  ${tr.cost}"
            )
        elif tr.status == "stalled":
            self.failed += 1
            self.failed_runs.append(tr)
            print(
                f"  {tr.name:<40s} "
                f"{RED}stalled{RESET}    {tr.satisfaction}%  "
                f"{tr.iterations} iters  ${tr.cost}"
            )
        else:
            self.failed += 1
            self.failed_runs.append(tr)
            print(
                f"  {tr.name:<40s} "
                f"{RED}error{RESET}      (see {RESULTS_DIR}/{tr.name}.log)"
            )

    def _print_validate(self, tr: TestResult) -> None:
        threshold = float(THRESHOLD)
        if tr.score >= threshold:
            self.passed += 1
            print(
                f"  {tr.name:<40s} "
                f"{GREEN}pass{RESET}       {tr.score}%  ${tr.cost_f:.4f}"
            )
        else:
            self.failed += 1
            self.failed_runs.append(tr)
            print(
                f"  {tr.name:<40s} {RED}fail{RESET}       {tr.score}%  ${tr.cost_f:.4f}"
            )

    def summary(self) -> int:
        header("Summary")
        total = self.passed + self.failed
        print(
            f"{GREEN}{self.passed} passed{RESET}, "
            f"{RED}{self.failed} failed{RESET} out of {total}"
        )
        print(f"Total cost: ${self.total_cost:.4f}")
        print(f"Results: {RESULTS_DIR}/")
        if self.failed_runs:
            print("\nFailed runs:")
            for tr in self.failed_runs:
                if tr.kind == "convergence":
                    print(
                        f"  {RED}{tr.name}{RESET}: {tr.status} at {tr.satisfaction}% "
                        f"({tr.iterations} iters, ${tr.cost})"
                    )
                else:
                    print(f"  {RED}{tr.name}{RESET}: {tr.score}%")
                print(f"    Log: {RESULTS_DIR}/{tr.name}.log")
        return 1 if self.failed_runs else 0


def _safe_float(s: str) -> float:
    try:
        return float(s)
    except ValueError:
        return 0.0


def header(title: str) -> None:
    print(f"\n{BOLD}=== {title} ==={RESET}")


# ---------------------------------------------------------------------------
# Test runners (return TestResult, no printing -- safe for threads)
# ---------------------------------------------------------------------------


def _run_convergence(
    name: str,
    spec: str,
    scenarios: str,
    extra_flags: list[str] | None = None,
) -> TestResult:
    """Run a convergence loop and return the result."""
    outfile = RESULTS_DIR / f"{name}.log"

    cmd = [
        BINARY,
        "run",
        "-spec",
        spec,
        "-scenarios",
        scenarios,
        "-budget",
        BUDGET,
        "-threshold",
        THRESHOLD,
        "-v",
        "2",
        "--skip-preflight",
        *(extra_flags or []),
    ]

    with open(outfile, "w") as f:
        subprocess.run(cmd, stdout=f, stderr=subprocess.STDOUT, text=True)

    log_text = outfile.read_text()

    def _extract(pattern: str, default: str = "0") -> str:
        m = re.search(pattern, log_text)
        return m.group(1) if m else default

    return TestResult(
        name=name,
        kind="convergence",
        status=_extract(r"Status:\s+(\S+)", "error"),
        iterations=_extract(r"Iterations:\s+(\d+)"),
        satisfaction=_extract(r"Satisfaction:\s+([\d.]+)"),
        cost=_extract(r"Cost:\s+\$([\d.]+)"),
    )


def _run_validate(
    name: str,
    scenarios: str,
    code: str,
    extra_flags: list[str] | None = None,
) -> TestResult:
    """Run validation against an existing codebase and return the result."""
    outfile = RESULTS_DIR / f"{name}.log"

    cmd = [
        BINARY,
        "validate",
        "-scenarios",
        scenarios,
        "-code",
        code,
        "--format",
        "json",
        "-v",
        "2",
        *(extra_flags or []),
    ]

    with open(outfile, "w") as f_err:
        result = subprocess.run(
            cmd,
            stdout=subprocess.PIPE,
            stderr=f_err,
            text=True,
        )

    score = 0.0
    cost = 0.0
    try:
        d = json.loads(result.stdout)
        score = float(d.get("aggregate_score", 0))
        cost = float(d.get("total_cost_usd", 0))
        (RESULTS_DIR / f"{name}.json").write_text(result.stdout)
    except (json.JSONDecodeError, TypeError, ValueError):
        pass

    return TestResult(
        name=name,
        kind="validate",
        status="pass" if score >= float(THRESHOLD) else "fail",
        iterations="0",
        satisfaction=str(score),
        cost=f"{cost:.4f}",
        score=score,
    )


def _run_parallel(tasks: list[tuple], r: Results) -> None:
    """Run tasks in parallel and record results in submission order."""
    with ThreadPoolExecutor(max_workers=PARALLEL) as pool:
        future_to_idx = {}
        for idx, (fn, *args) in enumerate(tasks):
            future = pool.submit(fn, *args)
            future_to_idx[future] = idx

        # Collect results, then print in original order
        results_by_idx: dict[int, TestResult] = {}
        for future in as_completed(future_to_idx):
            idx = future_to_idx[future]
            results_by_idx[idx] = future.result()

    for idx in sorted(results_by_idx):
        r.record(results_by_idx[idx])


# ---------------------------------------------------------------------------
# Test suites
# ---------------------------------------------------------------------------


def suite_basic(r: Results) -> None:
    header("Basic Convergence")
    print("Simple convergence on bundled examples.")

    _run_parallel(
        [
            (
                _run_convergence,
                "hello-api-basic",
                "examples/hello-api/spec.md",
                "examples/hello-api/scenarios/",
            ),
            (
                _run_convergence,
                "todo-app-basic",
                "examples/todo-app/spec.md",
                "examples/todo-app/scenarios/",
            ),
        ],
        r,
    )


def suite_stratified(r: Results) -> None:
    header("Stratified Convergence")
    print("Tier-based progressive validation.")

    _run_parallel(
        [
            (
                _run_convergence,
                "hello-api-stratified",
                "examples/hello-api/spec.md",
                "examples/hello-api/scenarios/",
                ["--stratified"],
            ),
            (
                _run_convergence,
                "kv-store-stratified",
                "examples/kv-store/spec.md",
                "examples/kv-store/scenarios/",
                ["--stratified"],
            ),
        ],
        r,
    )


def suite_modes(r: Results) -> None:
    header("Generation Modes")
    print("Agentic, patch, and gene-transfused generation.")

    # Gene extraction must happen before the gene convergence run
    gene_file = str(RESULTS_DIR / "genes-go-rest.json")
    subprocess.run(
        [
            BINARY,
            "extract",
            "-source-dir",
            "examples/exemplars/go-rest",
            "-output",
            gene_file,
        ],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )

    _run_parallel(
        [
            (
                _run_convergence,
                "hello-api-agentic",
                "examples/hello-api/spec.md",
                "examples/hello-api/scenarios/",
                ["--agentic"],
            ),
            (
                _run_convergence,
                "hello-api-genes",
                "examples/hello-api/spec.md",
                "examples/hello-api/scenarios/",
                ["-genes", gene_file],
            ),
            (
                _run_convergence,
                "sensor-telemetry-patch",
                "examples/sensor-telemetry/spec.md",
                "examples/sensor-telemetry/scenarios/",
                ["--patch"],
            ),
        ],
        r,
    )


def suite_multilang(r: Results) -> None:
    header("Multi-Language Generation")
    print("Same spec, different target languages.")

    spec = "examples/kv-store/spec.md"
    scenarios = "examples/kv-store/scenarios/"

    _run_parallel(
        [
            (_run_convergence, "kv-store-go", spec, scenarios, ["-language", "go"]),
            (
                _run_convergence,
                "kv-store-python",
                spec,
                scenarios,
                ["-language", "python"],
            ),
            (_run_convergence, "kv-store-node", spec, scenarios, ["-language", "node"]),
        ],
        r,
    )


def suite_validate(r: Results) -> None:
    header("Validation Checks")
    print("Validate exemplar codebases against scenarios.")

    scenarios = "examples/hello-api/scenarios/"

    _run_parallel(
        [
            (
                _run_validate,
                "go-rest-validate",
                scenarios,
                "examples/exemplars/go-rest",
            ),
            (
                _run_validate,
                "python-rest-validate",
                scenarios,
                "examples/exemplars/python-rest",
            ),
            (
                _run_validate,
                "node-rest-validate",
                scenarios,
                "examples/exemplars/node-rest",
            ),
        ],
        r,
    )


SUITE_MAP = {
    "basic": suite_basic,
    "stratified": suite_stratified,
    "modes": suite_modes,
    "multilang": suite_multilang,
    "validate": suite_validate,
}


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------


def main() -> None:
    print(f"{BOLD}OctopusGarden E2E Test{RESET}")
    print("================================")
    print(f"Budget per run: ${BUDGET}")
    print(f"Threshold: {THRESHOLD}%")
    print(f"Parallelism: {PARALLEL}")
    print()

    # Build if needed
    if not Path(BINARY).is_file():
        print(f"Building {BINARY}...")
        subprocess.run(["make", "build"], check=True)

    RESULTS_DIR.mkdir(parents=True, exist_ok=True)

    # Determine suites
    suites = sys.argv[1:] if len(sys.argv) > 1 else list(AVAILABLE_SUITES)
    for s in suites:
        if s not in SUITE_MAP:
            print(f"Unknown suite: {s}")
            print(f"Available: {', '.join(AVAILABLE_SUITES)}")
            sys.exit(1)

    r = Results()
    for s in suites:
        SUITE_MAP[s](r)

    sys.exit(r.summary())


if __name__ == "__main__":
    main()
