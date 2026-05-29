## Context

The baseline scenario (`scenarios/baseline-openmeter/`) reproduces OpenMeter's `om_events` table verbatim and gives us one performance data point. To know whether the baseline's design choices are the right ones for this workload, we need head-to-head measurements against deliberate variants. Three commonly-debated knobs are worth measuring first, each isolating one design variable while holding everything else (seed shape, query mix, parameter bindings) constant. The harness already supports this: it discovers any directory under `scenarios/` containing an `init.sql`, runs the same seed and the same query set, and writes per-scenario result files. So scenario authoring is purely declarative — three new directories on disk.

## Goals / Non-Goals

**Goals:**

- Make three table-design variables independently measurable against the baseline using the existing harness, seeder, and query set.
- Keep each variant a faithful one-variable delta from baseline. A reviewer must be able to read each `init.sql` and immediately see what changed and what didn't.
- Reuse the baseline query set unchanged where possible. Only `data-as-json` needs query rewrites (its column type changes how `JSON_VALUE` works); the other two scenarios run baseline queries verbatim.
- Result files for the new scenarios MUST be shape-compatible with baseline results so cross-scenario comparison is a `jq` away.

**Non-Goals:**

- Multi-variable scenarios (e.g. "JSON + projections combined"). Each variant tests one knob. Combining knobs is a future change once we know which individual variants matter.
- Changes to the harness, seeder, or scenario-format spec. Pure additive scenario authoring.
- Cluster (multi-node) testing. Single-node single-shard runs against the devenv ClickHouse, same as baseline. The kind+operator change is the upstream piece for clustered runs.
- Performance verdicts in this change. We add the scenarios and run them; analysis of which variant wins is a separate, post-measurement activity.
- Backporting JSON-type behavior or DESC ordering into the baseline. The baseline stays a verbatim OpenMeter reproduction; these are exploratory siblings.

## Decisions

**Decision: One combined change adds three scenarios, not one change per scenario.**

The three variants are independent experiments but they share infrastructure (the same query mix, the same parameter set, the same A/B comparison protocol) and they're small (each is one `init.sql` plus the `queries/` set, mostly symlinked or copied from baseline). Bundling them in one change keeps the proposal/design/tasks ceremony to one set of files instead of three near-identical sets. We split if any variant grows scope (e.g. needs its own seeder).

*Alternative considered:* three separate changes. Rejected — would triplicate the proposal/design/tasks files for no useful isolation; the scenarios don't depend on each other and don't conflict on disk.

**Decision: `data-as-json` uses ClickHouse's native `JSON` type (24.8+), not `Object('JSON')` (the experimental predecessor).**

ClickHouse promoted `JSON` from experimental to stable in 24.8. The devenv ships 26.x. `Object('JSON')` is the older form and is deprecated. Using the stable `JSON` type means the scenario is forward-compatible with the cluster targets the upstream `kind-clickhouse-operator-cluster` change will introduce.

*Alternative considered:* `String` + materialized columns (e.g., `data_value Float64 MATERIALIZED toFloat64OrNull(JSON_VALUE(data, '$.value'))`). Rejected for this change — it's a third distinct variant ("denormalize hot fields into typed columns") and deserves its own scenario rather than being conflated with the JSON-type experiment.

**Decision: `data-as-json` rewrites queries; `time-desc` and `with-projections` reuse baseline queries verbatim.**

`JSON_VALUE(data, '$.value')` against a `JSON` column does not work the same way as against a `String`. The native JSON type exposes dot-path access (`data.value` returns a typed value directly). So `data-as-json/queries/` needs its own rewritten query files. By contrast, changing the ORDER BY direction or adding projections doesn't change the query surface — the planner picks projections automatically when applicable, and the ORDER BY direction is transparent to the query. So `time-desc` and `with-projections` reuse baseline queries by copying the same `.sql` files. (Symlinks would tempt drift; explicit copies make each scenario fully self-contained on disk, matching how `baseline-openmeter` is structured.)

*Alternative considered:* extract baseline queries into a shared `scenarios/_queries/` directory that variants symlink to. Rejected — the harness's discovery rule explicitly ignores `_`-prefixed directories, which is correct for a benchmark repo where each scenario should be readable in isolation. The cost of duplicated `.sql` files is small; the clarity benefit is large.

**Decision: `time-desc` flips the time sub-key direction, not the entire ORDER BY.**

The baseline ORDER BY is `(namespace, type, subject, toStartOfHour(time))`. We change only the last sub-key to `toStartOfHour(time) DESC`. Flipping `namespace`/`type`/`subject` to DESC would test something unrelated (string-key direction) and would make the variant non-comparable. Keeping the first three keys identical ensures every difference between baseline and `time-desc` results is attributable to the time direction.

