## Why

Our baseline query suite faithfully reproduces OpenMeter's *float-path* meter queries, but `openmeter/streaming/clickhouse/meter_query.go` emits several shapes we never exercise — most importantly the **decimal-precision** aggregations (`toDecimal128OrNull(nullIf(JSON_VALUE(data, path), 'null'), 19)`) used when a meter is configured for exact arithmetic. Because `Decimal128` has a different cost profile from `Float64` (wider type, no SIMD fast-path, different null handling), a benchmark that only measures the float path may rank table designs differently from how OpenMeter actually performs in production. To trust our "optimal table" conclusion, the query suite must cover the decimal path and the remaining windowing/projection shapes OpenMeter generates.

## What Changes

- Add **decimal-precision aggregation queries** mirroring `meter_query.go`: `sum`/`avg`/`min`/`max`/`argMax`(LATEST) over `toDecimal128OrNull(nullIf(JSON_VALUE(data, '$.value'), 'null'), 19)` instead of the `ifNotFinite(toFloat64OrNull(...))` float path.
- Add the **month window** shape (`toDateTime(tumbleStart(time, toIntervalMonth(1), 'UTC'), 'UTC')` + matching `tumbleEnd`) and a **non-UTC timezone** window variant, which our suite currently omits (we only have minute/hour/day + implicit).
- Propagate the new query files across every scenario variant (baseline-openmeter, time-desc, with-projections) byte-for-byte; rewrite them to read typed columns for materialized-columns; and add the native-subcolumn decimal equivalents to data-as-json.

We deliberately do **not** reproduce OpenMeter's customer_id `WITH map(...) AS subject_to_customer_id` projection: that map is built from a per-meter subject→customer configuration that our seeder has no analogue for, so reproducing it would require hardcoding fabricated customer identifiers. The shape is noted as out of scope until the seeder grows a real customer dimension.
- Re-run the 10M sweep and extend the analysis note so the optimal-table ranking accounts for decimal-path cost, not just float-path.

## Capabilities

### New Capabilities
<!-- none; this refines existing query-coverage requirements -->

### Modified Capabilities
- `baseline-openmeter-scenario`: the "Canonical meter query coverage" requirement is broadened from "at minimum one query per aggregation type (float path)" to *also* mandate the decimal-precision variant of each numeric aggregation and the month/timezone window shapes — matching the numeric surface of `meter_query.go`. The customer_id map projection is explicitly excluded (no customer dimension in the seeder).

## Impact

- **Queries**: new `.sql` files under each `scenarios/<variant>/queries/` directory. No existing query is modified or removed; queries are byte-identical across baseline/time-desc/with-projections, read typed columns in materialized-columns, and use native subcolumns in data-as-json (per the scenario-format contract).
- **Harness**: the fixed v1 default param set already binds `{namespace}`, `{type}`, `{subjects}`, `{from}`, `{to}`, `{group1}`. The decimal and window variants reuse existing params. No harness code change required.
- **Analysis**: `bench/results/ANALYSIS-optimal-table.md` gains a decimal-path section; the optimal-table recommendation is re-validated against it.
- **Reference**: shapes are derived verbatim from upstream `openmeter/streaming/clickhouse/meter_query.go` and its `meter_query_test.go` golden queries.
