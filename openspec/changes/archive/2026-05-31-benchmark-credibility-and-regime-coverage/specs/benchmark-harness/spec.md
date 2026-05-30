## ADDED Requirements

### Requirement: Run-to-run variance

The harness SHALL support running each scenario more than once in a single invocation (`--repeat N`) and SHALL aggregate across the repeats — reporting a median-of-medians and a spread measure — so a reported delta (e.g. a −7% CPU difference) can be distinguished from run-to-run noise. Within-run `clickhouse-benchmark` percentiles capture iteration spread but not the run-to-run variance the README repeatedly hedges about ("ingest cost varies run-to-run", "verify on target hardware"); this requirement closes that gap.

#### Scenario: Repeats are aggregated with a spread
- **WHEN** the harness runs a scenario with `--repeat N` for N > 1
- **THEN** the recorded figures include a median-of-medians across the N repeats and a spread measure, so a delta smaller than the spread is recognizable as noise

### Requirement: Lookup-by-id index-pruning capture

For the lookup-by-id access pattern, the harness SHALL capture the `EXPLAIN indexes = 1` granule-pruning signal against a literal id — granules/parts scanned with the skip index disabled versus enabled — and record it in the result file. The `lookup_by_id` query's own wall-clock is explicitly not a clean latency benchmark (its inner id-resolver subquery dominates), so the captured pruning ratio is the bloom filter's real, diffable evidence rather than prose in a SQL comment.

#### Scenario: Granule pruning recorded for lookup-by-id
- **WHEN** the harness measures a scenario carrying a `bloom_filter` skip index on `id`
- **THEN** the result file records the granules/parts scanned with `use_skip_indexes = 0` and with the index active for a literal-id lookup, so the pruning ratio is captured

### Requirement: Clean-tree provenance guard

The harness already records the harness git commit with a `-dirty` suffix when the working tree has uncommitted changes (this behavior is unchanged). It SHALL additionally provide a guard (`--require-clean`) that refuses to run — or, at minimum, emits a loud warning before writing results — when the tree is dirty, so headline result files are not published from an unreproducible state.

#### Scenario: Dirty tree is refused under the guard
- **WHEN** the harness is run with `--require-clean` and the working tree has uncommitted changes
- **THEN** the run is refused (or gated behind an explicit `--allow-dirty`) so a `-dirty` result is not silently committed

#### Scenario: Commit provenance still recorded
- **WHEN** the harness runs without the guard on a dirty tree
- **THEN** it proceeds and records the `-dirty`-suffixed commit exactly as before, preserving existing behavior
