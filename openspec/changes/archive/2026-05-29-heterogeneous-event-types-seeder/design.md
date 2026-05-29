## Context

The seeder (`bench/seed/seed.go`) emits one payload shape for every row: `{"value": <float>, "group1": <enum>, "group2": <enum>}`, with `type` chosen from a flat list `[api_request, compute_seconds]` that has no effect on the payload. The 10M sweep's verdict — `materialized-columns` wins by 3.5× query CPU — rests on that uniformity: three `MATERIALIZED` columns (`value`, `group1`, `group2`) cover 100% of queries because 100% of rows have exactly those fields.

Production OpenMeter `data` is the opposite: user-controlled, and each event `type` carries a different field-set. A `kong.llm_request` has `tokens`/`model`/`provider`; a `workload` has `duration_seconds`/`region`/`zone`. No fixed column set materializes all of them. The benchmark must reproduce that heterogeneity so the table-design ranking reflects how these designs behave when `materialized-columns` can only pre-extract fields for a subset of types.

Constraints: the `om_events` column list is frozen (faithful to upstream, `data String`); the existing 14 baseline queries and their `{value,group1,group2}` contract must keep working; RNG must stay seedable so every table variant sees byte-identical data.

## Goals / Non-Goals

**Goals:**

- Seeder emits ≥4 distinct event types, each with its own realistic JSON field-set (modeled on, but not copied from, the kong.api_request / kong.llm_request / workload / agent_run examples), plus the retained baseline type carrying `value`/`group1`/`group2`.
- Per-row type selection by a configurable **weighted** distribution (realistic skew: one or two dominant types, some rare), deterministic under a fixed seed.
- Per-type benchmark queries that aggregate each new type's real fields, filtered by that `type`, added at least to `baseline-openmeter`.
- Re-run the 10M sweep on all single-node variants under heterogeneous data and re-rank; update the analysis note.
- Reproducibility preserved: same seed ⇒ same rows ⇒ valid cross-variant comparison.

**Non-Goals:**

- Changing the `om_events` schema. `data` stays `String`; the change is in payload content, not columns.
- Adding new materialized columns to the `materialized-columns` scenario to "fix" its coverage. Its gap under heterogeneous data is the finding, not a bug to patch. (A follow-up change may explore type-aware materialization.)
- Modeling the full CloudEvents envelope (`specversion`, `source`, `id` formats). Only the `data` payload and `type` vary per the experiment; envelope columns keep their current seeder values.
- Per-type custom time/subject distributions. Type selection is independent of time/subject so the only new variable is payload shape.
- Re-running the cluster scenario. Single-node re-rank only, consistent with the prior sweep's scope.

## Decisions

**Decision: An event-type catalog of payload builders, selected by weight.**

`seed.Config` gains an `EventTypes []EventType` field where each `EventType` carries `{ Name string; Weight int; BuildData func(rng) map[string]any }`. The seeder builds a cumulative-weight table once, then per row draws `rng.IntN(totalWeight)` to pick a type and calls its `BuildData`. Default catalog ships the retained baseline type plus the four heterogeneous types. `type` written to the row is the chosen `EventType.Name`.

*Alternative considered:* a `switch` on a randomly chosen type-name string inside the row loop. Rejected — a data-driven catalog keeps payload logic colocated with the type, makes weights/fields configurable, and lets tests enumerate the catalog.

**Decision: Retain a `value`/`group1`/`group2` baseline type, give it the dominant weight.**

The existing 14 queries filter `type = {type:String}` (default `api_request`) and read `$.value`/`$.group1`/`$.group2`. To keep them meaningful, the baseline type is named `api_request` (matching the default param) and carries those exact fields, and it gets a high weight so it remains the bulk of rows — the baseline queries still scan a large, realistic population. The four new types are added alongside with lower weights.

*Alternative considered:* drop the baseline type and rewrite the 14 queries to target a new type. Rejected — the user chose "keep baseline + add per-type queries"; this preserves the reference workload as a control.

**Decision: Field values mirror the examples' shapes but are randomized, not copied.**

Each `BuildData` emits the example's keys with generated values of matching kind: `tokens` as a small int-as-string, `model`/`provider` from a small enum, `response_http_status` weighted toward `"200"`, `duration_seconds` as a float-ish string, `region`/`zone`/`instance_type` from enums, IDs as random hex. Numeric-looking fields are emitted **as JSON strings** (matching the examples, where `"tokens": "1"`, `"response_http_status": "200"`), so the queries must apply `toFloat64OrNull(JSON_VALUE(...))` exactly like the real OpenMeter path — preserving query-shape fidelity and the JSON-parse cost that the table designs compete on.

