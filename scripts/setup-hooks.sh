#!/usr/bin/env sh
# Set up tracked Git hooks for Hover.
# This updates the shared repository config so future worktrees inherit it.

set -eu

hooks_path=".githooks"

printf 'Setting up Git hooks...\n'
git config core.hooksPath "$hooks_path"

printf 'Git hooks configured successfully.\n'
printf 'Shared hooks path: %s\n' "$hooks_path"
printf 'Future worktrees created from this clone will inherit the same hooks path.\n'
printf '\n'
printf 'Active hooks:\n'
printf '  pre-commit: formats and validates staged changes\n'
printf '  commit-msg: blocks AI attribution in commit messages\n'
printf '\n'
printf 'To commit without running hooks (not recommended):\n'
printf '  git commit --no-verify\n'
