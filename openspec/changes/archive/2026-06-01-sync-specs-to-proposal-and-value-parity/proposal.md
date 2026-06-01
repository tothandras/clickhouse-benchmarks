# Sync specs to the proposal scenario and value-parity workflow

## Why

The live specs have drifted from what the repo actually ships. Several
capabilities landed in code and committed result files without a matching
spec update, and two existing specs describe an earlier shape of a feature
that has since changed. This change reconciles `openspec/specs/` with the
built, committed reality — it documents no new work, only what already exists
on `main`/the current branch.

Concretely, the gaps:

- **The `proposal` scenario is undocumented.** It is the repo's headline
  recommendation — the all-in-one table design (`data JSON` + `CODEC(ZSTD(3))`
  + `bloom_filter` on `id`) plus two live materialized-view rollups for the
  known-schema Kong meters, with 3-part hybrid (raw head + rollup interior +
  raw tail) billing-exact reads — yet no spec describes its contract. Its
  meter definitions live in `scenarios/proposal/meters.yaml` as the source of
  truth the rollups implement.

- **Value parity is undocumented.** Each run now captures a normalized result
  digest per query (rounded floats/decimals, order-independent), records the
  seeded window, and `bench compare` diffs the digests across two scenarios —
  gated on an identical window — as a CI-style proof that two table designs
  compute the same meter values. None of this is specced.

- **Per-scenario table names.** Scenarios were given isolated table names
  (`baseline_openmeter_events`, `proposal_events`) so they can coexist in one
  database. The `baseline-openmeter-scenario` spec still asserts the table is
  named `om_events`.

- **Cold cache is now a paired axis.** `cache-state-control` describes a
  cold-only mode; the harness ships `--cold-paired`, which measures every
  query warm AND cold in one run (two `BenchResult`s per query differing by
  `cache_state`). Warm-only remains the default.

- **`result-comparison` now also emits the value-parity table**, not just the
  p50/CPU/ingest delta table.

- **`--time-end` shared seed window** is the prerequisite that makes value
  parity meaningful: pinning it makes two scenarios seed a byte-identical event
  stream so their digests are comparable.

The references to removed scenarios (`data-as-json`, `time-desc`,
`with-projections`, `data-as-map`) live only in `changes/archive/` as
historical record; they are not in live `specs/`, so there is nothing to
remove here.

## What Changes

- **ADD** a `proposal-scenario` capability spec covering the recommended
  all-in-one table design, the two known-meter MV rollups (the `kong.llm_request`
  dims-full rollup that pays off; the `kong.api_request` dims-bounded rollup
  retained as a documented negative result), the 3-part hybrid billing-exact
  reads, and `meters.yaml` as the rollups' source of truth.
- **ADD** a `value-parity` capability spec covering per-query result digests
  in the run record, the seeded-window gate, and the `bench compare` value-diff
  that fails when two designs disagree.
- **MODIFY** `baseline-openmeter-scenario` to use the per-scenario table name
  `baseline_openmeter_events` while keeping the upstream-parity intent (the
  column set / engine / partition / order-by still match upstream `om_events`).
- **MODIFY** `cache-state-control` to describe `--cold-paired` (warm + cold in
  one run, two result entries per query) instead of a cold-only mode.
- **MODIFY** `result-comparison` to additionally emit the value-parity table.
- **MODIFY** `benchmark-harness` to document the `--time-end` shared seed
  window that underpins cross-scenario value parity.

## Impact

- Affected specs: `proposal-scenario` (new), `value-parity` (new),
  `baseline-openmeter-scenario`, `cache-state-control`, `result-comparison`,
  `benchmark-harness`.
- No code changes — the implementation already exists and is committed. This
  change exists solely to bring the spec set back in sync, then archive into
  `openspec/specs/`.
