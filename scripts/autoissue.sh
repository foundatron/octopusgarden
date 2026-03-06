#!/usr/bin/env bash
set -euo pipefail

# autoissue.sh — Fully automated GitHub issue solver for OctopusGarden
#
# 6-phase pipeline with information barriers between phases:
#   Phase 1: Plan          -> plan.md
#   Phase 2: Review Plan   -> reviewed-plan.md (+ complexity rating)
#   Phase 3: Implement     -> git commit (model chosen by complexity)
#   Phase 4: Review Code   -> review-findings.md
#   Phase 5: Fix Findings  -> amended commit, push, PR
#   Phase 6: CI Retry      -> fix CI failures (max 2 retries), merge
#
# Usage:
#   ./scripts/autoissue.sh <issue-number>... [options]
#
# Options:
#   --budget <usd>           Max budget for implementation phase (default: 10)
#   --plan-model <model>     Model for planning phase (default: opus)
#   --review-model <model>   Model for review phases (default: opus)
#   --impl-model <model>     Model for implementation phase (default: sonnet)
#   --no-merge               Skip auto-merge after CI passes
#   --dry-run                Print what would happen without running
#
# Examples:
#   ./scripts/autoissue.sh 77
#   ./scripts/autoissue.sh 81 82 83
#   ./scripts/autoissue.sh 77 --budget 5
#   ./scripts/autoissue.sh 81 82 --no-merge
#   ./scripts/autoissue.sh 77 --review-model sonnet
#
# Prerequisites:
#   - claude CLI installed and authenticated
#   - gh CLI installed and authenticated
#   - git configured with push access

REPO="foundatron/octopusgarden"
DEFAULT_BUDGET=10
PLAN_MODEL=opus
REVIEW_MODEL=opus
IMPL_MODEL=sonnet
IMPL_MODEL_OVERRIDE=false
MERGE=true
DRY_RUN=false

usage() {
  echo "Usage: $0 <issue-number>... [--budget <usd>] [--plan-model <model>] [--review-model <model>] [--impl-model <model>] [--no-merge] [--dry-run]"
  exit 1
}

# --- Parse args ---
[[ $# -lt 1 ]] && usage

ISSUES=()
BUDGET="$DEFAULT_BUDGET"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --budget) BUDGET="$2"; shift 2 ;;
    --plan-model) PLAN_MODEL="$2"; shift 2 ;;
    --review-model) REVIEW_MODEL="$2"; shift 2 ;;
    --impl-model) IMPL_MODEL="$2"; IMPL_MODEL_OVERRIDE=true; shift 2 ;;
    --no-merge) MERGE=false; shift ;;
    --dry-run) DRY_RUN=true; shift ;;
    --*) echo "Unknown option: $1"; usage ;;
    *)
      if [[ "$1" =~ ^[0-9]+$ ]]; then
        ISSUES+=("$1")
      else
        echo "Error: not a valid issue number: $1"
        exit 1
      fi
      shift
      ;;
  esac
done

