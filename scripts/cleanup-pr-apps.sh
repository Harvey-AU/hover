#!/bin/bash
# Cleanup script for orphaned Fly.io PR preview apps
# Run this manually to clean up apps from closed PRs

set -e

# Check if flyctl is installed
if ! command -v flyctl &> /dev/null; then
    echo "Error: flyctl is not installed"
    echo "Install with: brew install flyctl"
    exit 1
fi

# Check if gh is installed
if ! command -v gh &> /dev/null; then
    echo "Error: gh (GitHub CLI) is not installed"
    echo "Install with: brew install gh"
    exit 1
fi

echo "🔍 Fetching all Fly.io apps..."
FLY_APPS=$(flyctl apps list | grep "hover-pr-" | awk '{print $1}' || true)

if [ -z "$FLY_APPS" ]; then
    echo "✅ No PR apps found!"
    exit 0
fi

echo "📋 Found PR apps:"
echo "$FLY_APPS"
echo ""

# Get list of open PRs
echo "🔍 Fetching open PRs from GitHub..."
OPEN_PRS=$(gh pr list --state open --json number --jq '.[].number' || true)

if [ -z "$OPEN_PRS" ]; then
    echo "⚠️  Could not fetch open PRs. Make sure you're authenticated with 'gh auth login'"
    echo "Continuing anyway - will prompt for each app..."
    OPEN_PRS=""
fi

echo "📋 Open PRs: ${OPEN_PRS:-none}"
echo ""

# Process each app
for APP in $FLY_APPS; do
    # Extract PR number from app name (e.g., hover-pr-114 -> 114)
    PR_NUM=$(echo "$APP" | sed 's/hover-pr-//')

    # Check if this PR is still open
    if echo "$OPEN_PRS" | grep -q "^${PR_NUM}$"; then
        echo "⏭️  Skipping $APP (PR #$PR_NUM is still open)"
        continue
    fi

    # App is from a closed PR
    echo "🗑️  Deleting $APP (PR #$PR_NUM is closed)..."

    if flyctl apps destroy "$APP" --yes 2>/dev/null; then
        echo "✅ Deleted $APP"
    else
        echo "⚠️  Failed to delete $APP (might not exist or insufficient permissions)"
    fi

    echo ""
done

echo "✨ Cleanup complete!"
