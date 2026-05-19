#!/bin/bash
# Test memory impact of high churn on queries with and without label matching (joins)
# Run with 10GB memory limit

set -e
cd "$(dirname "$0")/.."

RESULTS_DIR="results"
mkdir -p "$RESULTS_DIR"
TIMESTAMP=$(date +%Y-%m-%d)
RESULTS_FILE="$RESULTS_DIR/churn-memory-test-$TIMESTAMP.md"

# Memory limit for components
MEMORY_LIMIT="${MEMORY_LIMIT:-10G}"
GOGC="${GOGC:-100}"
GOMEMLIMIT="${GOMEMLIMIT:-9G}"

echo "# Churn Memory Impact Test - $TIMESTAMP" > "$RESULTS_FILE"
echo "" >> "$RESULTS_FILE"
echo "Memory limit: $MEMORY_LIMIT per component" >> "$RESULTS_FILE"
echo "GOGC: $GOGC, GOMEMLIMIT: $GOMEMLIMIT" >> "$RESULTS_FILE"
echo "" >> "$RESULTS_FILE"

# Check bucket has data
BUCKET_DIR="${BUCKET_DIR:-/tmp/thanos-bucket}"
BLOCK_COUNT=$(find "$BUCKET_DIR" -name "meta.json" 2>/dev/null | wc -l)
echo "Bucket: $BUCKET_DIR ($BLOCK_COUNT blocks)" >> "$RESULTS_FILE"
echo "" >> "$RESULTS_FILE"

# Start harness with store gateway
echo "Starting harness with Store Gateway..."
./thanos-harness start \
  --objstore-config=configs/objstore-filesystem.yaml \
  --memory-limit="$MEMORY_LIMIT" \
  --gogc="$GOGC" \
  --gomemlimit="$GOMEMLIMIT" &
HARNESS_PID=$!

# Wait for components to be ready
echo "Waiting for components to start..."
sleep 30

# Check if querier is responding
until curl -s http://localhost:9090/-/ready > /dev/null 2>&1; do
  echo "Waiting for querier..."
  sleep 5
done

# Additional wait for store gateway sync
echo "Waiting for store gateway to sync blocks..."
sleep 60

# Helper to run query and capture memory
run_query() {
  local name="$1"
  local query="$2"
  local time_range="$3"

  echo "Running: $name"
  echo "" >> "$RESULTS_FILE"
  echo "## $name" >> "$RESULTS_FILE"
  echo '```' >> "$RESULTS_FILE"
  echo "Query: $query" >> "$RESULTS_FILE"
  echo "Time range: $time_range" >> "$RESULTS_FILE"
  echo '```' >> "$RESULTS_FILE"
  echo "" >> "$RESULTS_FILE"

  # Get memory before
  MEM_BEFORE=$(./thanos-harness stats 2>&1 | grep -E "(querier|store)" || true)

  # Run query
  START_TIME=$(date +%s%N)
  RESULT=$(./thanos-harness query "$query" --time-range="$time_range" 2>&1) || true
  END_TIME=$(date +%s%N)
  DURATION=$(( (END_TIME - START_TIME) / 1000000 ))

  # Get memory after
  MEM_AFTER=$(./thanos-harness stats 2>&1 | grep -E "(querier|store)" || true)

  echo "Duration: ${DURATION}ms" >> "$RESULTS_FILE"
  echo "" >> "$RESULTS_FILE"
  echo "### Memory Before" >> "$RESULTS_FILE"
  echo '```' >> "$RESULTS_FILE"
  echo "$MEM_BEFORE" >> "$RESULTS_FILE"
  echo '```' >> "$RESULTS_FILE"
  echo "" >> "$RESULTS_FILE"
  echo "### Memory After" >> "$RESULTS_FILE"
  echo '```' >> "$RESULTS_FILE"
  echo "$MEM_AFTER" >> "$RESULTS_FILE"
  echo '```' >> "$RESULTS_FILE"
  echo "" >> "$RESULTS_FILE"
  echo "### Query Output" >> "$RESULTS_FILE"
  echo '```' >> "$RESULTS_FILE"
  echo "$RESULT" | head -50 >> "$RESULTS_FILE"
  echo '```' >> "$RESULTS_FILE"
  echo "" >> "$RESULTS_FILE"

  # Give GC time to run
  sleep 5
}

echo "" >> "$RESULTS_FILE"
echo "# Test Results" >> "$RESULTS_FILE"

# Simple queries - no label matching
echo "" >> "$RESULTS_FILE"
echo "## Part 1: Simple Queries (no joins)" >> "$RESULTS_FILE"

run_query "Count all series (1h)" \
  'count(test_metric)' \
  "1h"

run_query "Count all series (6h - full range)" \
  'count(test_metric)' \
  "6h"

run_query "Sum by instance (1h)" \
  'sum by (instance) (test_metric)' \
  "1h"

run_query "Sum by instance (6h)" \
  'sum by (instance) (test_metric)' \
  "6h"

run_query "Rate over 5m (1h range)" \
  'rate(test_metric[5m])' \
  "1h"

# Queries that simulate joins (label matching across high-cardinality dimensions)
echo "" >> "$RESULTS_FILE"
echo "## Part 2: Label Matching Queries (join-like)" >> "$RESULTS_FILE"

run_query "Group by all labels (1h)" \
  'count by (instance, job, env) (test_metric)' \
  "1h"

run_query "Group by all labels (6h)" \
  'count by (instance, job, env) (test_metric)' \
  "6h"

# Label value queries (high cardinality enumeration)
run_query "Label values enumeration" \
  'count(test_metric) by (instance)' \
  "6h"

# Topk queries (require full series scan)
run_query "TopK by instance (6h)" \
  'topk(100, sum by (instance) (test_metric))' \
  "6h"

# Regex matching across churned labels
run_query "Regex match on churned labels (6h)" \
  'count(test_metric{instance=~".*churn.*"})' \
  "6h"

run_query "Count stable vs churned series (6h)" \
  'count(test_metric{instance!~".*churn.*"})' \
  "6h"

# Heavy aggregation
run_query "Histogram quantile simulation (6h)" \
  'avg(test_metric) by (job)' \
  "6h"

# Stop harness
echo "" >> "$RESULTS_FILE"
echo "# Final Stats" >> "$RESULTS_FILE"
echo '```' >> "$RESULTS_FILE"
./thanos-harness stats >> "$RESULTS_FILE" 2>&1 || true
echo '```' >> "$RESULTS_FILE"

kill $HARNESS_PID 2>/dev/null || true

echo ""
echo "Results written to: $RESULTS_FILE"
cat "$RESULTS_FILE"
