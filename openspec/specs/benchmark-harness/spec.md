# benchmark-harness Specification

## Purpose

Define how the benchmark harness discovers scenarios, connects to a ClickHouse cluster, measures ingest, measures queries, and persists results. The harness is orchestrator-agnostic (any reachable ClickHouse works), delegates query-time measurement to the production-hardened `clickhouse-benchmark` CLI, and writes self-describing result files so historical comparisons remain reproducible across cluster topologies and harness commits.
## Requirements
### Requirement: Scenario discovery

The benchmark harness SHALL discover scenarios by scanning the `scenarios/` directory at runtime. The user MAY restrict the run to a subset via a `--scenario` flag (repeatable) or a comma-separated env var. Scenarios that do not satisfy the `scenario-format` contract SHALL be skipped with a logged warning, never aborting the run.

#### Scenario: Discover all scenarios
- **WHEN** the harness is invoked with no scenario filter
- **THEN** it runs every directory under `scenarios/` that satisfies the `scenario-format` contract

#### Scenario: Filter to one scenario
- **WHEN** the harness is invoked with `--scenario baseline-openmeter`
- **THEN** it runs only that scenario and skips others without error

### Requirement: Cluster connection via DSN

The harness SHALL connect to ClickHouse via a DSN supplied through the `CLICKHOUSE_DSN` environment variable (or an equivalent `--dsn` flag). It SHALL NOT assume the cluster is deployed by any specific orchestrator (kind, docker-compose, ClickHouse Cloud), so the same harness binary can target any reachable cluster. The harness SHALL also derive a host:port pair from the DSN to invoke the `clickhouse-benchmark` binary, since the binary takes `--host`/`--port` flags rather than a DSN.

#### Scenario: DSN required
- **WHEN** the harness is invoked without `CLICKHOUSE_DSN` set and without `--dsn`
- **THEN** it exits with a non-zero status and a clear error message naming the missing env var

### Requirement: Ingest measurement

For each scenario, before running queries, the harness SHALL run the scenario's seed and record total events inserted, total wall-clock seconds, and resulting throughput in events/sec. The user MAY configure the insert batch size and the ClickHouse `async_insert` setting via flags so the same scenario can be benchmarked under different insert regimes.

#### Scenario: Ingest result recorded
- **WHEN** seeding completes for a scenario
- **THEN** the result file for that scenario contains an `ingest` entry with `rows`, `duration_seconds`, `events_per_second`, and the `async_insert`/batch settings used

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

Results SHALL be written to `bench/results/<scenario>/<timestamp>.json` (or equivalent structured file). Each result file SHALL include the scenario name, the cluster fingerprint (ClickHouse version, cluster topology via `system.clusters`, and an `is_single_node` flag determined by probing the actual shard and replica counts — not by string-matching the cluster name), the harness git commit (with `-dirty` suffix when the working tree has uncommitted changes), the concurrency level used, the per-invocation sweep id used for CPU correlation, and the timestamps of run start/end, so historical comparisons remain reproducible. Per-query CPU (`cpu_p50_us`, `cpu_p95_us`) and memory (`mem_avg_bytes`) SHALL be persisted alongside the latency/throughput fields, or null when `query_log` was unavailable. In addition to the JSON file, each run SHALL also write a sibling human-readable markdown report (see the `benchmark-reports` capability). The `bench/results/` directory and its contents SHALL be tracked in version control (not git-ignored), so committed runs leave a reviewable record.

#### Scenario: Result file is self-describing
- **WHEN** a result file is read in isolation
- **THEN** a reviewer can determine which scenario produced it, against which ClickHouse version, from which commit of ch-playground (clean or dirty), at what concurrency level, and when, without needing any other file

#### Scenario: CPU recorded or explicitly null
- **WHEN** a result file is read for a run where `query_log` was disabled on the target
- **THEN** every query's `cpu_p50_us` / `cpu_p95_us` / `mem_avg_bytes` are `null` (not absent, not zero), making the absence of CPU data unambiguous

