## Why

The baseline scenario reproduces OpenMeter's `om_events` table verbatim, which gives us one data point. To know whether the baseline is the *right* design for this workload, we need head-to-head measurements against deliberate variants — different storage shapes for `data`, different ordering keys, and different access-pattern accelerators (projections). Three commonly-debated knobs are worth measuring first:

1. **`data` as native `JSON`** vs. `String` + `JSON_VALUE` — does the JSON object type pay for itself on the aggregation queries?
2. **`time` in DESC order** within the sorting key — meter queries scan by recency; does flipping the time sub-key tighten part skipping?
3. **Projections by `stored_at` and `store_row_id`** — do these accelerate ingestion-time and point-lookup queries enough to justify the storage overhead?

Each is one independent variable. Sliced as separate scenarios, they slot into the existing harness — the same seeder, the same query files, the same A/B JSON output — and produce comparable result files alongside `baseline-openmeter/`.

## What Changes

- Add `scenarios/data-as-json/` — copy of baseline but `data` is declared `JSON` (ClickHouse 24.8+ native type). Queries swap `JSON_VALUE(data, '$.value')` for direct path access (`data.value` etc.). The seeder writes the same JSON strings; the JSON column accepts them on INSERT.
- Add `scenarios/time-desc/` — copy of baseline but the sorting key is `(namespace, type, subject, toStartOfHour(time) DESC)`. All queries unchanged. Measures whether DESC on the time sub-key helps the "recent window" access pattern.
- Add `scenarios/with-projections/` — copy of baseline plus two projections: one ordered by `stored_at` (for ingestion-time lookups) and one ordered by `store_row_id` (for point lookups by row id). Queries unchanged at first; future query files can target the projections explicitly.
- Each scenario reuses the baseline's `init.sql` shape (idempotent `CREATE TABLE IF NOT EXISTS`) and the same `queries/` set — only the deltas differ. The harness's existing scenario discovery picks them up with no code changes.
- No changes to the benchmark harness, seeder, or scenario format spec — this is purely additive scenario authoring.

## Capabilities

### New Capabilities

- `data-as-json-scenario`: Variant where `data` uses ClickHouse's native `JSON` type, with queries adapted to dot-path access. Tested against the same workload as `baseline-openmeter-scenario`.
- `time-desc-scenario`: Variant where the sorting key uses DESC ordering on the time sub-key. Tested with unchanged baseline queries to isolate the ordering effect.
- `with-projections-scenario`: Variant where the table carries two projections (by `stored_at` and by `store_row_id`). Tested with unchanged baseline queries to measure projection overhead on the existing query mix.

### Modified Capabilities

<!-- None — the baseline-openmeter-scenario, benchmark-harness, and scenario-format specs are unchanged. -->

## Impact

- **Code**: None. The harness already discovers any directory under `scenarios/` containing an `init.sql`; the three new directories slot in.
- **Data**: Each scenario seeds its own `om_events` table from scratch. Reruns are idempotent (the harness applies `init.sql` on every run; `CREATE TABLE IF NOT EXISTS` is a no-op). Run them against separate databases or drop the table between scenarios when comparing on a shared single-node ClickHouse.
- **Results**: New result files appear under `bench/results/data-as-json/`, `bench/results/time-desc/`, and `bench/results/with-projections/`, with the same JSON shape as baseline results. Cross-scenario comparison is a `jq` away.
- **Dependencies**: `data-as-json` requires ClickHouse ≥24.8 (the version that promoted `JSON` from experimental). The devenv ships 26.x, so this is satisfied locally; cluster targets will need to confirm.
- **Risk**: Low. Each scenario is isolated on disk and at runtime. If a variant errors, the harness reports it per-query and moves on; the other scenarios are unaffected.
