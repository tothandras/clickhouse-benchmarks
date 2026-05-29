## Why

The benchmark harness measures latency and throughput (p50…p99.99, QPS, RPS, MiB/s) but not **CPU**. The stated goal is to find the table+query combination with the *lowest CPU usage and response times*, and we can't optimize for a metric we don't capture. CPU is also the more honest cost signal for these meter queries: at small scale latency is dominated by fixed overhead (~3-5ms floor) and looks identical across variants, while CPU work scales with the actual per-row cost (JSON parsing, value extraction) and discriminates designs even before latency does.

A further gap blocks a credible "find the optimal" answer: past sweeps ran at 100k rows, where design differences are invisible (everything lands at p50 3-7ms). A scaled sweep (10M rows) is needed for the variables to diverge.

(Historical note: this change originally also added a `materialized-columns` scenario — typed `MATERIALIZED` columns precomputing hot JSON fields — as the strongest query-CPU lever. That scenario was later **disqualified** because OpenMeter meters define arbitrary per-meter JSON paths, so a fixed materialized column set is non-generic; it has since been removed from the repo. The CPU-profiling work below is independent of it and remains in force. See `bench/results/ANALYSIS-optimal-table.md`.)

## What Changes

- **Capture per-query CPU + memory.** Every measured query is tagged with `SETTINGS log_comment = 'bench:<sweep-id>:<scenario>:<query>'`. After the `clickhouse-benchmark` run for a query, the harness queries `system.query_log` for that `log_comment`, computing `OSCPUVirtualTimeMicroseconds` p50/p95 (total CPU across threads — the right "lowest CPU" metric) and average `memory_usage`. These land in each query's result entry as `cpu_p50_us`, `cpu_p95_us`, `mem_avg_bytes`.
- **Enable `query_log` in the dev ClickHouse** (`.devenv/clickhouse-server/config.xml` currently has `query_log remove="1"`), since CPU capture depends on it. Already done as the first step; this change records it.
- **Run a 10M-row sweep** across all candidate scenarios with matched seed/iterations, dropping `om_events` between each, and record the ranking — query-time CPU + latency — in a committed analysis note so the "optimal" finding is reproducible and auditable.
- **Optimization metric is explicit**: query-time CPU + query latency, ignoring ingest cost. "Optimal table + query" reads as the query-time cost of the combination; ingest cost is recorded (it already is) but not part of the ranking objective. A scenario that wins query CPU at the price of ingest CPU still wins under this objective — noted so the trade-off isn't a later surprise.

## Capabilities

### New Capabilities

- `cpu-profiling`: harness behavior for capturing per-query CPU and memory from `system.query_log`, correlated via `log_comment`, and recording it in result files.

### Modified Capabilities

- `benchmark-harness`: the query-measurement and result-persistence requirements gain CPU/memory fields and the `log_comment` correlation mechanism. The harness must require `query_log` to be enabled (or degrade gracefully, recording CPU as null with a warning, when it isn't).

## Impact

- **Code**: `bench/runner/benchmark.go` (inject `log_comment`, parse), a new post-run `system.query_log` probe in `bench/runner/`, result struct fields in `bench/runner/results.go`. `bench/cmd/bench/main.go` to thread a per-run sweep id.
- **Config**: `.devenv/clickhouse-server/config.xml` `query_log` enabled.
- **Results**: result files gain `cpu_p50_us`, `cpu_p95_us`, `mem_avg_bytes` per query. A committed analysis artifact records the 10M-row ranking.
- **Dependency on query_log**: CPU capture requires `query_log` enabled on the *target* ClickHouse. For a user's own server where it may be off, the harness degrades gracefully (null CPU + warning) rather than failing.
- **Risk**: low. CPU capture is additive (latency numbers unchanged). The only cross-cutting change is the result struct, which is backward-additive (new optional fields).
