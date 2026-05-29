## MODIFIED Requirements

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
