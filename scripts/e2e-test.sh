#!/usr/bin/env bash
#
# e2e-test.sh - Full end-to-end convergence testing
#
# Runs attractor loop convergence across multiple specs, languages, and modes.
# Finds real issues that cheaper tests miss (TTL gaps, build failures, etc.).
#
# Typical cost: $1-3 depending on configuration
# Typical time: 5-15 minutes
#
# Usage: ./scripts/e2e-test.sh              # run all suites
#        ./scripts/e2e-test.sh basic         # run only basic suite
#        ./scripts/e2e-test.sh multilang     # run only multi-language suite
#        make e2e                            # run all via make
#
set -euo pipefail

BINARY="./octog"
BUDGET="${E2E_BUDGET:-3}"
THRESHOLD="${E2E_THRESHOLD:-95}"
RESULTS_DIR="${E2E_RESULTS_DIR:-e2e-results}"

# Colors
if [ -t 1 ]; then
    GREEN='\033[0;32m'
    RED='\033[0;31m'
    YELLOW='\033[0;33m'
    BOLD='\033[1m'
    RESET='\033[0m'
else
    GREEN='' RED='' BOLD='' RESET=''
    # shellcheck disable=SC2034
    YELLOW=''
fi

PASS=0
FAIL=0
TOTAL_COST=0
declare -A RUN_RESULTS

######################################################################
# Helpers
######################################################################

header() {
    printf '\n%b=== %s ===%b\n' "${BOLD}" "$1" "${RESET}"
}

# run_convergence <name> <spec> <scenarios> <extra_flags...>
# Runs a convergence loop and records the result.
run_convergence() {
    local name="$1" spec="$2" scenarios="$3"
    shift 3
    local extra_flags=("$@")

    local outfile="$RESULTS_DIR/${name}.log"
    printf "  %-40s " "$name"

    if ! $BINARY run \
        -spec "$spec" \
        -scenarios "$scenarios" \
        -budget "$BUDGET" \
        -threshold "$THRESHOLD" \
        -v 2 \
        --skip-preflight \
        "${extra_flags[@]}" \
        >"$outfile" 2>&1; then
        # Non-zero exit is OK for stalled runs
        true
    fi

    # Parse result
    local status iterations satisfaction cost
    status=$(grep -oP '(?<=Status:\s{7})\S+' "$outfile" 2>/dev/null || echo "error")
    iterations=$(grep -oP '(?<=Iterations:\s{3})\d+' "$outfile" 2>/dev/null || echo "0")
    satisfaction=$(grep -oP '(?<=Satisfaction: )[\d.]+' "$outfile" 2>/dev/null || echo "0")
    cost=$(grep -oP '(?<=Cost:\s{9}\$)[\d.]+' "$outfile" 2>/dev/null || echo "0")

    TOTAL_COST=$(echo "$TOTAL_COST + $cost" | bc -l)

    RUN_RESULTS[$name]="$status|$iterations|$satisfaction|$cost"

    case "$status" in
        converged)
            PASS=$((PASS + 1))
            printf '%bconverged%b  %s%%  %s iters  $%s\n' "${GREEN}" "${RESET}" "$satisfaction" "$iterations" "$cost"
            ;;
        stalled)
            FAIL=$((FAIL + 1))
            printf '%bstalled%b    %s%%  %s iters  $%s\n' "${RED}" "${RESET}" "$satisfaction" "$iterations" "$cost"
            ;;
        *)
            FAIL=$((FAIL + 1))
            printf '%berror%b      (see %s)\n' "${RED}" "${RESET}" "$outfile"
            ;;
    esac
}

# run_validate <name> <scenarios> <code_dir> <extra_flags...>
# Runs validation against an existing codebase.
run_validate() {
    local name="$1" scenarios="$2" code="$3"
    shift 3
    local extra_flags=("$@")

    local outfile="$RESULTS_DIR/${name}.log"
    printf "  %-40s " "$name"

    local json_out
    json_out=$($BINARY validate \
        -scenarios "$scenarios" \
        -code "$code" \
        --format json \
        -v 2 \
        "${extra_flags[@]}" 2>"$outfile")

    local score
    score=$(echo "$json_out" | python3 -c "import sys,json; print(json.load(sys.stdin)['aggregate_score'])" 2>/dev/null || echo "0")
    local cost
    cost=$(echo "$json_out" | python3 -c "import sys,json; print(f'{json.load(sys.stdin)[\"total_cost_usd\"]:.4f}')" 2>/dev/null || echo "0")

    echo "$json_out" >"$RESULTS_DIR/${name}.json"
    TOTAL_COST=$(echo "$TOTAL_COST + $cost" | bc -l)

    if [ "$(echo "$score >= $THRESHOLD" | bc -l)" -eq 1 ]; then
        PASS=$((PASS + 1))
        printf '%bpass%b       %s%%  $%s\n' "${GREEN}" "${RESET}" "$score" "$cost"
    else
        FAIL=$((FAIL + 1))
        printf '%bfail%b       %s%%  $%s\n' "${RED}" "${RESET}" "$score" "$cost"
    fi
}

