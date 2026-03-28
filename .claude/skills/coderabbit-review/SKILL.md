---
name: coderabbit-review
description:
  Resolve Coderabbit PR comments with one commit per actionable item and
  explicit PR-thread acknowledgement for skipped comments.
---

# Coderabbit Review

You are the Coderabbit Review skill.

Use this whenever the user asks to process current PR Coderabbit comments.

1. Confirm GitHub CLI authentication with `gh auth status`.
   - If not authenticated, ask for authentication and pause.
2. Locate the open PR for the current branch (`gh pr status`); stop if none
   exists.
3. Run `bash scripts/pr-review-summary.sh` to generate `.claude/pr-review.md`.
   - This fetches all CodeRabbit inline comments, the walkthrough summary, and
     CI check statuses into a single structured file.
   - Read `.claude/pr-review.md` as your source of truth for what needs doing.
4. From the inline comments section, build a numbered action list with file path
   and minimal required change. Ignore comments already marked as resolved (✅
   Addressed in commit …) unless explicitly requested.
5. Handle comments one by one.
   - Implement the smallest safe code change for each comment.
   - Do not edit `.md` files unless explicitly requested.
   - Run targeted validation for that area.
   - Create exactly one commit per resolved comment.
6. For any comment that cannot be fixed safely:
   - add a PR-thread reply explaining the blocker and why it is being skipped
   - include concrete conditions for reconsideration
7. Continue until all actionable comments are processed.
8. Re-run `bash scripts/pr-review-summary.sh` to refresh `.claude/pr-review.md`
   and confirm no unresolved items remain, then push all commits in one final
   push.
9. Final response: list commit hash per resolved comment, skipped comments with
   reply text, validation run results, and push confirmation.
