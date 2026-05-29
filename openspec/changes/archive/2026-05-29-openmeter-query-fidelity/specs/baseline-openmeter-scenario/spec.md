## MODIFIED Requirements

### Requirement: Canonical meter query coverage

The scenario's `queries/` directory SHALL include at minimum one query per supported OpenMeter aggregation type (`SUM`, `COUNT`, `AVG`, `MIN`, `MAX`, `UNIQUE_COUNT`, `LATEST`), each constructed in the shape OpenMeter's `meter_query.go:108-362` emits: `tumbleStart`/`tumbleEnd` windowing, `JSON_VALUE(data, '$.value')` extraction with `nullIf`/`ifNotFinite` null-safety, filtering by namespace/type/subject and `time >= ? AND time < ?`. The directory SHALL additionally include variants exercising group-by on a JSON-extracted column and variants with/without `PREWHERE` reordering, so per-aggregation, per-window-size, and per-optimization timings can be compared.

The scenario SHALL ALSO cover the remaining numeric shapes `meter_query.go` emits beyond the float path:

- **Decimal-precision aggregations.** For each numeric aggregation (`SUM`, `AVG`, `MIN`, `MAX`, `LATEST`), the directory SHALL include a variant that, on JSON-reading scenarios, extracts the value via `toDecimal128OrNull(nullIf(JSON_VALUE(data, '$.value'), 'null'), 19)` instead of the `ifNotFinite(toFloat64OrNull(...))` float path. On scenarios that precompute the value into a typed column, the decimal variant MAY instead aggregate that column cast to `Decimal128`, so float-vs-decimal aggregate cost can be compared on the same data and table without re-parsing JSON.
- **Month and timezone windows.** The directory SHALL include a month-window query using `toDateTime(tumbleStart(time, toIntervalMonth(1), 'UTC'), 'UTC')` with the matching `tumbleEnd`, and at least one window query using a non-UTC timezone argument, matching the windowing branches upstream produces.

Decimal and float variants SHALL coexist (the float query is not replaced), so results report both side by side. The customer_id `WITH map(...) AS subject_to_customer_id` projection that `meter_query.go` emits is OUT OF SCOPE for this scenario, because the seed has no subject→customer mapping to reproduce; covering it would require fabricated customer identifiers.

#### Scenario: Aggregation coverage
- **WHEN** the scenario's queries are enumerated
- **THEN** at least one query exists for each of SUM, COUNT, AVG, MIN, MAX, UNIQUE_COUNT, LATEST

#### Scenario: Query shape fidelity
- **WHEN** any meter query in the scenario is parsed
- **THEN** it uses `tumbleStart`/`tumbleEnd` for windowing (or omits windowing entirely for the no-window case), extracts numeric values via `toFloat64OrNull(JSON_VALUE(data, '$.value'))` or `toDecimal128OrNull(...)`, and filters by `namespace`, `type`, and a `time` range — matching the shape upstream OpenMeter produces

#### Scenario: Decimal-precision variant exists for each numeric aggregation
- **WHEN** the scenario's queries are enumerated
- **THEN** for each of SUM, AVG, MIN, MAX, and LATEST there exists a variant whose value extraction uses `toDecimal128OrNull(nullIf(JSON_VALUE(data, '$.value'), 'null'), 19)`, alongside (not replacing) the corresponding float-path query

#### Scenario: Month and timezone window shapes are covered
- **WHEN** the scenario's queries are enumerated
- **THEN** there exists a month-window query using `toIntervalMonth(1)` and at least one query using a non-UTC timezone argument to `tumbleStart`/`tumbleEnd`
