#!/bin/bash
# Test hypothesis: Label join queries load all data series even when filter is selective

set -e

HARNESS_BIN="${HARNESS_BIN:-/tmp/thanos-harness}"
DATA_DIR="${DATA_DIR:-/tmp/thanos-harness-join-test}"
MEMORY_LIMIT="${MEMORY_LIMIT:-6G}"
PROM_URL="http://localhost:19090"
QUERIER_URL="http://localhost:19093"

# Parameters
DATA_SERIES="${DATA_SERIES:-500000}"    # High cardinality data metric
INFO_SELECTIVITY="${INFO_SELECTIVITY:-0.01}"  # 1% of instances match filter
DURATION_MIN="${DURATION_MIN:-10}"

echo "=== Join Memory Test ==="
echo "Data series: $DATA_SERIES"
echo "Info selectivity: $INFO_SELECTIVITY ($(echo "$DATA_SERIES * $INFO_SELECTIVITY" | bc | cut -d. -f1) matching)"
echo ""

# Cleanup
cleanup() {
    pkill -9 prometheus 2>/dev/null || true
    pkill -9 thanos 2>/dev/null || true
    sleep 2
    for cg in /sys/fs/cgroup/thanos-harness/thanos-harness-*; do
        sudo rmdir "$cg" 2>/dev/null || true
    done
    sudo rmdir /sys/fs/cgroup/thanos-harness 2>/dev/null || true
    rm -rf "$DATA_DIR"
}

cleanup

# Start harness
echo "Starting harness with $MEMORY_LIMIT limit..."
$HARNESS_BIN start --memory-limit=$MEMORY_LIMIT --dir=$DATA_DIR >/dev/null 2>&1 &
sleep 10

# Generate and send data metric via remote write
echo "Generating data_metric with $DATA_SERIES series..."
python3 << PYTHON
import requests
import struct
import snappy
import time
import sys

# Simple protobuf encoding for remote write (avoiding full protobuf dependency)
def encode_varint(value):
    bits = value & 0x7f
    value >>= 7
    result = b''
    while value:
        result += bytes([0x80 | bits])
        bits = value & 0x7f
        value >>= 7
    result += bytes([bits])
    return result

def encode_string(field_num, s):
    tag = (field_num << 3) | 2
    encoded = s.encode('utf-8')
    return encode_varint(tag) + encode_varint(len(encoded)) + encoded

def encode_label(name, value):
    # Label message: field 1 = name, field 2 = value
    content = encode_string(1, name) + encode_string(2, value)
    return content

def encode_sample(timestamp_ms, value):
    # Sample message: field 1 = value (double), field 2 = timestamp (int64)
    import struct
    content = bytes([0x09]) + struct.pack('<d', value)  # field 1, wire type 1 (64-bit)
    content += bytes([0x10]) + encode_varint(timestamp_ms)  # field 2, wire type 0 (varint)
    return content

def encode_timeseries(labels, samples):
    # TimeSeries: field 1 = labels (repeated), field 2 = samples (repeated)
    content = b''
    for name, value in labels:
        label_bytes = encode_label(name, value)
        content += bytes([0x0a]) + encode_varint(len(label_bytes)) + label_bytes
    for ts, val in samples:
        sample_bytes = encode_sample(ts, val)
        content += bytes([0x12]) + encode_varint(len(sample_bytes)) + sample_bytes
    return content

def encode_write_request(timeseries_list):
    content = b''
    for ts_bytes in timeseries_list:
        content += bytes([0x0a]) + encode_varint(len(ts_bytes)) + ts_bytes
    return content

def send_batch(url, timeseries_list):
    write_req = encode_write_request(timeseries_list)
    compressed = snappy.compress(write_req)

    resp = requests.post(
        url + '/api/v1/write',
        data=compressed,
        headers={
            'Content-Type': 'application/x-protobuf',
            'Content-Encoding': 'snappy',
            'X-Prometheus-Remote-Write-Version': '0.1.0'
        }
    )
    if resp.status_code >= 400:
        print(f"Error: {resp.status_code} {resp.text}", file=sys.stderr)
        return False
    return True

# Generate data
num_series = $DATA_SERIES
selectivity = $INFO_SELECTIVITY
duration_min = $DURATION_MIN
now = int(time.time() * 1000)
start = now - (duration_min * 60 * 1000)

# Number of instances that will be in "region_a" (the selective filter)
num_matching = max(1, int(num_series * selectivity))

print(f"Generating {num_series} data_metric series...")
print(f"  {num_matching} instances will be in region_a (selective)")
print(f"  {num_series - num_matching} instances will be in region_b (filtered out)")

batch = []
batch_size = 1000
total_sent = 0

# Generate data_metric series
for i in range(num_series):
    instance = f"instance_{i}"
    labels = [("__name__", "data_metric"), ("instance", instance), ("job", "test")]

    # Generate samples at 60s intervals
    samples = []
    for t in range(start, now, 60000):
        samples.append((t, float(i % 100)))

    ts_bytes = encode_timeseries(labels, samples)
    batch.append(ts_bytes)

    if len(batch) >= batch_size:
        if send_batch("$PROM_URL", batch):
            total_sent += len(batch)
            print(f"\r  Sent {total_sent}/{num_series} data_metric series", end="", flush=True)
        batch = []

