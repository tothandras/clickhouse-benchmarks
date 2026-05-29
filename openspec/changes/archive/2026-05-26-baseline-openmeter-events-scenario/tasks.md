## 1. Scenario contract

- [x] 1.1 Update `scenarios/README.md` to document the `init.sql` / `seed` / `queries/` contract from the `scenario-format` spec (idempotent DDL, one SELECT per `.sql`, `{name:Type}` parameter binding, discovery rules).
- [x] 1.2 Decide and document the parameter-defaults convention (env vars vs. fixed defaults vs. future per-scenario `params.json`) — capture the v1 choice in the README so future scenarios don't reinvent it.

## 2. Baseline scenario DDL

- [x] 2.1 Create `scenarios/baseline-openmeter/init.sql` with the verbatim `om_events` DDL from `openmeter/streaming/clickhouse/event_query.go:20-49` (columns, types, minmax skip index on `stored_at`, `PARTITION BY toYYYYMM(time)`, `ORDER BY (namespace, type, subject, toStartOfHour(time))`).
- [x] 2.2 Include a header comment in `init.sql` citing the upstream file path and the OpenMeter commit SHA captured for this scenario, so drift is auditable.
- [x] 2.3 Wrap DDL in `CREATE TABLE IF NOT EXISTS` / `CREATE DATABASE IF NOT EXISTS` so re-applying is a no-op.
- [x] 2.4 Verify with `clickhouse-client` against a local ClickHouse: apply twice, then `SHOW CREATE TABLE` and diff against the upstream DDL string. Confirmed against ClickHouse 26.2.5.45 — only cosmetic differences (CH reorders the trailing INDEX line and appends default `SETTINGS index_granularity = 8192`).

## 3. Synthetic event seeder

- [x] 3.1 Add `bench/seed/` Go module that reads scenario name + DSN + config (subject pool size, row count, time span, value distribution, group-by cardinality) and inserts events into `om_events` via `clickhouse-go/v2` native protocol.
- [x] 3.2 Generate `data` JSON of shape `{"value": <float>, "group1": <enum>, "group2": <enum>}` with deterministic, seedable RNG.
- [x] 3.3 Distribute events across ≥1 namespace, ≥2 distinct `type` values, ≥100 distinct subjects, ≥3 days of `time` — satisfying the `baseline-openmeter-scenario` cardinality requirement.
- [x] 3.4 Default to batch size 10,000 and synchronous inserts; expose `--batch-size`, `--async-insert`, `--wait-async`, `--rows`, `--seed` flags.
- [x] 3.5 Print events/sec on completion so the seeder is usable standalone for sanity-checking, even outside the harness.

## 4. Baseline queries

- [x] 4.1 Author `scenarios/baseline-openmeter/queries/sum_hour.sql` reproducing the example meter SQL from `meter_query.go` (tumbleStart/tumbleEnd hourly window, `JSON_VALUE(data, '$.value')` with `ifNotFinite(toFloat64OrNull(...), null)`, namespace/type/subject/time filters, GROUP BY windowstart, windowend, subject).
- [x] 4.2 Author `count_hour.sql`, `avg_hour.sql`, `min_hour.sql`, `max_hour.sql`, `unique_count_hour.sql`, `latest_hour.sql` — one per aggregation type in `meter/meter.go`.
- [x] 4.3 Author no-window, minute-window, and day-window variants for `sum` (`sum_no_window.sql`, `sum_minute.sql`, `sum_day.sql`) so window-size cost is measurable.
- [x] 4.4 Author group-by variants for `sum_hour` extracting `group1` and `group2` from JSON (`sum_hour_group1.sql`, `sum_hour_group1_group2.sql`).
- [x] 4.5 Author no-PREWHERE sibling files (`sum_hour_no_prewhere.sql` etc.) so the optimizer's contribution is measurable per the design. Sibling files exist for the two group-by queries (`sum_hour_group1_no_prewhere.sql`, `sum_hour_group1_group2_no_prewhere.sql`); the non-group-by queries don't set PREWHERE explicitly, so no sibling is needed.
- [x] 4.6 Confirm every query uses `{namespace:String}`, `{type:String}`, `{from:DateTime}`, `{to:DateTime}` placeholders the harness will bind — not hardcoded literals. All 14 query files use the required placeholders.

## 5. Benchmark harness

