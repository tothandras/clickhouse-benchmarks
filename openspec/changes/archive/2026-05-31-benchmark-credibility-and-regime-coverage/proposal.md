## Why

The harness is solid: real `clickhouse-benchmark` timing, per-query CPU from
`system.query_log`, self-describing result files, scenario-per-table isolation.
But the **conclusions** it produces outrun the **regime** it measures in, and
two correctness/reproducibility gaps weaken the audit trail. This change
proposes a prioritized set of improvements, grouped by what each one does to the
credibility of the findings — not a grab-bag of knobs.

The single most important observation: the README recommends `proposal`
(`data JSON` + `ZSTD(3)` + `bloom_filter` on `id`) and is explicit that the
bloom / I/O wins bind under **"multi-tenant, not-fully-cached, concurrent
load."** The bench runs:

- **single-namespace** (`namespace = 'default'` only — `defaultParams` binds one),
- **warm-cache** (`clickhouse-benchmark` reuses the page cache across iterations),
- **concurrency = 1** (the default; no sweep above it).

All three conditions the recommendation targets are absent. The harness cannot
currently exercise the regime in which its own headline win is supposed to
appear — so the bloom's value is argued from `EXPLAIN` granule counts in prose,
not measured. Closing that gap is the priority.

A second, subtler gap: the README's headline "correctness fix" (read JSON paths
untyped via `toDecimal128OrNull(toString(...))`, never `.:Float64`, so
string/int-stored values don't silently read as zero) is **never exercised on
the `$.value` path** — the canonical 50% of rows. The seeder emits `value` as a
uniform JSON `Float64` (`buildBaseline`: `rng.Float64() * 1000`), so on the
seeded data `.:Float64` and `toString(...)` are provably identical. Verified on
the live 100k `data_as_json_events` table: both forms return `uniqExact = 49883`
over all 49883 `api_request` rows, all of which resolve as `Float64`. The
correctness fix's value rests entirely on the minority string-stored types
(`tokens`, `duration_seconds`); the dominant path can't regress because the data
never makes it.

## What Changes

Grouped into four priority tiers. Tier 1 validates-or-threatens the actual
conclusions; lower tiers harden and add convenience. Each tier can land as its
own OpenSpec change — this proposal is the umbrella plan, not a single
implementation unit.

### Tier 1 — Make the bench exercise the regime the recommendation targets

The bloom-on-`id` / ZSTD-disk / JSON-typed-subcolumn wins are claimed to bind
under multi-tenant, cold-cache, concurrent load. Add the three missing axes so
the claims are measured, not asserted:

- **Cold-cache pass.** Add a per-query `enable_filesystem_cache = 0` mode (a
  `--cold` flag or a paired warm/cold measurement) so the I/O reduction the
  bloom and ZSTD produce is visible. The README itself calls this out as a
  debugging knob; promote it to a first-class measurement axis. Today every
  number is best-case warm.
- **Concurrency sweep.** Today `--concurrency` defaults to 1 and no run varies
  it. Add a sweep (e.g. `--concurrency 1,8,16`) and record each level, so the
  "p99 under concurrency roughly halves" claim for the bloom is a recorded
  number per scenario, not a one-off note in a SQL comment.
- **Multi-namespace / multi-tenant seed.** The seeder writes one namespace and
  queries bind `namespace = 'default'`. Real OpenMeter is multi-tenant via
  `namespace`. Note the causal subtlety: `namespace` is the **first** ORDER BY
  key, so on a multi-tenant table the primary sparse index already prunes to one
  tenant's contiguous range *before* the bloom on `id` runs. The current
  single-namespace bench (whole table = one namespace) is therefore plausibly
  the bloom's **best** case, and may **overstate** its production benefit —
  giving the bloom sole credit for pruning the namespace prefix would do anyway.
  Adding a configurable namespace count and binding a representative tenant
  measures the bloom's real *marginal* benefit on top of the namespace-prefix
  pruning. (This direction should be confirmed against the ORDER BY prefix
  before the run is treated as decisive — see Open questions.)
- **Capture the `lookup_by_id` EXPLAIN signal in the harness.** The query file
  itself says its wall-clock is meaningless (the inner id-resolver subquery
  dominates) and that the real signal is `EXPLAIN indexes = 1` granule pruning.
  Have the harness run that EXPLAIN on a literal id and record
  granules-before/after into the result file, turning the bloom's headline
  evidence into a captured, diffable metric instead of prose.

### Tier 2 — Correctness faithfulness and reproducibility hygiene

- **Seeder type-heterogeneity for `value`.** Emit the `$.value` path in mixed
  JSON storage under one path — some rows as a JSON number, some as a
  JSON-stringified number (`"123.4"`), some as a bigint that overflows
  `Float64`. This is the change that actually stress-tests the README's
  correctness fix on the canonical path: `.:Float64` would then read NULL on the
  string/bigint rows while `toDecimal128OrNull(toString(...))` reads them
  correctly, making the cross-variant comparison meaningful and turning the
  `unique_count_hour` inconsistency (Tier 4) into a measurable divergence rather
  than a cosmetic one.
