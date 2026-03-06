Solve GitHub issue #$ARGUMENTS for the octopusgarden project.

Step 1 — Understand the issue:
Fetch and read the full issue: https://github.com/foundatron/octopusgarden/issues/$ARGUMENTS
Read all comments on the issue.

Step 2 — Plan:
Analyze the codebase to understand what needs to change. Read all relevant files.
Create a detailed implementation plan. Consider edge cases, test coverage, and design invariants.
Present the plan for review.

Step 3 — Implement:
After the plan is approved, implement all changes.
Follow project coding standards (CLAUDE.md).
Write table-driven tests for new functionality.
Run `make build && make test && make lint` and fix any issues.

Step 4 — Architect review:
Review all changes as a senior Go architect. Check for:
- Correctness and completeness
- Error handling (wrapped errors, sentinel errors)
- Test coverage (table-driven, edge cases)
- Code style (no stuttering, structured logging, context propagation)
- Design invariants (holdout isolation, no unnecessary dependencies)
- Security (no injection, no leaked secrets)
List all recommended changes.

Step 5 — Fix review findings:
Implement ALL fixes from the architect review.
Re-run `make build && make test && make lint`.

Step 6 — Commit and PR:
Commit with a conventional commit message.
Push and create a PR with "Closes #$ARGUMENTS" in the body.
