#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

APP="hover,hover-worker"
INTERVAL=10
SAMPLES=400
ITERATIONS=1440  # 4 hours at 10s intervals
RUN_ID=""
OUTPUT_ROOT="logs"
CLEANUP_OLD=true
CLEANUP_DAYS=1
CLEANUP_MODE="zip"
PYTHON_CMD=""
PYTHON_ARGS=()

usage() {
    cat <<'USAGE'
Usage: monitor_logs.sh [options]

Fetch recent Fly logs on a fixed cadence, archive the raw output, and write
per-minute summaries describing how often each log level/message occurred.

Automatic cleanup (enabled by default):
  - Zips raw logs and iteration JSONs from runs older than 1 day
  - Keeps summary.md, summary.json, and monitor.log
  - Use --no-cleanup to disable or --cleanup-mode delete to remove everything

Options:
  --app NAMES           Fly application name(s), comma-separated
                        (default: hover,hover-worker)
  --interval SECONDS    Seconds to wait between samples (default: 60)
  --samples N           Number of log lines to request each run (default: 400)
  --iterations N        Number of iterations to perform (0 = run forever)
  --run-id ID           Identifier used when naming output directories
  --no-cleanup          Disable automatic cleanup (default: enabled)
  --cleanup-days N      Clean runs older than N days (default: 1)
  --cleanup-mode MODE   How to clean: 'zip' or 'delete' (default: zip)
                        zip: archives raw/ and iteration JSONs, keeps summaries
                        delete: removes entire run directory
  -h, --help            Show this message and exit

Environment variables with the same names (APP, INTERVAL, SAMPLES, ITERATIONS,
RUN_ID) override the defaults as well.

Examples:
  # Default: auto-zip raw logs from runs older than 1 day
  ./monitor_logs.sh

  # Disable cleanup
  ./monitor_logs.sh --no-cleanup

  # Delete entire runs older than 3 days
  ./monitor_logs.sh --cleanup-days 3 --cleanup-mode delete
USAGE
}

# Allow environment variables to override defaults
APP=${APP:-$APP}
INTERVAL=${INTERVAL:-$INTERVAL}
SAMPLES=${SAMPLES:-$SAMPLES}
ITERATIONS=${ITERATIONS:-$ITERATIONS}
RUN_ID=${RUN_ID:-$RUN_ID}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --app)
            APP="$2"
            shift 2
            ;;
        --interval)
            INTERVAL="$2"
            shift 2
            ;;
        --samples)
            SAMPLES="$2"
            shift 2
            ;;
        --iterations)
            ITERATIONS="$2"
            shift 2
            ;;
        --run-id)
            RUN_ID="$2"
            shift 2
            ;;
        --no-cleanup)
            CLEANUP_OLD=false
            shift
            ;;
        --cleanup-days)
            CLEANUP_DAYS="$2"
            shift 2
            ;;
        --cleanup-mode)
            CLEANUP_MODE="$2"
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "Unknown option: $1" >&2
            usage
            exit 1
            ;;
    esac
done

if ! [[ "$INTERVAL" =~ ^[0-9]+$ && "$INTERVAL" -gt 0 ]]; then
    echo "interval must be a positive integer" >&2
    exit 1
fi

if ! [[ "$SAMPLES" =~ ^[0-9]+$ && "$SAMPLES" -ge 1 && "$SAMPLES" -le 10000 ]]; then
    echo "samples must be an integer between 1 and 10000" >&2
    exit 1
fi

if ! [[ "$ITERATIONS" =~ ^[0-9]+$ ]]; then
    echo "iterations must be an integer >= 0" >&2
    exit 1
fi

if ! [[ "$CLEANUP_DAYS" =~ ^[0-9]+$ && "$CLEANUP_DAYS" -ge 0 ]]; then
    echo "cleanup-days must be a non-negative integer" >&2
    exit 1
fi

if [[ "$CLEANUP_MODE" != "zip" && "$CLEANUP_MODE" != "delete" ]]; then
    echo "cleanup-mode must be 'zip' or 'delete'" >&2
    exit 1
fi

# Split APP into an array on commas (and strip whitespace around entries).
IFS=',' read -r -a APPS <<< "$APP"
for i in "${!APPS[@]}"; do
    APPS[i]="${APPS[i]// /}"
