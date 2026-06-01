# cpu-profiling Specification

## Purpose

Define how the harness captures per-query CPU and memory from ClickHouse's
`system.query_log` alongside the latency/throughput it already records, so
table-design experiments can be ranked by true server-side CPU cost rather than
wall-clock alone. CPU is correlated to a specific measured query via a unique
`log_comment` carrying the run's sweep id, prefers the OS thread-CPU counter and
falls back to summed thread wall-time when that counter is unavailable, and
degrades gracefully (fields null, one warning) when `query_log` is absent.

## Requirements
### Requirement: Per-query CPU and memory capture

For each measured query, the harness SHALL record the query's CPU consumption and memory usage from ClickHouse's `system.query_log`, in addition to the latency and throughput metrics it already records. CPU SHALL be reported preferring `OSCPUVirtualTimeMicroseconds` (true OS thread CPU, total across all query threads); when that counter is unavailable on the target (some hosts — notably ClickHouse running inside Docker on macOS — report it as 0), the harness SHALL fall back to `RealTimeMicroseconds` (wall-clock summed across query threads), which equals CPU time for the CPU-bound aggregation queries this harness runs. The harness SHALL record which counter was used in a `cpu_source` field (`"os_cpu"` or `"real_time"`). CPU is reported at p50 and p95, and memory as the average `memory_usage` in bytes, across the measured iterations. These appear in each query's result entry as `cpu_p50_us`, `cpu_p95_us`, `mem_avg_bytes`, and `cpu_source`.

#### Scenario: CPU fields present in result file
- **WHEN** a query is measured against a ClickHouse with `query_log` enabled
- **THEN** that query's entry in the result file contains numeric `cpu_p50_us`, `cpu_p95_us`, and `mem_avg_bytes` values reflecting the measured iterations, plus a `cpu_source` naming the counter used

#### Scenario: Fallback to RealTime when OS CPU counter is unavailable
- **WHEN** the target reports `OSCPUVirtualTimeMicroseconds` as 0 for the measured query (OS thread CPU counters inaccessible)
- **THEN** the harness records `RealTimeMicroseconds`-based figures and sets `cpu_source` to `"real_time"`, so the substitution is explicit and cross-host comparisons are not silently mixed

### Requirement: Correlation via log_comment

The harness SHALL correlate `query_log` rows to a specific measured query by injecting a unique `SETTINGS log_comment` into the query SQL before handing it to `clickhouse-benchmark`, then querying `system.query_log` filtered by that exact `log_comment`. The `log_comment` SHALL incorporate a per-invocation sweep identifier so rows from prior or concurrent harness runs cannot be miscounted. The harness SHALL force a synchronous flush (`SYSTEM FLUSH LOGS`) before reading, and SHALL only aggregate rows with `type = 'QueryFinish'`.

#### Scenario: Sweep id isolates runs
- **WHEN** the harness runs the same scenario twice in succession
- **THEN** each run's CPU figures are computed only from `query_log` rows tagged with that run's sweep id, never mixing the two

#### Scenario: Only finished queries counted
- **WHEN** the harness aggregates CPU for a query
- **THEN** it includes only `query_log` rows with `type = 'QueryFinish'`, excluding `QueryStart` and exception rows

### Requirement: Graceful degradation without query_log

When the target ClickHouse does not have `query_log` available (table absent or logging disabled), the harness SHALL record the CPU and memory fields as null, log a single clear warning naming `query_log` as the reason, and continue — latency and throughput measurement MUST still complete. CPU capture is best-effort enrichment, never a hard dependency for a benchmark run.

#### Scenario: Missing query_log does not abort the run
- **WHEN** the harness runs against a ClickHouse without `query_log`
- **THEN** the run completes, latency/throughput are recorded as normal, the CPU/memory fields are null, and one warning is emitted explaining that `query_log` was unavailable

