## MODIFIED Requirements

### Requirement: Scenario comparison command

The harness SHALL provide a command that reads the latest result file from each
of two scenarios and emits the per-query delta table — p50 Δ, CPU Δ, and ingest
Δ — that the README otherwise maintains by hand. Generating the comparison
removes a class of transcription error and makes re-running a head-to-head
cheap. The command SHALL match queries by name across the two result files
(using the warm, lowest-concurrency measurement per query so the comparison is
apples-to-apples) and SHALL clearly mark queries present in only one side rather
than silently dropping them. In addition to the latency/CPU/ingest delta table,
the command SHALL emit the value-parity table — diffing the two runs' recorded
per-query result digests, gated on an identical seeded window — so the same
command both compares performance and proves the two designs compute the same
values.

#### Scenario: Delta table generated from two result files
- **WHEN** the comparison command is run for scenario A against scenario B
- **THEN** it reads the latest result JSON from each and prints a per-query table of A→B deltas (p50, CPU, ingest), computed from the recorded values, in the same column shape the README uses

#### Scenario: Unmatched queries are surfaced, not dropped
- **WHEN** a query exists in one scenario's result file but not the other
- **THEN** the comparison marks that query as present on only one side rather than omitting it, so coverage differences are visible

#### Scenario: Value-parity table emitted alongside the deltas
- **WHEN** both result files carry value-parity digests over an identical window
- **THEN** the command additionally prints a per-query MATCH/DIFFERS table from the digests and exits non-zero if any query's values differ; when the windows differ or a run predates digest capture, it prints an explicit skip message instead
