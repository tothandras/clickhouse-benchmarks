## ADDED Requirements

### Requirement: Events table uses native JSON column

The scenario SHALL create an `om_events` MergeTree table identical to `baseline-openmeter-scenario`'s table EXCEPT that the `data` column is declared with ClickHouse's native `JSON` type instead of `String`. All other columns, the minmax skip index on `stored_at`, `PARTITION BY toYYYYMM(time)`, and `ORDER BY (namespace, type, subject, toStartOfHour(time))` MUST be unchanged so the variant isolates only the `data` column type.

#### Scenario: Schema parity except data column
- **WHEN** `SHOW CREATE TABLE om_events` is run after `init.sql` applies
- **THEN** every column other than `data` matches the baseline definition exactly, and `data` is declared as `JSON`

#### Scenario: ClickHouse version requirement
- **WHEN** `init.sql` is applied against a ClickHouse server older than 24.8
- **THEN** the apply MUST fail fast with a server-side error, and the harness MUST report the failure per-scenario without affecting other scenarios

### Requirement: Queries adapted to JSON column access

The scenario's `queries/` directory SHALL provide one query file per file in `scenarios/baseline-openmeter/queries/`, with each query rewritten to access JSON fields via the native `JSON` column's **typed-subcolumn** syntax (e.g. `data.value.:Float64`, `data.group1.:String`) instead of `JSON_VALUE(data, '$.value')`. Window functions, filters, GROUP BY columns, parameter placeholders, ORDER BY clauses, and the baseline's null-safety wrapper chain (`ifNotFinite(toFloat64OrNull(toString(...)), NULL)`, `nullIf(toString(...), 'null')`) MUST otherwise be identical to the baseline counterpart so that any latency difference is attributable to the column type and access path alone.

Note: `toFloat64OrNull` and `nullIf` only accept `String` input, so the wrapper chain stringifies the typed subcolumn (`toString(data.value.:Float64)`) before applying the baseline-shape wrappers. This is intentional — the experiment freezes query shape and measures only the storage cost. A "best-of-JSON" experiment that drops the wrappers in favor of native typed access is a separate future variant.

`JSON_VALUE` is unavailable on native `JSON` columns in ClickHouse 26.x (the server rejects it with `ILLEGAL_TYPE_OF_ARGUMENT`), so byte-identical reuse of baseline queries is not an option for this variant.

#### Scenario: Query file coverage matches baseline
- **WHEN** the scenario's `queries/` directory is enumerated
- **THEN** the set of `.sql` filenames matches `scenarios/baseline-openmeter/queries/` exactly

#### Scenario: Parameter placeholders match baseline
- **WHEN** any query file in the scenario is parsed
- **THEN** it uses the same `{namespace:String}`, `{type:String}`, `{from:DateTime}`, `{to:DateTime}`, `{subjects:Array(String)}` placeholders that the baseline queries use, so the harness's default parameter set binds without modification

### Requirement: Seed reuses baseline shape

The scenario SHALL be seeded by the same Go seeder (`bench/seed/`) the baseline scenario uses, with the same default cardinality (≥1 namespace, ≥2 types, ≥100 subjects, ≥3 days of time, JSON payload `{"value", "group1", "group2"}`). The seeder MUST insert the same JSON string into the `data` column; the native `JSON` type accepts well-formed JSON strings on INSERT and parses them server-side.

#### Scenario: Identical cardinality to baseline
- **WHEN** the seed completes for this scenario with the same `--rows` and `--seed` as a baseline run
- **THEN** the row count, distinct subjects, distinct types, and time span match the baseline run exactly
