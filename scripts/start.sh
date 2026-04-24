#!/bin/sh
# Set file descriptor limits
ulimit -n 65536 2>/dev/null || ulimit -n $(ulimit -Hn) 2>/dev/null
echo "fd soft limit: $(ulimit -n)"

# Binary to launch — defaults to ./main (API). Pass "worker" (or any other
# compiled binary in the image) as $1 to launch that instead. Keeps the
# Alloy sidecar in one place so every Fly process group exports metrics,
# regardless of which Go binary it runs.
APP_BIN="${1:-main}"

# Start Alloy metrics agent in background (skipped if either credential is absent)
alloy_pid=""
if [ -n "$GRAFANA_CLOUD_API_KEY" ] && [ -n "$GRAFANA_CLOUD_USER" ]; then
  echo "Starting Alloy metrics agent for ${APP_BIN}"
  /usr/local/bin/alloy run --storage.path=/tmp/alloy-wal /app/alloy.river &
  alloy_pid=$!
else
  echo "Grafana Cloud credentials not fully set, skipping metrics agent"
fi

# Forward SIGTERM/SIGINT to both processes and wait for clean shutdown
term() {
  [ -n "$alloy_pid" ] && kill "$alloy_pid" 2>/dev/null || true
  [ -n "$app_pid" ] && kill "$app_pid" 2>/dev/null || true
}
trap term INT TERM

if [ ! -x "./${APP_BIN}" ]; then
  echo "start.sh: ./${APP_BIN} is not executable in $(pwd)" >&2
  exit 127
fi

# Start application and wait (keeps script as PID 1 for signal handling)
"./${APP_BIN}" &
app_pid=$!
wait "$app_pid"
status=$?

[ -n "$alloy_pid" ] && kill "$alloy_pid" 2>/dev/null || true
wait "$alloy_pid" 2>/dev/null || true
exit "$status"