- **Stop committing dirty-tree results.** The committed `proposal` result is
  stamped `harness_commit: 3907e52-dirty`. The harness already records the
  `-dirty` suffix (good); add a `--require-clean` guard (or at least a loud
  warning) so headline results aren't published from an uncommitted tree, and
  re-run the committed results from a clean commit.
- **Run-to-run variance, not just within-run percentiles.** Every result is a
  single `clickhouse-benchmark` invocation; percentiles capture
  iteration-to-iteration spread but not run-to-run variance, and the README
  repeatedly hedges ("ingest cost varies run-to-run", "verify on target
  hardware"). Add an `--repeat N` that runs each scenario N times and records
  median-of-medians plus spread, so a −7% claim can be distinguished from noise.

### Tier 3 — Comparison and analysis convenience

- **A `compare` subcommand.** All head-to-head deltas in the README
  (`−41% p50`, `−35% CPU`, the per-query tables) are computed by hand from two
  result files. Add `bench compare <scenarioA> <scenarioB>` (or
  `--baseline <dir>`) that reads the latest JSON from each and emits the delta
  table the README currently hand-maintains — same shape, generated. This makes
  re-running the comparison cheap and removes a class of transcription error.
- **Optional CI smoke run.** A tiny-row (`--rows 10_000`), single-iteration run
  of all scenarios on PRs would catch query-syntax breakage and DDL drift
  (`baseline-openmeter/init.sql` already documents a wished-for drift check)
  without needing the full 10M sweep.

### Tier 4 — Minor cleanups (only meaningful after Tier 2's seeder change)

- **`unique_count_hour` type consistency.** The three JSON scenarios
  (`data-as-json`, `order-by-extended-time`, `proposal`) still read
  `uniqExact(nullIf(toString(data.value.:Float64), 'null'))` while every other
  meter query moved to the type-agnostic `toString(data.<path>)` form. On the
  current uniform-Float64 data this is provably equivalent (verified:
  `uniqExact = 49883` either way) — so it is a **cosmetic inconsistency today,
  not a billing bug** (`uniqExact` excludes NULLs rather than summing them to
  zero, so there is no read-as-zero failure mode). It becomes a genuine,
  measurable divergence only once Tier 2 makes `value` type-heterogeneous; fix
  it then, not in isolation, and keep the production-faithful type-agnostic form
  everywhere.

## Capabilities

### New Capabilities

- `cache-state-control` — warm vs cold (`enable_filesystem_cache=0`) measurement
  as a first-class harness axis.
- `concurrency-sweep` — measure and record each query across multiple
  concurrency levels in one run.
- `result-comparison` — a harness command that diffs two scenarios' latest
  result files into a delta table.

### Modified Capabilities

- `benchmark-harness` — adds cold-cache and concurrency-sweep measurement axes;
  adds `--repeat` for run-to-run variance; adds a clean-tree guard; captures the
  `lookup_by_id` EXPLAIN granule-pruning signal into the result file.
- `heterogeneous-event-seeding` — `value` emitted in mixed JSON storage under
  one path; configurable namespace count for a multi-tenant seed.
- `data-as-json-scenario` — `unique_count_hour` moved to the type-agnostic
  `toString(...)` form (Tier 4), consistent with the other meter queries.

## Impact

- **Code**: `bench/runner/benchmark.go` (cold-cache SETTING, concurrency loop,
  EXPLAIN capture), `bench/runner/results.go` (new per-query fields:
  cache-state, concurrency level, granules-before/after; run-repeat
  aggregation), `bench/cmd/bench/main.go` (`--cold`, `--concurrency` list,
  `--repeat`, `--require-clean`, `compare` subcommand, multi-namespace param
  binding), `bench/seed/seed.go` (mixed-storage `value`, namespace count).
- **Scenarios**: `scenarios/*/queries/unique_count_hour.sql` (Tier 4).
- **Results**: result files gain cache-state / concurrency / granule-pruning
  fields; comparison output is generated rather than hand-written.
- **Docs**: README Findings re-run from a clean tree under the new regime;
  per-query tables become `bench compare` output.
- **Risk**: low and additive. New measurement axes default off (warm, c=1,
  single namespace) so existing result files stay comparable; the seeder change
  is the one behavior shift and is gated behind config with the current
  uniform-Float64 distribution as the default until the comparison is re-run.

## Open questions

- Should cold-cache be a separate scenario run or a paired axis within one run
  (doubling rows in each result file)? Paired keeps warm/cold honest on
  identical data but complicates the result schema.
- For run-to-run variance, is `--repeat` (whole-scenario reruns) enough, or do
  we want the harness to also drop and re-seed between repeats to capture
  seed-to-merge layout variance? Re-seeding is the more honest but much slower
  option.
