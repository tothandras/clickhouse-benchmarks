## ADDED Requirements

### Requirement: Events table matches OpenMeter om_events

The scenario SHALL create a single MergeTree table named `om_events` whose column list, types, engine, `PARTITION BY`, `ORDER BY`, and skip index match the table defined in `openmeter/streaming/clickhouse/event_query.go:20-49` of the upstream OpenMeter repository. Specifically: columns `namespace String, id String, type LowCardinality(String), subject String, source String, time DateTime, data String, ingested_at DateTime, stored_at DateTime, store_row_id String`; a minmax skip index on `stored_at` with `GRANULARITY 4`; `PARTITION BY toYYYYMM(time)`; `ORDER BY (namespace, type, subject, toStartOfHour(time))`.

#### Scenario: Schema parity with upstream
- **WHEN** `SHOW CREATE TABLE om_events` is run after `init.sql` applies
- **THEN** the result contains every column, the minmax skip index on `stored_at`, the monthly `toYYYYMM(time)` partition expression, and the 4-tuple ORDER BY in the exact order `(namespace, type, subject, toStartOfHour(time))`

### Requirement: Synthetic event generation

The scenario SHALL provide a seed mechanism that populates `om_events` with synthetic events whose `data` column is a JSON object containing at least a numeric `value` field and at least one categorical group-by field (e.g. `group1`, `group2`). The seed SHALL distribute events across at least one namespace, at least two distinct `type` values, and at least 100 distinct `subject` values over a multi-day time range, so that meter queries exercise namespace/type/subject filtering and JSON extraction realistically.

#### Scenario: Seed populates sufficient cardinality
- **WHEN** the seed completes
- **THEN** `om_events` contains at least 1,000,000 rows spanning ≥2 types, ≥100 subjects, and ≥3 days of `time` values, with parseable JSON in every `data` row

### Requirement: Canonical meter query coverage

The scenario's `queries/` directory SHALL include at minimum one query per supported OpenMeter aggregation type (`SUM`, `COUNT`, `AVG`, `MIN`, `MAX`, `UNIQUE_COUNT`, `LATEST`), each constructed in the shape OpenMeter's `meter_query.go:108-362` emits: `tumbleStart`/`tumbleEnd` windowing, `JSON_VALUE(data, '$.value')` extraction with `nullIf`/`ifNotFinite` null-safety, filtering by namespace/type/subject and `time >= ? AND time < ?`. The directory SHALL additionally include variants exercising group-by on a JSON-extracted column and variants with/without `PREWHERE` reordering, so per-aggregation, per-window-size, and per-optimization timings can be compared.

#### Scenario: Aggregation coverage
- **WHEN** the scenario's queries are enumerated
- **THEN** at least one query exists for each of SUM, COUNT, AVG, MIN, MAX, UNIQUE_COUNT, LATEST

#### Scenario: Query shape fidelity
- **WHEN** any meter query in the scenario is parsed
- **THEN** it uses `tumbleStart`/`tumbleEnd` for windowing (or omits windowing entirely for the no-window case), extracts numeric values via `toFloat64OrNull(JSON_VALUE(data, '$.value'))` or `toDecimal128OrNull(...)`, and filters by `namespace`, `type`, and a `time` range — matching the shape upstream OpenMeter produces