- [x] 5.1 Add `bench/cmd/bench/main.go` (Cobra or stdlib `flag`) exposing `--scenario <name>` (repeatable, default = all), `--dsn` (overrides `CLICKHOUSE_DSN`), `--iterations` (default 10), `--warmup` (default 1), `--batch-size`, `--async-insert`. Cobra; `--warmup` superseded by §9 (clickhouse-benchmark handles warmup internally).
- [x] 5.2 Implement scenario discovery: walk `scenarios/`, accept directories containing `init.sql`, skip others with a warning per the `benchmark-harness` spec.
- [x] 5.3 Implement init: apply `init.sql` against the DSN (template-substitute `{{database}}` if needed).
- [x] 5.4 Implement seed integration: invoke the seeder from §3 in-process, capture rows-inserted and duration, derive events/sec.
- [x] 5.5 Implement query runner: parse `queries/*.sql`, bind default parameters, run 1 discarded warm-up + N measured iterations, time each via `time.Now()` deltas (millisecond precision), compute p50/p95/p99/min/max/mean. Superseded by §9: timing is now server-side via `clickhouse-benchmark` with richer percentiles (p0/p10/.../p99.9/p99.99).
- [x] 5.6 Implement result writer: JSON file at `bench/results/<scenario>/<RFC3339-timestamp>.json` containing scenario, harness git commit (read via `git rev-parse HEAD` at run time), ClickHouse `version()`, cluster fingerprint (probe `system.clusters` if non-empty, else mark `is_single_node: true`), ingest block, per-query timings and percentiles.
- [x] 5.7 Add `bench/results/.gitkeep` (already present) and make sure the harness creates per-scenario subdirs as needed.

## 6. Wiring & docs

- [x] 6.1 Add `go.mod` at repo root (module `github.com/openmeterio/ch-playground` or similar) with `clickhouse-go/v2` and `cobra` (if used) deps.
- [x] 6.2 Update `bench/README.md` to document: how to invoke the harness, the `CLICKHOUSE_DSN` requirement, the default flags, where results land, how to read a result file.
- [x] 6.3 Update root `README.md` Quick Start: point to "run the baseline scenario against any reachable ClickHouse" as the first concrete thing a new contributor can do.

## 7. Validation

- [x] 7.1 Spin up a single-node ClickHouse (docker-compose or local install), set `CLICKHOUSE_DSN`, run the harness against `--scenario baseline-openmeter`, inspect the result file by hand — every aggregation should report sane percentiles, no query should error. Validated against the devenv ClickHouse 26.2.5.45 on 127.0.0.1:9100 with 200k seeded rows; all 14 queries reported sane percentiles (p50 4-15ms), zero errors.
- [x] 7.2 Re-run the harness; confirm a second result file is created next to the first, and `init.sql` doesn't error on the second apply (idempotency check). Second run wrote a fresh timestamped file next to the first; no init.sql error.
- [x] 7.3 Sanity-check ingest throughput against a plausible range (≥50k events/sec on a laptop single-node with batch=10000, sync inserts); if dramatically lower, profile before declaring done. Seeder reported ~126k events/sec on this laptop — comfortably above the ≥50k bar.
- [x] 7.4 Run `openspec validate` and confirm no errors before marking the change ready to archive. `openspec validate baseline-openmeter-events-scenario` → "is valid".

## 9. clickhouse-benchmark migration

- [x] 9.1 Delete `RunQuery`, `statsFor`, `percentile`, `drainRows` from `bench/runner/runner.go`; remove their `QueryResult` / `IterationStats` shape and replace with the new `BenchResult` shape capturing the percentiles `clickhouse-benchmark` actually emits (p0/p10/.../p95/p99/p99.9/p99.99) plus throughput (QPS, RPS, MiB/s, result RPS, result MiB/s).
- [x] 9.2 Add `bench/runner/benchmark.go` containing `Bench(ctx, host, port, q, params, iterations, concurrency)` that: renders the SQL by substituting `{name:Type}` placeholders with literal values from the param set; invokes `clickhouse-benchmark --host H --port P --iterations N --delay 0 --concurrency C --query <sql>`; parses the stderr percentile block + summary line; returns a `BenchResult`.
- [x] 9.3 Probe for `clickhouse-benchmark` on PATH at harness start; fail fast with a clear error pointing at the dev shell if missing.
- [x] 9.4 Add `--concurrency` flag to `bench/cmd/bench/main.go` (default 1, preserves single-stream semantics).
- [x] 9.5 Update `defaultParams` to return a `map[string]string` of rendered SQL literals (the new wrapper does string substitution, not driver-side binding) — keep the same v1 default set (`namespace`, `type`, `from`, `to`, `subjects`, `group1`, `group2`).
- [x] 9.6 Update `bench/README.md` to document: clickhouse-benchmark dependency, new result fields, `--concurrency` flag, why the switch.
- [x] 9.7 Re-run end-to-end against a seeded local ClickHouse; inspect the new result file shape; confirm server-side timings are noticeably tighter than the previous client-side timings (expected ~30-50% lower p50 for cheap queries).

## 8. Follow-up (capture, don't implement)

- [x] 8.1 File a note in `openspec/changes/` for a future change: "drift detection against upstream OpenMeter DDL" (CI job diffing `event_query.go` generated string against `scenarios/baseline-openmeter/init.sql`). Captured in `openspec/changes/drift-detect-openmeter-ddl/`.
- [x] 8.2 File a note for "kind + Altinity ClickHouse Operator bring-up" so the harness can target the cluster instead of single-node. Captured in `openspec/changes/kind-clickhouse-operator-cluster/`.
- [x] 8.3 File a note for "real-traffic seed profile" sampled from an actual OpenMeter deployment. Captured in `openspec/changes/real-traffic-seed-profile/`.
