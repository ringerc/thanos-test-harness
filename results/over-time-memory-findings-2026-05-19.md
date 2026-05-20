# Range Vector (_over_time) Memory Impact Analysis

> **METHODOLOGY CORRECTION (2026-05-20)**: The findings below were later found to be inaccurate due to measuring sudo wrapper processes instead of actual Thanos processes. See [memory-profiling-corrections-2026-05-20.md](memory-profiling-corrections-2026-05-20.md) for corrected data showing query memory DOES scale at ~1.4 kB per output series.

## Test Configuration
- Same high-churn dataset: 10k base series, 80% @ 50% churn rate
- 12h duration, ~106k unique series per 2h block
- Memory limit: 10GB per component

## Findings

### Key Result: Query-Time Memory is NOT Impacted by _over_time Functions

| Query | Lookback | Duration | Memory During | Memory After |
|-------|----------|----------|---------------|--------------|
| avg_over_time(test_metric[5m]) | 5 min | 537ms | 1.9 GiB | 1.9 GiB |
| avg_over_time(test_metric[1h]) | 1 hour | 1.4s | 1.9 GiB | 1.9 GiB |
| avg_over_time(test_metric[6h]) | 6 hours | 3.5s | 1.9 GiB | 1.9 GiB |
| max_over_time(test_metric[6h]) | 6 hours | 3.5s | 1.9 GiB | 1.9 GiB |

### Memory Profile
- **Baseline/Runtime**: 1.9 GiB (stable throughout all queries)
- **Peak**: 20.2 GiB (occurred during block loading, NOT queries)
- **During _over_time queries**: No increase from baseline

### Query Performance Scales with Lookback Duration

| Lookback | Instant Query | Series Count |
|----------|---------------|--------------|
| 5 min | ~400-500ms | 14,000 |
| 1 hour | ~1.2-1.4s | 68,000 |
| 6 hours | ~3.5s | 308,000 |

Longer lookbacks return more series because they capture series that existed
at any point during the lookback window (due to churn).

### Range Query Performance

| Query | Range | Step | Points × Series | Duration |
|-------|-------|------|-----------------|----------|
| avg_over_time[5m] | 1h | 60s | 61 × 72k | 1.5s |
| avg_over_time[1h] | 2h | 60s | 121 × 164k | 5.5s |
| max_over_time[6h] | 6h | 60s | 361 × 316k | 50s |
| sum(avg_over_time[1h]) by (job) | 6h | 60s | 361 × 10 | 16ms |

## Conclusions

1. **_over_time functions do NOT increase query-time memory**
   - Memory stays constant at ~1.9 GiB regardless of lookback duration
   - Thanos/Prometheus streaming architecture processes data incrementally

2. **Memory pressure comes from block loading, not queries**
   - Peak 20.2 GiB occurs when Store Gateway loads block indexes
   - This happens at startup/sync, independent of query patterns

3. **Query TIME scales with lookback, but not MEMORY**
   - Longer lookbacks = more samples to process = slower queries
   - But samples are streamed, not buffered in memory

4. **Aggregation drastically reduces cost**
   - `sum(...) by (job)` reduces 316k series to 10 series
   - Query time drops from 50s to 16ms

## Recommendations

1. For memory sizing: Focus on block loading, not query patterns
2. For query performance: Use aggregation to reduce output cardinality
3. Fine step granularity (15s vs 60s) has more impact than lookback duration
4. Pre-aggregate high-cardinality metrics with recording rules

## Reproduction

### Data Generation

```bash
# Clean previous test data
./thanos-harness clean --all

# Generate high-churn dataset (12h, 10k base series, 80% churnable @ 50% rate)
./thanos-harness backfill --series=10000 --duration=12h \
  --churn-rate=0.5 --churn-fraction=0.8 --churn-interval=5m

# Start components with memory limit
./thanos-harness start --objstore-config=configs/objstore-filesystem.yaml \
  --memory-limit=10G
```

### Queries Executed

All queries run against Thanos Querier at historical timestamps within the data range.

**Instant queries with varying lookback (at t=-6h):**
```promql
avg_over_time(test_metric[5m])
avg_over_time(test_metric[1h])
avg_over_time(test_metric[6h])
max_over_time(test_metric[6h])
sum_over_time(test_metric[6h])
```

