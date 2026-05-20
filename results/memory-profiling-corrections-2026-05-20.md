# Memory Profiling Corrections - 2026-05-20

## Prior Claims Being Tested

The previous findings (over-time-memory-findings-2026-05-19.md) claimed:
1. "Query-time memory is NOT impacted by _over_time functions"
2. "Memory stays constant at ~1.9 GiB regardless of lookback duration"
3. Labels are references, so `[]Series` allocation is negligible

## Methodology Correction

**Critical Finding: Prior profiling tracked wrong PIDs**

The harness profiler was tracking the `sudo` wrapper processes (used for cgroup management), not the actual Go processes:

```
# Tracked (wrong - sudo wrappers):
sudo (PID 128400): RSS=6,648 kB  
sudo (PID 128507): RSS=6,688 kB

# Actual processes:
prometheus (PID 128402): RSS=85,564 kB
thanos query (PID 128509): RSS=441,052 kB
thanos store (PID 128471): RSS=381,508 kB
```

The ~6.5 MiB constant RSS reported in prior tests was the sudo wrapper memory, not the actual Thanos component memory.

## Corrected Findings

### Test Configuration
- 10k base series, 80% churnable at 50% churn rate per 5min
- 4h duration, creating up to 106k unique series per block
- Memory sampled at 10-20ms intervals from actual process PIDs

### Query-Time Memory DOES Scale

| Query Type | Output Series | Store Delta | Querier Delta | Per-Series |
|------------|---------------|-------------|---------------|------------|
| count(test_metric) | 1 | 40 kB | 32 kB | 32 kB |
| sum by (job) | 10 | 16 kB | 2.9 MB | 290 kB |
| avg_over_time[5m] | 10,000 | 260 kB | 14.7 MB | 1.5 kB |
| avg_over_time[1h] | 54,000 | 2.6 MB | 72.4 MB | 1.3 kB |
| avg_over_time[2h] | 102,000 | 12.3 MB | 143 MB | 1.4 kB |
| test_metric (raw) | 10,000 | 0 kB | 3.1 MB | 0.3 kB |
| regex on churned | 8,000 | 1.7 MB | 2.3 MB | 0.3 kB |

### Key Corrections to Prior Claims

1. **Query-time memory DOES scale with output cardinality**
   - Querier uses approximately **1.4 kB per output series**
   - Store Gateway uses approximately **0.12 kB per output series**
   - This is NOT negligible for high-cardinality queries

2. **_over_time lookback duration affects memory indirectly**
   - Longer lookback captures more churned series
   - 5m lookback: 10k series returned
   - 2h lookback: 102k series returned (due to churn)
   - Memory scales with series count, not lookback duration directly

3. **Block loading is still the largest single spike**
   - Store Gateway peak from block loading: 390-460 MB
   - Query execution added: 12-143 MB depending on cardinality
   - But query spikes are additive and can compound

### Memory Profile During Heavy Query (102k series)

```
Time         Store RSS    Querier RSS
00:35:31.4   381,508 kB   441,052 kB  (before)
00:35:31.6   352,560 kB   441,052 kB  (GC in progress)
00:35:31.8   383,320 kB   441,108 kB  (fetching)
00:35:32.1   385,756 kB   462,572 kB  (processing)
00:35:32.3   385,756 kB   545,820 kB  (peak processing)
00:35:32.5   385,756 kB   547,872 kB  (sending results)
```

Peak querier memory: 577,744 kB (564 MB)
Total increase during query: ~137 MB

## Impact on Prior Documentation

### corrections-memory-findings-2026-05-20.md needs update

The following claims should be revised:

1. ~~"Query-time memory from `[]Series` allocation is negligible"~~
   - **Corrected**: Query-time memory is ~1.4 kB per series, significant for high-cardinality

2. ~~"RSS stayed constant at 6.5-6.6 MiB"~~
   - **Corrected**: This was measuring sudo wrappers, not actual processes

3. ~~"Memory stays constant at ~1.9 GiB regardless of lookback duration"~~
   - **Corrected**: Memory scales with output series count. Lookback affects this via churn.

### Claims that remain valid

1. **Block loading is still the largest single memory spike** - 20+ GiB for high-churn workloads
2. **Aggregation reduces memory** - `sum by (job)` uses far less than returning raw series
3. **Ring buffers are streaming** - sample processing is incremental

### Claims that need clarification

1. ~~"Labels are shared references to block indexes"~~
   - **Only true within Store Gateway** (same process, memory-mapped indexes)
   - **False for Querier** - data arrives via gRPC, requiring full deserialization
   
   The Querier receives `storepb.Series` over the network. Cross-process memory references are impossible. The ZLabel optimization ([label.go:117-120](../../thanos/pkg/store/labelpb/label.go#L117)) reuses the **gRPC receive buffer** (avoiding one string copy), not the Store Gateway's indexes.
   
   Data flow:
   ```
   Store Gateway              Querier
   ─────────────              ───────
   indexes (mmap) ──serialize──► gRPC ──deserialize──► []storepb.Series
                                                       (new allocations)
   ```

## Revised Memory Sizing Guidance

For Querier:
- Base memory: ~100-200 MB
- Per concurrent query with N output series: ~1.5 kB × N
- Example: 10 concurrent queries × 100k series each = 1.5 GB query overhead

For Store Gateway:
- Base memory: ~50-100 MB after index loading
- Index loading: ~200 bytes × unique_series_per_block × blocks
- Per query: ~0.15 kB × output series

## Reproduction

```bash
# Generate high-churn data
./thanos-harness clean --all
./thanos-harness backfill --series=10000 --duration=4h \
  --churn-rate=0.5 --churn-fraction=0.8 --churn-interval=5m

# Start with profiling-friendly setup
./thanos-harness start --objstore-config=configs/objstore-filesystem.yaml \
  --memory-limit=10G

# Find actual process PIDs (not sudo wrappers)
ps aux | grep -E "/thanos|/prometheus" | grep -v sudo | grep -v harness

# Monitor during query
(while true; do
  store_rss=$(grep VmRSS /proc/<STORE_PID>/status | awk '{print $2}')
  querier_rss=$(grep VmRSS /proc/<QUERIER_PID>/status | awk '{print $2}')
  echo "$(date +%s.%3N) $store_rss $querier_rss"
  sleep 0.02
done) > mem_samples.txt &

# Run query
curl "http://localhost:19200/api/v1/query?query=avg_over_time(test_metric[2h])"
```
