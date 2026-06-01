## ADDED Requirements

### Requirement: Shared seed window for cross-scenario parity

The harness SHALL accept a `--time-end` flag (RFC3339) that pins the seeder's
`TimeEnd` — the newest event time — so that, when set, every scenario seeded in
the run uses the identical event time window. Without it, each scenario defaults
to its own `time.Now()` (truncated to the minute), which makes two scenarios'
seeded event streams diverge. Pinning `--time-end` (together with the shared RNG
`--seed`) is the prerequisite for cross-scenario value parity: only when both
scenarios seed a byte-identical event stream are their per-query result digests
comparable. The resolved window SHALL be recorded in the run so a reader can see
which window the run (and its digests) cover, and a malformed `--time-end` SHALL
fail fast with a clear error before any seeding begins.

#### Scenario: Pinned time-end makes scenarios seed identical windows
- **WHEN** the harness runs two scenarios in one invocation with the same `--time-end` and `--seed`
- **THEN** both scenarios seed events over the identical time window, so their value-parity digests are computed over the same events and are comparable

#### Scenario: Unpinned time-end defaults per scenario
- **WHEN** the harness runs without `--time-end`
- **THEN** the seeder uses `time.Now()` truncated to the minute as before, and the run records the resolved window so a later comparison can detect that two such runs cover different windows

#### Scenario: Malformed time-end fails fast
- **WHEN** the harness is given a `--time-end` that is not valid RFC3339
- **THEN** it exits non-zero with an error naming the flag and the expected format before seeding any scenario
