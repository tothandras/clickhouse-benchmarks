## ADDED Requirements

### Requirement: Sorting key uses DESC on the time sub-key

The scenario SHALL create an `om_events` MergeTree table identical to `baseline-openmeter-scenario`'s table EXCEPT that the sorting key changes the last sub-key direction from implicit ASC to explicit DESC: `ORDER BY (namespace, type, subject, toStartOfHour(time) DESC)`. Column definitions, the minmax skip index on `stored_at`, and `PARTITION BY toYYYYMM(time)` MUST be unchanged so the variant isolates only the time sub-key direction. The first three sub-keys (`namespace`, `type`, `subject`) MUST remain in their baseline (ascending) direction so the variant tests only one variable.

#### Scenario: Schema parity except time sub-key direction
- **WHEN** `SHOW CREATE TABLE om_events` is run after `init.sql` applies
- **THEN** every column, the skip index, and the PARTITION BY match the baseline, and the ORDER BY is `(namespace, type, subject, toStartOfHour(time) DESC)`

### Requirement: Queries reused verbatim from baseline

The scenario's `queries/` directory SHALL contain the same query files as `scenarios/baseline-openmeter/queries/`, byte-for-byte identical. The ORDER BY direction is transparent at the SQL surface; reusing baseline queries unchanged ensures any latency difference is attributable to the sorting key direction alone.

#### Scenario: Query file equivalence with baseline
- **WHEN** any query file in the scenario is diffed against its counterpart in `scenarios/baseline-openmeter/queries/`
- **THEN** the files are byte-for-byte identical

### Requirement: Seed reuses baseline shape

The scenario SHALL be seeded by the same Go seeder (`bench/seed/`) the baseline scenario uses, with the same default cardinality (≥1 namespace, ≥2 types, ≥100 subjects, ≥3 days of time, JSON payload `{"value", "group1", "group2"}`). Seeded row order at insert time MUST NOT depend on the sorting key direction — the seeder produces events in random time order, so the variant exercises the engine's responsibility to sort on merge.

#### Scenario: Identical cardinality to baseline
- **WHEN** the seed completes for this scenario with the same `--rows` and `--seed` as a baseline run
- **THEN** the row count, distinct subjects, distinct types, and time span match the baseline run exactly
