#!/bin/sh
# Set file descriptor limits
ulimit -n 65536 2>/dev/null || ulimit -n $(ulimit -Hn) 2>/dev/null
echo "fd soft limit: $(ulimit -n)"

# Start Alloy metrics agent in background (production only — skipped if secrets absent)
if [ -n "$GRAFANA_CLOUD_API_KEY" ]; then
  echo "Starting Alloy metrics agent"
  /usr/local/bin/alloy run --storage.path=/tmp/alloy-wal /app/alloy.river &
else
  echo "GRAFANA_CLOUD_API_KEY not set, skipping metrics agent"
fi

# Start main application
exec ./main
