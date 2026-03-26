#!/bin/bash
set -e

# Colours
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

# Simple Load Test Script
# Calls the /v1/jobs API endpoint to create real jobs at intervals
# Creates one job per unique domain in the DOMAINS array
#
# Usage: ./load-test-simple.sh [interval:VALUE] [jobs:N] [concurrency:N|random]
#  - VALUE can be minutes (default), e.g. `interval:2` or `interval:2m`
#  - or seconds via an `s` suffix, e.g. `interval:45s`
#  - `batch:N` remains as a backwards-compatible alias for minutes
#  - `concurrency:N` sets the per-job concurrency limit (1-50)
#  - `concurrency:random` generates random concurrency (10-50) for each job (default)
#
# Examples:
#   ./load-test-simple.sh interval:1m jobs:10 concurrency:5
#   ./load-test-simple.sh interval:30s jobs:20 concurrency:random
#
# Test Scenarios (Phase 3 - Throughput Optimisation):
#
# TEST 1 - Baseline (verify no regression)
#   Server config: WORKER_CONCURRENCY=1, WORKER_POOL_SIZE=50
#   Command: ./load-test-simple.sh interval:30s jobs:10 concurrency:3
#   Expected: ~2-3 tasks/sec (same as before Phase 1 & 2)
#
# TEST 2 - Concurrent workers without job limits (sanity check)
#   Server config: WORKER_CONCURRENCY=5, WORKER_POOL_SIZE=100
#   Command: ./load-test-simple.sh interval:30s jobs:10 concurrency:20
#   Expected: High throughput, no job-level throttling
#
# TEST 3 - Mixed job concurrency (production-like)
#   Server config: WORKER_CONCURRENCY=5, WORKER_POOL_SIZE=100
#   Command: ./load-test-simple.sh interval:30s jobs:30 concurrency:random
#   Expected: Each job respects its own limit (10-50), fair distribution across all jobs
#
# TEST 4 - Target throughput (validation)
#   Server config: WORKER_CONCURRENCY=5, WORKER_POOL_SIZE=100
#   Command: ./load-test-simple.sh interval:15s jobs:62 concurrency:3
#   Expected: 120-180 tasks/sec aggregate throughput
#   Notes: 100 workers × 5 concurrency = 500 total capacity
#          62 jobs × 3 concurrency = 186 max concurrent tasks
#          Should sustain 120-180 tasks/sec with typical site response times

# Parse command line arguments
BATCH_INTERVAL_SECONDS=180
JOBS_PER_BATCH=3
JOB_CONCURRENCY="random"

parse_interval_seconds() {
  local raw="$1"

  if [[ "$raw" =~ ^[0-9]+[sS]$ ]]; then
    echo "${raw%[sS]}"
  elif [[ "$raw" =~ ^[0-9]+[mM]$ ]]; then
    echo $(( ${raw%[mM]} * 60 ))
  elif [[ "$raw" =~ ^[0-9]+$ ]]; then
    echo $(( raw * 60 ))
  else
    return 1
  fi
}

format_interval() {
  local seconds=$1

  if (( seconds <= 0 )); then
    echo "0s"
  elif (( seconds < 60 )); then
    echo "${seconds}s"
  elif (( seconds % 60 == 0 )); then
    local minutes=$(( seconds / 60 ))
    if (( minutes == 1 )); then
      echo "1 minute"
    else
      echo "$minutes minutes"
    fi
  else
    local minutes=$(( seconds / 60 ))
    local remaining=$(( seconds % 60 ))
    echo "${minutes}m ${remaining}s"
  fi
}

