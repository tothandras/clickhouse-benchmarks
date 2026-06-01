## MODIFIED Requirements

### Requirement: Cold-cache measurement axis

The harness SHALL support measuring a query with the ClickHouse filesystem cache
disabled, by injecting `SETTINGS enable_filesystem_cache = 0` into the query
before handing it to `clickhouse-benchmark`. Cold-cache mode exposes the real
I/O cost that a warm page cache hides — the regime in which the `bloom_filter`
skip index and `ZSTD` column compression produce their claimed wins. Cold cache
SHALL be offered as a paired axis (`--cold-paired`): when requested, every query
is measured both warm AND cold in a single run, producing two result entries per
query with the same query name and differing `cache_state` (`"warm"` and
`"cold"`), so warm and cold are captured side by side from one invocation rather
than two. The cache state used for each measurement SHALL be recorded in the
result file so warm and cold numbers are never silently compared. Warm-only
SHALL remain the default so existing result files stay comparable.

#### Scenario: Cold-paired measures each query warm and cold
- **WHEN** the harness is run with `--cold-paired`
- **THEN** each query produces two result entries — one with `cache_state = "warm"` and one run with `enable_filesystem_cache = 0` merged into its SETTINGS and `cache_state = "cold"` — sharing the same query name

#### Scenario: Warm is the default and is recorded
- **WHEN** the harness runs without `--cold-paired`
- **THEN** queries run against the warm page cache as before and each result entry records `cache_state = "warm"`, so older result files (which predate the field) and new ones are distinguishable
