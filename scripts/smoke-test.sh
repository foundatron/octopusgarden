#!/usr/bin/env bash
#
# smoke-test.sh - Quick validation of octog CLI (Rounds 1-3)
#
# Runs zero-cost, low-cost, and medium-cost tests to catch regressions
# without burning significant API budget. Typical cost: ~$0.15
#
# Usage: ./scripts/smoke-test.sh
#        make smoke
#
set -euo pipefail

BINARY="./octog"
PASS=0
FAIL=0
SKIP=0
RESULTS=()

# Colors (disabled if not a terminal)
if [ -t 1 ]; then
    GREEN='\033[0;32m'
    RED='\033[0;31m'
    YELLOW='\033[0;33m'
    BOLD='\033[1m'
    RESET='\033[0m'
else
    GREEN='' RED='' YELLOW='' BOLD='' RESET=''
fi

pass() {
    PASS=$((PASS + 1))
    RESULTS+=("${GREEN}PASS${RESET}  $1")
    printf '%b  %s\n' "${GREEN}PASS${RESET}" "$1"
}

fail() {
    FAIL=$((FAIL + 1))
    RESULTS+=("${RED}FAIL${RESET}  $1: $2")
    printf '%b  %s: %s\n' "${RED}FAIL${RESET}" "$1" "$2"
}

skip() {
    SKIP=$((SKIP + 1))
    RESULTS+=("${YELLOW}SKIP${RESET}  $1: $2")
    printf '%b  %s: %s\n' "${YELLOW}SKIP${RESET}" "$1" "$2"
}

header() {
    printf '\n%b--- %s ---%b\n' "${BOLD}" "$1" "${RESET}"
}

# Check prerequisites
check_prereqs() {
    if [ ! -f "$BINARY" ]; then
        echo "Building $BINARY..."
        make build
    fi

    # Check for API key (needed for rounds 2-3)
    if [ -z "${ANTHROPIC_API_KEY:-}" ] && [ -z "${OPENAI_API_KEY:-}" ]; then
        # Try loading from config
        config="$HOME/.octopusgarden/config"
        if [ -f "$config" ]; then
            if grep -q 'ANTHROPIC_API_KEY\|OPENAI_API_KEY' "$config" 2>/dev/null; then
                return 0
            fi
        fi
        echo "Warning: No API key found. Rounds 2-3 will be skipped."
        return 1
    fi
    return 0
}

######################################################################
# Round 1: Zero Cost
######################################################################
round1() {
    header "Round 1: Zero Cost"

    # Build
    if make build >/dev/null 2>&1; then
        pass "make build"
    else
        fail "make build" "compilation failed"
        return 1
    fi

    # Tests
    if make test >/dev/null 2>&1; then
        pass "make test"
    else
        fail "make test" "unit tests failed"
    fi

    # Lint
    if make lint >/dev/null 2>&1; then
        pass "make lint"
    else
        fail "make lint" "lint errors found"
    fi

    # --help exit code
    if $BINARY --help >/dev/null 2>&1; then
        pass "--help exit 0"
    else
        fail "--help exit 0" "exit code $?"
    fi

    # Subcommand help
    local subcmds=(run validate status extract lint models interview preflight configure)
    local help_ok=0
    local help_fail=0
    for cmd in "${subcmds[@]}"; do
        if $BINARY "$cmd" --help >/dev/null 2>&1; then
            help_ok=$((help_ok + 1))
        else
            help_fail=$((help_fail + 1))
        fi
    done
    if [ "$help_fail" -eq 0 ]; then
        pass "subcommand --help (${help_ok}/${#subcmds[@]})"
    else
        fail "subcommand --help" "${help_fail}/${#subcmds[@]} failed"
    fi

    # Graceful errors on missing args (error goes to stderr via slog)
    local run_out
    run_out=$($BINARY run 2>&1 || true)
    if echo "$run_out" | grep -qiE "required|error|usage"; then
        pass "run (no args) graceful error"
    else
        fail "run (no args)" "no error message"
    fi

    local val_out
    val_out=$($BINARY validate 2>&1 || true)
    if echo "$val_out" | grep -qiE "required|error|usage"; then
        pass "validate (no args) graceful error"
    else
        fail "validate (no args)" "no error message"
    fi

    # Lint on bundled examples
    if $BINARY lint -spec examples/hello-api/spec.md -scenarios examples/hello-api/scenarios/ 2>&1 | grep -q "No issues"; then
        pass "lint hello-api example"
    else
        fail "lint hello-api" "issues found in bundled example"
    fi

    # Status command
    if $BINARY status >/dev/null 2>&1; then
        pass "status"
    else
        fail "status" "command failed"
    fi

    # Status JSON
    if $BINARY status --format json 2>/dev/null | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
        pass "status --format json (valid JSON)"
    else
        fail "status --format json" "invalid JSON output"
    fi
}