for arg in "$@"; do
  case $arg in
    batch:*)
      minutes="${arg#*:}"
      if [[ ! "$minutes" =~ ^[0-9]+$ ]]; then
        echo "Invalid batch interval: $minutes (expected integer minutes)"
        exit 1
      fi
      BATCH_INTERVAL_SECONDS=$(( minutes * 60 ))
      ;;
    interval:*)
      interval_value="${arg#*:}"
      if ! parsed_seconds=$(parse_interval_seconds "$interval_value"); then
        echo "Invalid interval format: $interval_value"
        echo "Use integers (minutes), suffix with m for minutes, or s for seconds (e.g. interval:30s, interval:2m)"
        exit 1
      fi
      BATCH_INTERVAL_SECONDS=$parsed_seconds
      ;;
    jobs:*)
      JOBS_PER_BATCH="${arg#*:}"
      ;;
    concurrency:*)
      JOB_CONCURRENCY="${arg#*:}"
      if [ "$JOB_CONCURRENCY" == "random" ]; then
        # Random concurrency mode - will generate 10-50 per job
        :
      elif [[ ! "$JOB_CONCURRENCY" =~ ^[0-9]+$ ]]; then
        echo "Invalid concurrency: $JOB_CONCURRENCY (expected integer 1-50 or 'random')"
        exit 1
      elif [ "$JOB_CONCURRENCY" -lt 1 ] || [ "$JOB_CONCURRENCY" -gt 50 ]; then
        echo "Concurrency must be between 1 and 50 (got: $JOB_CONCURRENCY)"
        exit 1
      fi
      ;;
    *)
      echo "Unknown argument: $arg"
      echo "Usage: $0 [interval:VALUE] [jobs:N] [concurrency:N] [batch:N]"
      exit 1
      ;;
  esac
done

if [ "$BATCH_INTERVAL_SECONDS" -le 0 ]; then
  echo "Interval must be greater than zero seconds."
  exit 1
fi

# Configuration (can still be overridden by environment variables)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
TOKEN_FILE="${TOKEN_FILE_PATH:-$REPO_ROOT/sb-auth-auth-token}"
AUTH_HELPER="$REPO_ROOT/scripts/auth/cli_auth.py"

API_URL="${API_URL:-https://hover.fly.dev}"
AUTH_TOKEN="${AUTH_TOKEN:-}"

if [ -z "$AUTH_TOKEN" ] && [ -f "$TOKEN_FILE" ]; then
  if ! token_from_file=$(
    python3 - "$TOKEN_FILE" <<'PY'
import json
import sys

try:
    with open(sys.argv[1], "r", encoding="utf-8") as fh:
        data = json.load(fh)
    token = (data.get("access_token") or "").strip()
    if not token:
        raise SystemExit("access_token missing in sb-auth-auth-token")
    print(token, end="")
except FileNotFoundError:
    raise SystemExit("Token file not found")
PY
  ); then
    echo -e "${RED}ERROR: Failed to parse $TOKEN_FILE for access_token${NC}"
    exit 1
  fi
  AUTH_TOKEN="$token_from_file"
fi

if [ -z "$AUTH_TOKEN" ] && [ -x "$AUTH_HELPER" ]; then
  if token_from_helper=$(python3 "$AUTH_HELPER" ensure); then
    AUTH_TOKEN="$token_from_helper"
  else
    echo -e "${YELLOW}Warning: automatic Supabase login failed. Run 'python3 $AUTH_HELPER login'.${NC}"
  fi
fi

