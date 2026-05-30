# cache-state-control Specification

## Purpose

Define the harness's ability to measure a query against a known filesystem-cache state — warm (default) or cold — and to record which state each measurement used. Cold-cache mode exposes the real I/O cost that a warm page cache hides, the regime in which the `bloom_filter` skip index and `ZSTD` column compression produce their claimed wins, so warm and cold numbers are never silently compared.

## Requirements

### Requirement: Cold-cache measurement axis

The harness SHALL support measuring a query with the ClickHouse filesystem cache disabled, by injecting `SETTINGS enable_filesystem_cache = 0` into the query before handing it to `clickhouse-benchmark`. Cold-cache mode exposes the real I/O cost that a warm page cache hides — the regime in which the `bloom_filter` skip index and `ZSTD` column compression produce their claimed wins. The cache state used for each measurement (`"warm"` or `"cold"`) SHALL be recorded in the result file so warm and cold numbers are never silently compared. Warm SHALL remain the default so existing result files stay comparable.

#### Scenario: Cold-cache flag disables the filesystem cache
- **WHEN** the harness measures a query in cold-cache mode
- **THEN** the query is run with `enable_filesystem_cache = 0` merged into its SETTINGS, and its result entry records `cache_state = "cold"`

#### Scenario: Warm is the default and is recorded
- **WHEN** the harness runs without requesting cold-cache mode
- **THEN** queries run against the warm page cache as before and each result entry records `cache_state = "warm"`, so older result files (which predate the field) and new ones are distinguishable
