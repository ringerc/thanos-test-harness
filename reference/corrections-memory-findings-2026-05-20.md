# Corrections and Clarifications Based on Memory Testing

Based on empirical testing documented in `test-harness/results/`, several claims in the reference documents require correction or clarification.

## Test Summary

Tests were conducted with:
- 10k base series, 80% churnable at 50% churn rate per 5min
- 12h duration creating ~106k unique series per 2h block
- Memory limit: 10GB per component
- Queries including `_over_time` functions with 5m to 6h lookbacks

Key findings (corrected 2026-05-20 after PID tracking fix):
- **Peak memory: 20.2 GiB** - occurred during Store Gateway block loading
- **Query-time memory scales at ~1.4 kB per output series** in the Querier
- **Cross-process data flow**: Querier cannot reference Store Gateway indexes — all series data is deserialized from gRPC

## Corrections to thanos-notes.md

### Lines 83-87: VectorOperator []Series Memory Impact

**Original claim:**
> Thanos PromQL engine's `VectorOperator` uses concrete golang slices of `[]Series` [...] to represent the data series being processed by a pipeline step. The concrete golang slice cannot easily be spilled to disk or abstracted for chunked fetching.

**Status: CONFIRMED (with measurement correction)**

Re-testing on 2026-05-20 with corrected methodology (tracking actual process PIDs, not sudo wrappers) shows:

| Output Series | Querier Memory Delta |
|---------------|---------------------|
| 1 | 32 kB |
| 10,000 | 14.7 MB |
| 54,000 | 72.4 MB |
| 102,000 | 143 MB |

**Measured cost: ~1.4 kB per output series** in the Querier.

The original claim is correct - `[]Series` allocation IS significant for high-cardinality queries. Earlier tests showing "constant 1.9 GiB" were measuring sudo wrapper processes, not the actual Thanos components.

**Practical impact:** A query returning 100k series adds ~140 MB to querier memory. This is additive with concurrent queries.

### Lines 54-55: All Data Must Fit in Memory

**Original claim:**
> It also means that all the data returned by all the selectors in a query must fit in memory at once.

**Status: CONFIRMED**

Re-testing shows this claim is accurate. Query memory scales with output series count:

- 10k series: +15 MB querier memory
- 102k series: +143 MB querier memory

The earlier claim of "constant 1.9 GiB" was due to measuring wrong process PIDs (sudo wrappers showed ~6.5 MB constant because they do no work).

**Note on label sharing:** The claim that "labels reference index data" is only valid within the Store Gateway (same process). In the Querier, all series data arrives via gRPC and must be fully deserialized — cross-process memory references are impossible. The 1.4 kB/series in the Querier is entirely new allocations from protobuf deserialization.

### Lines 89-94: VectorOperator Step Processing

**Original claim:**
> When processing a data set in `VectorOperator`, the sample data is represented using an index into the ordered series slice. [...] This means that processing of data points must have access to all the entries in the (potentially very large) series array.

**Status: CONFIRMED**

The claim is correct. In the Querier, `[]Series` cannot reference external data because:
1. Series data arrives via gRPC from Store Gateway (separate process)
2. Protobuf deserialization creates new memory allocations
3. ZLabel optimization reuses the gRPC receive buffer, not block indexes

Memory cost in Querier: ~1.4 kB per series (full protobuf deserialization).

The actual sample processing does use streaming ring buffers, but the series metadata (labels) must be fully materialized in Querier memory.

## Corrections to Metrics stack update.md

### Line 23: Source of Memory Spikes

**Original claim:**
> Most real-world memory-use spike problems are caused by intermediate data fetching stages where Thanos Query requests very large data sets via Series gRPCs from Store, Receive, Sidecar etc.

**Clarification (revised 2026-05-20):**
Testing shows block index loading (20.2 GiB) is the largest single spike, but query-time memory is also significant:
- Querier adds ~1.4 kB per output series during query execution
- 100k series query = ~140 MB additional memory
- Concurrent queries are additive

The original claim about "intermediate data fetching" causing spikes is partially correct — the Querier must deserialize all series data from gRPC, which requires full memory allocation (not references to Store Gateway indexes).

**Recommendation:** Size for both block loading AND concurrent query overhead.

### Line 26: Query Splitting Memory Benefits

**Original claim:**
> Splitting up queries by time may have a larger cost and lower benefit than hoped [...] the memory cost of loading the series is similar for the 1000 step case and the 100 step case

