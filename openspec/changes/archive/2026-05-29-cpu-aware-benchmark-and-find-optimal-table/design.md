## Context

The harness delegates query timing to `clickhouse-benchmark`, which reports latency percentiles + throughput but no CPU. ClickHouse records per-query CPU in `system.query_log.ProfileEvents['OSCPUVirtualTimeMicroseconds']` (total CPU microseconds across all query threads) plus `memory_usage`. `clickhouse-benchmark` doesn't expose these, but it runs the SQL we hand it, so we can tag each query with a `log_comment` and correlate after the run. The dev ClickHouse had `query_log` disabled (`remove="1"`); that's now enabled.

A 10M-row sweep ranks the candidate scenarios on query CPU + latency. (This change originally also introduced a `materialized-columns` candidate — typed columns precomputing hot JSON fields — but that scenario was later disqualified as non-generic and removed; see `bench/results/ANALYSIS-optimal-table.md`. The CPU-profiling capability defined here is independent of it.)

## Goals / Non-Goals

**Goals:**

- Record per-query CPU (`OSCPUVirtualTimeMicroseconds` p50/p95) and average `memory_usage` in every result file, correlated to the exact query via `log_comment`.
- Run a 10M-row sweep across all candidates and produce a reproducible ranking that answers "which table+query has the lowest query-time CPU and latency."
- Degrade gracefully when `query_log` is unavailable on the target (null CPU + warning, latency still recorded).

**Non-Goals:**

- Optimizing or even ranking ingest cost. Recorded (already is), but not part of the objective. The metric is query-time CPU + latency.
- Per-thread CPU breakdown, flamegraphs, or `trace_log` profiling. `OSCPUVirtualTimeMicroseconds` from `query_log` is the single scalar we optimize.
- Changing how latency is measured. CPU capture is strictly additive; `clickhouse-benchmark` still owns timing.
- Auto-enabling `query_log` on arbitrary target clusters. We enable it in the dev server config; for other targets the user is responsible (we detect + warn).
- Tuning ClickHouse server settings (threads, marks cache, etc.) as optimization levers. The objective is table-design + query-shape, not server config.

## Decisions

**Decision: Correlate CPU via `log_comment`, not by timestamp window.**

Each measured query gets `SETTINGS log_comment = 'bench:<sweep-id>:<scenario>:<query>'` appended. `<sweep-id>` is a per-harness-invocation ULID/timestamp so concurrent or repeated runs don't collide. After `clickhouse-benchmark` finishes a query, the harness runs `SYSTEM FLUSH LOGS` then:

```sql
SELECT quantile(0.5)(ProfileEvents['OSCPUVirtualTimeMicroseconds']) AS cpu_p50_us,
       quantile(0.95)(ProfileEvents['OSCPUVirtualTimeMicroseconds']) AS cpu_p95_us,
       avg(memory_usage) AS mem_avg
FROM system.query_log
WHERE log_comment = 'bench:<sweep-id>:<scenario>:<query>' AND type = 'QueryFinish'
```

The `log_comment` carries through `clickhouse-benchmark`'s N iterations (it's part of the query text), so all iterations share the comment and the quantiles are computed across the real measured runs — same population `clickhouse-benchmark` timed.

*Alternative considered:* match `query_log` rows by `event_time` falling inside the benchmark's wall-clock window. Rejected — fragile under concurrency, picks up the harness's own bookkeeping queries, and needs clock alignment. `log_comment` is exact.

**Decision: `OSCPUVirtualTimeMicroseconds` is the CPU metric.**

It's total CPU time across all threads that served the query — exactly "how much CPU did this query burn." A query that parallelizes across 8 threads for 2ms wall-clock reports ~16ms CPU — which is the cost we want to minimize, not the wall-clock.

