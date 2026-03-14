#!/usr/bin/env bash
# shellcheck shell=bash
# gene-benchmark.sh — Compare octog run performance with and without gene transfusion.
#
# Usage:
#   scripts/gene-benchmark.sh <spec-dir> <exemplar-dir> [--runs N] [--yes]
#
#   <spec-dir>      Directory containing spec.md and scenarios/
#   <exemplar-dir>  Source directory to extract gene patterns from
#   --runs N        Number of convergence loops per configuration (default: 1)
#   --yes           Skip the confirmation prompt (for CI/non-interactive use)
#
# Dependencies: jq, octog (run `make build && export PATH=$PWD/bin:$PATH`)
#
# Warning: each run invokes real LLM APIs and can cost several dollars.
# With --runs 3, expect 6 full convergence loops total.

set -euo pipefail

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

usage() {
  sed -n '3,13p' "$0" | sed 's/^# \?//'
  exit 1
}

die() {
  echo "error: $*" >&2
  exit 1
}

fmt_duration() {
  local secs=${1%.*}  # truncate fractional part if present (mean returns floats)
  printf "%dm%ds" $((secs / 60)) $((secs % 60))
}

# pct_delta <baseline> <with_gene>
# Prints signed percentage change, e.g. "-12.3" or "+5.6"
pct_delta() {
  local base=$1 new=$2
  if [[ "$base" == "null" || "$base" == "0" || "$base" == "0.0" ]]; then
    echo "n/a"
    return
  fi
  awk -v b="$base" -v n="$new" 'BEGIN {
    d = (n - b) / b * 100
    printf "%+.1f", d
  }'
}

# run_once <label> <extra-flags...>
# Runs a single octog convergence loop, records wall time, and prints raw metrics.
# Stores results in RUN_ITERATIONS, RUN_COST, RUN_SATISFACTION, RUN_WALL_TIME.
run_once() {
  local label=$1
  shift
  echo ""
  echo "--- ${label} ---"
  local t0=$SECONDS
  local run_exit=0
  octog run \
    --spec "${SPEC_DIR}/spec.md" \
    --scenarios "${SPEC_DIR}/scenarios" \
    --skip-preflight \
    "$@" || run_exit=$?
  local elapsed
  elapsed=$(( SECONDS - t0 ))

  # Non-zero exit is expected on budget exhaustion; log a warning for other causes.
  if [[ $run_exit -ne 0 ]]; then
    echo "  warning: octog run exited with code ${run_exit} (budget exhaustion or error)" >&2
  fi

  local status_json
  if ! status_json=$(octog status --format json 2>/dev/null); then
    echo "  warning: octog status failed; recording null metrics for this run" >&2
    RUN_WALL_TIME=$elapsed
    RUN_ITERATIONS="null"
    RUN_COST="null"
    RUN_SATISFACTION="null"
    return
  fi

  # Validate that status_json is non-empty valid JSON before feeding to jq.
  if ! jq -e . >/dev/null 2>&1 <<< "$status_json"; then
    echo "  warning: octog status returned invalid JSON; recording null metrics for this run" >&2
    RUN_WALL_TIME=$elapsed
    RUN_ITERATIONS="null"
    RUN_COST="null"
    RUN_SATISFACTION="null"
    return
  fi

  RUN_WALL_TIME=$elapsed
  RUN_ITERATIONS=$(jq -r '.runs[-1].iterations // "null"' <<< "$status_json")
  RUN_COST=$(jq -r '.runs[-1].total_cost_usd // "null"' <<< "$status_json")
  RUN_SATISFACTION=$(jq -r '.runs[-1].satisfaction // "null"' <<< "$status_json")

  echo "  iterations:   ${RUN_ITERATIONS}"
  echo "  satisfaction: ${RUN_SATISFACTION}"
  echo "  cost (USD):   ${RUN_COST}"
  echo "  wall time:    $(fmt_duration "$elapsed")"
}