#### Scenario: Run produces both JSON and markdown
- **WHEN** a scenario run completes
- **THEN** both a `<timestamp>.json` and a sibling `<timestamp>.md` are written under `bench/results/<scenario>/`, and neither is excluded by `.gitignore`

### Requirement: Run-to-run variance

The harness SHALL support running each scenario more than once in a single invocation (`--repeat N`) and SHALL aggregate across the repeats — reporting a median-of-medians and a spread measure — so a reported delta (e.g. a −7% CPU difference) can be distinguished from run-to-run noise. Within-run `clickhouse-benchmark` percentiles capture iteration spread but not the run-to-run variance the README repeatedly hedges about ("ingest cost varies run-to-run", "verify on target hardware"); this requirement closes that gap.

#### Scenario: Repeats are aggregated with a spread
- **WHEN** the harness runs a scenario with `--repeat N` for N > 1
- **THEN** the recorded figures include a median-of-medians across the N repeats and a spread measure, so a delta smaller than the spread is recognizable as noise

### Requirement: Lookup-by-id index-pruning capture

For the lookup-by-id access pattern, the harness SHALL capture the `EXPLAIN indexes = 1` granule-pruning signal against a literal id — granules/parts scanned with the skip index disabled versus enabled — and record it in the result file. The `lookup_by_id` query's own wall-clock is explicitly not a clean latency benchmark (its inner id-resolver subquery dominates), so the captured pruning ratio is the bloom filter's real, diffable evidence rather than prose in a SQL comment.

#### Scenario: Granule pruning recorded for lookup-by-id
- **WHEN** the harness measures a scenario carrying a `bloom_filter` skip index on `id`
- **THEN** the result file records the granules/parts scanned with `use_skip_indexes = 0` and with the index active for a literal-id lookup, so the pruning ratio is captured

### Requirement: Clean-tree provenance guard

The harness already records the harness git commit with a `-dirty` suffix when the working tree has uncommitted changes (this behavior is unchanged). It SHALL additionally provide a guard (`--require-clean`) that refuses to run — or, at minimum, emits a loud warning before writing results — when the tree is dirty, so headline result files are not published from an unreproducible state.

#### Scenario: Dirty tree is refused under the guard
- **WHEN** the harness is run with `--require-clean` and the working tree has uncommitted changes
- **THEN** the run is refused (or gated behind an explicit `--allow-dirty`) so a `-dirty` result is not silently committed

#### Scenario: Commit provenance still recorded
- **WHEN** the harness runs without the guard on a dirty tree
- **THEN** it proceeds and records the `-dirty`-suffixed commit exactly as before, preserving existing behavior

### Requirement: Shared seed window for cross-scenario parity

The harness SHALL accept a `--time-end` flag (RFC3339) that pins the seeder's
`TimeEnd` — the newest event time — so that, when set, every scenario seeded in
the run uses the identical event time window. Without it, each scenario defaults
to its own `time.Now()` (truncated to the minute), which makes two scenarios'
seeded event streams diverge. Pinning `--time-end` (together with the shared RNG
`--seed`) is the prerequisite for cross-scenario value parity: only when both
scenarios seed a byte-identical event stream are their per-query result digests
comparable. The resolved window SHALL be recorded in the run so a reader can see
which window the run (and its digests) cover, and a malformed `--time-end` SHALL
fail fast with a clear error before any seeding begins.

#### Scenario: Pinned time-end makes scenarios seed identical windows
- **WHEN** the harness runs two scenarios in one invocation with the same `--time-end` and `--seed`
- **THEN** both scenarios seed events over the identical time window, so their value-parity digests are computed over the same events and are comparable

#### Scenario: Unpinned time-end defaults per scenario
- **WHEN** the harness runs without `--time-end`
- **THEN** the seeder uses `time.Now()` truncated to the minute as before, and the run records the resolved window so a later comparison can detect that two such runs cover different windows

#### Scenario: Malformed time-end fails fast
- **WHEN** the harness is given a `--time-end` that is not valid RFC3339
- **THEN** it exits non-zero with an error naming the flag and the expected format before seeding any scenario

