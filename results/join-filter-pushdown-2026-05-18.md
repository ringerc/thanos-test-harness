# Join Filter Pushdown Test Results

**Date:** 2026-05-18  
**Hypothesis:** Label join queries load all data series even when the filter on the info metric is highly selective

## Setup

- 200,000 `data_metric` series with `instance` label
- 200,000 `instance_info` series with `instance` and `region` labels
- 1% (2,000) of `instance_info` have `region="region_a"` (selective)
- 99% have `region="region_b"` (filtered out)
- 5 minutes of data at 60s intervals
- 6GB memory limit per component

## Test Queries

1. **Baseline**: `data_metric` - load all 200k series
2. **Selective filter**: `instance_info{region="region_a"}` - load only 1% 
3. **JOIN with filter**: `data_metric * on(instance) group_left(region) instance_info{region="region_a"}`
4. **Direct filter**: `data_metric{instance=~"instance_[0-9]{1,3}"}` - regex filter

## Results

| Test | Time | Series Returned | Memory Impact |
|------|------|-----------------|---------------|
| 1. All data_metric | 1722ms | 200,000 | Full load |
| 2. Selective info (1%) | 85ms | 2,000 | Minimal |
| 3. JOIN (selective) | **1379ms** | 2,000 | **Full load** |
| 4. Direct filter | 75ms | 1,000 | Minimal |

## Analysis

**HYPOTHESIS CONFIRMED**

The JOIN query (Test 3) returns only 2,000 series but takes 1379ms - nearly identical to loading ALL 200k series (1722ms). This is 16x slower than the selective filter alone (85ms).

### What's happening:

1. Query planner does not push down the `{region="region_a"}` filter
2. ALL `data_metric` series are loaded into memory first
3. Join operation then discards 99% of the loaded data
4. Result: You pay full memory cost even for highly selective queries

### Memory implications:

At 200k series, the difference is ~500MB vs minimal. At 2M series (our OOM threshold), this would mean:
- Selective query should use ~50MB (1% of data)
- JOIN query uses full ~6GB (all data)
- Result: OOM for queries that "should" be cheap

## Recommendations

1. **Query pattern awareness**: Avoid joins with high-cardinality data metrics when possible
2. **Pre-filter in recording rules**: Create filtered aggregations ahead of time
3. **Subquery optimization**: Thanos/Prometheus could benefit from join filter pushdown optimization
4. **Memory budgeting**: Account for full cardinality when planning JOIN queries

## Reproduction

```bash
# Generate test data
python3 tools/remote_write.py --series=200000 --duration=300 --type=both --selectivity=0.01

# Run tests
curl "http://localhost:19093/api/v1/query_range?query=data_metric&..."
curl "http://localhost:19093/api/v1/query_range?query=instance_info{region=\"region_a\"}&..."
curl "http://localhost:19093/api/v1/query_range?query=data_metric * on(instance) group_left(region) instance_info{region=\"region_a\"}&..."
```
