## Context

`bench/results/ANALYSIS-optimal-table.md` ranks five table designs against a query suite that reproduces OpenMeter's meter queries. That suite was built from the *float* code path in `openmeter/streaming/clickhouse/meter_query.go`: every numeric aggregation extracts the value via `ifNotFinite(toFloat64OrNull(JSON_VALUE(data, '$.value')), NULL)`. But `meter_query.go` has a second numeric path — selected when a meter's value property is configured for exact arithmetic — that uses `toDecimal128OrNull(nullIf(JSON_VALUE(data, '$.value'), 'null'), 19)`. We never measure it. `Decimal128` is a 16-byte fixed-point type with no Float64 SIMD fast-path and different null semantics, so its scan/aggregate cost differs enough that the optimal-table ranking could change. The suite also omits the month window, a non-UTC timezone window, and the customer_id `map(...)` projection that upstream emits.

## Goals / Non-Goals

**Goals:**
- Add decimal-precision variants of every numeric aggregation (`sum`, `avg`, `min`, `max`, `LATEST`/`argMax`) using the exact `toDecimal128OrNull(nullIf(...), 19)` shape from `meter_query.go`.
- Add the month-window and non-UTC-timezone window shapes.
- Keep queries byte-identical across baseline-openmeter / time-desc / with-projections; rewrite them to read typed columns for materialized-columns; provide native-subcolumn equivalents for data-as-json.
- Re-sweep at 10M and extend the analysis with a decimal-path comparison.

**Non-Goals:**
- Changing table DDL or the seeder. The seeded `value` is already a JSON-string number; `toDecimal128OrNull` parses it just like `toFloat64OrNull` does. No new event types.
- Introducing a per-scenario `params.json`. The new queries reuse the existing v1 bound param set.
- Reproducing OpenMeter's Go-side query *builder* — we only mirror the SQL it emits.
- Reproducing the customer_id `WITH map(...) AS subject_to_customer_id` projection. The seeder has no customer dimension, so the map would require hardcoded, fabricated customer identifiers. Deferred until the seeder grows a real subject→customer mapping.

## Decisions

**Decision: scale 19 for `toDecimal128OrNull`.** Mirror upstream verbatim (`meter_query.go` hard-codes 19). Alternative — picking a scale tuned to our seed values — was rejected because the point is fidelity to what OpenMeter runs, not to our data.

**Decision: decimal queries are new files, not replacements.** We keep both `sum_hour` (float) and a new `sum_hour_decimal`. Rationale: the benchmark's value is the *side-by-side* float-vs-decimal cost on the same data and table, which a replacement would destroy. Naming: append `_decimal` to the existing float query name so the pairing is obvious in results.

**Decision: customer_id projection is dropped, not faked.** `meter_query.go` builds `map('subject1','customer1', ...)` from a per-meter subject→customer configuration. Our seeder has no customer dimension, so any map we wrote would hardcode fabricated customer identifiers — benchmarking a shape against invented data, which misrepresents the workload. We exclude the shape and note it as out of scope until the seeder grows a real customer dimension. Alternatives considered: (a) add a customer field to the seeder — rejected as scope creep that re-touches the just-archived heterogeneous-event work; (b) an identity `subject→subject` map — rejected because an identity map still measures a contrived lookup, not a real one.

**Decision: materialized-columns reads typed columns, not a byte-identical copy.** That scenario's entire premise is reading precomputed typed columns (`om_events.value`) instead of parsing JSON at query time. A byte-identical `JSON_VALUE` copy would defeat the variant. So its new queries read `om_events.value` directly; the decimal variants cast it at query time via `CAST(om_events.value AS Nullable(Decimal128(19)))`. Trade-off: the source column is `Nullable(Float64)`, so the cast inherits float's precision — it does not deliver the *exactness* a true decimal column would. We accept this because adding a `Decimal128` materialized column is a DDL change (a Non-Goal); the cast still exercises the decimal aggregation/serialization cost on the typed-column read path, which is the comparison we want.

**Decision: data-as-json gets native-subcolumn decimal variants.** For that scenario, `toFloat64OrNull(JSON_VALUE(...))` was already rewritten to `toString(data.value.:Float64)`-style subcolumn access. The decimal variant follows the same rule: `toDecimal128OrNull(nullIf(toString(data.value.:Float64), 'null'), 19)`, preserving the one-variable delta the scenario exists to measure.

## Risks / Trade-offs

- **[Decimal parse on a non-numeric `value` differs from float]** → Under the heterogeneous mix, ~50% of rows have no `$.value`. `JSON_VALUE` returns `''`; `nullIf(..., 'null')` does not catch `''`, and `toDecimal128OrNull('')` returns NULL (same as `toFloat64OrNull('')`). So decimal and float queries aggregate the same row population. Verified against `meter_query_test.go` golden output, which relies on this exact NULL behavior.
- **[Query count roughly doubles for numeric aggregations]** → Sweep time grows. Mitigated by the sweep already being a background job; the marginal cost is acceptable for the fidelity gain.
- **[Month window over a multi-day seed yields few buckets]** → The seed spans days, not months, so a month window collapses to ~1 bucket. That is fine: the shape (not the bucket count) is what we benchmark, and a single wide bucket is a legitimate cost point.
