## ADDED Requirements

### Requirement: Per-run markdown report

The harness SHALL render a human-readable markdown report for each scenario run and write it next to that run's JSON result file at `bench/results/<scenario>/<timestamp>.md`, sharing the JSON file's basename. The report SHALL be derived solely from the same `Run` record used to produce the JSON, so the two renderings never diverge. The report SHALL contain: a header identifying the scenario, the harness git commit (including any `-dirty` suffix), the ClickHouse version and single-node/topology fingerprint, the run start/finish timestamps, the sweep id, and the concurrency level; an ingest summary (rows, duration, events/sec, batch settings) or an explicit "skipped" marker when seeding was skipped; and a per-query table with at least the query name, latency percentiles (p50/p95/p99), QPS, CPU p50 with its source label, and average memory.

#### Scenario: Report written alongside JSON
- **WHEN** a scenario run completes and its JSON result file is written
- **THEN** a sibling `<timestamp>.md` report exists in the same `bench/results/<scenario>/` directory, with the same basename as the JSON file

#### Scenario: CPU and memory absence is explicit
- **WHEN** a run's queries have no CPU/memory data (query_log was unavailable, so the fields are null)
- **THEN** the report renders those cells as an explicit `n/a` marker rather than `0` or a blank, so a reader cannot mistake missing data for a measured zero

#### Scenario: Errored query is shown, not hidden
- **WHEN** a query in the run recorded an error instead of metrics
- **THEN** the report row for that query shows the error message in place of the latency/CPU/memory metrics, so a failed query is visible in the report

### Requirement: Per-query SQL in a collapsed section

For each measured query, the report SHALL include the actual SQL text of the query that ran, inside a collapsed `<details>` / `<summary>` block tagged with the query name, so a reader can audit what was measured without the SQL bodies dominating the rendered report. When SQL text is unavailable (e.g. the renderer was called with no query bodies), the report SHALL omit the collapsed section rather than render an empty block, leaving the metrics table intact.

#### Scenario: SQL renders inside a collapsed details block
- **WHEN** the renderer is given the SQL bodies for the queries that ran
- **THEN** each query appears in the report inside a `<details>` element with its name in the `<summary>` and its SQL inside a fenced code block, collapsed by default

#### Scenario: Missing SQL omits the section
- **WHEN** the renderer is given no SQL bodies (or a query has no matching body)
- **THEN** no empty `<details>` block is emitted for that query and the metrics table still renders
