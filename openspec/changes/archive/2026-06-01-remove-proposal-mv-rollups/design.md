## Context

The `proposal` scenario is the repo's recommended ClickHouse table design for
OpenMeter usage metering. It currently bundles two layers: (1) table-level
levers on the base `proposal_events` table — `data JSON`, `CODEC(ZSTD(3))`, and
a `bloom_filter` on `id` — and (2) two per-meter materialized-view rollups for
the known-schema Kong meters (`kong.llm_request` SUM tokens; `kong.api_request`
COUNT), with 3-part hybrid queries to read them billing-exact over arbitrary
windows.

Layer (2) has not held up. The `kong.api_request` rollup is already recorded in
`init.sql`/`meters.yaml` as a measured failure (≈1.0× compression — its bounded
ID dimensions cross-multiply past the event count, so the GROUP BY collapses
nothing). More fundamentally, per-meter MVs violate the project's standing
constraint that the table stay meter-agnostic for thousands of unknown-schema
meters; the rollups are a special case that does not generalize and adds ingest
cost. This change removes layer (2) and keeps layer (1).

## Goals / Non-Goals

**Goals:**
- Remove both rollup tables and both materialized views from the proposal.
- Remove the queries that can only run against those rollups.
- Keep the proposal a faithful, runnable benchmark of the table-level design
  (JSON + ZSTD(3) + bloom) on the full Kong + generic workload.
- Bring the `proposal-scenario` spec and the READMEs back in sync.

**Non-Goals:**
- No change to the base `proposal_events` table schema.
- No change to the harness, seeder, value-parity, or compare code.
- No rewrite of historical committed result files; old rollup-query rows stay
  as record and simply stop being produced on re-runs.
- Not touching the `baseline-openmeter` scenario.

## Decisions

**Keep the base-table Kong queries; delete only the rollup-served ones.**
The Kong total/grouped queries (`kong_*_total`, `kong_api_request_by_*`,
`kong_status_by_route`) are plain reads against `proposal_events` and exercise
the JSON+ZSTD+bloom design on realistic Kong payloads — exactly what the
proposal is meant to measure. Only `kong_llm_tokens_total_hybrid.sql`,
`kong_api_request_total_hybrid.sql`, and `kong_status_by_route_rollup.sql` read
from the dropped tables, so only those three are deleted. The "oracle" base-table
totals (`kong_llm_tokens_total.sql`, `kong_api_request_total.sql`) become
standalone (their comments referencing a hybrid sibling are updated).
*Alternative considered:* delete all Kong-specific queries and leave only the
generic aggregations — rejected as over-trimming; it would drop the proposal's
realistic Kong coverage for no benefit.

**Delete `meters.yaml`.** Its declared role was "the source of truth the
materialized-view rollups implement." With the rollups gone, it documents
nothing the surviving queries depend on, so it is removed rather than left as
orphaned prose. *Alternative considered:* keep it as plain meter definitions —
rejected to avoid leaving a file whose stated purpose no longer exists.

**Spec delta uses REMOVED, not MODIFIED, for the three rollup requirements.**
The rollup/hybrid/meters.yaml requirements are being deleted outright, not
rephrased; only "Recommended all-in-one events table" survives unchanged.

## Risks / Trade-offs

- **Loss of the only billing-exact-rollup example** → Mitigation: the removal
  is the point (the rollups didn't pay off and broke the meter-agnostic
  constraint); the 3-part hybrid pattern remains recoverable from git history
  if ever revisited on different data.
- **Stale references in committed result files / docs** → Mitigation: this
  change explicitly refreshes `scenarios/README.md` and the top-level
  `README.md`; old `bench/results/proposal/*.json|md` are left as dated record,
  not edited.
- **`init.sql` no longer idempotently drops pre-existing rollup objects** →
  Mitigation: the scenario uses per-run isolated databases; a stale rollup
  table left in a reused DB is inert (nothing references it). No `DROP` is
  added, matching the scenario-format contract (CREATE-only, idempotent).
