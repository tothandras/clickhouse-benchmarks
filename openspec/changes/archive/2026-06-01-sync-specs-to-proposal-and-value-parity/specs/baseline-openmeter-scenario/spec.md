## MODIFIED Requirements

### Requirement: Events table matches OpenMeter om_events

The scenario SHALL create a single MergeTree table named `baseline_openmeter_events`
whose column list, types, engine, `PARTITION BY`, `ORDER BY`, and skip index
match the upstream OpenMeter `om_events` table defined in
`openmeter/streaming/clickhouse/event_query.go:20-49`. A per-scenario table name
(rather than the literal `om_events`) lets the baseline and other scenarios
coexist in one database without clobbering each other; the schema is otherwise
the verbatim upstream reproduction. Specifically: columns `namespace String, id
String, type LowCardinality(String), subject String, source String, time
DateTime, data String, ingested_at DateTime, stored_at DateTime, store_row_id
String`; a minmax skip index on `stored_at` with `GRANULARITY 4`; `PARTITION BY
toYYYYMM(time)`; `ORDER BY (namespace, type, subject, toStartOfHour(time))`.

#### Scenario: Schema parity with upstream
- **WHEN** `SHOW CREATE TABLE baseline_openmeter_events` is run after `init.sql` applies
- **THEN** the result contains every column, the minmax skip index on `stored_at`, the monthly `toYYYYMM(time)` partition expression, and the 4-tuple ORDER BY in the exact order `(namespace, type, subject, toStartOfHour(time))`, matching the upstream `om_events` definition column-for-column
