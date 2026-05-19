# Cardinality Memory Sweep Results

**Date:** 2026-05-18  
**Configuration:** 6GB memory limit per component, range queries over 5-30min, 60s step  
**Harness version:** commit 74a5fd3

## Summary

Querier is the memory bottleneck at ~2x Prometheus memory usage. OOM occurs at ~2M series with 6GB limit.

## Results

| Series | Query Time | Prometheus | Querier | Sidecar | Status |
|--------|------------|------------|---------|---------|--------|
| 25k    | 891ms      | 131 MB     | 209 MB  | 65 MB   | OK |
| 50k    | 1.6s       | 225 MB     | 472 MB  | 107 MB  | OK |
| 75k    | 2.2s       | 320 MB     | 585 MB  | 154 MB  | OK |
| 100k   | 2.7s       | 417 MB     | 759 MB  | 211 MB  | OK |
| 150k   | 3.9s       | 597 MB     | 1.26 GB | 266 MB  | OK |
| 200k   | 3.6s       | 542 MB     | 1.0 GB  | 283 MB  | OK |
| 300k   | 5.4s       | 802 MB     | 1.6 GB  | 380 MB  | OK |
| 400k   | 7.3s       | 1.05 GB    | 2.0 GB  | 498 MB  | OK |
| 500k   | 8.8s       | 1.3 GB     | 2.6 GB  | 644 MB  | OK |
| 750k   | 10.8s      | 1.8 GB     | 3.0 GB  | 827 MB  | OK |
| 1M     | 14.4s      | 2.5 GB     | 4.2 GB  | 1.3 GB  | OK |
| 1.25M  | 13.4s      | 2.2 GB     | 3.9 GB  | 1.1 GB  | OK |
| 1.5M   | 16.5s      | 2.6 GB     | 5.2 GB  | 1.4 GB  | OK |
| 1.75M  | 19.8s      | 3.0 GB     | 4.9 GB  | 1.5 GB  | OK |
| 2M     | 21.9s      | 3.2 GB     | 6.0 GB  | 1.7 GB  | **OOM** |

## Key Findings

1. **Querier is the bottleneck** - Uses ~2x the memory of Prometheus for the same query
2. **~3KB per series** in querier for range queries
3. **6GB querier limit** handles ~1.75M series safely
4. **Linear scaling** - Doubling series roughly doubles memory
5. **Query time** scales roughly linearly with cardinality

## Test Details

- Query: `test_metric` range query over full duration
- Step: 60 seconds
- Memory limit enforcement: cgroup v2 with OOM kill
- Fresh harness restart between each test point
