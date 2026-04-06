#!/bin/bash
set -e

# Thin wrapper around the native hover CLI.
#
# Usage: ./scripts/generate-test-jobs.sh [interval:VALUE] [jobs:N] [repeats:N] [status-interval:VALUE] [concurrency:N|random] [pr:N] [anon-key:VALUE]
#
# This script translates the legacy key:value argument style into hover CLI
# flags and delegates to `hover jobs generate`. Build the CLI first:
#
#   go build -o hover ./cmd/hover/

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
HOVER_BIN="${HOVER_BIN:-$REPO_ROOT/hover}"

if [ ! -x "$HOVER_BIN" ]; then
  echo "hover binary not found at $HOVER_BIN"
  echo "Build it first:  go build -o hover ./cmd/hover/"
  exit 1
fi

# Translate legacy key:value args to --flag value pairs.
CLI_ARGS=()
for arg in "$@"; do
  case $arg in
    interval:*)  CLI_ARGS+=(--interval "${arg#*:}") ;;
    batch:*)     CLI_ARGS+=(--interval "${arg#*:}m") ;;
    jobs:*)      CLI_ARGS+=(--jobs "${arg#*:}") ;;
    repeats:*)   CLI_ARGS+=(--repeats "${arg#*:}") ;;
    status-interval:*) CLI_ARGS+=(--status-interval "${arg#*:}") ;;
    concurrency:*) CLI_ARGS+=(--concurrency "${arg#*:}") ;;
    pr:*)        CLI_ARGS+=(--pr "${arg#*:}") ;;
    anon-key:*)  CLI_ARGS+=(--anon-key "${arg#*:}") ;;
    auth-url:*)  CLI_ARGS+=(--auth-url "${arg#*:}") ;;
    api-url:*)   CLI_ARGS+=(--api-url "${arg#*:}") ;;
    *)
      echo "Unknown argument: $arg"
      exit 1
      ;;
  esac
done

exec "$HOVER_BIN" jobs generate "${CLI_ARGS[@]}"
