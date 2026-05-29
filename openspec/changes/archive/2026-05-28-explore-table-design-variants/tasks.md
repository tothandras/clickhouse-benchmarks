## 1. data-as-json scenario

- [x] 1.1 Create `scenarios/data-as-json/init.sql` cloning the baseline DDL with `data` declared as `JSON` instead of `String`; keep every other column, the minmax skip index on `stored_at`, `PARTITION BY toYYYYMM(time)`, and `ORDER BY (namespace, type, subject, toStartOfHour(time))` unchanged. Use `CREATE TABLE IF NOT EXISTS` for idempotency.
- [x] 1.2 Add a header comment in `init.sql` noting that this variant requires ClickHouse ≥24.8 (when the native `JSON` type became stable) and stating the design intent (isolate `data` column type as the only variable).
- [x] 1.3 Create `scenarios/data-as-json/queries/` with one `.sql` per baseline query (same filenames), rewriting every `JSON_VALUE(data, '$.field')` to native typed-subcolumn access (`om_events.data.value.:Float64`, `om_events.data.group1.:String`, `om_events.data.group2.:String`). The baseline's null-safety wrapper chain (`toFloat64OrNull`, `ifNotFinite`, `nullIf`) is preserved — these wrappers only accept `String`, so the chain becomes `toFloat64OrNull(toString(data.field.:Type))`. This freezes query shape so latency differences are attributable to column type alone. `JSON_VALUE` was investigated on native `JSON` columns and rejected by the server in 26.x, ruling out byte-identical reuse.
- [x] 1.4 Verify all 14 baseline filenames are present in `scenarios/data-as-json/queries/` and each file uses the same `{namespace:String}` / `{type:String}` / `{from:DateTime}` / `{to:DateTime}` / `{subjects:Array(String)}` placeholders the harness binds.

## 2. time-desc scenario

- [x] 2.1 Create `scenarios/time-desc/init.sql` cloning the baseline DDL exactly EXCEPT `ORDER BY (namespace, type, subject, toStartOfHour(time) DESC)`. Use `CREATE TABLE IF NOT EXISTS` for idempotency. ClickHouse 26.x gates reversed sub-keys behind `allow_experimental_reverse_key`; the table sets this via `SETTINGS allow_experimental_reverse_key = 1`.
- [x] 2.2 Add a header comment in `init.sql` stating the design intent (only the time sub-key direction differs from baseline) and noting the fallback path (precomputed inverted column) if a target ClickHouse version rejects DESC on a sub-key.
- [x] 2.3 Copy every `.sql` from `scenarios/baseline-openmeter/queries/` into `scenarios/time-desc/queries/` byte-for-byte (no edits — DESC ordering is transparent at the query surface).

## 3. with-projections scenario

- [x] 3.1 Create `scenarios/with-projections/init.sql` cloning the baseline DDL plus two `PROJECTION` clauses: `proj_by_stored_at` with `SELECT * ORDER BY (stored_at)` and `proj_by_store_row_id` with `SELECT * ORDER BY (store_row_id)`. Use `CREATE TABLE IF NOT EXISTS` for idempotency.
- [x] 3.2 Add a header comment in `init.sql` stating the design intent (measure projection storage and ingest cost on the existing query mix) and noting that baseline queries are reused unchanged — projection selection is the planner's responsibility.
- [x] 3.3 Copy every `.sql` from `scenarios/baseline-openmeter/queries/` into `scenarios/with-projections/queries/` byte-for-byte.

## 4. Documentation

- [x] 4.1 Update `scenarios/README.md` to list the three new variants, briefly describing the single design variable each isolates (column type, sub-key direction, projections) and noting that all three reuse the baseline seed and (where unchanged) the baseline queries.
- [x] 4.2 Add a short note in `scenarios/README.md` about the shared-database footgun: each scenario's `init.sql` targets `om_events`, so two scenarios cannot coexist on the same database simultaneously. The intended workflow for cross-scenario comparison on a single ClickHouse is to drop the table between runs or point each at its own database via DSN.

## 5. Validation

- [x] 5.1 Spin up a single-node ClickHouse via the devenv (`clickhouse server` on port 9100), set `CLICKHOUSE_DSN`, and run the harness against `--scenario data-as-json` with a small row count (`--rows 100000 --iterations 5`). Confirmed: all 14 queries pass, p50 4-6ms, seed 122k events/sec. Result file at `bench/results/data-as-json/2026-05-25T22-57-21Z.json`.
- [x] 5.2 Drop `om_events`, run the harness against `--scenario time-desc` with the same `--rows`, `--seed`, `--iterations`. Confirmed: all 14 queries pass, p50 4-7ms, seed 129k events/sec. `SHOW CREATE TABLE` confirms `toStartOfHour(time) DESC`.
- [x] 5.3 Drop `om_events`, run the harness against `--scenario with-projections` with the same `--rows`, `--seed`, `--iterations`. Confirmed: all 14 queries pass, p50 4-7ms, seed 120k events/sec. `system.projection_parts` shows both `proj_by_stored_at` and `proj_by_store_row_id` materialized across all data parts.
- [x] 5.4 Drop `om_events` and run the harness against `--scenario baseline-openmeter` with identical `--rows 100000 --iterations 5 --seed 42` so the four result files form a comparable A/B/C/D set. Done: ran all four (baseline, data-as-json, time-desc, with-projections) in one sweep, dropping `om_events` between each. Result files under `bench/results/<scenario>/2026-05-26T14-53-*.json`.
- [x] 5.5 `jq` the four result files for `p50_sec`, `qps`, and `ingest.events_per_second`; sanity-check that variant results land in the same order of magnitude as baseline. Verdict: all four land in the same band — p50 3-7ms, ingest 119k-132k events/sec, no massive deviations, no bug. At 100k rows the design variables don't visibly diverge: data-as-json matches baseline (typed subcolumns as cheap as `JSON_VALUE` at this size), time-desc matches baseline (sub-key direction invisible at small scale), with-projections matches on query latency with ~4% lower ingest (119k vs 125k) — the projection write cost, far below the design's "2-3× worse" concern. Meaningful divergence would need a larger dataset (10M+ rows); the equality at 100k is itself the finding.
- [x] 5.6 Run `openspec validate explore-table-design-variants` and confirm "is valid".
