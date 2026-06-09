# heterogeneous-event-seeding (delta)

## ADDED Requirements

### Requirement: Streaming generator with preserved determinism
The seeder's event generation SHALL be exposed as a streaming generator usable by the ingest driver, producing the event at any index as a pure function of (seed, index). The bulk seeder SHALL be reimplemented on top of it, and seeded output SHALL remain byte-identical for identical flags (verified via the existing digest machinery). The generator SHALL support an event-time override that performs and discards the stream's time draw so all other fields remain deterministic.

#### Scenario: refactor preserves seeded output
- **WHEN** a table is seeded with identical flags before and after the generator extraction
- **THEN** the seeded-table digests are identical

#### Scenario: time override preserves payloads
- **WHEN** the generator produces event (seed=42, idx=N) with and without the time override
- **THEN** type, payload, subject, and id are identical; only time-derived fields differ
