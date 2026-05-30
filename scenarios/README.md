# scenarios

Each subdirectory is one table-design variant for the benchmark use-case.

Current variants:

- `baseline-openmeter/` — Verbatim reproduction of OpenMeter's `om_events`. The reference.
- `data-as-json/` — Same as baseline, but `data` is declared as ClickHouse's native `JSON` type. Queries access JSON via dot-path (`toString(data.value)`) instead of `JSON_VALUE(data, '$.value')`. Requires ClickHouse ≥24.8. This is the recommended generic optimum (see the Findings section in the top-level `README.md`).
- `data-as-map/` — Same as baseline, but `data` is declared as `Map(String, String)`. Queries access fields by Map key (`data['value']`) instead of JSON parsing. Trades flexibility (no nested objects, every value is a String) for query-time simplicity (no JSON Variant overhead). The seeder detects the column type from `system.columns` and emits a flat map directly; nested payloads are explicitly disallowed (`TestSeedNoNestedPayloads`). The meter `valueProperty` JSON paths translate to keys by stripping the `$.` prefix.
- `order-by-extended-time/` — Same as `data-as-json`, but splits `PRIMARY KEY` from `ORDER BY`: PK stays the 4-column prefix ending in `toStartOfHour(time)`, ORDER BY extends with raw `time` for in-granule sort. Queries are byte-identical to `data-as-json` — the only variable is the table layout. Measures whether finer-grained intra-granule sorting (and the resulting better `time`-column compression) wins on time-range meter queries.
- `proposal/` — **The recommended default.** Combines `data JSON` + `CODEC(ZSTD(3))` + a `bloom_filter` skip index on `id`. Wins on the metrics that are stable across runs — best p50 and −43% disk; the query-CPU effect of ZSTD is run-variable (see the top-level README's Findings). Includes the full 20-query meter set plus `lookup_by_id.sql` so the bloom is exercised in the same scenario. See the top-level README's Findings section for the head-to-head numbers.
- `with-id-bloom/` — Same as `data-as-json`, plus one **`bloom_filter` skip index on `id`** (`INDEX om_events_id_bloom id TYPE bloom_filter(0.01) GRANULARITY 1`). Targets the `event_query_v2` lookup-by-id path (`WHERE namespace = … AND id = …`, no time bound), which the `(namespace, type, subject, hour)` ORDER BY cannot prune. Generic (names no meter path), format-agnostic (hashes the whole id string; `id` is user-provided with no fixed format), no row duplication. It ships only `queries/lookup_by_id.sql` — the meter-aggregation queries are byte-identical to `data-as-json` and provably unaffected by the index, so they are not duplicated here; run `data-as-json` for those. `lookup_by_id.sql` is a **functional check, not a clean latency benchmark** — `id` has no harness-bindable parameter so the query resolves a real id via an inner subquery whose scan dominates the reported wall-clock; the real signal is `EXPLAIN indexes = 1` on a *literal* id, now captured automatically into each result's `index_pruning` block. At 10M rows the bloom prunes `1226 → 18` granules (≈68×). Single-query wall-clock is near break-even on a single in-cache node (the scan is fast when cached); the payoff is multi-tenant / cold-cache / concurrent load, where the I/O reduction binds.

**Table naming.** Each scenario uses its own table name, derived from the
directory: `scenarios/<dir>/` → `<dir_with_underscores>_events` (e.g.
`data-as-json` → `data_as_json_events`, `proposal` → `proposal_events`).
Scenarios coexist in the same database without clobbering each other — no
need to drop between scenarios or point at separate databases. The
harness derives the table name from the scenario directory; init.sql,
queries, and the seeder all use the same derived name.

```
scenarios/<name>/
  init.sql          # DDL: CREATE TABLE / MATERIALIZED VIEW / PROJECTION statements
  queries/          # one .sql file per benchmark query (one SELECT each)
  seed.sql          # optional: pure-SQL data loader (use INSERT ... SELECT generateRandom() etc.)
```

The benchmark harness in `bench/` discovers scenarios by directory name. Every
direct subdirectory containing an `init.sql` is treated as a runnable scenario;
other directories are skipped with a warning.

## init.sql contract

- MUST be idempotent. Every statement uses `CREATE ... IF NOT EXISTS` so
  re-applying is a no-op. The harness applies `init.sql` on every run.
- MAY contain multiple statements separated by `;`. The harness executes them
  in order against the configured database.
- MAY use `{{database}}` for the target database name (the harness substitutes
  it from the DSN). If absent, statements use whatever default database the
  connection lands in.

## Seed contract

Scenarios that need data populated can either:

1. **Provide a `seed.sql`** for pure-SQL loaders (e.g.
   `INSERT INTO <scenario>_events SELECT * FROM generateRandom(...) LIMIT N`).
   The harness executes it once after `init.sql`. Best when the data shape can
   be expressed in pure SQL.

2. **Rely on the shared Go seeder under `bench/seed/`**. This is the default
   for scenarios that need a structured JSON payload, deterministic RNG, or
   controllable group-by cardinality (most baseline OpenMeter-shaped
   scenarios). The seeder writes events matching the OpenMeter event shape;
   the harness invokes it in-process when no `seed.sql` is present.

   The seeder emits a **weighted mix of heterogeneous event types** to model
   real OpenMeter usage, where the `data` field is user-controlled and differs
   per event type. The default catalog is: `api_request` (50%, the baseline
   type carrying `{value, group1, group2}` that the canonical meter queries
   read), `kong_api_request` (25%, HTTP request/response fields), `llm_request`
   (15%, `tokens`/`model`/`provider`), `workload` (7%, `duration_seconds`/
   `region`), and `agent_run` (3%, `agent_name`). Numeric payload fields are
   emitted as JSON strings (e.g. `"tokens":"1"`) to match real producers, so
   queries must extract them with `toDecimal128OrNull(...)` over the path's
   stringified form (any JSON-stored type, for exact billing). Selection
   is seedable, so reruns produce byte-identical data across table variants.
   Per-type queries (`llm_tokens_by_model`, `kong_status_by_route`,
   `workload_seconds_by_region`, `agent_runs_by_name`) aggregate each type's
   own fields filtered by its `type`.

A scenario MAY ship neither — the harness will then skip seeding and run
queries against whatever data is already in the table (useful when comparing
schemas against a pre-populated reference dataset).

## queries/ contract

- One `.sql` file per query. The filename without extension is the query
  identifier reported in results.
- Exactly one SELECT statement per file. Multiple statements produce an error
  and that query is skipped.
- Parameters use ClickHouse's native `{name:Type}` placeholder syntax. The
  harness binds them from a fixed default set documented below.

### Default parameter set (v1)

For v1 the harness binds a small, fixed set of parameters from compiled-in
defaults so scenarios that conform to the baseline OpenMeter event shape work
out of the box without per-scenario configuration:

| Parameter            | Type     | Default                            |
| -------------------- | -------- | ---------------------------------- |
| `{namespace:String}` | String   | `"default"`                        |
| `{type:String}`      | String   | `"api_request"`                    |
| `{from:DateTime}`    | DateTime | seeder time-span start             |
| `{to:DateTime}`      | DateTime | seeder time-span end               |
| `{subjects:Array(String)}` | Array(String) | first 10 subjects from the seeder pool |

A future scenario that needs different parameters will introduce a
per-scenario `params.json` manifest. We deliberately delay that until a second
variant forces the need, to keep v1 small.

## Discovery rules

- The harness walks `scenarios/` to depth 1 and treats every direct
  subdirectory containing `init.sql` as a scenario.
- Files starting with `_` or `.`, and the `README.md` itself, are ignored.
- A scenario can be selected explicitly via `--scenario <name>` (repeatable)
  on the harness CLI.