**Confirmed and clarified:**
This is correct. Query step count affects execution time but not memory.

However, the claim about "small overhead" was wrong. Memory cost is determined by:
1. Block index loading (at startup) — largest single spike
2. Output series count — ~1.4 kB per series in Querier (NOT small for high cardinality)

Query step count affects **execution time** (51s for 15s steps vs 9s for 60s steps). Memory scales with **output series count**, not step count.

## New Findings Not in Reference Docs

### 1. Churn is the Primary Memory Driver for Block Loading

High series churn dramatically increases Store Gateway memory by creating many unique series per block:
- Low churn (~10k series): 8-10 GB sufficient
- High churn (40% effective rate creating ~106k series/block): 20-25 GB required

Formula: `~200 bytes × unique_series_per_block` for index loading overhead.

### 2. Query Memory Measurement Methodology - CORRECTED

**Initial methodology flaw:** The test harness profiler tracked sudo wrapper PIDs, not actual process PIDs. This made all measurements show ~6.5 MB constant (the sudo process memory).

**Corrected methodology (2026-05-20):** 
- Identify actual process PIDs via `ps aux | grep -E "/thanos|/prometheus"`
- Sample `/proc/{pid}/status` VmRSS at 10-20ms intervals
- Track both current RSS and VmHWM (high water mark)

### 3. Cross-Process Data Flow (Critical Architectural Insight)

Prior analysis assumed "labels are shared references to block indexes" applied to the Querier. This is architecturally impossible:

```
Store Gateway (Process A)          Querier (Process B)
─────────────────────────          ──────────────────
memory-mapped indexes              CANNOT reference Process A memory
        │                                    │
        ▼                                    ▼
serialize to protobuf ───── gRPC ─────► deserialize protobuf
                                              │
                                              ▼
                                        []storepb.Series
                                        (new allocations)
```

**Code evidence:**
- [querier.go:229](../thanos/pkg/query/querier.go#L229): `s.seriesSet = append(s.seriesSet, *r.GetSeries())` — copies each Series from gRPC
- [label.go:117-120](../thanos/pkg/store/labelpb/label.go#L117): ZLabel reuses the gRPC receive buffer (not indexes)

**Implication:** The 1.4 kB/series in Querier is real memory cost, not "negligible pointer overhead."

### 4. Block Loading vs Query Execution - REVISED

| Phase | Memory Impact | Notes |
|-------|--------------|-------|
| Block loading | 390-460 MB (Store Gateway) | One-time at startup |
| Query execution | +1.4 kB per output series (Querier) | Scales with cardinality |
| High-cardinality query | +143 MB for 102k series | Significant for analytics |

**Key insight:** Both block loading AND query execution contribute to memory. Block loading is the largest single spike, but query memory is significant and scales with output cardinality.

### 5. Corrected Memory Scaling Data

| Query | Output Series | Querier Delta |
|-------|---------------|---------------|
| count(test_metric) | 1 | +32 kB |
| sum by (job) | 10 | +2.9 MB |
| avg_over_time[5m] | 10k | +14.7 MB |
| avg_over_time[1h] | 54k | +72.4 MB |
| avg_over_time[2h] | 102k | +143 MB |

Per-series cost: **~1.4 kB** in Querier, **~0.12 kB** in Store Gateway

## Recommendations - REVISED

1. **Memory sizing**: 
   - Store Gateway: Size for block loading (~200 bytes × series per block × blocks)
   - Querier: Add ~1.5 kB × max_concurrent_queries × avg_output_series
   
2. **Churn management**: Limit series churn to control both:
   - Block index size (affects Store Gateway baseline)
   - Output series count (affects query-time memory)

3. **Query optimization**: 
   - Use aggregation (`sum by`, `count by`) to reduce output cardinality
   - Longer lookback with high churn = more series = more memory
   - Consider pre-aggregation with recording rules

4. **Monitoring**: 
   - Track actual process PIDs, not wrapper processes
   - Use VmRSS and VmHWM from `/proc/{pid}/status`
   - Sample at ≤20ms intervals to capture query spikes

## References

- [over-time-memory-findings-2026-05-19.md](../test-harness/results/over-time-memory-findings-2026-05-19.md)
- [churn-memory-test-aggressive-2026-05-19.md](../test-harness/results/churn-memory-test-aggressive-2026-05-19.md)
- [churn-findings-summary.md](../test-harness/results/churn-findings-summary.md)