*Alternative considered:* emit numbers as JSON numbers. Rejected — the reference payloads quote them, and OpenMeter's meter queries already wrap extraction in `toFloat64OrNull`, so string-typed numerics are the faithful shape.

**Decision: Per-type queries live in `baseline-openmeter/queries/`, are byte-copied into every scenario the sweep runs.**

New queries (e.g. `llm_tokens_by_model.sql`, `kong_status_by_route.sql`, `workload_seconds_by_region.sql`, `agent_runs_by_name.sql`) are authored against the baseline JSON shape (`toFloat64OrNull(JSON_VALUE(data,'$.tokens'))`, etc.). For the `materialized-columns` scenario these queries are copied **unchanged** — they still call `JSON_VALUE`, because the new types' fields are *not* materialized. That is deliberate: it measures the realistic case where materialized columns help only the baseline type and the rest pay full JSON-parse cost. The harness binds existing params; new queries needing a non-default `{type:String}` are handled by the existing per-scenario fixed param set (the `type` literal is written into each query rather than parameterized where it must differ from the default).

*Alternative considered:* parameterize `type` per query via a new manifest. Rejected for now — the v1 harness uses a fixed compiled-in param set; embedding the literal `type` for type-specific queries avoids introducing the manifest the README explicitly defers.

**Decision: Re-sweep at the same 10M / 10-iter / seed=42 parameters, append (not overwrite) the analysis.**

The sweep command is unchanged except it now seeds heterogeneous data. The analysis note gains a second section, "Heterogeneous-data re-rank," preserving the original uniform-data ranking for contrast so the contamination effect is visible. The winner under realistic data is reported explicitly, with the per-type coverage of `materialized-columns` called out.

**Decision (discovered during apply): the `materialized-columns` `value` column must be `Nullable(Float64)`.**

Under the uniform seeder every row had `$.value`, so the non-nullable `value Float64 MATERIALIZED ifNotFinite(toFloat64OrNull(JSON_VALUE(data,'$.value')), NULL)` never produced NULL. Under the heterogeneous mix the ~50% of rows that are not `api_request` carry no `$.value` field, the expression evaluates to NULL, and the insert fails (`Cannot convert NULL value to non-Nullable type`). This is itself a finding: a fixed materialized column cannot serve event types that lack the field. The faithful fix is `value Nullable(Float64)`, which matches the baseline query's `ifNotFinite(..., NULL)` semantics exactly — `sum`/`avg`/`min`/`max` ignore NULLs — so the precomputed value equals the per-query baseline value for the rows that have it. `group1`/`group2` stay `String` (missing fields yield `''`, which inserts fine). The analysis note records that materialized columns only cover the `api_request` slice; the other types still pay full JSON-parse cost at query time.

## Risks / Trade-offs

- **[Baseline queries scan fewer matching rows once type weight is split]** → With the dominant baseline weight the `api_request` type still holds the majority of rows, keeping per-query populations large at 10M. Mitigation: set the baseline weight so `api_request` ≥ ~50% of rows; document the resulting per-type row counts in the analysis note so query CPU is interpreted against the right denominator.
- **[Heterogeneous payloads change ingest cost/throughput]** → Expected and fine; ingest is recorded, not ranked. Wider payloads (kong.api_request has ~12 fields) inflate `data` size and insert bytes. Mitigation: the result file's `ingest` block captures it; weights keep the wide types a minority.
- **[`materialized-columns` could still win if baseline type dominates enough]** → Possible — if `api_request` is 80% of rows, materialized columns cover 80% of the scan and may still rank first. That is a *legitimate* finding, not a failure; the note will report the win margin alongside the type mix so the result is honest about why. Mitigation: pick a mix realistic enough that non-materialized types are a meaningful fraction (e.g. baseline ~50%, others sharing the rest).
- **[Embedding literal `type` in per-type queries diverges from the parameterized baseline style]** → Minor inconsistency. Mitigation: documented in the query files; revisit with a `params.json` manifest if a third reason to parameterize appears (the README already names that trigger).
- **[Non-determinism if `BuildData` reads a shared RNG out of order]** → Each row draws type-pick then payload from the same `rng` in a fixed sequence; as long as the catalog order and draw order are stable, the stream is reproducible. Mitigation: a test asserts that seeding twice with the same seed produces identical rows.
- **[Reproducibility break vs. the prior sweep]** → This change alters what seed=42 produces, so new results are not comparable to the pre-change files. Mitigation: the analysis note keeps the old ranking in a clearly-labeled "uniform-data (pre-change)" section and dates both runs; the harness records `harness_commit` so each result file is traceable to the seeder version that made it.