if batch:
    if send_batch("$PROM_URL", batch):
        total_sent += len(batch)

print(f"\n  Total: {total_sent} data_metric series")

# Generate instance_info series (1 sample each, info metrics have value 1)
print(f"Generating {num_series} instance_info series...")
batch = []
total_sent = 0

for i in range(num_series):
    instance = f"instance_{i}"
    # Only first num_matching instances are in region_a
    region = "region_a" if i < num_matching else "region_b"

    labels = [("__name__", "instance_info"), ("instance", instance), ("region", region)]
    samples = [(now, 1.0)]

    ts_bytes = encode_timeseries(labels, samples)
    batch.append(ts_bytes)

    if len(batch) >= batch_size:
        if send_batch("$PROM_URL", batch):
            total_sent += len(batch)
            print(f"\r  Sent {total_sent}/{num_series} instance_info series", end="", flush=True)
        batch = []

if batch:
    if send_batch("$PROM_URL", batch):
        total_sent += len(batch)

print(f"\n  Total: {total_sent} instance_info series")
print("Data generation complete.")
PYTHON

# Wait for data to be queryable
echo "Waiting for data to be indexed..."
sleep 5

# Reset memory peaks
for cg in /sys/fs/cgroup/thanos-harness/thanos-harness-*; do
    echo 0 | sudo tee $cg/memory.peak >/dev/null 2>&1
done

echo ""
echo "=== Test 1: Baseline - Query all data_metric ==="
echo "Query: count(data_metric)"
start_time=$(date +%s%N)
result=$(curl -s "$QUERIER_URL/api/v1/query?query=count(data_metric)")
end_time=$(date +%s%N)
echo "Result: $(echo $result | jq -r '.data.result[0].value[1]') series"
echo "Time: $(( (end_time - start_time) / 1000000 ))ms"

echo ""
echo "Memory after baseline query:"
for cg in /sys/fs/cgroup/thanos-harness/thanos-harness-*; do
    name=$(basename $cg)
    peak=$(cat $cg/memory.peak 2>/dev/null)
    echo "  $name: $(numfmt --to=iec $peak)"
done

# Reset peaks
for cg in /sys/fs/cgroup/thanos-harness/thanos-harness-*; do
    echo 0 | sudo tee $cg/memory.peak >/dev/null 2>&1
done
sleep 2

echo ""
echo "=== Test 2: Selective filter only ==="
echo "Query: count(instance_info{region=\"region_a\"})"
start_time=$(date +%s%N)
result=$(curl -s "$QUERIER_URL/api/v1/query?query=count(instance_info{region=\"region_a\"})")
end_time=$(date +%s%N)
echo "Result: $(echo $result | jq -r '.data.result[0].value[1]') series"
echo "Time: $(( (end_time - start_time) / 1000000 ))ms"

echo ""
echo "Memory after selective query (should be LOW - only ${INFO_SELECTIVITY}% of series):"
for cg in /sys/fs/cgroup/thanos-harness/thanos-harness-*; do
    name=$(basename $cg)
    peak=$(cat $cg/memory.peak 2>/dev/null)
    echo "  $name: $(numfmt --to=iec $peak)"
done

# Reset peaks
for cg in /sys/fs/cgroup/thanos-harness/thanos-harness-*; do
    echo 0 | sudo tee $cg/memory.peak >/dev/null 2>&1
done
sleep 2

echo ""
echo "=== Test 3: JOIN query - data * on(instance) info{region=\"region_a\"} ==="
echo "This is the critical test - does it load ALL data_metric series?"
echo "Query: sum(data_metric * on(instance) group_left(region) instance_info{region=\"region_a\"})"
start_time=$(date +%s%N)
result=$(curl -s --max-time 300 "$QUERIER_URL/api/v1/query?query=sum(data_metric%20*%20on(instance)%20group_left(region)%20instance_info%7Bregion%3D%22region_a%22%7D)")
end_time=$(date +%s%N)
status=$(echo $result | jq -r '.status')
echo "Status: $status"
if [ "$status" = "success" ]; then
    echo "Result: $(echo $result | jq -r '.data.result[0].value[1]')"
fi
echo "Time: $(( (end_time - start_time) / 1000000 ))ms"

echo ""
echo "=== CRITICAL: Memory after JOIN query ==="
echo "If hypothesis is correct, this should be HIGH (all data loaded)"
echo "If optimizer is smart, this should be LOW (only matching data loaded)"
for cg in /sys/fs/cgroup/thanos-harness/thanos-harness-*; do
    name=$(basename $cg)
    peak=$(cat $cg/memory.peak 2>/dev/null)
    oom=$(grep oom_kill $cg/memory.events 2>/dev/null | awk '{print $2}')
    echo "  $name: $(numfmt --to=iec $peak) (OOM kills: $oom)"
done

echo ""
echo "=== Summary ==="
echo "If querier memory for Test 3 ≈ Test 1, hypothesis CONFIRMED: full data loaded despite selective filter"
echo "If querier memory for Test 3 ≈ Test 2, hypothesis REJECTED: optimizer pushes filter down"

# Cleanup
cleanup
