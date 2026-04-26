#!/bin/sh
# start-analysis.sh — entrypoint for the hover-analysis service.
#
# Boots the Alloy metrics sidecar (when Grafana Cloud credentials are
# present) alongside ./analysis. Mirrors scripts/start.sh exactly so a
# review-app or production analysis pod looks identical to the API and
# worker on Grafana — same scrape path, same labelling pipeline.
#
# Kept as its own script (rather than reusing start.sh with $1=analysis)
# so the analysis image stays minimal: no static HTML, no migration
# files, no need for the existing start.sh logic that switches between
# main and worker binaries.

ulimit -n 65536 2>/dev/null || ulimit -n "$(ulimit -Hn)" 2>/dev/null
echo "fd soft limit: $(ulimit -n)"

alloy_pid=""
if [ -n "$GRAFANA_CLOUD_API_KEY" ] && [ -n "$GRAFANA_CLOUD_USER" ]; then
  echo "Starting Alloy metrics agent for hover-analysis"
  /usr/local/bin/alloy run --storage.path=/tmp/alloy-wal /app/alloy.river &
  alloy_pid=$!
else
  echo "Grafana Cloud credentials not fully set, skipping metrics agent"
fi

term() {
  [ -n "$alloy_pid" ] && kill "$alloy_pid" 2>/dev/null || true
  [ -n "$app_pid" ] && kill "$app_pid" 2>/dev/null || true
}
trap term INT TERM

if [ ! -x "./analysis" ]; then
  echo "start-analysis.sh: ./analysis is not executable in $(pwd)" >&2
  exit 127
fi

./analysis &
app_pid=$!
wait "$app_pid"
status=$?

[ -n "$alloy_pid" ] && kill "$alloy_pid" 2>/dev/null || true
wait "$alloy_pid" 2>/dev/null || true
exit "$status"