# Test domains - 115 diverse real-world sites (all under 50k pages)
DOMAINS=(
  # Australian businesses (6) - small-medium corporate sites
  "bankaust.com.au" "australiansuper.com" "bunnings.com.au"
  "jbhifi.com.au" "kmart.com.au" "officeworks.com.au"

  # E-commerce & retail (10) - DTC brands, smaller catalogs
  "merrypeople.com" "aesop.com" "allbirds.com" "everlane.com" "warbyparker.com"
  "casper.com" "glossier.com" "away.com" "brooklinen.com" "kotn.com"

  # Tech blogs & publications (5) - niche editorial sites
  "csswizardry.com" "heydesigner.com" "sidebar.io" "stefanjudis.com" "smolblog.com"

  # WordPress blogs & design sites (10) - typical WP installs
  "smashingmagazine.com" "css-tricks.com" "webdesignerdepot.com" "sitepoint.com" "alistapart.com"
  "designmodo.com" "creativebloq.com" "awwwards.com" "onextrapixel.com" "hongkiat.com"

  # Small business / agency sites (10) - small Aussie agencies
  "studiothink.com.au" "zeroseven.com.au" "humaan.com.au" "noice.com.au" "willandco.com.au"
  "thecontentlab.com.au" "thisisgold.com.au" "wethecollective.com.au" "tworedshoes.com.au"

  # Developer docs & tools (8) - compact doc sites
  "fly.io" "railway.app" "render.com" "tailwindcss.com"
  "nextjs.org" "react.dev" "astro.build" "svelte.dev"

  # Additional dev frameworks & tooling (30) - focused documentation hubs
  "vitejs.dev" "nuxt.com" "remix.run" "solidjs.com" "qwik.dev"
  "parceljs.org" "rollupjs.org" "esbuild.github.io" "bun.sh" "deno.com"
  "cypress.io" "vitest.dev" "pnpm.io" "turbo.build"
  "nx.dev" "oclif.io" "temporal.io" "directus.io" "strapi.io"
  "sanity.io" "payloadcms.com" "pocketbase.io" "supabase.com" "plane.so"
  "appsmith.com" "tooljet.com" "budibase.com" "windmill.dev" "tauri.app"

  # SaaS & productivity apps (12) - lean marketing sites
  "linear.app" "height.app" "reclaim.ai" "mem.ai" "reflect.app"
  "cron.com" "retool.com" "cal.com" "around.co" "raycast.com"
  "warp.dev" "cursor.so"

  # Niche e-commerce & DTC brands (17) - smaller catalogs
  "studioneat.com" "feals.com" "magicspoon.com" "atlascoffeeclub.com"
  "blueland.com" "publicgoods.com" "outerknown.com" "grovemade.com"
  "ridgewallet.com" "ouraring.com" "carawayhome.com" "maap.cc"
  "bellroy.com" "ritual.com" "cuyana.com" "thesill.com" "parachutehome.com"

  # Indie analytics & SaaS (7) - focused product sites
  "plausible.io" "simpleanalytics.com" "savvycal.com" "commandbar.com"
  "pirsch.io" "clarityflow.com" "swapcard.com"
)

# Check prerequisites
if [ -z "$AUTH_TOKEN" ]; then
  echo -e "${RED}ERROR: AUTH_TOKEN not set${NC}"
  if [ -x "$AUTH_HELPER" ]; then
    echo "Run the login helper:"
    echo "  python3 $AUTH_HELPER login"
  else
    echo "Get a token from your app and run:"
    echo "  export AUTH_TOKEN='your-jwt-token'"
  fi
  exit 1
fi

echo -e "${GREEN}=== Simple Load Test ===${NC}"
echo "API URL:           $API_URL"
echo "Batch interval:    $(format_interval "$BATCH_INTERVAL_SECONDS")"
echo "Jobs per batch:    $JOBS_PER_BATCH"
if [ "$JOB_CONCURRENCY" == "random" ]; then
  echo "Job concurrency:   random (10-50 tasks/job)"
else
  echo "Job concurrency:   $JOB_CONCURRENCY tasks/job"
fi
echo "Available domains: ${#DOMAINS[@]}"
echo ""

