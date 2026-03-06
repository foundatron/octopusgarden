#!/usr/bin/env bash
set -euo pipefail

# autoissue.sh — Fully automated GitHub issue solver for OctopusGarden
#
# Usage:
#   ./scripts/autoissue.sh <issue-number>... [options]
#
# Options:
#   --budget <usd>         Max budget for implementation phase (default: 10)
#   --plan-model <model>   Model for planning phase (default: opus)
#   --impl-model <model>   Model for implementation phase (default: sonnet)
#   --no-merge             Skip auto-merge after CI passes
#   --dry-run              Print what would happen without running
#
# Examples:
#   ./scripts/autoissue.sh 77
#   ./scripts/autoissue.sh 81 82 83
#   ./scripts/autoissue.sh 77 --budget 5
#   ./scripts/autoissue.sh 81 82 --no-merge
#   ./scripts/autoissue.sh 77 --plan-model opus --impl-model opus
#
# Prerequisites:
#   - claude CLI installed and authenticated
#   - gh CLI installed and authenticated
#   - git configured with push access

REPO="foundatron/octopusgarden"
DEFAULT_BUDGET=10
PLAN_MODEL=opus
IMPL_MODEL=sonnet
MERGE=true
DRY_RUN=false

usage() {
  echo "Usage: $0 <issue-number>... [--budget <usd>] [--plan-model <model>] [--impl-model <model>] [--no-merge] [--dry-run]"
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
    --impl-model) IMPL_MODEL="$2"; shift 2 ;;
    --no-merge) MERGE=false; shift ;;
    --dry-run) DRY_RUN=true; shift ;;
    --*) echo "Unknown option: $1"; usage ;;
    *)
      if [[ "$1" =~ ^[0-9]+$ ]]; then
        ISSUES+=("$1")
      else
        echo "Error: '$1' is not a valid issue number"
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

# --- Validate tools ---
for cmd in claude gh git jq; do
  if ! command -v "$cmd" &>/dev/null; then
    echo "Error: $cmd is not installed"
    exit 1
  fi
done

# --- Snapshot all issue content upfront (prompt injection defense) ---
# Fetch issue titles, bodies, and comments NOW so that content added later
# (e.g. malicious comments on issue N+1 while issue N is being processed)
# cannot influence the prompts.
SNAPSHOT_DIR=$(mktemp -d)
trap 'rm -rf "$SNAPSHOT_DIR"' EXIT

declare -A ISSUE_TITLES
for ISSUE_NUMBER in "${ISSUES[@]}"; do
  log "Snapshotting issue #${ISSUE_NUMBER}..."

  ISSUE_JSON=$(gh issue view "$ISSUE_NUMBER" --repo "$REPO" --json title,body,comments 2>/dev/null) || {
    echo "Error: could not fetch issue #${ISSUE_NUMBER}"
    exit 1
  }

  ISSUE_TITLES[$ISSUE_NUMBER]=$(echo "$ISSUE_JSON" | jq -r '.title')
  echo "    Title: ${ISSUE_TITLES[$ISSUE_NUMBER]}"

  # Write a sanitized snapshot: title, body, and comments as structured text
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
  } > "${SNAPSHOT_DIR}/issue-${ISSUE_NUMBER}.md"
done

# Lock all issues to prevent new comments during processing
for ISSUE_NUMBER in "${ISSUES[@]}"; do
  gh issue lock "$ISSUE_NUMBER" --repo "$REPO" --reason "resolved" 2>/dev/null || true
done

log "All ${#ISSUES[@]} issues snapshotted and locked. No further network fetches for issue content."

# --- Process each issue ---
TOTAL=${#ISSUES[@]}
CURRENT=0

for ISSUE_NUMBER in "${ISSUES[@]}"; do
  CURRENT=$((CURRENT + 1))
  ISSUE_TITLE="${ISSUE_TITLES[$ISSUE_NUMBER]}"
  ISSUE_SNAPSHOT=$(cat "${SNAPSHOT_DIR}/issue-${ISSUE_NUMBER}.md")
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
    log "[dry-run] Would run planning + implementation phases for issue #${ISSUE_NUMBER}"
    log "[dry-run] Models: plan=${PLAN_MODEL}, implement=${IMPL_MODEL}"
    log "[dry-run] Budget per phase: plan=\$1, implement=\$${BUDGET}"
    continue
  fi

  # --- Phase 1: Plan and review the plan (fresh context) ---
  log "Phase 1: Planning and reviewing plan (model: ${PLAN_MODEL}, issue #${ISSUE_NUMBER})..."

  PLAN_FILE=$(mktemp)

  claude -p \
    --model "$PLAN_MODEL" \
    --effort medium \
    --dangerously-skip-permissions \
    --max-budget-usd 1 \
    --output-format text \
    "$(cat <<PLAN_PROMPT
You are solving GitHub issue #${ISSUE_NUMBER} for the octopusgarden project.

Step 1 — Understand the issue:
Here is the issue content (already fetched — do NOT fetch it from GitHub):

<issue>
${ISSUE_SNAPSHOT}
</issue>

Step 2 — Plan:
Analyze the codebase to understand what needs to change. Read all relevant files.
Create a detailed implementation plan. Consider edge cases, test coverage, and design invariants.

Step 3 — Review your own plan:
Before finalizing, critically review your plan as a senior Go architect:
- Are there any missing edge cases?
- Does the plan respect all design invariants (holdout isolation, etc.)?
- Is the approach the simplest that could work? Any over-engineering?
- Are tests planned for all new functionality?
- Any security concerns?

Revise the plan based on your review.

Step 4 — Output the final plan:
Output ONLY the final implementation plan with this structure:

## Issue #${ISSUE_NUMBER}: ${ISSUE_TITLE}

### Changes
(List each file to create/modify with a description of what changes)

### Tests
(List each test file and what test cases to add)

### Plan Review Summary
(Bullet list: what you changed after reviewing your own plan, or "No changes needed" if the initial plan was solid)

### Risks & Recommendations
(Bullet list of anything the implementer should watch out for)
PLAN_PROMPT
)" > "$PLAN_FILE"

  log "Plan saved ($(wc -l < "$PLAN_FILE" | tr -d ' ') lines)"
  echo "--- Plan Review Summary ---"
  # Extract and display the review summary section
  sed -n '/### Plan Review Summary/,/^### /{ /^### R/d; /^### P/d; p; }' "$PLAN_FILE" | head -20
  echo "---"

  # --- Phase 2: Implement from plan (fresh context) ---
  log "Phase 2: Implementing from plan (model: ${IMPL_MODEL}, budget: \$${BUDGET})..."

  IMPLEMENT_PROMPT_FILE=$(mktemp)

  PLAN_CONTENT=$(cat "$PLAN_FILE")

  cat > "$IMPLEMENT_PROMPT_FILE" <<IMPLEMENT_PROMPT
