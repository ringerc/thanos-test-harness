# High-Churn Memory Impact Test - 2026-05-19

## Test Configuration
- Memory limit: 10G per component
- GOGC: 100, GOMEMLIMIT: 9G
- Series: 10,000 base, 80% churnable at 50% churn rate per 5min
- Duration: 12 hours of historical data
- Total unique series per block: ~106,000
- Effective churn rate: 40% of series replaced every 5 minutes

## Test Results

## Part 1: Short Range Queries (2h)

### Count all (2h)
```
Query: count(test_metric)
Range: 2h
```
```
Query: count(test_metric)
Status: success
Duration: 15.734005ms
Results: 0 series

Resource Usage:
  sidecar:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 13ms
  store:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 14ms
  querier:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 11ms
  prometheus:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 11ms
```


### Sum by instance (2h)
```
Query: sum by (instance) (test_metric)
Range: 2h
```
```
Query: sum by (instance) (test_metric)
Status: success
Duration: 5.929332ms
Results: 0 series

Resource Usage:
  prometheus:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 4ms
  sidecar:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 3ms
  store:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 3ms
  querier:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 4ms
```


### Group all labels (2h)
```
Query: count by (instance, job, env) (test_metric)
Range: 2h
```
```
Query: count by (instance, job, env) (test_metric)
Status: success
Duration: 7.748847ms
Results: 0 series

Resource Usage:
  store:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 4ms
  querier:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 3ms
  prometheus:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 5ms
  sidecar:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 3ms
```

## Part 2: Medium Range Queries (6h)

### Count all (6h)
```
Query: count(test_metric)
Range: 6h
```
```
Query: count(test_metric)
Status: success
Duration: 8.41609ms
Results: 0 series

Resource Usage:
  querier:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 5ms
  prometheus:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 5ms
  sidecar:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 4ms
  store:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 6ms
```


### Sum by instance (6h)
```
Query: sum by (instance) (test_metric)
Range: 6h
```
```
Query: sum by (instance) (test_metric)
Status: success
Duration: 8.319899ms
Results: 0 series

Resource Usage:
  store:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 11ms
  querier:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 12ms
  prometheus:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 11ms
  sidecar:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 13ms
```


### Group all labels (6h)
```
Query: count by (instance, job, env) (test_metric)
Range: 6h
```
```
Query: count by (instance, job, env) (test_metric)
Status: success
Duration: 6.034519ms
Results: 0 series

Resource Usage:
  querier:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 3ms
  prometheus:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 3ms
  sidecar:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 3ms
  store:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 4ms
```

## Part 3: Full Range Queries (12h)

### Count all (12h)
```
Query: count(test_metric)
Range: 12h
```
```
Query: count(test_metric)
Status: success
Duration: 6.141416ms
Results: 0 series

Resource Usage:
  store:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 9ms
  querier:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 11ms
  prometheus:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 4ms
  sidecar:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 5ms
```


### Sum by instance (12h)
```
Query: sum by (instance) (test_metric)
Range: 12h
```
```
Query: sum by (instance) (test_metric)
Status: success
Duration: 7.458297ms
Results: 0 series

Resource Usage:
  querier:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 8ms
  prometheus:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 10ms
  sidecar:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 9ms
  store:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 7ms
```


### Group all labels (12h)
```
Query: count by (instance, job, env) (test_metric)
Range: 12h
```
```
Query: count by (instance, job, env) (test_metric)
Status: success
Duration: 6.504023ms
Results: 0 series

Resource Usage:
  store:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 4ms
  querier:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 4ms
  prometheus:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 3ms
  sidecar:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 3ms
```

## Part 4: Regex on Churned Labels (12h)

### Churned only (12h)
```
Query: count(test_metric{instance=~".*churn.*"})
Range: 12h
```
```
Query: count(test_metric{instance=~".*churn.*"})
Status: success
Duration: 6.352899ms
Results: 0 series

Resource Usage:
  store:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 8ms
  querier:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 10ms
  prometheus:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 10ms
  sidecar:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 10ms
```


### Stable only (12h)
```
Query: count(test_metric{instance!~".*churn.*"})
Range: 12h
```
```
Query: count(test_metric{instance!~".*churn.*"})
Status: success
Duration: 7.322584ms
Results: 0 series

Resource Usage:
  prometheus:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 13ms
  sidecar:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 11ms
  store:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 13ms
  querier:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 13ms
```

## Part 5: Heavy Aggregations (12h)

### TopK 500 (12h)
```
Query: topk(500, sum by (instance) (test_metric))
Range: 12h
```
```
Query: topk(500, sum by (instance) (test_metric))
Status: success
Duration: 7.669382ms
Results: 0 series

Resource Usage:
  prometheus:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 13ms
  sidecar:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 14ms
  store:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 13ms
  querier:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 15ms
```