# Calculate batches based on domain count, not time
TOTAL_DOMAINS=${#DOMAINS[@]}
TOTAL_BATCHES=$(( (TOTAL_DOMAINS + JOBS_PER_BATCH - 1) / JOBS_PER_BATCH ))  # Round up
ESTIMATED_DURATION_SECONDS=$(( (TOTAL_BATCHES - 1) * BATCH_INTERVAL_SECONDS ))

echo "Will create $TOTAL_DOMAINS jobs (one per unique domain) across $TOTAL_BATCHES batches"
echo "Estimated duration: $(format_interval "$ESTIMATED_DURATION_SECONDS")"
echo ""
read -p "Continue? [y/N] " -n 1 -r
echo
if [[ ! $REPLY =~ ^[Yy]$ ]]; then
  echo "Cancelled"
  exit 0
fi

# Create job function
create_job() {
  local domain=$1
  local batch_num=$2

  # Generate random concurrency between 10 and 50 for mixed load testing
  # Unless JOB_CONCURRENCY is explicitly set to a specific value
  local job_concurrency
  if [ "$JOB_CONCURRENCY" == "random" ]; then
    job_concurrency=$((RANDOM % 41 + 10))
    echo -e "${YELLOW}Creating job for $domain (batch $batch_num, concurrency: $job_concurrency)${NC}"
  else
    job_concurrency=$JOB_CONCURRENCY
    echo -e "${YELLOW}Creating job for $domain (batch $batch_num)${NC}"
  fi

  response=$(curl -s -w "\n%{http_code}" -X POST "$API_URL/v1/jobs" \
    -H "Authorization: Bearer $AUTH_TOKEN" \
    -H "Content-Type: application/json" \
    -d "{
      \"domain\": \"$domain\",
      \"use_sitemap\": true,
      \"find_links\": true,
      \"max_pages\": 10000,
      \"concurrency\": $job_concurrency
    }")

  http_code=$(echo "$response" | tail -n1)
  body=$(echo "$response" | sed '$d')

  if [ "$http_code" -eq 200 ] || [ "$http_code" -eq 201 ]; then
    job_id=$(echo "$body" | jq -r '.data.id // .job.id // .id' 2>/dev/null || echo "unknown")
    echo -e "${GREEN}✓ Created job $job_id for $domain${NC}"
    JOBS_CREATED=$((JOBS_CREATED + 1))
  else
    echo -e "${RED}✗ Failed to create job for $domain (HTTP $http_code)${NC}"
    echo "$body" | jq '.' 2>/dev/null || echo "$body"

    # Exit immediately on auth errors
    if [ "$http_code" -eq 401 ]; then
      echo -e "${RED}Authentication failed. Please check your AUTH_TOKEN.${NC}"
      exit 1
    fi
  fi
}

mkdir -p ./logs

# Track start time for throughput calculation
START_TIME=$(date +%s)
JOBS_CREATED=0

# Shuffle all domains once at the start for global uniqueness
all_shuffled=("${DOMAINS[@]}")
for ((i=${#all_shuffled[@]}-1; i>0; i--)); do
  j=$((RANDOM % (i+1)))
  temp="${all_shuffled[i]}"
  all_shuffled[i]="${all_shuffled[j]}"
  all_shuffled[j]="$temp"
done

# Track which domain index we're up to
domain_index=0

# Run batches
for ((batch=1; batch<=TOTAL_BATCHES; batch++)); do
  echo ""
  echo -e "${GREEN}=== Batch $batch/$TOTAL_BATCHES ===${NC}"
  echo "$(date)"

  # Select next N domains from shuffled list (stop when we run out)
  selected_domains=()
  for ((i=0; i<JOBS_PER_BATCH && domain_index<${#all_shuffled[@]}; i++)); do
    selected_domains+=("${all_shuffled[$domain_index]}")
    domain_index=$((domain_index + 1))
  done

  # Create jobs
  for domain in "${selected_domains[@]}"; do
    create_job "$domain" "$batch"
    sleep 2  # Small delay between creates
  done

  # Wait for next batch (unless this is the last one)
  if [ $batch -lt $TOTAL_BATCHES ]; then
    echo ""
    echo -e "${YELLOW}Waiting $(format_interval "$BATCH_INTERVAL_SECONDS") until next batch...${NC}"
    sleep $BATCH_INTERVAL_SECONDS
  fi
done

echo ""
echo -e "${GREEN}=== Load test complete ===${NC}"

# Calculate and display throughput metrics
END_TIME=$(date +%s)
ELAPSED_SECONDS=$((END_TIME - START_TIME))

echo ""
echo "Jobs created:      $JOBS_CREATED"
echo "Total duration:    $(format_interval "$ELAPSED_SECONDS")"

if [ "$JOBS_CREATED" -gt 0 ] && [ "$ELAPSED_SECONDS" -gt 0 ]; then
  JOBS_PER_SECOND=$(echo "scale=2; $JOBS_CREATED / $ELAPSED_SECONDS" | bc)
  echo "Job creation rate: ${JOBS_PER_SECOND} jobs/sec"
  echo ""
  echo "Expected task throughput (once jobs start):"
  echo "  With WORKER_CONCURRENCY=1:  ~2-3 tasks/sec"
  echo "  With WORKER_CONCURRENCY=5:  ~120-180 tasks/sec target"
  echo "  (Actual throughput depends on server configuration and site response times)"
fi

echo ""
echo "Check job status with:"
echo "  curl -H 'Authorization: Bearer \$AUTH_TOKEN' $API_URL/v1/jobs"
echo ""
echo "Monitor job progress:"
echo "  curl -H 'Authorization: Bearer \$AUTH_TOKEN' $API_URL/v1/jobs/<job-id>"