done
if [[ ${#APPS[@]} -eq 0 || -z "${APPS[0]}" ]]; then
    echo "at least one app name is required" >&2
    exit 1
fi

# Resolve a working Python interpreter for log processing helpers.
if command -v python3 >/dev/null 2>&1 && python3 -c "import sys" >/dev/null 2>&1; then
    PYTHON_CMD="python3"
elif command -v python >/dev/null 2>&1 && python -c "import sys" >/dev/null 2>&1; then
    PYTHON_CMD="python"
elif command -v py >/dev/null 2>&1 && py -3 -c "import sys" >/dev/null 2>&1; then
    PYTHON_CMD="py"
    PYTHON_ARGS=(-3)
fi

# Auto-generate settings suffix with appropriate units
# Interval: use minutes if >= 60s, otherwise seconds
if [[ "$INTERVAL" -ge 60 ]]; then
    INTERVAL_MINUTES=$(( INTERVAL / 60 ))
    INTERVAL_STR="${INTERVAL_MINUTES}m"
else
    INTERVAL_STR="${INTERVAL}s"
fi

if [[ "$ITERATIONS" -eq 0 ]]; then
    SETTINGS_SUFFIX="${INTERVAL_STR}_forever"
else
    # Calculate total duration in seconds
    DURATION_SECONDS=$(( ITERATIONS * INTERVAL ))

    # Duration: use days if >= 24h, hours if >= 60m, otherwise minutes
    if [[ "$DURATION_SECONDS" -ge 86400 ]]; then
        DURATION_DAYS=$(( (DURATION_SECONDS + 43200) / 86400 ))
        DURATION_STR="${DURATION_DAYS}d"
    elif [[ "$DURATION_SECONDS" -ge 3600 ]]; then
        DURATION_HOURS=$(( (DURATION_SECONDS + 1800) / 3600 ))
        DURATION_STR="${DURATION_HOURS}h"
    else
        DURATION_MINUTES=$(( (DURATION_SECONDS + 30) / 60 ))
        DURATION_STR="${DURATION_MINUTES}m"
    fi

    SETTINGS_SUFFIX="${INTERVAL_STR}_${DURATION_STR}"
fi

# Combine custom name (if provided) with settings
if [[ -z "$RUN_ID" ]]; then
    RUN_ID="$SETTINGS_SUFFIX"
else
    RUN_ID="${RUN_ID}_${SETTINGS_SUFFIX}"
fi

# Create directory structure: logs/YYYYMMDD/HHMM_run-id/
DATE_DIR="$OUTPUT_ROOT/$(date +"%Y%m%d")"
TIME_PREFIX=$(date +"%H%M")
RUN_DIR="$DATE_DIR/${TIME_PREFIX}_${RUN_ID}"
LOG_FILE="$RUN_DIR/monitor.log"

mkdir -p "$RUN_DIR"
for app in "${APPS[@]}"; do
    mkdir -p "$RUN_DIR/$app/raw"
done

# Cleanup old runs if requested
if [[ "$CLEANUP_OLD" == "true" ]]; then
    echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] Cleaning up old runs (older than $CLEANUP_DAYS days, mode: $CLEANUP_MODE)" | tee -a "$LOG_FILE"

    # Calculate cutoff date (days ago)
    if [[ "$(uname)" == "Darwin" ]]; then
        # macOS date command
        CUTOFF_DATE=$(date -u -v-${CLEANUP_DAYS}d +"%Y%m%d" 2>/dev/null || date -u +"%Y%m%d")
    else
        # GNU date command
        CUTOFF_DATE=$(date -u -d "$CLEANUP_DAYS days ago" +"%Y%m%d" 2>/dev/null || date -u +"%Y%m%d")
    fi

    # Find old run directories
    if [[ -d "$OUTPUT_ROOT" ]]; then
        find "$OUTPUT_ROOT" -mindepth 2 -maxdepth 2 -type d | while read -r run_dir; do
            # Extract date from path (logs/YYYYMMDD/HHMM_run-id)
            date_dir=$(basename "$(dirname "$run_dir")")

            # Skip if not a date directory
            if ! [[ "$date_dir" =~ ^[0-9]{8}$ ]]; then
                continue
            fi

            # Skip if directory is from today or newer than cutoff
            if [[ "$date_dir" -ge "$CUTOFF_DATE" ]]; then
                continue
            fi

            run_name=$(basename "$run_dir")

            if [[ "$CLEANUP_MODE" == "zip" ]]; then
                # Zip mode: archive any raw/ dirs and iteration JSONs, keep summaries.
                # Handles both legacy layout ($run_dir/raw) and per-app layout
                # ($run_dir/<app>/raw).
                while IFS= read -r raw_dir; do
                    [[ -z "$raw_dir" ]] && continue
                    zip_parent=$(dirname "$raw_dir")
                    [[ -f "$zip_parent/raw.zip" ]] && continue
                    rel=${raw_dir#"$run_dir/"}
                    echo "  Zipping raw logs: $date_dir/$run_name/$rel" | tee -a "$LOG_FILE"
                    (cd "$zip_parent" && zip -q -r "raw.zip" "raw" && rm -rf "raw") || {
                        echo "  Failed to zip raw directory $raw_dir" | tee -a "$LOG_FILE"
                    }
                done < <(find "$run_dir" -type d -name raw 2>/dev/null)

                # Remove iteration JSONs anywhere under the run dir (keep summaries).
                iter_files=$(find "$run_dir" -type f -name '*_iter*.json' 2>/dev/null)
                if [[ -n "$iter_files" ]]; then
                    echo "  Removing iteration JSONs: $date_dir/$run_name" | tee -a "$LOG_FILE"
                    printf '%s\n' "$iter_files" | xargs rm -f || {
                        echo "  Failed to remove iteration JSONs in $run_dir" | tee -a "$LOG_FILE"
                    }
                fi
            else
                # Delete mode: remove entire run directory
                echo "  Deleting: $date_dir/$run_name" | tee -a "$LOG_FILE"
                rm -rf "$run_dir" || {
                    echo "  Failed to delete $run_dir" | tee -a "$LOG_FILE"
                }
            fi
        done
    fi

    echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] Cleanup complete" | tee -a "$LOG_FILE"
fi

echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] Starting log monitor" | tee -a "$LOG_FILE"
echo "Apps: ${APPS[*]} | Interval: ${INTERVAL}s | Samples: $SAMPLES | Iterations: $ITERATIONS" | tee -a "$LOG_FILE"
echo "Run directory: $RUN_DIR" | tee -a "$LOG_FILE"
for app in "${APPS[@]}"; do
    echo "  [$app] raw: $RUN_DIR/$app/raw  summaries: $RUN_DIR/$app" | tee -a "$LOG_FILE"
done
if [[ -z "$PYTHON_CMD" ]]; then
    echo "Python not found; continuing with raw log capture only" | tee -a "$LOG_FILE"
fi

# capture_app APP_NAME TIMESTAMP ITER — fetch + process logs for a single app.
capture_app() {
    local app="$1" ts="$2" iter="$3"
    local raw_file="$RUN_DIR/$app/raw/${ts}_iter${iter}.log"
    local summary_file="$RUN_DIR/$app/${ts}_iter${iter}.json"

    if ! flyctl logs --app "$app" --no-tail 2>&1 | tail -n "$SAMPLES" > "$raw_file"; then
        echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] [$app] Failed to fetch logs from Fly; raw output stored in $raw_file" | tee -a "$LOG_FILE"
        return
    fi

    if [[ -z "$PYTHON_CMD" ]]; then
        echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] [$app] Captured raw logs only (Python unavailable)" | tee -a "$LOG_FILE"
        return
    fi

    if ! env PYTHONUTF8=1 "$PYTHON_CMD" "${PYTHON_ARGS[@]+"${PYTHON_ARGS[@]}"}" "$SCRIPT_DIR/process_logs.py" "$raw_file" "$summary_file" >> "$LOG_FILE" 2>&1; then
        echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] [$app] Failed to process logs (see output above)" | tee -a "$LOG_FILE"
        return
    fi

    env PYTHONUTF8=1 "$PYTHON_CMD" "${PYTHON_ARGS[@]+"${PYTHON_ARGS[@]}"}" "$SCRIPT_DIR/aggregate_logs.py" "$RUN_DIR/$app" >> "$LOG_FILE" 2>&1
}

