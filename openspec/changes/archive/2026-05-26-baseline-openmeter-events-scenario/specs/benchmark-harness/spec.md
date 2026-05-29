## ADDED Requirements

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

For each query in a scenario, the harness SHALL delegate execution measurement to the `clickhouse-benchmark` CLI shipped with ClickHouse, rather than timing queries in-process. The harness substitutes parameter placeholders (`{name:Type}`) in the query SQL to literal values from the default parameter set, invokes `clickhouse-benchmark --iterations N --delay 0 --query <sql> --host <h> --port <p>` (plus `--concurrency C` when configured), captures the percentile block + per-host summary line from stderr, and records the parsed values in the result file. This ensures the recorded numbers reflect server-side query time (not Go driver deserialization overhead), supports realistic concurrent load via `--concurrency`, and inherits `clickhouse-benchmark`'s production-hardened measurement (T-test, multi-host comparison) for free.

#### Scenario: clickhouse-benchmark is invoked per query
- **WHEN** the harness runs a query for a scenario
- **THEN** it shells out to `clickhouse-benchmark` exactly once per query (no in-process timing loop)

#### Scenario: Server-side percentiles recorded
- **WHEN** `clickhouse-benchmark` emits its percentile block for a query
- **THEN** the harness records `p0`, `p10`, `p20`, `p30`, `p40`, `p50`, `p60`, `p70`, `p80`, `p90`, `p95`, `p99`, `p99_9`, `p99_99` (all in seconds) plus `qps`, `rps`, `mib_per_sec`, `result_rps`, `result_mib_per_sec` for that query in the result file

#### Scenario: Concurrency is configurable and recorded
- **WHEN** the harness is invoked with `--concurrency 16`
- **THEN** every query passes `--concurrency 16` to `clickhouse-benchmark` and the per-query result records `concurrency: 16`

#### Scenario: clickhouse-benchmark binary missing
- **WHEN** the harness cannot find `clickhouse-benchmark` on PATH
- **THEN** it exits with a non-zero status and a clear error message naming the missing binary and pointing to the dev shell that ships it

### Requirement: Result persistence

Results SHALL be written to `bench/results/<scenario>/<timestamp>.json` (or equivalent structured file). Each result file SHALL include the scenario name, the cluster fingerprint (ClickHouse version, cluster topology if available via `system.clusters`), the harness git commit (with `-dirty` suffix when the working tree has uncommitted changes), the concurrency level used, and the timestamps of run start/end, so historical comparisons remain reproducible.

#### Scenario: Result file is self-describing
- **WHEN** a result file is read in isolation
- **THEN** a reviewer can determine which scenario produced it, against which ClickHouse version, from which commit of ch-playground (clean or dirty), at what concurrency level, and when, without needing any other file
