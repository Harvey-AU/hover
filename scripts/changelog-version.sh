#!/usr/bin/env bash
# changelog-version.sh — shared changelog parsing and version calculation.
# Outputs (via $GITHUB_OUTPUT or stdout):
#   release_type   — major, minor, or patch
#   should_release — true if [Unreleased] has content
#   changelog_content — the unreleased content (heredoc-safe)
#   current_version — current git tag (e.g. v0.7.0)
#   next_version    — bumped version (e.g. v0.7.1)
#   cli_changed     — true if cmd/hover/ or npm/ changed
#   cli_current     — current CLI tag
#   cli_next        — next CLI tag
#
# Usage:
#   source scripts/changelog-version.sh
#   # or in CI: bash scripts/changelog-version.sh

set -euo pipefail

# --- Release type from changelog header ---
if ! grep -q "^## \[Unreleased" CHANGELOG.md; then
  echo "should_release=false" >> "${GITHUB_OUTPUT:-/dev/null}"
  echo "No [Unreleased] section found — skipping release"
  if [ -z "${GITHUB_OUTPUT:-}" ]; then
    echo "should_release=false"
  fi
  exit 0
fi

CHANGELOG_HEADER=$(grep "^## \[Unreleased" CHANGELOG.md | head -1)

if echo "$CHANGELOG_HEADER" | grep -qi "\[Unreleased:major\]"; then
  RELEASE_TYPE="major"
elif echo "$CHANGELOG_HEADER" | grep -qi "\[Unreleased:minor\]"; then
  RELEASE_TYPE="minor"
else
  RELEASE_TYPE="patch"
fi

# --- Extract unreleased content ---
UNRELEASED_CONTENT=$(awk '/^## \[Unreleased/ {flag=1; next} /^## \[[0-9]/ {flag=0} flag' CHANGELOG.md)

if [ -z "$(echo "$UNRELEASED_CONTENT" | grep -v '^[[:space:]]*$')" ]; then
  SHOULD_RELEASE="false"
else
  SHOULD_RELEASE="true"
fi

# --- Calculate version bump ---
CURRENT_TAG=$(git tag -l 'v[0-9]*' --sort=-version:refname | head -1)
CURRENT_TAG=${CURRENT_TAG:-v0.6.4}
CURRENT_VERSION=${CURRENT_TAG#v}
CURRENT_VERSION=${CURRENT_VERSION%%-*}

IFS='.' read -r MAJOR MINOR PATCH <<< "$CURRENT_VERSION"

if [ "$RELEASE_TYPE" = "major" ]; then
  MAJOR=$((MAJOR + 1)); MINOR=0; PATCH=0
elif [ "$RELEASE_TYPE" = "minor" ]; then
  MINOR=$((MINOR + 1)); PATCH=0
else
  PATCH=$((PATCH + 1))
fi

NEXT_VERSION="v${MAJOR}.${MINOR}.${PATCH}"

# --- CLI change detection ---
CLI_CHANGED="false"
CLI_CURRENT=""
CLI_NEXT=""
COMPARE_REF="${COMPARE_REF:-origin/main}"

if git diff --name-only "${COMPARE_REF}"...HEAD -- cmd/hover/ npm/ 2>/dev/null | grep -q .; then
  CLI_CHANGED="true"
  LATEST_CLI_TAG=$(git tag -l 'cli-v[0-9]*.[0-9]*.[0-9]*' --sort=-version:refname | head -1)
  if [ -z "$LATEST_CLI_TAG" ]; then
    CLI_CURRENT="cli-v0.0.0"
    CLI_NEXT="cli-v0.1.0"
  else
    CLI_CURRENT="$LATEST_CLI_TAG"
    CLI_VER="${LATEST_CLI_TAG#cli-v}"
    IFS='.' read -r CMAJ CMIN CPAT <<< "$CLI_VER"
    CPAT=$((CPAT + 1))
    CLI_NEXT="cli-v${CMAJ}.${CMIN}.${CPAT}"
  fi
fi

# --- Write outputs ---
if [ -n "${GITHUB_OUTPUT:-}" ]; then
  {
    echo "release_type=$RELEASE_TYPE"
    echo "should_release=$SHOULD_RELEASE"
    echo "current_version=$CURRENT_TAG"
    echo "next_version=$NEXT_VERSION"
    echo "cli_changed=$CLI_CHANGED"
    echo "cli_current=$CLI_CURRENT"
    echo "cli_next=$CLI_NEXT"
  } >> "$GITHUB_OUTPUT"

  # Changelog content as heredoc (safe for multiline)
  if [ "$SHOULD_RELEASE" = "true" ]; then
    {
      echo "changelog_content<<CHANGELOG_EOF"
      echo "$UNRELEASED_CONTENT"
      echo "CHANGELOG_EOF"
    } >> "$GITHUB_OUTPUT"
  fi
else
  echo "release_type=$RELEASE_TYPE"
  echo "should_release=$SHOULD_RELEASE"
  echo "current_version=$CURRENT_TAG"
  echo "next_version=$NEXT_VERSION"
  echo "cli_changed=$CLI_CHANGED"
  echo "cli_current=$CLI_CURRENT"
  echo "cli_next=$CLI_NEXT"
fi
