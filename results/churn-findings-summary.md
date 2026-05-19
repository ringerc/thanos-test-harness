# High-Churn Memory Impact Findings

## Test Summary

Two test runs were performed to measure memory impact of series churn on Thanos Store Gateway:

1. **Moderate churn**: 10k series, 50% churnable at 20% rate/5min, 2h duration
2. **Aggressive churn**: 10k series, 80% churnable at 50% rate/5min, 12h duration

## Key Findings

### 1. Memory Limit Exceeded Under High Churn

| Test | Configured Limit | Peak Memory | Result |
|------|------------------|-------------|--------|
| Moderate | 10 GB | 8.2 GiB | OK |
| Aggressive | 10 GB | 20.2 GiB | **EXCEEDED 2x** |

The aggressive test created ~106k unique series per 2h block (vs ~31k for moderate).

### 2. Peak Memory Occurs During Block Loading, Not Queries

- Peak memory (20.2 GiB) occurred when Store Gateway loaded/synced blocks
- Runtime memory during queries: 2.6-2.8 GiB
- Query execution itself does not cause memory spikes

### 3. Query Performance Scales with Output Cardinality

| Query Type | Duration | Notes |
|------------|----------|-------|
| Instant (count, sum) | 200-400ms | Fast regardless of churn |
| Range 6h @ 60s step | 5-9s | Moderate |
| Range 12h @ 15s step | 51.6s | Fine resolution is expensive |
| Range with 292k series output | 8.4s | High cardinality adds cost |

### 4. Churn Creates Massive Label Cardinality

- 40% effective churn rate (50% of 80%) every 5 minutes
- Over 6 hours: 292,334 unique instance label values created
- Range queries that materialize all series are expensive but complete

## Recommendations

### Memory Sizing

| Churn Level | Series/Block | Recommended Memory |
|-------------|--------------|-------------------|
| Low (<10% effective) | ~10-30k | 8-10 GB |
| Moderate (10-20%) | ~30-60k | 12-15 GB |
| High (30-50%) | ~100k+ | 20-25 GB |

Formula: `~200 bytes × unique_series_per_block` for index loading overhead.

### Query Optimization

1. **Use instant queries** when possible - consistently fast (<500ms)
2. **Increase step interval** for range queries - 15s vs 60s = 6x slower
3. **Limit time range** - 12h queries are 2x slower than 6h
4. **Filter before aggregation** - reduce series count with label matchers

### Architectural Considerations

1. **Compaction** reduces block count but not unique series within blocks
2. **Retention** policies should account for churn creating large historical cardinality
3. **Store Gateway replicas** help with query load but not memory per instance
4. **Downsampling** reduces sample count but not series cardinality

## Test Data Reference

- Moderate test: [churn-memory-test-2026-05-19.md](churn-memory-test-2026-05-19.md)
- Aggressive test: [churn-memory-test-aggressive-2026-05-19.md](churn-memory-test-aggressive-2026-05-19.md)

## Reproduction

```bash
# Moderate churn test
./thanos-harness clean --all
./thanos-harness backfill --series=10000 --duration=2h \
  --churn-rate=0.2 --churn-fraction=0.5 --churn-interval=5m
./thanos-harness start --objstore-config=configs/objstore-filesystem.yaml \
  --memory-limit=10G

# Aggressive churn test  
./thanos-harness clean --all
./thanos-harness backfill --series=10000 --duration=12h \
  --churn-rate=0.5 --churn-fraction=0.8 --churn-interval=5m
./thanos-harness start --objstore-config=configs/objstore-filesystem.yaml \
  --memory-limit=10G
```