**Fallback (added after testing on the dev host):** ClickHouse running inside Docker on macOS cannot read the kernel's per-thread OS CPU counters — `OSCPUVirtualTimeMicroseconds`, `UserTimeMicroseconds`, and `SystemTimeMicroseconds` all report 0 even with `metrics_perf_events_enabled=1` and on multi-million-row scans. Only `RealTimeMicroseconds` (wall-clock summed across query threads) is populated there. So the probe reads both: if `max(OSCPUVirtualTimeMicroseconds) > 0` across the matched rows it uses the OS counter (`cpu_source: "os_cpu"`); otherwise it falls back to `RealTimeMicroseconds` (`cpu_source: "real_time"`). For the CPU-bound, in-memory aggregation queries this harness runs, summed thread wall-time equals CPU time within rounding (the two diverge only when threads block on IO/locks/network, which these queries don't). A 1M-row discriminator check confirmed the fallback metric still separates designs cleanly: a JSON-parse design (~137-189ms) vs a typed-column read design (~51-77ms), a consistent ~2.7× — so relative ranking, the thing the sweep needs, is valid. Absolute numbers are only comparable within one host (stated in the analysis note), which was already true for any CPU metric.

**Decision: Optimize query-time CPU + latency; ingest is recorded but not ranked.**

The plain reading of "optimal table + query" is the query-time cost of the pair. A scenario that shifts cost from query time to insert time (precomputing hot fields, building a covering projection) is allowed to win under a query-time objective. The analysis note surfaces the ingest trade-off explicitly (e.g. "−X% query CPU, +Y% ingest CPU") so the objective's blind spot is visible, but the ranking key is query CPU then query p50.

**Decision: 10M rows for the discriminating sweep.**

At 100k rows everything is at the latency floor and CPU differences are within noise. 10M rows (~100s seed at ~100k events/sec) makes per-row query cost dominate, so JSON-parse vs typed-column-read diverges. Above 100M the iteration loop gets painful for little extra signal. The sweep uses matched `--rows 10000000 --iterations 10 --seed 42` and drops `om_events` between scenarios.

**Decision: Graceful degradation when `query_log` is off.**

Before the sweep, the harness probes `SELECT 1 FROM system.query_log LIMIT 1` (or checks `system.tables`). If `query_log` is absent/disabled, CPU fields are written as `null` and a one-line warning is logged; latency still records. This keeps the harness usable against a user's own ClickHouse or a CHI where `query_log` may be off, rather than hard-failing.

## Risks / Trade-offs

- **[`SYSTEM FLUSH LOGS` between every query adds overhead]** → It's a cheap operation (flushes the in-memory log buffer) and runs once per query (14× per scenario), not per iteration. Negligible vs. the query runtimes. Mitigation: flush once after each query's benchmark completes, not per iteration.
- **[`log_comment` correlation could pick up stale rows from a prior run]** → The `<sweep-id>` is unique per harness invocation, so a `WHERE log_comment = 'bench:<sweep-id>:...'` filter can't match a previous run. Mitigation: generate the sweep-id once at harness start.
- **[`query_log` flush latency races the read]** → `query_log` flushes asynchronously; the post-query read might run before the rows land. Mitigation: explicit `SYSTEM FLUSH LOGS` before the read forces synchronous flush. If a count comes back short, retry once after a 200ms sleep.
- **[A candidate that precomputes at insert time changes ingest throughput]** → Expected — that's the trade. The result file's `ingest` block captures it; the analysis note reports it. Not a correctness risk.
- **[10M-row sweep is slow / heavy on a laptop]** → ~100s seed + ~14 queries × 10 iterations per scenario, across the candidate scenarios. Order of 15-25 min total. Acceptable for a one-shot "find the optimal" run; documented so it's not a surprise. The harness already drops the table between scenarios to bound disk.
- **[CPU numbers depend on the host]** → `OSCPUVirtualTimeMicroseconds` is absolute CPU time, so cross-machine comparison is meaningless — but the sweep runs all candidates on the *same* host back-to-back, so the *relative* ranking is valid. The analysis note states the host + CH version so the absolute numbers are interpretable.
- **[Scope is single-node table design]** → The sweep targets a single ClickHouse node; the "optimal" answer is the single-node table design and query shape. Multi-node fan-out behavior is out of scope — its CPU is measured on different machines and is not directly comparable to single-node numbers.
