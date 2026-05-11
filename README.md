# Thanos Test Harness

> **⚠️ Experimental** — This is an LLM-led research project investigating Thanos memory consumption patterns during high-cardinality PromQL queries. The code was primarily generated through collaborative development with Claude (Anthropic). Use at your own risk; APIs and behavior may change without notice.

Local test environment for high-cardinality query research. Runs a complete Thanos pipeline with per-component resource accounting.

## Background

This harness supports research into memory efficiency improvements for Thanos query execution, particularly for queries that touch large numbers of time series. It provides instrumented test infrastructure for measuring per-component resource usage during query evaluation.

## Quick Start

```bash
# Build the harness
go build -o thanos-harness ./cmd/thanos-harness

# Build Thanos/Prometheus from local source (auto-detects workspace layout)
./thanos-harness build

# Or build with explicit paths
./thanos-harness build --thanos=../thanos --prometheus=../thanos-prometheus

# Build with debug symbols for delve
./thanos-harness build --debug

# Start pipeline (auto-detects binaries from workspace/bin)
./thanos-harness start

# Or with explicit binary paths
./thanos-harness start --prometheus=/path/to/prometheus --thanos=/path/to/thanos

# In another terminal, seed test data
./thanos-harness seed --series=10000 --duration=1h

# Run a query with resource tracking
./thanos-harness query 'count(test_metric)'
```

## Commands

| Command | Description |
|---------|-------------|
| `build` | Build Thanos/Prometheus from local source directories |
| `start` | Start Prometheus + Sidecar + Querier pipeline |
| `query` | Execute PromQL and show resource metrics |
| `seed` | Generate configurable test data |
| `stats` | Show current resource stats for all components |
| `info` | Show harness configuration |

## Features

- **Process isolation**: Each component runs in a separate systemd scope (when available)
- **Cgroup accounting**: Per-component memory and CPU tracking
- **Data generation**: Configurable cardinality, info metrics for joins
- **Query metrics**: Resource usage delta per query

## Resource Metrics

When systemd is available, each component runs in a cgroup scope providing:
- Memory usage (current, peak, RSS, cache)
- CPU time (user, system)
- OOM kill detection

Without systemd, falls back to process-level metrics via `/proc`.

## Architecture

```
┌─────────────────┐     ┌─────────────────┐     ┌─────────────────┐
│   Prometheus    │────▶│  Thanos Sidecar │────▶│  Thanos Querier │
│   (port 19090)  │     │  (gRPC: 19092)  │     │  (port 19093)   │
└─────────────────┘     └─────────────────┘     └─────────────────┘
        │                       │                       │
        └───────────────────────┴───────────────────────┘
                          cgroup stats
```

## Development

```bash
# Run tests
go test ./...

# Build for debugging
go build -gcflags="all=-N -l" -o thanos-harness ./cmd/thanos-harness
```

## Related

- [Spec 002: Local Test Harness](../specs/002-local-test-harness/spec.md)
- [Spec 001: High-Cardinality Queries](../specs/001-high-cardinality-queries/spec.md)