######################################################################
# Test Suites
######################################################################

suite_basic() {
    header "Basic Convergence"
    echo "Simple convergence on bundled examples."

    run_convergence \
        "hello-api-basic" \
        "examples/hello-api/spec.md" \
        "examples/hello-api/scenarios/"

    run_convergence \
        "todo-app-basic" \
        "examples/todo-app/spec.md" \
        "examples/todo-app/scenarios/"
}

suite_stratified() {
    header "Stratified Convergence"
    echo "Tier-based progressive validation."

    run_convergence \
        "hello-api-stratified" \
        "examples/hello-api/spec.md" \
        "examples/hello-api/scenarios/" \
        --stratified

    run_convergence \
        "kv-store-stratified" \
        "examples/kv-store/spec.md" \
        "examples/kv-store/scenarios/" \
        --stratified
}

suite_modes() {
    header "Generation Modes"
    echo "Agentic, patch, and gene-transfused generation."

    run_convergence \
        "hello-api-agentic" \
        "examples/hello-api/spec.md" \
        "examples/hello-api/scenarios/" \
        --agentic

    # Gene transfusion: extract then run
    local gene_file="$RESULTS_DIR/genes-go-rest.json"
    $BINARY extract \
        -source-dir examples/exemplars/go-rest \
        -output "$gene_file" 2>/dev/null

    run_convergence \
        "hello-api-genes" \
        "examples/hello-api/spec.md" \
        "examples/hello-api/scenarios/" \
        -genes "$gene_file"

    run_convergence \
        "sensor-telemetry-patch" \
        "examples/sensor-telemetry/spec.md" \
        "examples/sensor-telemetry/scenarios/" \
        --patch
}

suite_multilang() {
    header "Multi-Language Generation"
    echo "Same spec, different target languages."

    local spec="examples/kv-store/spec.md"
    local scenarios="examples/kv-store/scenarios/"

    run_convergence "kv-store-go" "$spec" "$scenarios" -language go
    run_convergence "kv-store-python" "$spec" "$scenarios" -language python
    run_convergence "kv-store-node" "$spec" "$scenarios" -language node
}

suite_validate() {
    header "Validation Checks"
    echo "Validate exemplar codebases against scenarios."

    run_validate \
        "go-rest-validate" \
        "examples/hello-api/scenarios/" \
        "examples/exemplars/go-rest"

    run_validate \
        "python-rest-validate" \
        "examples/hello-api/scenarios/" \
        "examples/exemplars/python-rest"

    run_validate \
        "node-rest-validate" \
        "examples/hello-api/scenarios/" \
        "examples/exemplars/node-rest"
}

######################################################################
# Main
######################################################################

main() {
    printf '%bOctopusGarden E2E Test%b\n' "${BOLD}" "${RESET}"
    echo "================================"
    echo "Budget per run: \$$BUDGET"
    echo "Threshold: $THRESHOLD%"
    echo ""

    # Build first
    if [ ! -f "$BINARY" ]; then
        echo "Building $BINARY..."
        make build
    fi

    mkdir -p "$RESULTS_DIR"

    # Determine which suites to run
    local suites=("$@")
    if [ ${#suites[@]} -eq 0 ]; then
        suites=(validate basic stratified modes multilang)
    fi

    for suite in "${suites[@]}"; do
        case "$suite" in
            basic) suite_basic ;;
            stratified) suite_stratified ;;
            modes) suite_modes ;;
            multilang) suite_multilang ;;
            validate) suite_validate ;;
            *)
                echo "Unknown suite: $suite"
                echo "Available: basic, stratified, modes, multilang, validate"
                exit 1
                ;;
        esac
    done

    # Summary
    header "Summary"
    local total=$((PASS + FAIL))
    printf '%b%d passed%b, %b%d failed%b out of %d\n' \
        "${GREEN}" "$PASS" "${RESET}" "${RED}" "$FAIL" "${RESET}" "$total"
    printf "Total cost: \$%.4f\n" "$TOTAL_COST"
    echo "Results: $RESULTS_DIR/"

    if [ "$FAIL" -gt 0 ]; then
        echo ""
        echo "Failed runs:"
        for name in "${!RUN_RESULTS[@]}"; do
            IFS='|' read -r status iterations satisfaction cost <<<"${RUN_RESULTS[$name]}"
            if [ "$status" != "converged" ]; then
                printf '  %b%s%b: %s at %s%% (%s iters, $%s)\n' "${RED}" \
                    "$name" "${RESET}" "$status" "$satisfaction" "$iterations" "$cost"
                echo "    Log: $RESULTS_DIR/${name}.log"
            fi
        done
        exit 1
    fi
}

main "$@"