if [[ ${#ISSUES[@]} -eq 0 ]]; then
  echo "Error: at least one issue number is required"
  usage
fi

log() { echo "==> $*"; }

# --- Helper: run a claude phase with prompt from file ---
# Usage: run_phase <phase_name> <model> <budget> <output_file> <prompt_file>
run_phase() {
  local phase_name="$1" model="$2" budget="$3" output_file="$4" prompt_file="$5"

  log "  Running ${phase_name} (model: ${model}, budget: \$${budget})..."

  claude -p \
    --model "$model" \
    --effort medium \
    --dangerously-skip-permissions \
    --max-budget-usd "$budget" \
    --output-format text \
    "$(cat "$prompt_file")" > "$output_file"

  local exit_code=$?
  if [[ $exit_code -ne 0 ]]; then
    log "  ERROR: ${phase_name} failed (exit code ${exit_code})"
    return 1
  fi

  local lines
  lines=$(wc -l < "$output_file" | tr -d ' ')
  log "  ${phase_name} complete (${lines} lines)"
}

# --- Helper: run a claude phase without capturing output ---
# Usage: run_phase_nocapture <phase_name> <model> <budget> <prompt_file>
run_phase_nocapture() {
  local phase_name="$1" model="$2" budget="$3" prompt_file="$4"

  log "  Running ${phase_name} (model: ${model}, budget: \$${budget})..."

  claude -p \
    --model "$model" \
    --effort medium \
    --dangerously-skip-permissions \
    --max-budget-usd "$budget" \
    "$(cat "$prompt_file")"

  local exit_code=$?
  if [[ $exit_code -ne 0 ]]; then
    log "  ERROR: ${phase_name} failed (exit code ${exit_code})"
    return 1
  fi

  log "  ${phase_name} complete"
}

# --- Helper: validate an artifact file ---
# Usage: validate_artifact <file> <min_lines> [required_section...]
validate_artifact() {
  local file="$1" min_lines="$2"
  shift 2

  if [[ ! -f "$file" ]]; then
    log "  ERROR: artifact not found: ${file}"
    return 1
  fi

  local line_count
  line_count=$(wc -l < "$file" | tr -d ' ')
  if [[ "$line_count" -lt "$min_lines" ]]; then
    log "  ERROR: artifact too short (${line_count} lines, need ${min_lines}+)"
    return 1
  fi

  for section in "$@"; do
    if ! grep -q "### ${section}" "$file"; then
      log "  ERROR: missing required section in artifact: ### ${section}"
      return 1
    fi
  done
}

# --- Helper: extract a markdown section ---
# Usage: extract_section <file> <section_name>
extract_section() {
  local file="$1" section="$2"
  sed -n "/^### ${section}/,/^### /{/^### /d; p;}" "$file"
}

# --- Helper: wait for CI checks ---
# Usage: wait_for_ci <pr_number>
# Returns 0 on pass, 1 on fail, 2 on timeout
wait_for_ci() {
  local pr_number="$1"
  local max_wait=600 elapsed=0 interval=30

  log "Waiting for CI checks to complete..."

  while [[ $elapsed -lt $max_wait ]]; do
    local check_status
    check_status=$(gh pr checks "$pr_number" --repo "$REPO" 2>&1) || true

    if echo "$check_status" | grep -q "fail"; then
      log "CI checks failed."
      echo "$check_status"
      return 1
    fi

    if echo "$check_status" | grep -q "pass"; then
      if ! echo "$check_status" | grep -q "pending"; then
        log "All CI checks passed!"
        return 0
      fi
    fi

    if [[ $elapsed -eq 0 ]]; then
      log "Checks still running, polling every ${interval}s (max ${max_wait}s)..."
    fi
    sleep "$interval"
    elapsed=$((elapsed + interval))
  done

  log "Timed out waiting for CI."
  return 2
}

# --- Helper: create PR from artifacts ---
# Usage: create_pr <branch> <issue_number> <issue_title> <work_dir>
create_pr() {
  local branch="$1" issue_number="$2" issue_title="$3" work_dir="$4"

  local changes_summary=""
  if [[ -f "${work_dir}/reviewed-plan.md" ]]; then
    changes_summary=$(extract_section "${work_dir}/reviewed-plan.md" "Changes")
  fi

  local review_summary=""
  if [[ -f "${work_dir}/review-findings.md" ]]; then
    review_summary=$(extract_section "${work_dir}/review-findings.md" "Summary")
  fi

  local body_file="${work_dir}/pr-body.md"
  cat > "$body_file" <<EOF
Closes #${issue_number}

## Changes
${changes_summary:-See commits for details.}

## Review Findings
${review_summary:-No findings.}
EOF

  gh pr create \
    --repo "$REPO" \
    --head "$branch" \
    --title "${issue_title}" \
    --body-file "$body_file"
}

# --- Helper: write prompt to file ---
# Usage: write_prompt <dest_file>
# Reads heredoc from stdin, writes to dest_file
write_prompt() {
  cat > "$1"
}

# --- Validate tools ---
for cmd in claude gh git jq; do
  if ! command -v "$cmd" &>/dev/null; then
    echo "Error: $cmd is not installed"
    exit 1
  fi
done

# --- Snapshot all issue content upfront (prompt injection defense) ---
WORK_DIR=$(mktemp -d)
cleanup() {
  rm -rf "$WORK_DIR"
  for n in "${ISSUES[@]}"; do
    gh issue unlock "$n" --repo "$REPO" 2>/dev/null || true
  done
}
trap cleanup EXIT

ISSUE_TITLES=()
for ISSUE_NUMBER in "${ISSUES[@]}"; do
  log "Snapshotting issue #${ISSUE_NUMBER}..."

  ISSUE_JSON=$(gh issue view "$ISSUE_NUMBER" --repo "$REPO" --json title,body,comments 2>/dev/null) || {
    echo "Error: could not fetch issue #${ISSUE_NUMBER}"
    exit 1
  }

  ISSUE_TITLES+=("$(echo "$ISSUE_JSON" | jq -r '.title')")
  echo "    Title: ${ISSUE_TITLES[${#ISSUE_TITLES[@]}-1]}"

  {
    echo "# Issue #${ISSUE_NUMBER}: $(echo "$ISSUE_JSON" | jq -r '.title')"
    echo ""
    echo "## Description"
    echo ""
    echo "$ISSUE_JSON" | jq -r '.body // "No description provided."'
    echo ""
    COMMENT_COUNT=$(echo "$ISSUE_JSON" | jq '.comments | length')
    if [[ "$COMMENT_COUNT" -gt 0 ]]; then
      echo "## Comments"
      echo ""
      echo "$ISSUE_JSON" | jq -r '.comments[] | "### \(.author.login) (\(.createdAt))\n\n\(.body)\n"'
    fi
  } > "${WORK_DIR}/issue-${ISSUE_NUMBER}.md"
done

# Lock all issues to prevent new comments during processing
for ISSUE_NUMBER in "${ISSUES[@]}"; do
  gh issue lock "$ISSUE_NUMBER" --repo "$REPO" --reason "resolved" 2>/dev/null || true
done

log "All ${#ISSUES[@]} issues snapshotted and locked. No further network fetches for issue content."

# --- Process each issue ---
TOTAL=${#ISSUES[@]}
CURRENT=0

for IDX in "${!ISSUES[@]}"; do
  ISSUE_NUMBER="${ISSUES[$IDX]}"
  CURRENT=$((CURRENT + 1))
  ISSUE_TITLE="${ISSUE_TITLES[$IDX]}"
  ISSUE_SNAPSHOT=$(cat "${WORK_DIR}/issue-${ISSUE_NUMBER}.md")
  ISSUE_WORK_DIR="${WORK_DIR}/${ISSUE_NUMBER}"
  mkdir -p "$ISSUE_WORK_DIR"
  log "===== Issue #${ISSUE_NUMBER}: ${ISSUE_TITLE} (${CURRENT}/${TOTAL}) ====="

  # --- Git: checkout main, pull, create branch ---
  log "Preparing branch..."
  git checkout main
  git pull --ff-only

  BRANCH="issue-${ISSUE_NUMBER}"
  if git show-ref --verify --quiet "refs/heads/${BRANCH}"; then
    log "Branch ${BRANCH} already exists, checking it out"
    git checkout "$BRANCH"
    git merge main --no-edit
  else
    git checkout -b "$BRANCH"
  fi

  if $DRY_RUN; then
    log "[dry-run] 6-phase pipeline for issue #${ISSUE_NUMBER}:"
    log "[dry-run]   Phase 1: Plan            model=${PLAN_MODEL}    budget=\$1"
    log "[dry-run]   Phase 2: Review Plan     model=${REVIEW_MODEL}  budget=\$1"
    log "[dry-run]   Phase 3: Implement       model=adaptive        budget=\$${BUDGET}"
    log "[dry-run]     (simple/moderate -> ${IMPL_MODEL}, complex -> ${PLAN_MODEL})"
    log "[dry-run]   Phase 4: Review Code     model=${REVIEW_MODEL}  budget=\$2"
    log "[dry-run]   Phase 5: Fix Findings    model=${IMPL_MODEL}    budget=\$2"
    log "[dry-run]   Phase 6: CI Retry        model=${IMPL_MODEL}    budget=\$2/attempt (max 2)"
    log "[dry-run]   Total estimated: \$16-20"
    continue
  fi

  # ============================================================
  # Phase 1: Plan
  # ============================================================
  log "Phase 1: Plan (model: ${PLAN_MODEL})..."

  PROMPT_FILE="${ISSUE_WORK_DIR}/prompt.tmp"

  cat > "$PROMPT_FILE" <<PLAN_PROMPT
You are solving GitHub issue #${ISSUE_NUMBER} for the octopusgarden project.

Here is the issue content (already fetched -- do NOT fetch it from GitHub):

<issue>
${ISSUE_SNAPSHOT}
</issue>

Analyze the codebase to understand what needs to change. Read all relevant files.
Create a detailed implementation plan.

Output ONLY the final implementation plan with this structure:

## Issue #${ISSUE_NUMBER}: ${ISSUE_TITLE}

### Changes
(List each file to create/modify with a description of what changes)

### Tests
(List each test file and what test cases to add)

### Risks & Recommendations
(Bullet list of anything the implementer should watch out for)
PLAN_PROMPT

  run_phase "Phase 1: Plan" "$PLAN_MODEL" 1 "${ISSUE_WORK_DIR}/plan.md" "$PROMPT_FILE"
  validate_artifact "${ISSUE_WORK_DIR}/plan.md" 10

  # ============================================================
  # Phase 2: Review Plan (information barrier: sees plan, NOT issue)
  # ============================================================
  log "Phase 2: Review Plan (model: ${REVIEW_MODEL})..."

  PLAN_CONTENT=$(cat "${ISSUE_WORK_DIR}/plan.md")

  cat > "$PROMPT_FILE" <<REVIEW_PLAN_PROMPT
You are a senior Go architect reviewing an implementation plan for the octopusgarden project.
You are seeing ONLY the plan -- you do not have access to the original issue.
Your job is to evaluate the plan on its own merits.

<plan>
${PLAN_CONTENT}
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
- Reason: (one sentence explaining the rating)
REVIEW_PLAN_PROMPT

  run_phase "Phase 2: Review Plan" "$REVIEW_MODEL" 1 "${ISSUE_WORK_DIR}/reviewed-plan.md" "$PROMPT_FILE"
  validate_artifact "${ISSUE_WORK_DIR}/reviewed-plan.md" 10 "Complexity"

  # Parse complexity rating to select implementation model
  COMPLEXITY=$(grep -A1 "### Complexity" "${ISSUE_WORK_DIR}/reviewed-plan.md" | grep -i "Rating:" | sed 's/.*Rating:[[:space:]]*//' | tr -d '[:space:]' | tr '[:upper:]' '[:lower:]')
  log "  Complexity rating: ${COMPLEXITY:-unknown}"

  # Adaptive model selection (only if user did not override --impl-model)
  PHASE3_MODEL="$IMPL_MODEL"
  if ! $IMPL_MODEL_OVERRIDE && [[ "$COMPLEXITY" == "complex" ]]; then
    PHASE3_MODEL="$PLAN_MODEL"
    log "  Complex task: upgrading implementation model to ${PHASE3_MODEL}"
  fi

  # ============================================================
  # Phase 3: Implement (from reviewed plan, fresh context)
  # ============================================================
  log "Phase 3: Implement (model: ${PHASE3_MODEL}, budget: \$${BUDGET})..."

  REVIEWED_PLAN_CONTENT=$(cat "${ISSUE_WORK_DIR}/reviewed-plan.md")

  cat > "$PROMPT_FILE" <<IMPLEMENT_PROMPT
You are implementing a reviewed plan for the octopusgarden project.

Here is the implementation plan (already reviewed and approved by a senior architect):

<plan>
${REVIEWED_PLAN_CONTENT}
</plan>

Instructions:
1. Implement all changes described in the plan. Follow the coding standards in CLAUDE.md.
2. Write tests as specified in the plan.
3. Run \`make build && make test && make lint\` and fix any issues.
4. Stage and commit all changes with a conventional commit message (e.g., feat(package): description).
5. Do NOT push the branch. Do NOT create a PR. Only commit locally.
IMPLEMENT_PROMPT

  run_phase_nocapture "Phase 3: Implement" "$PHASE3_MODEL" "$BUDGET" "$PROMPT_FILE"

  # Validate: at least one new commit on branch
  COMMIT_COUNT=$(git rev-list --count main..HEAD)
  if [[ "$COMMIT_COUNT" -eq 0 ]]; then
    log "ERROR: Phase 3 produced no commits"
    exit 1
  fi
  log "  Phase 3 produced ${COMMIT_COUNT} commit(s)"

  # ============================================================
  # Phase 4: Review Code (information barrier: sees diff, NOT plan or issue)
  # ============================================================
  log "Phase 4: Review Code (model: ${REVIEW_MODEL})..."

  # Diff size guard: use stat + per-file for very large diffs
  DIFF_FULL=$(git diff main...HEAD)
  DIFF_SIZE=${#DIFF_FULL}

  if [[ "$DIFF_SIZE" -gt 100000 ]]; then
    log "  Large diff (${DIFF_SIZE} chars), using stat + per-file strategy"
    DIFF_STAT=$(git diff main...HEAD --stat)
    DIFF_TOP_FILES=$(git diff main...HEAD --stat --numstat | sort -k1 -rn | head -10 | awk '{print $3}' | xargs git diff main...HEAD -- | head -3000)
    DIFF_FOR_REVIEW="${DIFF_STAT}

--- Per-file diffs for largest changes (truncated) ---

${DIFF_TOP_FILES}"
  else
    DIFF_FOR_REVIEW="$DIFF_FULL"
  fi

  cat > "$PROMPT_FILE" <<REVIEW_CODE_PROMPT
You are a senior Go architect performing a cold code review for the octopusgarden project.
You are seeing ONLY the git diff -- you do not have the original issue or plan.
Your job is to evaluate the code changes on their own merits.

<diff>
${DIFF_FOR_REVIEW}
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
(PASS if 0 errors and 0 warnings. NEEDS CHANGES otherwise.)
REVIEW_CODE_PROMPT

  run_phase "Phase 4: Review Code" "$REVIEW_MODEL" 2 "${ISSUE_WORK_DIR}/review-findings.md" "$PROMPT_FILE"
  validate_artifact "${ISSUE_WORK_DIR}/review-findings.md" 3 "Summary"

  # Display review summary
  echo "--- Review Summary ---"
  extract_section "${ISSUE_WORK_DIR}/review-findings.md" "Summary" | head -10
  echo "---"

  # ============================================================
  # Phase 5: Fix Findings (skip if PASS with 0 errors, 0 warnings)
  # ============================================================
  ASSESSMENT=$(grep -i "Assessment:" "${ISSUE_WORK_DIR}/review-findings.md" | tail -1 | sed 's/.*Assessment:[[:space:]]*//' | tr -d '[:space:]' | tr '[:upper:]' '[:lower:]')

  if [[ "$ASSESSMENT" == "pass" ]]; then
    log "Phase 5: Skipped (review assessment: PASS)"
  else
    log "Phase 5: Fix Findings (model: ${IMPL_MODEL})..."

    REVIEW_FINDINGS=$(cat "${ISSUE_WORK_DIR}/review-findings.md")

    cat > "$PROMPT_FILE" <<FIX_PROMPT
You are fixing code review findings for the octopusgarden project.

A senior architect reviewed the current changes and found issues that need fixing:

<review-findings>
${REVIEW_FINDINGS}
</review-findings>

Instructions:
1. Fix ALL errors and warnings listed in the findings. Nits are optional but encouraged.
2. Run \`make build && make test && make lint\` and fix any issues.
3. Stage all fixes and amend the previous commit: \`git add -A && git commit --amend --no-edit\`
4. Do NOT push. Do NOT create a PR.
FIX_PROMPT

    run_phase_nocapture "Phase 5: Fix Findings" "$IMPL_MODEL" 2 "$PROMPT_FILE"
  fi

  # --- Push and create PR (script-controlled, not Claude) ---
  log "Pushing branch and creating PR..."
  git push --force-with-lease -u origin "$BRANCH"
  create_pr "$BRANCH" "$ISSUE_NUMBER" "$ISSUE_TITLE" "$ISSUE_WORK_DIR"

  PR_NUMBER=$(gh pr list --repo "$REPO" --head "$BRANCH" --json number --jq ".[0].number" 2>/dev/null)

  if [[ -z "$PR_NUMBER" || "$PR_NUMBER" == "null" ]]; then
    log "ERROR: No PR found for branch ${BRANCH} after creation"
    exit 1
  fi

  PR_URL="https://github.com/${REPO}/pull/${PR_NUMBER}"
  log "PR created: ${PR_URL}"

  if ! $MERGE; then
    log "Skipping merge (--no-merge). PR is ready for review: ${PR_URL}"
    gh issue unlock "$ISSUE_NUMBER" --repo "$REPO" 2>/dev/null || true
    continue
  fi

  # ============================================================
  # Phase 6: CI Retry Loop
  # ============================================================
  log "Phase 6: CI check and retry..."

  CI_RETRIES=0
  MAX_CI_RETRIES=2

  while true; do
    if wait_for_ci "$PR_NUMBER"; then
      break
    fi

    CI_RETRIES=$((CI_RETRIES + 1))
    if [[ $CI_RETRIES -gt $MAX_CI_RETRIES ]]; then
      log "CI failed after ${MAX_CI_RETRIES} retries. PR: ${PR_URL}"
      exit 1
    fi

    log "CI retry ${CI_RETRIES}/${MAX_CI_RETRIES}..."

    # Get failed run logs
    FAILED_RUN_ID=$(gh run list --repo "$REPO" --branch "$BRANCH" --status failure --json databaseId --jq ".[0].databaseId" 2>/dev/null || echo "")
    CI_LOGS=""
    if [[ -n "$FAILED_RUN_ID" && "$FAILED_RUN_ID" != "null" ]]; then
      CI_LOGS=$(gh run view "$FAILED_RUN_ID" --repo "$REPO" --log-failed 2>/dev/null | tail -200 || echo "No logs available")
    fi

    cat > "$PROMPT_FILE" <<CI_FIX_PROMPT
The CI checks failed for the octopusgarden project. Fix the failures.

<ci-logs>
${CI_LOGS:-No CI logs available. Check make build, make test, and make lint.}
</ci-logs>

Instructions:
1. Analyze the CI failure logs above.
2. Fix the issues in the code.
3. Run \`make build && make test && make lint\` locally to verify.
4. Stage all fixes and amend the commit: \`git add -A && git commit --amend --no-edit\`
5. Do NOT push. Do NOT create a PR.
CI_FIX_PROMPT

    run_phase_nocapture "Phase 6: CI Fix (retry ${CI_RETRIES})" "$IMPL_MODEL" 2 "$PROMPT_FILE"

    # Push the fix
    git push --force-with-lease origin "$BRANCH"
  done

  # --- Merge ---
  log "Merging PR #${PR_NUMBER}..."
  gh pr merge "$PR_NUMBER" --repo "$REPO" --squash --delete-branch

  gh issue unlock "$ISSUE_NUMBER" --repo "$REPO" 2>/dev/null || true

  log "Done! Issue #${ISSUE_NUMBER} resolved and merged."
  echo "    PR: ${PR_URL}"
done

if [[ $TOTAL -gt 1 ]]; then
  log "===== All ${TOTAL} issues processed ====="
fi
