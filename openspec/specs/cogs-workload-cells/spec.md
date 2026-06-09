# cogs-workload-cells Specification

## Purpose

Define the workload-cell manifest contract and the drivers it configures: sink-shaped paced ingest, live wall-clock event timing with deterministic payloads, sliding query windows, and the default cell matrix, so COGS runs reproduce the production workload pattern rather than bulk-load behavior.

## Requirements

### Requirement: Cell manifest contract
A workload cell SHALL be defined by a JSON manifest in `cells/<name>.json` declaring scenario, preload rows, phase durations, ingest parameters (rate, batching thresholds, async mode, namespaces, mixed-value, seed), query parameters (qps, arrival process, mix key, cold fraction, concurrency cap, settings), and pricing profile. Manifests SHALL be decoded strictly: unknown fields are an error.

#### Scenario: unknown field rejected
- **WHEN** a cell manifest contains a field not in the schema
- **THEN** loading fails with an error naming the field

#### Scenario: idle cell is legal
- **WHEN** a manifest sets `events_per_sec: 0` and `qps: 0`
- **THEN** the cell validates and runs as an awake-but-unloaded floor measurement

### Requirement: Sink-shaped ingest pacing
The ingest driver SHALL pace inserts with a token bucket at the target events/sec and SHALL flush batches when either `batch_max_rows` or `flush_interval` is reached, whichever comes first, mirroring the OpenMeter sink's part-creation pattern.

#### Scenario: rate control under fast generation
- **WHEN** the generator can produce events faster than the target rate
- **THEN** achieved events/sec stays at the target within tolerance and batch sizes reflect the dual thresholds

### Requirement: Live event-time policy
The ingest driver SHALL stamp event time with the current wall clock at generation while preserving the generator's deterministic payload stream (the generator's time draw is performed and discarded). The query replayer SHALL bind a sliding time window per arrival: `to = now`, `from = now − TimeSpan`, rendered as UTC Unix seconds.

#### Scenario: payload determinism under time override
- **WHEN** the same (seed, index) event is produced by the seeder and by the ingest driver
- **THEN** all fields except event time, stored_at, and the store_row_id timestamp prefix are identical

#### Scenario: queries observe fresh data
- **WHEN** the replayer issues a query during the measure window
- **THEN** its rendered window includes rows the ingest driver wrote earlier in the same window

### Requirement: Default cell matrix
The repository SHALL ship cell manifests covering: idle; ingest-only at 1k/5k/25k eps; query-only at 1/4/16 qps; a full-cold query cell; a mixed ingest+query cell; and an async-insert ingest cell. A `--profile ci` option SHALL shorten phase durations for smoke runs without editing manifests.

#### Scenario: CI profile override
- **WHEN** a cell is run with `--profile ci`
- **THEN** soak/measure/drain durations are overridden to the short profile and the result records the effective durations
