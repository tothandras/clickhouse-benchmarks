## 1. Decimal-precision queries (baseline-openmeter)

- [x] 1.1 Add `sum_hour_decimal.sql` — clone `sum_hour.sql`, replace value extraction with `toDecimal128OrNull(nullIf(JSON_VALUE(om_events.data, '$.value'), 'null'), 19)`
- [x] 1.2 Add `avg_hour_decimal.sql` — clone `avg_hour.sql` with the decimal extraction
- [x] 1.3 Add `min_hour_decimal.sql` — clone `min_hour.sql` with the decimal extraction
- [x] 1.4 Add `max_hour_decimal.sql` — clone `max_hour.sql` with the decimal extraction
- [x] 1.5 Add `latest_hour_decimal.sql` — clone `latest_hour.sql`, decimal extraction inside `argMax(..., om_events.time)`

## 2. Window queries (baseline-openmeter)

- [x] 2.1 Add `sum_month.sql` — `toDateTime(tumbleStart(time, toIntervalMonth(1), 'UTC'), 'UTC')` + matching `tumbleEnd`, float value path
- [x] 2.2 Add `sum_hour_tz.sql` — clone `sum_hour.sql` but pass a non-UTC timezone (e.g. `'America/New_York'`) to `tumbleStart`/`tumbleEnd`
- [x] 2.3 customer_id query DROPPED — seeder has no subject→customer mapping; reproducing the `WITH map(...)` shape would require fabricated customer IDs. Documented as out of scope in proposal/design/spec.

## 3. Propagate to other variants

- [x] 3.1 Copy all new `.sql` files byte-for-byte into `scenarios/time-desc/queries/`
- [x] 3.2 Copy all new `.sql` files byte-for-byte into `scenarios/with-projections/queries/`
- [x] 3.3 Add to `scenarios/materialized-columns/queries/` reading typed columns (NOT byte-identical): decimal variants `CAST(om_events.value AS Nullable(Decimal128(19)))`, window variants `sum(om_events.value)` — the scenario never parses JSON at query time
- [x] 3.4 Add data-as-json equivalents: rewrite each new query's `JSON_VALUE`/`toDecimal128OrNull(nullIf(JSON_VALUE(...)))` to native-subcolumn form (`toDecimal128OrNull(nullIf(toString(data.value.:Float64), 'null'), 19)`), preserving the scenario's one-variable delta

## 4. Verify the new queries run

- [x] 4.1 Apply `init.sql` + seed a small dataset for baseline-openmeter; run every new `.sql` and confirm each returns rows without error — all 25 queries ran (avg_hour_decimal slowest at 108ms CPU vs 42ms float)
- [x] 4.2 Confirm a decimal query and its float twin (e.g. `sum_hour` vs `sum_hour_decimal`) aggregate the same row population — both yield 703 non-null buckets, identical
- [x] 4.3 Repeat 4.1 for the data-as-json subcolumn variants — all 25 ran; also smoke-tested materialized-columns typed-column variants (cheapest at ~20ms CPU)

## 5. Re-sweep and re-rank

- [x] 5.1 Run the 10M sweep across all five scenarios with the expanded query suite — all 5 completed, 25 queries each, results written
- [x] 5.2 Aggregate the result JSON and compare decimal-path vs float-path CPU/latency per aggregation per table design — decimal penalty: +29-36% baseline, +59-88% data-as-json, +283-315% materialized-columns
- [x] 5.3 Extend `bench/results/ANALYSIS-optimal-table.md` with a decimal-path section and re-validate the optimal-table recommendation — added Part 3; ranking inverts (data-as-json fastest on decimal, mat-cols slowest)