iteration=0

while true; do
    iteration=$((iteration + 1))
    ts=$(date -u +"%Y%m%dT%H%M%SZ")

    echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] Iteration $iteration: capturing logs" | tee -a "$LOG_FILE"

    for app in "${APPS[@]}"; do
        capture_app "$app" "$ts" "$iteration"
    done

    if [[ "$ITERATIONS" -ne 0 && "$iteration" -ge "$ITERATIONS" ]]; then
        break
    fi

    sleep "$INTERVAL"
done

echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] Monitoring finished after $iteration iteration(s)" | tee -a "$LOG_FILE"

# Final aggregation per app
if [[ -z "$PYTHON_CMD" ]]; then
    echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] Skipping aggregation (Python unavailable)" | tee -a "$LOG_FILE"
else
    echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] Running final aggregation..." | tee -a "$LOG_FILE"
    for app in "${APPS[@]}"; do
        env PYTHONUTF8=1 "$PYTHON_CMD" "${PYTHON_ARGS[@]+"${PYTHON_ARGS[@]}"}" "$SCRIPT_DIR/aggregate_logs.py" "$RUN_DIR/$app" >> "$LOG_FILE" 2>&1
    done
    echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] Aggregation complete" | tee -a "$LOG_FILE"
fi
