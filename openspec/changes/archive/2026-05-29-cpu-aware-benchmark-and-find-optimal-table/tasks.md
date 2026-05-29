## 1. Enable query_log in the dev ClickHouse

- [x] 1.1 Replace `<query_log remove="1"/>` in `.devenv/clickhouse-server/config.xml` with an enabled `query_log` config (system.query_log, 1s flush interval). Restart ClickHouse. (Done as the first step of this goal.)
- [x] 1.2 Confirm `query_log` captures CPU: run a real query with `SETTINGS log_comment='probe'`, `SYSTEM FLUSH LOGS`, then read `ProfileEvents['OSCPUVirtualTimeMicroseconds']` and `memory_usage` for that `log_comment`. Confirmed: a 50M-row scan recorded ~7.1M µs CPU.

## 2. CPU capture in the harness

- [x] 2.1 Add `CPUp50Us`, `CPUp95Us *float64` and `MemAvgBytes *float64` (nullable) fields to `BenchResult` in `bench/runner/benchmark.go` with `cpu_p50_us` / `cpu_p95_us` / `mem_avg_bytes` JSON tags.
- [x] 2.2 Generate a per-invocation sweep id once at harness start (timestamp-based is fine) and thread it through to each `Bench` call.
- [x] 2.3 In `Bench`, append `SETTINGS log_comment = 'bench:<sweep>:<scenario>:<query>'` to the rendered SQL before invoking `clickhouse-benchmark`. If the query already has a `SETTINGS` clause, merge (append with comma) rather than producing a second `SETTINGS`.
- [x] 2.4 Add a `BenchOpts` field carrying the scenario name + sweep id so the `log_comment` can be constructed; pass a `driver.Conn` (or a small CPU-probe callback) into `Bench` so it can read `system.query_log` after the benchmark completes.
- [x] 2.5 After `clickhouse-benchmark` returns for a query, run `SYSTEM FLUSH LOGS` then the correlation query (`quantile(0.5)/(0.95)(ProfileEvents['OSCPUVirtualTimeMicroseconds'])`, `avg(memory_usage)` WHERE `log_comment = ...` AND `type='QueryFinish'`). Populate the CPU/mem fields. Retry the read once after 200ms if it returns zero rows.
- [x] 2.6 Probe for `query_log` availability once at harness start (`EXISTS system.query_log` or a guarded `SELECT`). If unavailable, set CPU/mem to null for every query and log one warning naming `query_log`. Never abort the run.
- [x] 2.7 Persist the sweep id in the `Run` record (`bench/runner/results.go`) so result files are self-describing about which `query_log` rows backed their CPU figures.
- [x] 2.8 Print CPU in the per-query console line (e.g. `cpu_p50=12.3ms` alongside the existing `p50=`/`QPS=`).

## 3. materialized-columns scenario (later disqualified + removed)

This scenario was built and swept as the strongest query-CPU lever, then **disqualified**: OpenMeter meters define arbitrary per-meter JSON paths, so a fixed materialized column set (`value`/`group1`/`group2`) is non-generic. It has since been removed from the repo. The tasks below are retained as the historical record; the finding lives in `bench/results/ANALYSIS-optimal-table.md`.

- [x] 3.1 Create `scenarios/materialized-columns/init.sql`: baseline DDL (retain `data String`) plus `value Float64 MATERIALIZED ifNotFinite(toFloat64OrNull(JSON_VALUE(data, '$.value')), NULL)`, `group1 String MATERIALIZED JSON_VALUE(data, '$.group1')`, `group2 String MATERIALIZED JSON_VALUE(data, '$.group2')`. `CREATE TABLE IF NOT EXISTS`. Header comment stating the design intent (move JSON parse from query time to insert time; isolate materialization as the only variable).
- [x] 3.2 Create `scenarios/materialized-columns/queries/` with one file per baseline query (same filenames, placeholders, windowing/grouping), rewritten to read `value` / `group1` / `group2` directly — no `JSON_VALUE`, no `toFloat64OrNull` at query time.
- [x] 3.3 Verify the seeder populates the materialized columns: seed a small batch, confirm `value` equals `toFloat64OrNull(JSON_VALUE(data, '$.value'))` row-for-row, and that all 14 query filenames match baseline + use the harness placeholders.
- [x] 3.4 Update `scenarios/README.md` to list `materialized-columns` and the single variable it isolates.

## 4. Scaled sweep + ranking

- [x] 4.1 Rebuild the harness. Confirm CPU fields populate end-to-end against the running single-node ClickHouse with a small smoke run (`--rows 100000 --iterations 3`) for one scenario.
- [x] 4.2 Run the discriminating sweep at `--rows 10000000 --iterations 10 --seed 42` across all single-node candidates that existed at sweep time (baseline-openmeter, data-as-json, time-desc, with-projections, materialized-columns), dropping `om_events` between each. (Only baseline-openmeter and data-as-json survive in the repo today; the others were exploratory and have been removed — see the analysis note.)
- [x] 4.3 Aggregate the result files: per scenario, per query, tabulate `cpu_p50_us`, `p50_sec`, `mem_avg_bytes`. Rank to find the table+query combo with the lowest query CPU + latency.
- [x] 4.4 Write a committed analysis note (`bench/results/ANALYSIS-optimal-table.md` or similar) recording: host + CH version, the ranking, the winning table+query, and the ingest trade-off for any query-CPU winner that pays at insert time. State explicitly that the objective was query-time CPU + latency.
- [x] 4.5 Run `openspec validate cpu-aware-benchmark-and-find-optimal-table` and confirm "is valid".
