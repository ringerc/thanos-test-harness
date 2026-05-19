# Range Vector (_over_time) Memory Impact Analysis

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