# mean <space-separated numbers>
mean() {
  awk '{if(NF==0){printf "0.0000"; exit} s=0; for(i=1;i<=NF;i++) s+=$i; printf "%.4f", s/NF}' <<< "$*"
}

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------

SPEC_DIR=""
EXEMPLAR_DIR=""
RUNS=1
YES=0

while [[ $# -gt 0 ]]; do
  case $1 in
    --runs)
      [[ $# -ge 2 ]] || die "--runs requires an argument"
      RUNS=$2
      shift 2
      ;;
    --yes)
      YES=1
      shift
      ;;
    --help|-h)
      usage
      ;;
    -*)
      die "unknown flag: $1"
      ;;
    *)
      if [[ -z "$SPEC_DIR" ]]; then
        SPEC_DIR=$1
      elif [[ -z "$EXEMPLAR_DIR" ]]; then
        EXEMPLAR_DIR=$1
      else
        die "unexpected argument: $1"
      fi
      shift
      ;;
  esac
done

# ---------------------------------------------------------------------------
# Preflight validation
# ---------------------------------------------------------------------------

[[ -n "$SPEC_DIR" ]]     || die "spec-dir is required"
[[ -n "$EXEMPLAR_DIR" ]] || die "exemplar-dir is required"
[[ -d "$SPEC_DIR" ]]     || die "spec-dir does not exist: ${SPEC_DIR}"
[[ -d "$EXEMPLAR_DIR" ]] || die "exemplar-dir does not exist: ${EXEMPLAR_DIR}"
[[ -f "${SPEC_DIR}/spec.md" ]]    || die "spec.md not found in: ${SPEC_DIR}"
[[ -d "${SPEC_DIR}/scenarios" ]]  || die "scenarios/ directory not found in: ${SPEC_DIR}"

command -v jq    >/dev/null 2>&1 || die "jq is required but not found (brew install jq)"
command -v octog >/dev/null 2>&1 || die "octog is not in PATH — run: make build && export PATH=\$PWD/bin:\$PATH"

[[ "$RUNS" -ge 1 ]] 2>/dev/null  || die "--runs must be a positive integer"

# ---------------------------------------------------------------------------
# Setup
# ---------------------------------------------------------------------------

# Initialize GENE_FILE so the trap can safely reference it even before creation.
GENE_FILE=""
trap '[[ -n "$GENE_FILE" ]] && rm -f "$GENE_FILE"' EXIT

echo "================================================================"
echo "gene-benchmark: ${RUNS} run(s) per configuration"
echo "spec-dir:       ${SPEC_DIR}"
echo "exemplar-dir:   ${EXEMPLAR_DIR}"
echo "================================================================"
echo ""
echo "WARNING: This benchmark invokes real LLM APIs. Each run can cost"
echo "several dollars. With --runs ${RUNS}, expect $((RUNS * 2)) full convergence loops."
echo ""

if [[ $YES -eq 0 ]]; then
  if [[ -t 0 ]]; then
    read -r -p "Continue? [y/N] " _confirm
    [[ "${_confirm,,}" =~ ^y ]] || { echo "Aborted."; exit 0; }
  else
    echo "Press Ctrl-C within 5 seconds to abort."
    sleep 5
  fi
fi

GENE_FILE=$(mktemp /tmp/gene-benchmark-XXXXXX.json)

# ---------------------------------------------------------------------------
# Baseline runs
# ---------------------------------------------------------------------------

echo ""
echo "### Baseline (no genes) ###"

BASELINE_ITERATIONS=()
BASELINE_COSTS=()
BASELINE_SATISFACTIONS=()
BASELINE_WALL_TIMES=()

for i in $(seq 1 "$RUNS"); do
  [[ $RUNS -gt 1 ]] && echo "(run ${i}/${RUNS})"
  run_once "baseline run ${i}"
  BASELINE_ITERATIONS+=("$RUN_ITERATIONS")
  BASELINE_COSTS+=("$RUN_COST")
  BASELINE_SATISFACTIONS+=("$RUN_SATISFACTION")
  BASELINE_WALL_TIMES+=("$RUN_WALL_TIME")