**Range queries:**
```promql
# 1h range, 60s step
avg_over_time(test_metric[5m])  # start=-7h, end=-6h, step=60s

# 2h range, 60s step
avg_over_time(test_metric[1h])  # start=-8h, end=-6h, step=60s

# 6h range, 60s step
max_over_time(test_metric[6h])  # start=-12h, end=-6h, step=60s

# Aggregated (dramatic speedup)
sum(avg_over_time(test_metric[1h])) by (job)  # start=-12h, end=-6h, step=60s
```

### Memory Monitoring

```bash
# Watch Store Gateway memory during queries
watch -n1 './thanos-harness stats'
```

## Methodological Notes and Caveats

### 1. Measurement Timing Limitation

The test harness measures memory **after** query completion, not during execution:

```go
// harness.go Query()
preStats := h.manager.GetAllStats()
resp, err := http.Get(queryURL + "?" + params.Encode())  // Query runs
result.EndTime = time.Now()
postStats := h.manager.GetAllStats()  // Measured AFTER completion
```

The "Memory During" values reported are actually post-query memory. Any transient
query-time allocations freed before measurement would be missed. The Go garbage
collector may have run between query completion and stats collection.

### 2. Why Memory Doesn't Increase: Labels Are References (INCORRECT)

> **CORRECTION (2026-05-20)**: This analysis was flawed. The "references to index data" claim only applies within the Store Gateway process. In the Querier, all series data arrives via gRPC and must be fully deserialized — cross-process memory references are impossible.

~~Analysis of the Thanos PromQL engine shows that series data uses references, not copies.~~

**Actual data flow in Querier:**
```
Store Gateway              Querier
─────────────              ───────
indexes (mmap) ──serialize──► gRPC ──deserialize──► []storepb.Series
                                                    (new allocations)
```

The ZLabel optimization ([label.go:117-120](../../thanos/pkg/store/labelpb/label.go#L117)) reuses the gRPC receive buffer to avoid one string copy, but this buffer is still new memory allocated in the Querier process.

**Measured cost: ~1.4 kB per series in the Querier** (see [memory-profiling-corrections-2026-05-20.md](memory-profiling-corrections-2026-05-20.md)).

### 3. Estimated Query-Time Memory Cost (INCORRECT)

> **CORRECTION (2026-05-20)**: These estimates were wrong. Measured cost is ~1.4 kB per series in the Querier.

~~For 300k series with ~5 labels each:~~
~~- Label string data: **shared with index** (not copied)~~
~~- Total query overhead: ~15-50 MB~~

**Corrected estimate for 300k series:**
- Measured cost: 1.4 kB × 300k = **~420 MB** in Querier
- This is NOT negligible for high-cardinality queries
- With 10 concurrent queries: 4.2 GB query overhead alone

### 4. The 20.2 GiB Peak Source

The `MemoryMax` (high water mark) was set during **block index loading** at startup,
not during queries. Index headers store:
- Posting offsets for all label combinations
- Symbol tables for label names/values
- Series metadata

With ~106k unique series per block × 7 blocks, index structures dominate memory.

### 5. VectorOperator Concern (CONFIRMED)

The Thanos PromQL engine's VectorOperator does create `[]Series` arrays containing
all matching series. This DOES cause memory to scale with series count.

> **CORRECTION (2026-05-20)**: The "mitigation" claims below were wrong.

~~However, the impact is mitigated by:~~
~~- Label data being shared references (not copies)~~

**Why the mitigation claim was wrong:**
- In the Querier, label data is NOT shared with indexes — it arrives via gRPC
- Cross-process memory references are impossible
- The ZLabel optimization only avoids one copy within the Querier's protobuf buffer

**Measured impact: ~1.4 kB per series** — significant for high-cardinality queries.

### Recommendations for Future Testing

To properly verify query-time memory behavior:
1. Use Go runtime memory profiling (`runtime.ReadMemStats`) during query execution
2. Sample memory at high frequency (10-100ms intervals) throughout the query
3. Disable GC temporarily during measurement to capture peak transient allocations
4. Use `GODEBUG=gctrace=1` to observe GC behavior during queries
