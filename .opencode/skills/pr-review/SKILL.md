---
name: pr-review
description:
  Fetch PR status, CI checks, and review comments (including CodeRabbit), then
  resolve actionable items with one commit per fix.
---

# PR Review

You are the PR Review skill.

Use this whenever the user asks to check PR status, review comments, CI results,
or process CodeRabbit feedback.

## Fetching review data

Run `bash scripts/pr-status-check.sh [PR_NUMBER]` to get structured output.
The PR number is optional — it auto-detects from the current branch.

Output sections:
- **CI CHECKS** — PASS/FAIL/RUNNING/PENDING/SKIP per check
- **CODERABBIT REVIEW COMMENTS** — deduplicated, with FILE/LINE/SEVERITY/SUMMARY
- **CODERABBIT AGENT PROMPT** — actionable instructions from the latest review

**Do not use raw `gh api` or `gh pr` commands to fetch review data.** The script
handles review aggregation, deduplication, severity extraction, and
agent-friendly formatting. Using `gh` directly wastes tokens and produces
inconsistent results.

## Resolving comments

1. Run `bash scripts/pr-status-check.sh` and read the output.
2. From the REVIEW COMMENTS section, build a numbered action list with file
   path, severity, and minimal required change.
3. Ignore comments already resolved in a prior commit unless explicitly asked.
4. Handle comments one by one:
   - Implement the smallest safe code change for each comment.
   - Do not edit `.md` files unless explicitly requested.
   - Run targeted validation for that area.
   - Create exactly one commit per resolved comment.
5. For any comment that cannot be fixed safely:
   - Add a PR-thread reply explaining the blocker and why it is being skipped.
   - Include concrete conditions for reconsideration.
6. Continue until all actionable comments are processed.
7. Re-run `bash scripts/pr-status-check.sh` to confirm no unresolved items
   remain, then push all commits in one final push.
8. Final response: list commit hash per resolved comment, skipped comments with
   reply text, validation run results, and push confirmation.
