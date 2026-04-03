---
name: pr-review
description: Fetch PR review comments, CI check results, and failure details.
---

# PR Review

Run `bash scripts/pr-status-check.sh` to fetch all review comments (including
CodeRabbit) and CI check statuses for the current branch's open PR.

**Do not use raw `gh api` or `gh pr` commands to fetch this data.** The script
handles review aggregation, deduplication, resolution status, severity
extraction, failed check logs, and agent-friendly formatting.

After the script completes, present:

1. A table of CI check statuses (PASS/FAIL/RUNNING/PENDING/SKIP)
2. Any failed check details and error logs
3. A numbered list of OPEN review comments grouped by file, with severity and
   summary
4. A brief note on RESOLVED comments

If the script fails (no open PR, not authenticated), report the error clearly.
