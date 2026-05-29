## MODIFIED Requirements

### Requirement: Query measurement via clickhouse-benchmark

For each query in a scenario, the harness SHALL delegate execution measurement to the `clickhouse-benchmark` CLI shipped with ClickHouse, rather than timing queries in-process. The harness substitutes parameter placeholders (`{name:Type}`) in the query SQL to literal values from the default parameter set, appends a unique `SETTINGS log_comment` (incorporating a per-invocation sweep id, scenario, and query name) so the run's `query_log` rows can be correlated back to this query, invokes `clickhouse-benchmark --iterations N --delay 0 --query <sql> --host <h> --port <p>` (plus `--concurrency C` when configured), captures the percentile block + per-host summary line from stderr, and records the parsed values in the result file. After the benchmark for a query completes, the harness SHALL force `SYSTEM FLUSH LOGS` and read `system.query_log` filtered by the query's `log_comment` (and `type = 'QueryFinish'`) to compute per-query CPU and memory. This ensures the recorded numbers reflect server-side query time (not Go driver deserialization overhead), supports realistic concurrent load via `--concurrency`, captures true CPU cost (not just wall-clock), and inherits `clickhouse-benchmark`'s production-hardened measurement for free.

#### Scenario: clickhouse-benchmark is invoked per query
- **WHEN** the harness runs a query for a scenario
- **THEN** it shells out to `clickhouse-benchmark` exactly once per query (no in-process timing loop)

#### Scenario: Server-side percentiles recorded
- **WHEN** `clickhouse-benchmark` emits its percentile block for a query
- **THEN** the harness records `p0`, `p10`, `p20`, `p30`, `p40`, `p50`, `p60`, `p70`, `p80`, `p90`, `p95`, `p99`, `p99_9`, `p99_99` (all in seconds) plus `qps`, `rps`, `mib_per_sec`, `result_rps`, `result_mib_per_sec` for that query in the result file

#### Scenario: CPU and memory recorded per query
- **WHEN** the harness measures a query against a ClickHouse with `query_log` enabled
- **THEN** that query's result entry additionally contains `cpu_p50_us`, `cpu_p95_us` (from `ProfileEvents['OSCPUVirtualTimeMicroseconds']`) and `mem_avg_bytes` (from `memory_usage`), computed across the measured iterations via `log_comment` correlation

#### Scenario: Concurrency is configurable and recorded
- **WHEN** the harness is invoked with `--concurrency 16`
- **THEN** every query passes `--concurrency 16` to `clickhouse-benchmark` and the per-query result records `concurrency: 16`

#### Scenario: clickhouse-benchmark binary missing
- **WHEN** the harness cannot find `clickhouse-benchmark` on PATH
- **THEN** it exits with a non-zero status and a clear error message naming the missing binary and pointing to the dev shell that ships it

### Requirement: Result persistence

Results SHALL be written to `bench/results/<scenario>/<timestamp>.json` (or equivalent structured file). Each result file SHALL include the scenario name, the cluster fingerprint (ClickHouse version, cluster topology via `system.clusters`, and an `is_single_node` flag determined by probing the actual shard and replica counts — not by string-matching the cluster name), the harness git commit (with `-dirty` suffix when the working tree has uncommitted changes), the concurrency level used, the per-invocation sweep id used for CPU correlation, and the timestamps of run start/end, so historical comparisons remain reproducible. Per-query CPU (`cpu_p50_us`, `cpu_p95_us`) and memory (`mem_avg_bytes`) SHALL be persisted alongside the latency/throughput fields, or null when `query_log` was unavailable.

#### Scenario: Result file is self-describing
- **WHEN** a result file is read in isolation
- **THEN** a reviewer can determine which scenario produced it, against which ClickHouse version, from which commit of ch-playground (clean or dirty), at what concurrency level, and when, without needing any other file

#### Scenario: CPU recorded or explicitly null
- **WHEN** a result file is read for a run where `query_log` was disabled on the target
- **THEN** every query's `cpu_p50_us` / `cpu_p95_us` / `mem_avg_bytes` are `null` (not absent, not zero), making the absence of CPU data unambiguous