######################################################################
# Round 2: Low Cost (~$0.03)
######################################################################
round2() {
    header "Round 2: Low Cost"

    # Gene extraction
    local tmpdir
    tmpdir=$(mktemp -d)

    if $BINARY extract -source-dir examples/exemplars/go-rest -output "$tmpdir/genes.json" 2>&1 | grep -q "Extracted"; then
        # Validate JSON structure
        if python3 -c "import json; d=json.load(open('$tmpdir/genes.json')); assert d['version']==1; assert d['language']=='go'" 2>/dev/null; then
            pass "gene extraction (go-rest)"
        else
            fail "gene extraction" "invalid JSON structure"
        fi
    else
        fail "gene extraction" "extract command failed"
    fi

    # Preflight
    if $BINARY preflight examples/hello-api/spec.md 2>&1 | grep -q "PASS\|WARN"; then
        pass "preflight hello-api"
    else
        fail "preflight" "no score output"
    fi

    # Preflight with scenarios
    local pf_out
    pf_out=$($BINARY preflight -scenarios examples/hello-api/scenarios/ examples/hello-api/spec.md 2>&1)
    if echo "$pf_out" | grep -q "Scenario preflight"; then
        pass "preflight --scenarios (second LLM call)"
    else
        fail "preflight --scenarios" "no scenario assessment"
    fi

    rm -rf "$tmpdir"
}

######################################################################
# Round 3: Medium Cost (~$0.05)
######################################################################
round3() {
    header "Round 3: Medium Cost"

    # Validate against go-rest exemplar
    local val_out
    val_out=$($BINARY validate -scenarios examples/hello-api/scenarios/ -code examples/exemplars/go-rest -v 1 2>&1)
    if echo "$val_out" | grep -q "Aggregate satisfaction"; then
        local score
        score=$(echo "$val_out" | grep "Aggregate" | grep -oE '[0-9]+\.[0-9]+')
        if [ "$(echo "$score >= 95" | bc -l)" -eq 1 ]; then
            pass "validate hello-api text ($score%)"
        else
            fail "validate hello-api" "score $score% < 95%"
        fi
    else
        fail "validate hello-api" "no aggregate score in output"
    fi

    # Validate JSON format with duration_ms check
    local json_out
    json_out=$($BINARY validate -scenarios examples/hello-api/scenarios/ -code examples/exemplars/go-rest --format json 2>/dev/null)
    if echo "$json_out" | python3 -c "
import sys, json
d = json.load(sys.stdin)
assert 'scenarios' in d
assert 'aggregate_score' in d
steps = [st for s in d['scenarios'] for st in s['steps']]
assert all('duration_ms' in st for st in steps)
assert all('reasoning' in st for st in steps)
print('ok')
" 2>/dev/null | grep -q "ok"; then
        pass "validate --format json (structure valid)"
    else
        fail "validate --format json" "missing fields in JSON output"
    fi

    # Validate v2 reasoning
    local v2_out
    v2_out=$($BINARY validate -scenarios examples/hello-api/scenarios/ -code examples/exemplars/go-rest -v 2 2>&1)
    if echo "$v2_out" | grep -qi "reasoning"; then
        pass "validate -v 2 (reasoning shown)"
    else
        fail "validate -v 2" "no reasoning in output"
    fi
}

######################################################################
# Main
######################################################################
main() {
    printf '%bOctopusGarden Smoke Test%b\n' "${BOLD}" "${RESET}"
    echo "================================"

    round1

    has_api=true
    if ! check_prereqs 2>/dev/null; then
        has_api=false
    fi

    if $has_api; then
        round2
        round3
    else
        skip "Round 2 (low cost)" "no API key"
        skip "Round 3 (medium cost)" "no API key"
    fi

    # Summary
    header "Summary"
    local total=$((PASS + FAIL + SKIP))
    printf '%b%d passed%b, %b%d failed%b, %b%d skipped%b out of %d\n' \
        "${GREEN}" "$PASS" "${RESET}" "${RED}" "$FAIL" "${RESET}" "${YELLOW}" "$SKIP" "${RESET}" "$total"

    if [ "$FAIL" -gt 0 ]; then
        echo ""
        echo "Failures:"
        for r in "${RESULTS[@]}"; do
            if echo "$r" | grep -q "FAIL"; then
                printf "  %b\n" "$r"
            fi
        done
        exit 1
    fi
}

main "$@"
