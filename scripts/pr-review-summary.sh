#!/usr/bin/env bash
# pr-review-summary.sh — Collect CodeRabbit comments and CI check results for
# the current branch's open PR, and write them to a structured markdown file.
#
# Usage: bash scripts/pr-review-summary.sh [output-file]
# Default output: .claude/pr-review.md
#
# Requires: gh (GitHub CLI), authenticated with repo access.

set -euo pipefail

OUTPUT="${1:-.claude/pr-review.md}"

# ── Locate PR ─────────────────────────────────────────────────────────────────

PR_JSON=$(gh pr view --json number,title,url,state,headRefName 2>/dev/null) || {
    echo "Error: no open PR found for the current branch." >&2
    exit 1
}
PR_NUMBER=$(echo "$PR_JSON" | grep -o '"number":[0-9]*' | grep -o '[0-9]*')
PR_TITLE=$(echo "$PR_JSON"  | python3 -c "import sys,json; d=json.load(sys.stdin); print(d['title'])")
PR_URL=$(echo "$PR_JSON"    | python3 -c "import sys,json; d=json.load(sys.stdin); print(d['url'])")
PR_BRANCH=$(echo "$PR_JSON" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d['headRefName'])")

# ── CI checks ─────────────────────────────────────────────────────────────────

CHECKS_JSON=$(gh pr checks "$PR_NUMBER" --json name,state,conclusion,link 2>/dev/null || echo "[]")

# ── CodeRabbit inline comments ────────────────────────────────────────────────

CR_COMMENTS=$(gh api "repos/{owner}/{repo}/pulls/${PR_NUMBER}/comments" \
    --paginate \
    --jq '[.[] | select(.user.login == "coderabbitai[bot]") | {
        path: .path,
        line: .line,
        body: .body,
        created_at: .created_at,
        url: .html_url
    }]' 2>/dev/null | python3 -c "
import sys, json
chunks = []
for line in sys.stdin:
    line = line.strip()
    if line:
        chunks.append(line)
# gh --paginate outputs multiple JSON arrays; merge them
all_comments = []
for chunk in chunks:
    try:
        data = json.loads(chunk)
        if isinstance(data, list):
            all_comments.extend(data)
    except json.JSONDecodeError:
        pass
print(json.dumps(all_comments))
")

# ── CodeRabbit PR-level review comment (walkthrough/summary) ──────────────────

CR_REVIEW=$(gh api "repos/{owner}/{repo}/issues/${PR_NUMBER}/comments" \
    --paginate \
    --jq '[.[] | select(.user.login == "coderabbitai[bot]") | {body: .body, created_at: .created_at}]' \
    2>/dev/null | python3 -c "
import sys, json, re
chunks = []
for line in sys.stdin:
    line = line.strip()
    if line:
        chunks.append(line)
all_comments = []
for chunk in chunks:
    try:
        data = json.loads(chunk)
        if isinstance(data, list):
            all_comments.extend(data)
    except json.JSONDecodeError:
        pass
# Find the main summary comment (contains walkthrough section)
for c in all_comments:
    body = c.get('body', '')
    if 'Walkthrough' in body or 'walkthrough' in body:
        # Strip HTML comments and internal state blocks
        body = re.sub(r'<!--.*?-->', '', body, flags=re.DOTALL)
        body = body.strip()
        print(body)
        break
")

# ── Failed CI logs ─────────────────────────────────────────────────────────────

FAILED_RUNS=$(gh run list --branch "$PR_BRANCH" --status failure \
    --json databaseId,name --limit 5 2>/dev/null || echo "[]")

# ── Write markdown ─────────────────────────────────────────────────────────────

mkdir -p "$(dirname "$OUTPUT")"

{
cat <<HEADER
# PR Review Summary

**PR:** [#${PR_NUMBER} — ${PR_TITLE}](${PR_URL})
**Branch:** \`${PR_BRANCH}\`
**Generated:** $(date -u '+%Y-%m-%d %H:%M UTC')

---

## CI Checks

HEADER

# Check statuses table
echo "| Check | Status |"
echo "|---|---|"
gh pr checks "$PR_NUMBER" 2>/dev/null | while IFS= read -r line; do
    [ -z "$line" ] && continue
    # gh pr checks output: "Name\tstatus\tduration\turl"
    name=$(echo "$line"   | cut -f1)
    status=$(echo "$line" | cut -f2)
    [ -z "$name" ] && continue
    case "$status" in
        pass)    icon="✅" ;;
        fail)    icon="❌" ;;
        pending) icon="⏳" ;;
        skip*)   icon="⏭️" ;;
        *)       icon="❓" ;;
    esac
    echo "| $name | $icon $status |"
done

echo ""

# Failed logs (truncated)
FAILED_COUNT=$(echo "$FAILED_RUNS" | python3 -c "import sys,json; d=json.load(sys.stdin); print(len(d))")
if [ "$FAILED_COUNT" -gt 0 ]; then
    echo "### Failed Job Logs"
    echo ""
    echo "$FAILED_RUNS" | python3 -c "
import sys, json
runs = json.load(sys.stdin)
for r in runs:
    print(r['databaseId'], r['name'])
" | while read -r run_id run_name; do
        echo "#### $run_name"
        echo ""
        echo '```'
        gh run view "$run_id" --log-failed 2>/dev/null | grep -v '^\s*$' | tail -40 || echo "(no output)"
        echo '```'
        echo ""
    done
fi

cat <<SECTION

---

## CodeRabbit Summary

SECTION

if [ -n "$CR_REVIEW" ]; then
    echo "$CR_REVIEW"
else
    echo "_No walkthrough comment found yet — CodeRabbit may still be processing._"
fi

cat <<SECTION

---

## CodeRabbit Inline Comments

SECTION

COMMENT_COUNT=$(echo "$CR_COMMENTS" | python3 -c "import sys,json; d=json.load(sys.stdin); print(len(d))")

if [ "$COMMENT_COUNT" -eq 0 ]; then
    echo "_No inline comments found._"
else
    echo "$CR_COMMENTS" | python3 -c "
import sys, json, re

comments = json.load(sys.stdin)

# Group by file
by_file = {}
for c in comments:
    path = c.get('path') or 'General'
    by_file.setdefault(path, []).append(c)

for path, items in sorted(by_file.items()):
    print(f'### \`{path}\`')
    print()
    for c in items:
        line = c.get('line') or '—'
        url  = c.get('url', '')
        body = c.get('body', '')

        # Strip HTML comment blocks (fingerprinting, internal state, prompts)
        body = re.sub(r'<details>.*?</details>', '', body, flags=re.DOTALL)
        body = re.sub(r'<!--.*?-->', '', body, flags=re.DOTALL)
        body = body.strip()

        # Extract severity tag if present (e.g. ⚠️ Potential issue | 🟠 Major)
        severity = ''
        sev_match = re.search(r'(_[⚠️🧹💡]+[^_]*_\s*\|\s*_[^_]+_)', body)
        if sev_match:
            severity = sev_match.group(1)
            body = body[sev_match.end():].strip()

        print(f'**Line {line}**' + (f' — {severity}' if severity else '') + (f' ([view]({url}))' if url else ''))
        print()
        print(body)
        print()
        print('---')
        print()
"
fi

} > "$OUTPUT"

echo "✅ PR review summary written to: $OUTPUT"