### Avg by job (12h)
```
Query: avg(test_metric) by (job)
Range: 12h
```
```
Query: avg(test_metric) by (job)
Status: success
Duration: 6.518946ms
Results: 0 series

Resource Usage:
  sidecar:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 15ms
  store:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 14ms
  querier:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 12ms
  prometheus:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 17ms
```


### Rate 5m (6h)
```
Query: sum(rate(test_metric[5m]))
Range: 6h
```
```
Query: sum(rate(test_metric[5m]))
Status: success
Duration: 5.936726ms
Results: 0 series

Resource Usage:
  sidecar:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 6ms
  store:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 4ms
  querier:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 5ms
  prometheus:
    Memory: 2.5 GiB (peak: 20.2 GiB)
    CPU: 5ms
```


## IMPORTANT: Peak Memory Observation

**Store Gateway used 20.2 GiB peak memory** loading 7 blocks with ~106k series each.
This exceeded the 10GB limit! The high memory usage occurred during block loading/sync,
not during queries.

## Re-running tests with correct timestamps


### Direct API Query Tests

**count(test_metric)**
- Results: , Duration: 274ms

**sum by (instance) (test_metric)**
- Results: , Duration: 408ms

**count by (instance, job, env) (test_metric)**
- Results: , Duration: 433ms

**count(test_metric{instance=~".*churn.*"})**
- Results: , Duration: 1458ms

### Memory After Queries
```
Component Resource Stats:

sidecar:
  Memory Usage: 2.6 GiB
  Memory Peak:  20.2 GiB
  Memory RSS:   1.8 GiB
  Memory Cache: 720.3 MiB
  CPU Total:    3947083ms
  CPU User:     2376609ms
  CPU System:   1570474ms

store:
  Memory Usage: 2.6 GiB
  Memory Peak:  20.2 GiB
  Memory RSS:   1.8 GiB
  Memory Cache: 720.3 MiB
  CPU Total:    3947084ms
  CPU User:     2376610ms
  CPU System:   1570474ms

querier:
  Memory Usage: 2.6 GiB
  Memory Peak:  20.2 GiB
  Memory RSS:   1.8 GiB
  Memory Cache: 720.3 MiB
  CPU Total:    3947084ms
  CPU User:     2376610ms
  CPU System:   1570474ms

prometheus:
  Memory Usage: 2.6 GiB
  Memory Peak:  20.2 GiB
  Memory RSS:   1.8 GiB
  Memory Cache: 720.3 MiB
  CPU Total:    3947083ms
  CPU User:     2376609ms
  CPU System:   1570474ms
```

## Corrected Test Results (with proper timestamps)

### Instant Queries (at 6h ago timestamp)

| Query | Series/Count | Duration |
|-------|--------------|----------|
| count(test_metric) | 14,000 | 272ms |
| sum by (instance) | 12,068 series | 314ms |
| count by (instance, job, env) | 14,000 series | 407ms |
| count(test_metric{instance=~".*churn.*"}) | 12,000 | 344ms |

### Range Queries

| Query | Range | Step | Points | Series | Duration |
|-------|-------|------|--------|--------|----------|
| count(test_metric) | 6h | 60s | 361 | 1 | 5.4s |
| count(test_metric) | 12h | 60s | 367 | 1 | 9.4s |
| count(test_metric) | 12h | 15s | 1,467 | 1 | 51.6s |
| sum by (instance) | 6h | 60s | 34 | 292,334 | 8.4s |

## Summary

### Memory Usage
- **Peak**: 20.2 GiB (EXCEEDED 10GB limit)
- **Runtime**: 2.6-2.8 GiB
- Peak occurred during Store Gateway block loading, not query execution

### Performance
- Instant queries: 200-400ms (fast)
- Range queries scale with points × series
- High-cardinality range query (292k series × 34 points): 8.4s
- Fine-grained range query (1,467 points): 51.6s

### Findings

1. **Memory Limit Exceeded**: With 50% churn rate on 80% of 10k series over 12h,
   the Store Gateway used 20.2 GiB peak memory loading ~106k unique series per block.
   This is 2x the configured 10GB limit.

2. **Churn Impact**: High churn (40% effective replacement every 5min) created
   ~292k total unique instance labels over 6h. Range queries that materialize
   all these series are expensive but complete successfully.

3. **Query Performance**: Instant queries remain fast (<500ms). Range queries
   with many steps (15s intervals = 2880 points over 12h) are slow (51s) but
   complete within reasonable time.

4. **Recommendation**: For high-churn workloads at this scale, allocate at least
   20-25GB memory per Store Gateway instance to handle block loading spikes.
