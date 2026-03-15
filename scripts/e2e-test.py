#!/usr/bin/env python3
"""Full end-to-end convergence testing for octog.

Runs attractor loop convergence across multiple specs, languages, and modes.
Finds real issues that cheaper tests miss (TTL gaps, build failures, etc.).

Typical cost: $1-3 depending on configuration
Typical time: 5-15 minutes

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
from pathlib import Path

BINARY = "./octog"
BUDGET = os.environ.get("E2E_BUDGET", "3")
THRESHOLD = os.environ.get("E2E_THRESHOLD", "95")
RESULTS_DIR = Path(os.environ.get("E2E_RESULTS_DIR", "e2e-results"))

AVAILABLE_SUITES = ("validate", "basic", "stratified", "modes", "multilang")

# ---------------------------------------------------------------------------
# Output helpers
# ---------------------------------------------------------------------------

_is_tty = sys.stdout.isatty()
GREEN = "\033[0;32m" if _is_tty else ""
RED = "\033[0;31m" if _is_tty else ""
BOLD = "\033[1m" if _is_tty else ""
RESET = "\033[0m" if _is_tty else ""


class Results:
    def __init__(self) -> None:
        self.passed = 0
        self.failed = 0
        self.total_cost = 0.0
        self.run_results: dict[str, RunResult] = {}

    def record_convergence(
        self,
        name: str,
        *,
        status: str,
        iterations: str,
        satisfaction: str,
        cost: str,
    ) -> None:
        cost_f = _safe_float(cost)
        self.total_cost += cost_f
        self.run_results[name] = RunResult(status, iterations, satisfaction, cost)

        if status == "converged":
            self.passed += 1
            print(
                f"{GREEN}converged{RESET}  {satisfaction}%  {iterations} iters  ${cost}"
            )
        elif status == "stalled":
            self.failed += 1
            print(
                f"{RED}stalled{RESET}    {satisfaction}%  {iterations} iters  ${cost}"
            )
        else:
            self.failed += 1
            print(f"{RED}error{RESET}      (see {RESULTS_DIR}/{name}.log)")

    def record_validate(self, name: str, *, score: float, cost: float) -> None:
        self.total_cost += cost
        threshold = float(THRESHOLD)
        if score >= threshold:
            self.passed += 1
            print(f"{GREEN}pass{RESET}       {score}%  ${cost:.4f}")
        else:
            self.failed += 1
            print(f"{RED}fail{RESET}       {score}%  ${cost:.4f}")

    def summary(self) -> int:
        header("Summary")
        total = self.passed + self.failed
        print(
            f"{GREEN}{self.passed} passed{RESET}, "
            f"{RED}{self.failed} failed{RESET} out of {total}"
        )
        print(f"Total cost: ${self.total_cost:.4f}")
        print(f"Results: {RESULTS_DIR}/")
        if self.failed > 0:
            print("\nFailed runs:")
            for name, rr in self.run_results.items():
                if rr.status != "converged":
                    print(
                        f"  {RED}{name}{RESET}: {rr.status} at {rr.satisfaction}% "
                        f"({rr.iterations} iters, ${rr.cost})"
                    )
                    print(f"    Log: {RESULTS_DIR}/{name}.log")
        return 1 if self.failed > 0 else 0


class RunResult:
    __slots__ = ("status", "iterations", "satisfaction", "cost")

    def __init__(
        self, status: str, iterations: str, satisfaction: str, cost: str
    ) -> None:
        self.status = status
        self.iterations = iterations
        self.satisfaction = satisfaction
        self.cost = cost


def _safe_float(s: str) -> float:
    try:
        return float(s)
    except ValueError:
        return 0.0


def header(title: str) -> None:
    print(f"\n{BOLD}=== {title} ==={RESET}")


# ---------------------------------------------------------------------------
# Test runners
# ---------------------------------------------------------------------------


def run_convergence(
    r: Results,
    name: str,
    spec: str,
    scenarios: str,
    extra_flags: list[str] | None = None,
) -> None:
    """Run a convergence loop and record the result."""
    outfile = RESULTS_DIR / f"{name}.log"
    print(f"  {name:<40s} ", end="", flush=True)

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

    # Parse result from log
    log_text = outfile.read_text()

    def _extract(pattern: str, default: str = "0") -> str:
        m = re.search(pattern, log_text)
        return m.group(1) if m else default

    status = _extract(r"Status:\s+(\S+)", "error")
    iterations = _extract(r"Iterations:\s+(\d+)")
    satisfaction = _extract(r"Satisfaction:\s+([\d.]+)")
    cost = _extract(r"Cost:\s+\$([\d.]+)")

    r.record_convergence(
        name,
        status=status,
        iterations=iterations,
        satisfaction=satisfaction,
        cost=cost,
    )


def run_validate(
    r: Results,
    name: str,
    scenarios: str,
    code: str,
    extra_flags: list[str] | None = None,
) -> None:
    """Run validation against an existing codebase."""
    outfile = RESULTS_DIR / f"{name}.log"
    print(f"  {name:<40s} ", end="", flush=True)

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

    result = subprocess.run(
        cmd,
        stdout=subprocess.PIPE,
        stderr=open(outfile, "w"),  # noqa: SIM115
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

    r.record_validate(name, score=score, cost=cost)


# ---------------------------------------------------------------------------
# Test suites
# ---------------------------------------------------------------------------


def suite_basic(r: Results) -> None:
    header("Basic Convergence")
    print("Simple convergence on bundled examples.")

    run_convergence(
        r,
        "hello-api-basic",
        "examples/hello-api/spec.md",
        "examples/hello-api/scenarios/",
    )
    run_convergence(
        r, "todo-app-basic", "examples/todo-app/spec.md", "examples/todo-app/scenarios/"
    )


def suite_stratified(r: Results) -> None:
    header("Stratified Convergence")
    print("Tier-based progressive validation.")

    run_convergence(
        r,
        "hello-api-stratified",
        "examples/hello-api/spec.md",
        "examples/hello-api/scenarios/",
        ["--stratified"],
    )
    run_convergence(
        r,
        "kv-store-stratified",
        "examples/kv-store/spec.md",
        "examples/kv-store/scenarios/",
        ["--stratified"],
    )


def suite_modes(r: Results) -> None:
    header("Generation Modes")
    print("Agentic, patch, and gene-transfused generation.")

    run_convergence(
        r,
        "hello-api-agentic",
        "examples/hello-api/spec.md",
        "examples/hello-api/scenarios/",
        ["--agentic"],
    )

    # Gene transfusion: extract then run
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

    run_convergence(
        r,
        "hello-api-genes",
        "examples/hello-api/spec.md",
        "examples/hello-api/scenarios/",
        ["-genes", gene_file],
    )

    run_convergence(
        r,
        "sensor-telemetry-patch",
        "examples/sensor-telemetry/spec.md",
        "examples/sensor-telemetry/scenarios/",
        ["--patch"],
    )


def suite_multilang(r: Results) -> None:
    header("Multi-Language Generation")
    print("Same spec, different target languages.")

    spec = "examples/kv-store/spec.md"
    scenarios = "examples/kv-store/scenarios/"

    run_convergence(r, "kv-store-go", spec, scenarios, ["-language", "go"])
    run_convergence(r, "kv-store-python", spec, scenarios, ["-language", "python"])
    run_convergence(r, "kv-store-node", spec, scenarios, ["-language", "node"])


def suite_validate(r: Results) -> None:
    header("Validation Checks")
    print("Validate exemplar codebases against scenarios.")

    scenarios = "examples/hello-api/scenarios/"
    run_validate(r, "go-rest-validate", scenarios, "examples/exemplars/go-rest")
    run_validate(r, "python-rest-validate", scenarios, "examples/exemplars/python-rest")
    run_validate(r, "node-rest-validate", scenarios, "examples/exemplars/node-rest")


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
