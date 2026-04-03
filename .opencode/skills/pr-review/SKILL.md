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

Run `bash scripts/pr-status-check.sh [PR_NUMBER]` to get structured output. The
PR number is optional — it auto-detects from the current branch.

Output sections:

- **CI CHECKS** — PASS/FAIL/RUNNING/PENDING/SKIP per check
- **CODERABBIT REVIEW COMMENTS** — deduplicated, with
  STATUS/FILE/LINE/SEVERITY/SUMMARY
- **CODERABBIT AGENT PROMPT** — actionable instructions from the latest review

**Do not use raw `gh api` or `gh pr` commands to fetch review data.** The script
handles review aggregation, deduplication, severity extraction, and
agent-friendly formatting. Using `gh` directly wastes tokens and produces
inconsistent results.

## Replying to and resolving comments

Use `bash scripts/pr-comment-reply.sh` to reply to, resolve, or skip review
threads. **Do not use raw `gh api` GraphQL mutations** — the script handles
thread indexing and mutations.

```bash
# List open threads (indexed)
bash scripts/pr-comment-reply.sh --list

# Reply to a thread
bash scripts/pr-comment-reply.sh --reply INDEX "message"

# Reply and resolve in one step
bash scripts/pr-comment-reply.sh --reply INDEX "message" --resolve

# Just resolve (no reply)
bash scripts/pr-comment-reply.sh --resolve INDEX
```

Thread indexes are 1-based and match the `--list` output. After resolving or
replying, the index numbers shift — always re-run `--list` before the next
action.

## Workflow

1. Run `bash scripts/pr-status-check.sh` and read the output.
2. From the CODERABBIT REVIEW COMMENTS section, build a numbered action list
   with file path, severity, and minimal required change.
3. Ignore comments already resolved in a prior commit unless explicitly asked.
4. Handle comments one by one:
   - **Actionable:** implement the smallest safe code change, run targeted
     validation, create one commit per fix, then resolve the thread with
     `pr-comment-reply.sh --reply INDEX "Fixed in <hash>" --resolve`.
   - **Skipped/deferred:** reply with the reason and resolve:
     `pr-comment-reply.sh --reply INDEX "Deferring — <reason>" --resolve`.
   - Do not edit `.md` files unless explicitly requested.
5. Continue until all actionable comments are processed.
6. Re-run `bash scripts/pr-status-check.sh` to confirm no unresolved items
   remain, then push all commits in one final push.
7. Final response: list commit hash per resolved comment, skipped comments with
   reply text, validation run results, and push confirmation.
