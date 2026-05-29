## Why

ch-playground exists to compare ClickHouse table designs for OpenMeter's event-ingestion + meter-query workload, but has no baseline to compare against. Every alternative design needs a reference: the schema OpenMeter actually ships today, exercised by the same generated SQL OpenMeter actually emits. Without that, "this variant is 2x faster" is meaningless — faster than what? This change establishes the baseline scenario by faithfully reproducing OpenMeter's current `om_events` table and meter-query patterns, and wraps them in the benchmark harness so every future variant has a fixed point to beat.

## What Changes

- Add `scenarios/baseline-openmeter/` with `init.sql` reproducing the `om_events` MergeTree schema verbatim from `openmeter/streaming/clickhouse/event_query.go:20-49` (columns, ORDER BY, PARTITION BY, minmax skip index on `stored_at`).
- Add `scenarios/baseline-openmeter/seed.sql` (or seed program) that generates synthetic events matching the OpenMeter event shape — namespace, type, subject, time, JSON-encoded `data` payload with a numeric `value` and 1-2 group-by fields.
- Add `scenarios/baseline-openmeter/queries/` with the canonical meter query shapes OpenMeter emits: per-aggregation (SUM / COUNT / AVG / MIN / MAX / UNIQUE_COUNT / LATEST), per-window-size (no-window, minute, hour, day), with and without group-by, with and without `PREWHERE` optimization.
- Add the first benchmark driver under `bench/` that runs these queries against the cluster and writes results to `bench/results/baseline-openmeter/`. Measures ingest throughput (events/sec at a chosen batch size and `async_insert` setting), and delegates per-query measurement to the official `clickhouse-benchmark` CLI for server-side timing, real concurrency, and richer percentiles (p50/p95/p99/p99.9/p99.99 plus QPS/RPS/MiB/s).
- Document the scenario contract in `scenarios/README.md` so future variants follow the same shape.

## Capabilities

### New Capabilities
- `scenario-format`: defines the on-disk contract a scenario directory must follow (init.sql, seed source, queries/, naming) so the benchmark driver can discover and run any scenario uniformly.
- `baseline-openmeter-scenario`: the specific baseline scenario reproducing OpenMeter's current event table and meter queries.
- `benchmark-harness`: defines how the Go driver discovers scenarios, runs ingest + query workloads against the cluster, and persists results.

### Modified Capabilities
<!-- None — no existing specs in this repo. -->

## Impact

- **New code**: `scenarios/baseline-openmeter/*`, `bench/*.go` (driver + result writer), `scenarios/README.md` contract update.
- **Cluster dependency**: requires a running ClickHouse cluster reachable from the bench host. This proposal does NOT include cluster bring-up (that's the upstream kind + clickhouse-operator change); for now the bench targets a connection string supplied via env (`CLICKHOUSE_DSN`), so the scenario can be exercised against any cluster (local single-node, docker-compose, eventual kind cluster) without coupling.
- **No production systems** touched — this is a benchmarking harness.
- **Source-of-truth coupling**: the scenario must stay in sync with OpenMeter's actual schema if it drifts. Drift detection is out of scope here but flagged in `tasks.md`.
