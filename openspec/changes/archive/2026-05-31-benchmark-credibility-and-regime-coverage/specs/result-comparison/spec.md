## ADDED Requirements

### Requirement: Scenario comparison command

The harness SHALL provide a command that reads the latest result file from each of two scenarios and emits the per-query delta table — p50 Δ, CPU Δ, and ingest Δ — that the README currently maintains by hand. Generating the comparison removes a class of transcription error and makes re-running a head-to-head cheap. The command SHALL match queries by name across the two result files and SHALL clearly mark queries present in only one side rather than silently dropping them.

#### Scenario: Delta table generated from two result files
- **WHEN** the comparison command is run for scenario A against scenario B
- **THEN** it reads the latest result JSON from each and prints a per-query table of A→B deltas (p50, CPU, ingest), computed from the recorded values, in the same column shape the README uses

#### Scenario: Unmatched queries are surfaced, not dropped
- **WHEN** a query exists in one scenario's result file but not the other
- **THEN** the comparison marks that query as present on only one side rather than omitting it, so coverage differences are visible
