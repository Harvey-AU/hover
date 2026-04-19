#!/bin/sh
# Set file descriptor limits
ulimit -n 65536 2>/dev/null || ulimit -n $(ulimit -Hn) 2>/dev/null
echo "fd soft limit: $(ulimit -n)"

# Start Alloy metrics agent in background (production only — skipped if either credential is absent)
alloy_pid=""
if [ -n "$GRAFANA_CLOUD_API_KEY" ] && [ -n "$GRAFANA_CLOUD_USER" ]; then
  echo "Starting Alloy metrics agent"
  /usr/local/bin/alloy run --storage.path=/tmp/alloy-wal /app/alloy.river &
  alloy_pid=$!
else
  echo "Grafana Cloud credentials not fully set, skipping metrics agent"
fi

# Forward SIGTERM/SIGINT to both processes and wait for clean shutdown
term() {
  [ -n "$alloy_pid" ] && kill "$alloy_pid" 2>/dev/null || true
  [ -n "$main_pid" ] && kill "$main_pid" 2>/dev/null || true
}
trap term INT TERM

# Start main application and wait (keeps script as PID 1 for signal handling)
./main &
main_pid=$!
wait "$main_pid"
status=$?

[ -n "$alloy_pid" ] && kill "$alloy_pid" 2>/dev/null || true
wait "$alloy_pid" 2>/dev/null || true
exit "$status"
