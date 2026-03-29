---
name: pr-review
description:
  Fetch CodeRabbit comments and CI check results for the current PR.
---

Run `bash scripts/pr-status-check.sh` to fetch all CodeRabbit review comments
and CI check statuses for the current branch's open PR.

After the script completes, present:

1. A table of CI check statuses (PASS/FAIL/RUNNING/PENDING/SKIP)
2. A numbered list of unresolved CodeRabbit inline comments grouped by file,
   with file path, line number, severity, and the comment summary
3. A brief note on any comments that are already addressed

If the script fails (no open PR, not authenticated), report the error clearly.
