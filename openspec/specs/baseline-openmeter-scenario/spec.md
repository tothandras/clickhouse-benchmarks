# baseline-openmeter-scenario Specification

## Purpose

Define the baseline OpenMeter events scenario: a faithful reproduction of OpenMeter's `om_events` MergeTree table, a synthetic seed that exercises realistic cardinality across namespaces / types / subjects, and a query suite that covers every supported OpenMeter aggregation type (SUM, COUNT, AVG, MIN, MAX, UNIQUE_COUNT, LATEST) in the shape OpenMeter's meter query builder emits. This scenario is the reference workload against which all ClickHouse tuning experiments in this repo are evaluated.

## Requirements

### Requirement: Events table matches OpenMeter om_events

The scenario SHALL create a single MergeTree table named `om_events` whose column list, types, engine, `PARTITION BY`, `ORDER BY`, and skip index match the table defined in `openmeter/streaming/clickhouse/event_query.go:20-49` of the upstream OpenMeter repository. Specifically: columns `namespace String, id String, type LowCardinality(String), subject String, source String, time DateTime, data String, ingested_at DateTime, stored_at DateTime, store_row_id String`; a minmax skip index on `stored_at` with `GRANULARITY 4`; `PARTITION BY toYYYYMM(time)`; `ORDER BY (namespace, type, subject, toStartOfHour(time))`.

#### Scenario: Schema parity with upstream
- **WHEN** `SHOW CREATE TABLE om_events` is run after `init.sql` applies
- **THEN** the result contains every column, the minmax skip index on `stored_at`, the monthly `toYYYYMM(time)` partition expression, and the 4-tuple ORDER BY in the exact order `(namespace, type, subject, toStartOfHour(time))`

### Requirement: Synthetic event generation

The scenario SHALL provide a seed mechanism that populates `om_events` with synthetic events. The seed MAY emit multiple event `type` values whose `data` payloads are heterogeneous — each type carrying its own JSON field-set, modeling user-controlled production data. The seed SHALL guarantee that at least one type's `data` is a JSON object containing a numeric `value` field and at least one categorical group-by field (e.g. `group1`, `group2`), and that this baseline type holds a substantial share of rows, so the canonical meter queries continue to exercise namespace/type/subject filtering and JSON extraction realistically. The seed SHALL distribute events across at least one namespace, at least two distinct `type` values, and at least 100 distinct `subject` values over a multi-day time range. Every `data` row SHALL be parseable JSON.

#### Scenario: Seed populates sufficient cardinality
- **WHEN** the seed completes
- **THEN** `om_events` contains at least 1,000,000 rows spanning ≥2 types, ≥100 subjects, and ≥3 days of `time` values, with parseable JSON in every `data` row

#### Scenario: Baseline type remains queryable under a heterogeneous mix
- **WHEN** the seed emits a heterogeneous event-type mix
- **THEN** at least one type's rows carry a numeric `value` plus `group1`/`group2`, and that type holds enough rows that the canonical `value`-aggregating meter queries still scan a large, representative population

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