*Alternative considered:* using `-time` as the sort expression. ClickHouse doesn't let you negate a DateTime in the sorting key cleanly; the explicit `DESC` modifier on the sub-key is the supported and readable form. (Note: per ClickHouse docs, sorting-key direction support varies by version and engine. If we hit a version constraint, the fallback is `toStartOfHour(-toUnixTimestamp(time))` or a precomputed inverted column — we'll address only if the straightforward DESC form fails to apply.)

**Decision: `with-projections` ships two projections — one ordered by `stored_at`, one ordered by `store_row_id` — both with `SELECT *`.**

Two projections, one per access pattern:

- `proj_by_stored_at`: `ORDER BY (stored_at)` — accelerates "what was ingested in this window" queries (debugging ingestion lag, late-arriving events).
- `proj_by_store_row_id`: `ORDER BY (store_row_id)` — accelerates point lookups by row id (debugging individual events, lineage from upstream `store_row_id`).

Both projections use `SELECT *` to keep the variant a pure access-pattern experiment; restricting the projection columns is a different knob that deserves its own scenario. The baseline `om_events` table doesn't carry these projections, so adding them measures the storage and ingest cost honestly. Existing baseline queries don't filter primarily by `stored_at` or `store_row_id`, so we don't expect query latency wins on the current query set — what we *do* expect is an ingest throughput cost and a storage size increase. Future queries (or an explicit "lookup-by-row-id" query file) can target the projections directly.

*Alternative considered:* `MATERIALIZED VIEW` instead of `PROJECTION`. Rejected — projections are part of the table definition and stay automatically in sync, which is the right ergonomic for "always-on accelerator." Materialized views require separate `SELECT` rewrites and are a heavier abstraction; they deserve their own scenario if we want to test pre-aggregated rollups.

**Decision: Each scenario is one capability with one spec file.**

The proposal lists three new capabilities (`data-as-json-scenario`, `time-desc-scenario`, `with-projections-scenario`), each corresponding to one new spec under `openspec/specs/<capability>/spec.md` at archive time. This mirrors how `baseline-openmeter-scenario` is structured. The specs are narrow: each captures the one schema change plus the constraint that the seed + query coverage match baseline, so future work can validate variants stay comparable.

## Risks / Trade-offs

- **[ClickHouse version dependency for `data-as-json`]** → Mitigation: pin the requirement in the spec (`ClickHouse ≥24.8` for the native `JSON` type). The devenv satisfies this; cluster targets need to confirm. If a target lacks `JSON`, the scenario fails fast on `init.sql` apply — the harness reports the error per-scenario and moves on; the other two variants are unaffected.
- **[Baseline-query reuse drift]** → Mitigation: tasks include a copy step from `baseline-openmeter/queries/` (the source of truth) into `time-desc/queries/` and `with-projections/queries/`. If the baseline query set evolves in a future change, a follow-up will need to re-sync the variants. We accept this drift cost as the price for self-contained scenarios. A future small change can introduce a CI guard (diff baseline `queries/` against variants) if drift becomes a problem.
- **[Projection write amplification]** → Mitigation: this is the *point* of the experiment, not a bug. The result file will show the ingest events/sec for `with-projections` vs. baseline; reviewers compare. Expect ~2-3× write cost (two extra projections each writing the same row set). Document the expectation in the spec so a "much worse" result triggers investigation rather than acceptance.
- **[DESC ordering behavior may vary by ClickHouse version]** → Mitigation: validate on the devenv (26.x) during the apply step. If the planner doesn't pick up DESC on the sub-key, fall back to a precomputed inverted column (`time_inv DateTime MATERIALIZED toDateTime(-toInt64(time))` with `ORDER BY (..., time_inv)`). Capture the fallback in the spec only if the straightforward form fails.
- **[Cross-scenario comparison fairness]** → Mitigation: run each scenario with the same `--rows`, `--seed`, `--iterations`, and `--concurrency` so the only difference between result files is the table design. Tasks include a validation step that runs all three new scenarios plus baseline in a single invocation, ensuring identical seed parameters across all four.
- **[Shared single-node ClickHouse]** → Mitigation: each scenario's `init.sql` uses `CREATE TABLE IF NOT EXISTS om_events ...` against the default database, so two scenarios cannot coexist on the same database simultaneously without one shadowing the other's schema. The intended workflow is to drop `om_events` between scenarios (or point each at its own database via DSN). Document this in `scenarios/README.md` if a user hits the footgun; for v1 the harness assumes operator awareness.
