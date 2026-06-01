# value-parity Specification

## Purpose

Define how the harness proves two table designs compute the same meter values,
not just that one is faster. Each run captures a normalized, order- and
type-independent result digest per query plus the seeded window; `bench compare`
diffs those digests across two scenarios — gated on an identical window — so a
performance comparison is also a correctness check. A divergence fails the
comparison (non-zero exit), making value parity a CI-style gate rather than a
claim asserted in prose.

## Requirements
### Requirement: Per-query result digests in the run record

For every query in a scenario, the harness SHALL compute a normalized
fingerprint of that query's result set once per run and persist it in the run
record (`value_parity.digests`, keyed by query name). The digest SHALL be a hash
over the result rows with float and decimal cells rounded to a fixed precision
and the rows sorted, so that a non-unique GROUP BY's tie-ordering and low-order
floating-point differences (from summing rows in a different physical order
under a different table layout) do not change the digest, while any real value
difference does. Column names and physical types SHALL be excluded from the
digest, so logically-equivalent formulations of the same query (e.g. `count(*)`
vs a native subcolumn read, a `Nullable` vs a bare column) produce the same
fingerprint for the same values. Digest capture SHALL be best-effort: a
per-query failure is recorded as a digest error and never aborts the run.

#### Scenario: Digest recorded per query
- **WHEN** a scenario run completes against a reachable database
- **THEN** the run record contains a `value_parity` entry with, for each query,
  a digest hash and row count (or an explicit error), computed from the same
  rendered parameters the timed run used

#### Scenario: Equivalent formulations digest equally
- **WHEN** two scenarios compute the same logical meter result over the same
  events via different SQL shapes (String vs JSON `data`, `Nullable` vs bare,
  float vs decimal extraction within rounding precision)
- **THEN** their digests for that query are equal, because the digest depends on
  the values only — not column names, physical types, or row order

### Requirement: Seeded-window gate on value comparison

The run record SHALL record the seeded time window the digests were computed
over (resolved `from`/`to`, plus the `--time-end` pin if one was set). A value
comparison between two runs SHALL only diff digests when both runs cover an
identical window; when the windows differ, the comparison SHALL be skipped with
an explicit message rather than reporting a false mismatch, because differing
windows mean the two runs saw different events.

#### Scenario: Comparison gated on identical window
- **WHEN** two runs' recorded windows differ (e.g. each used an unpinned,
  per-run `time.Now()` as the seed end)
- **THEN** the value comparison is skipped with a message naming the two
  windows and instructing the user to re-run both with the same `--time-end`,
  rather than reporting the queries as differing

### Requirement: Value-parity diff fails on disagreement

When two runs share an identical window, the comparison SHALL diff their
per-query digests and classify each query as MATCH, DIFFERS, or present on only
one side, and SHALL summarize the counts. If any query's values genuinely
differ (or errored), the comparison SHALL exit non-zero so it can serve as a CI
gate proving that two table designs compute the same meter values.

#### Scenario: Matching designs pass, diverging designs fail
- **WHEN** the comparison diffs two runs over an identical window
- **THEN** queries with equal digests are reported MATCH and queries with
  unequal digests are reported DIFFERS; the command exits zero when none differ
  and non-zero (naming the differing queries) when any do