done

# ---------------------------------------------------------------------------
# Gene extraction (once)
# ---------------------------------------------------------------------------

echo ""
echo "### Gene extraction ###"
echo "Extracting patterns from: ${EXEMPLAR_DIR}"
octog extract --source-dir "${EXEMPLAR_DIR}" --output "${GENE_FILE}"
echo "Gene file: ${GENE_FILE}"

# ---------------------------------------------------------------------------
# Gene runs
# ---------------------------------------------------------------------------

echo ""
echo "### With gene transfusion ###"

GENE_ITERATIONS=()
GENE_COSTS=()
GENE_SATISFACTIONS=()
GENE_WALL_TIMES=()

for i in $(seq 1 "$RUNS"); do
  [[ $RUNS -gt 1 ]] && echo "(run ${i}/${RUNS})"
  run_once "gene run ${i}" --genes "${GENE_FILE}"
  GENE_ITERATIONS+=("$RUN_ITERATIONS")
  GENE_COSTS+=("$RUN_COST")
  GENE_SATISFACTIONS+=("$RUN_SATISFACTION")
  GENE_WALL_TIMES+=("$RUN_WALL_TIME")
done

# ---------------------------------------------------------------------------
# Summary table
# ---------------------------------------------------------------------------

if [[ $RUNS -gt 1 ]]; then
  B_ITER=$(mean "${BASELINE_ITERATIONS[*]}")
  B_COST=$(mean "${BASELINE_COSTS[*]}")
  B_SAT=$(mean "${BASELINE_SATISFACTIONS[*]}")
  B_TIME=$(mean "${BASELINE_WALL_TIMES[*]}")

  G_ITER=$(mean "${GENE_ITERATIONS[*]}")
  G_COST=$(mean "${GENE_COSTS[*]}")
  G_SAT=$(mean "${GENE_SATISFACTIONS[*]}")
  G_TIME=$(mean "${GENE_WALL_TIMES[*]}")
else
  B_ITER=${BASELINE_ITERATIONS[0]}
  B_COST=${BASELINE_COSTS[0]}
  B_SAT=${BASELINE_SATISFACTIONS[0]}
  B_TIME=${BASELINE_WALL_TIMES[0]}

  G_ITER=${GENE_ITERATIONS[0]}
  G_COST=${GENE_COSTS[0]}
  G_SAT=${GENE_SATISFACTIONS[0]}
  G_TIME=${GENE_WALL_TIMES[0]}
fi

B_TIME_FMT=$(fmt_duration "${B_TIME%.*}")
G_TIME_FMT=$(fmt_duration "${G_TIME%.*}")

echo ""
echo "================================================================"
if [[ $RUNS -gt 1 ]]; then
  echo "Results (averages over ${RUNS} runs)"
else
  echo "Results"
fi
echo "================================================================"
printf "%-20s  %-12s  %-12s  %-10s\n" "Metric" "Baseline" "With Gene" "Delta %"
printf "%-20s  %-12s  %-12s  %-10s\n" "--------------------" "------------" "------------" "----------"
printf "%-20s  %-12s  %-12s  %-10s\n" "Iterations"    "${B_ITER}"      "${G_ITER}"      "$(pct_delta "$B_ITER" "$G_ITER")"
printf "%-20s  %-12s  %-12s  %-10s\n" "Cost (USD)"    "${B_COST}"      "${G_COST}"      "$(pct_delta "$B_COST" "$G_COST")"
printf "%-20s  %-12s  %-12s  %-10s\n" "Satisfaction"  "${B_SAT}"       "${G_SAT}"       "$(pct_delta "$B_SAT" "$G_SAT")"
printf "%-20s  %-12s  %-12s  %-10s\n" "Wall time"     "${B_TIME_FMT}"  "${G_TIME_FMT}"  "$(pct_delta "$B_TIME" "$G_TIME")"
echo ""
echo "Note: results are stochastic — run with --runs 3+ for reliable averages."
echo "================================================================"