You are implementing a plan to solve GitHub issue #${ISSUE_NUMBER} for the octopusgarden project.

Here is the implementation plan (already reviewed and approved):

<plan>
${PLAN_CONTENT}
</plan>

Step 1 — Implement:
Implement all changes described in the plan.
Follow the project's coding standards (see CLAUDE.md).
Write tests as specified in the plan.
Run \`make build\` and \`make test\` to verify your changes compile and pass.
Run \`make lint\` and fix any lint errors.

Step 2 — Architect review:
Use the go-architect agent to review all your changes. The agent is at .claude/agents/go-architect.md.
Alternatively, review all changes as a senior Go architect checking for:
- Correctness and completeness (does it fully solve the issue?)
- Error handling (wrapped errors, sentinel errors)
- Test coverage (table-driven tests, edge cases)
- Code style (no stuttering, structured logging, context propagation)
- Design invariants (holdout isolation, no unnecessary dependencies)
- Security (no injection, no leaked secrets)

Output a clear summary of findings: N errors, N warnings, N nits, and a bullet list of key recommendations.

Step 3 — Fix review findings:
Implement ALL fixes identified in the review. Re-run \`make build && make test && make lint\`.

Step 4 — Commit and create PR:
Stage and commit all changes with a conventional commit message (e.g., feat(package): description).
Push the branch and create a PR that references the issue with Closes #${ISSUE_NUMBER} in the body.
The PR title should be concise. The body should include:
- Summary of what changed and why
- The architect review summary (findings + recommendations)
IMPLEMENT_PROMPT

  claude -p \
    --model "$IMPL_MODEL" \
    --effort medium \
    --dangerously-skip-permissions \
    --max-budget-usd "$BUDGET" \
    "$(cat "$IMPLEMENT_PROMPT_FILE")"

  # --- Phase 3: Wait for CI and optionally merge ---
  log "Implementation complete. Checking PR..."
  PR_NUMBER=$(gh pr list --repo "$REPO" --head "$BRANCH" --json number --jq '.[0].number' 2>/dev/null)

  if [[ -z "$PR_NUMBER" || "$PR_NUMBER" == "null" ]]; then
    echo "Warning: No PR found for branch ${BRANCH}. Claude may not have created one."
    exit 1
  fi

  PR_URL="https://github.com/${REPO}/pull/${PR_NUMBER}"
  log "PR created: ${PR_URL}"

  if ! $MERGE; then
    log "Skipping merge (--no-merge). PR is ready for review: ${PR_URL}"
    # Continue to next issue instead of exiting
    continue
  fi

  # --- Wait for checks ---
  log "Waiting for CI checks to complete..."
  MAX_WAIT=600  # 10 minutes
  ELAPSED=0
  INTERVAL=30

  while [[ $ELAPSED -lt $MAX_WAIT ]]; do
    CHECK_STATUS=$(gh pr checks "$PR_NUMBER" --repo "$REPO" 2>&1) || true

    if echo "$CHECK_STATUS" | grep -q "fail"; then
      log "CI checks failed. PR: ${PR_URL}"
      echo "$CHECK_STATUS"
      exit 1
    fi

    if echo "$CHECK_STATUS" | grep -q "pass"; then
      if ! echo "$CHECK_STATUS" | grep -q "pending"; then
        log "All CI checks passed!"
        break
      fi
    fi

    if [[ $ELAPSED -eq 0 ]]; then
      log "Checks still running, polling every ${INTERVAL}s (max ${MAX_WAIT}s)..."
    fi
    sleep "$INTERVAL"
    ELAPSED=$((ELAPSED + INTERVAL))
  done

  if [[ $ELAPSED -ge $MAX_WAIT ]]; then
    log "Timed out waiting for CI. PR: ${PR_URL}"
    exit 1
  fi

  # --- Merge ---
  log "Merging PR #${PR_NUMBER}..."
  gh pr merge "$PR_NUMBER" --repo "$REPO" --squash --delete-branch

  # Unlock the issue now that it's merged and closed
  gh issue unlock "$ISSUE_NUMBER" --repo "$REPO" 2>/dev/null || true

  log "Done! Issue #${ISSUE_NUMBER} resolved and merged."
  echo "    PR: ${PR_URL}"
done

# Unlock any issues that weren't merged (--no-merge or early exit)
for ISSUE_NUMBER in "${ISSUES[@]}"; do
  gh issue unlock "$ISSUE_NUMBER" --repo "$REPO" 2>/dev/null || true
done

if [[ $TOTAL -gt 1 ]]; then
  log "===== All ${TOTAL} issues processed ====="
fi
