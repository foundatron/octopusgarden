#!/usr/bin/env bash
set -euo pipefail

# autoissue.sh — Fully automated GitHub issue solver for OctopusGarden
#
# Usage:
#   ./scripts/autoissue.sh <issue-number> [--budget <usd>] [--no-merge] [--dry-run]
#
# Examples:
#   ./scripts/autoissue.sh 77
#   ./scripts/autoissue.sh 77 --budget 5
#   ./scripts/autoissue.sh 77 --no-merge
#
# Prerequisites:
#   - claude CLI installed and authenticated
#   - gh CLI installed and authenticated
#   - git configured with push access

REPO="foundatron/octopusgarden"
ISSUE_URL="https://github.com/${REPO}/issues"
DEFAULT_BUDGET=10
MERGE=true
DRY_RUN=false

usage() {
  echo "Usage: $0 <issue-number> [--budget <usd>] [--no-merge] [--dry-run]"
  exit 1
}

# --- Parse args ---
[[ $# -lt 1 ]] && usage
ISSUE_NUMBER="$1"
shift

BUDGET="$DEFAULT_BUDGET"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --budget) BUDGET="$2"; shift 2 ;;
    --no-merge) MERGE=false; shift ;;
    --dry-run) DRY_RUN=true; shift ;;
    *) echo "Unknown option: $1"; usage ;;
  esac
done

if ! [[ "$ISSUE_NUMBER" =~ ^[0-9]+$ ]]; then
  echo "Error: issue number must be a positive integer"
  exit 1
fi

log() { echo "==> $*"; }

# --- Validate tools ---
for cmd in claude gh git; do
  if ! command -v "$cmd" &>/dev/null; then
    echo "Error: $cmd is not installed"
    exit 1
  fi
done

# --- Fetch issue title for branch name ---
log "Fetching issue #${ISSUE_NUMBER}..."
ISSUE_TITLE=$(gh issue view "$ISSUE_NUMBER" --repo "$REPO" --json title --jq '.title' 2>/dev/null) || {
  echo "Error: could not fetch issue #${ISSUE_NUMBER}"
  exit 1
}
echo "    Title: ${ISSUE_TITLE}"

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
  log "[dry-run] Would invoke claude with --model opus --effort medium --max-budget-usd ${BUDGET}"
  log "[dry-run] Prompt: solve issue #${ISSUE_NUMBER}, review, fix, commit, create PR"
  exit 0
fi

# --- Phase 1: Plan and implement ---
log "Phase 1: Planning and implementing (budget: \$${BUDGET})..."

PROMPT_FILE=$(mktemp)
trap 'rm -f "$PROMPT_FILE"' EXIT
cat > "$PROMPT_FILE" <<PROMPT
You are solving GitHub issue #${ISSUE_NUMBER} for the octopusgarden project.

Step 1 — Understand the issue:
Read the issue: ${ISSUE_URL}/${ISSUE_NUMBER}
Read all comments on the issue as well.

Step 2 — Plan:
Analyze the codebase to understand what needs to change. Read relevant files.
Create a detailed implementation plan. Consider edge cases and test coverage.

Step 3 — Implement:
Implement all changes needed to resolve the issue.
Follow the project's coding standards (see CLAUDE.md).
Write tests for new functionality.
Run \`make build\` and \`make test\` to verify your changes compile and pass.
Run \`make lint\` and fix any lint errors.

Step 4 — Self-review:
Review all your changes as if you were a senior Go architect. Check for:
- Correctness and completeness (does it fully solve the issue?)
- Error handling (wrapped errors, sentinel errors)
- Test coverage (table-driven tests, edge cases)
- Code style (no stuttering, structured logging, context propagation)
- Design invariants (holdout isolation, no unnecessary dependencies)
- Security (no injection, no leaked secrets)

Step 5 — Fix issues from review:
Implement ALL fixes identified in the review. Re-run \`make build && make test && make lint\`.

Step 6 — Commit and create PR:
Stage and commit all changes with a conventional commit message (e.g., feat(package): description).
Push the branch and create a PR that references the issue with Closes #${ISSUE_NUMBER} in the body.
The PR title should be concise. The body should summarize what changed and why.
PROMPT

claude -p \
  --model opus \
  --effort medium \
  --dangerously-skip-permissions \
  --max-budget-usd "$BUDGET" \
  "$(cat "$PROMPT_FILE")"

# --- Phase 2: Wait for CI and optionally merge ---
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
  exit 0
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

log "Done! Issue #${ISSUE_NUMBER} resolved and merged."
echo "    PR: ${PR_URL}"
