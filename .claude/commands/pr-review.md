---
name: pr-review
description: Fetch CodeRabbit comments and CI check results for the current PR and write them to .claude/pr-review.md
---

Run `bash scripts/pr-review-summary.sh` to collect all CodeRabbit inline comments, the walkthrough summary, and CI check statuses for the current branch's open PR.

Output is written to `.claude/pr-review.md`.

After the script completes, read `.claude/pr-review.md` and present:
1. A table of CI check statuses
2. A numbered list of unresolved CodeRabbit inline comments grouped by file, with file path, line number, severity, and the comment body
3. A brief note on any comments that are already addressed (mention the fixing commit if visible in the body)

If the script fails (no open PR, not authenticated), report the error clearly.
