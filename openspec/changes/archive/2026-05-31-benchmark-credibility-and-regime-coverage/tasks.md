# Tasks

Tiers are independent and can land as separate changes. Within a tier, order matters.

## Implementation status (2026-05-31)

24/28 implemented and verified end-to-end against a live single-node CH 26.2
(`data_as_json_events` + a seeded `with_id_bloom_events`). `go build`,
`go test` (19 tests), and `go vet` clean.

Design decisions taken (the proposal's two open questions):
- **Cold-cache = paired axis** (`--cold-paired`): each query is measured warm
  AND cold in one run; `cache_state` is recorded per `BenchResult`. (No bare
  `--cold`; the paired form is strictly more useful and the schema carries it.)
- **`--repeat` reuses seeded data** (no re-seed between repeats): captures
  query-time variance. Surfaced as a median + min/max p50 "Run-to-run spread"
  table in the report.

Verified facts worth keeping:
- EXPLAIN capture: against `with_id_bloom_events`, the harness recorded
  `granules 26 → 1`, matching a hand-run `EXPLAIN indexes=1` (bloom `Granules:
  1/26`). The keep-last parser is correct (indexes apply sequentially; last
  `Granules:` line = final survivors).
- `compare` reads the OLD scalar-`concurrency` committed result files via
  `IntList.UnmarshalJSON` (scalar-or-array). Confirmed against the committed
  `proposal` result.
- Mixed-value storage: a `--mixed-value` reseed proved the correctness claim on
  the `$.value` path — `toString(data.value)` reads all 24,820 api_request rows,
  `data.value.:Float64` reads only 8,304 (the ⅓ JSON-number share). This is the
  empirical basis for the whole "type-agnostic extraction" headline.

Cross-regime note (document, don't fix): under multi-namespace seeding,
`granules_without` becomes the *namespace-pruned* PK count, so the pruning ratio
reads "PK-pruned → bloom" (the bloom's marginal benefit) rather than the
single-namespace "total → bloom". That is the intended multi-tenant measurement.

The 5 remaining tasks are NOT apply-time code edits:
- **6.2** — re-run committed results from a clean tree (needs a full 10M sweep).
- **9.1 / 9.2** — CI smoke + DDL drift check (lowest priority; deferred).
- **10.2** — verify mixed-storage divergence (needs a re-seed with `--mixed-value`).
- **10.3** — apply the `data-as-json-scenario` MODIFIED delta; this happens at
  `openspec archive`, NOT by hand-editing the base spec now.

## Tier 1 — Exercise the claimed regime (highest priority)

### 1. Cold-cache measurement axis
- [x] 1.1 Add a `--cold` flag to `bench/cmd/bench/main.go`. When set, append `enable_filesystem_cache = 0` to each query's SETTINGS (merge with any existing SETTINGS clause, reusing `withLogComment`'s merge logic in `bench/runner/benchmark.go`).
- [x] 1.2 Record the cache state per query in the result file (`cache_state: "warm" | "cold"` on `BenchResult`, `bench/runner/results.go`). Default `"warm"` so existing files remain valid.
- [x] 1.3 Optionally run a paired warm+cold pass per query in one invocation (`--cold-paired`); decide schema per the proposal's open question before implementing.

### 2. Concurrency sweep
- [x] 2.1 Accept a comma-list for `--concurrency` (e.g. `1,8,16`) in `main.go`; default stays `1`.
- [x] 2.2 Loop `Bench` over each concurrency level; tag each result with its level (already a field — ensure it's populated per level, not per run).
- [x] 2.3 Report each level in the console line and the markdown report (`bench/runner/report.go` gains a concurrency column or per-level sub-tables).

### 3. Multi-namespace / multi-tenant seed
- [x] 3.1 Add `Namespaces int` to `seed.Config` (default 1 — current behavior). When >1, assign each row a namespace deterministically from `(Seed, idx)`.
- [x] 3.2 Bind a representative namespace in `defaultParams` (`bench/cmd/bench/main.go`) when the seed used >1, so queries hit a realistically diluted table.
- [x] 3.3 Add a seeder test asserting namespace distribution is deterministic and matches the requested count.

### 4. Capture lookup_by_id EXPLAIN signal
- [x] 4.1 In the harness, after seeding, resolve a literal id from the table and run `EXPLAIN indexes = 1 SELECT ... WHERE namespace = ? AND id = '<literal>'`.
- [x] 4.2 Parse granules/parts before (with `use_skip_indexes = 0`) and after (default) from the EXPLAIN output; record `granules_scanned` / `granules_pruned` into the `lookup_by_id` result entry.
- [x] 4.3 Render the pruning ratio in the markdown report so the bloom's evidence is captured, not prose. Keep the existing wall-clock caveat.

## Tier 2 — Correctness faithfulness + reproducibility

### 5. Seeder type-heterogeneity for `value`
- [x] 5.1 In `buildBaseline` (`bench/seed/seed.go`), emit `value` in mixed JSON storage under the single `$.value` path: a deterministic split of JSON number / JSON-stringified number / bigint-that-overflows-Float64. Keep it seed-deterministic.
- [x] 5.2 Gate it behind a config flag (e.g. `Config.MixedValueStorage`) defaulting to the current uniform-Float64 distribution, so existing committed results stay comparable until intentionally re-run.
- [x] 5.3 Add a seeder test asserting all three storage forms appear and that `toDecimal128OrNull(toString(value))` reads every form while `.:Float64` reads NULL on the string/bigint rows.

### 6. Stop committing dirty-tree results
- [x] 6.1 Add `--require-clean` to `main.go`: if `HarnessCommit()` returns a `-dirty` suffix, refuse to run (or require `--allow-dirty`). At minimum, print a loud warning before writing results.
- [ ] 6.2 Re-run the committed `bench/results/*` from a clean commit and replace the `-dirty` artifacts; update README numbers if they shift.

### 7. Run-to-run variance
- [x] 7.1 Add `--repeat N` to `main.go`: run each scenario N times.
- [x] 7.2 Aggregate across repeats (median-of-medians + spread) into the result file or a sibling summary; decide whether repeats re-seed (proposal open question).
- [x] 7.3 Surface the spread in the report so a delta can be judged against noise.

## Tier 3 — Comparison + CI convenience

### 8. `compare` subcommand
- [x] 8.1 Add `bench compare <scenarioA> <scenarioB>` (or `--baseline <dir>`) that reads the latest JSON from each scenario's results dir.
- [x] 8.2 Emit the per-query delta table (p50 Δ, CPU Δ, ingest Δ) the README currently hand-maintains — same columns, generated.
- [x] 8.3 Add a test on two fixture result files asserting the delta math and table shape.

### 9. Optional CI smoke run
- [ ] 9.1 A CI job running all scenarios at `--rows 10_000 --iterations 1` against an ephemeral ClickHouse, asserting every query returns without error (catches SQL/DDL breakage).
- [ ] 9.2 (Stretch) The drift check `baseline-openmeter/init.sql` wishes for: compare the committed baseline DDL against the upstream OpenMeter template and fail on divergence.

## Tier 4 — Minor cleanup (do after Tier 2.5)

### 10. unique_count_hour type consistency
- [x] 10.1 Change `uniqExact(nullIf(toString(data.value.:Float64), 'null'))` to `uniqExact(nullIf(toString(data.value), 'null'))` in `scenarios/{data-as-json,order-by-extended-time,proposal}/queries/unique_count_hour.sql`. (Verified via MCP: new form returns `uniqExact = 49883`, identical to the `.:Float64` form on current uniform-Float64 data.)
- [x] 10.2 With Tier 2.5's mixed-storage data, confirm the new form reads the string/bigint `value` rows the `.:Float64` form dropped; record the divergence as the justification (not a silent change). (Verified via MCP on a `--mixed-value` reseed of `data_as_json_events`: over 24,820 `api_request` rows, `toString(data.value)` and `toDecimal128OrNull(toString(...))` read **all 24,820**, while `data.value.:Float64` reads only **8,304** — exactly the ⅓ JSON-number share; `uniqExact` likewise 17,551 vs 8,304. The `.:Float64` form silently undercounts a billing sum by ⅔.)
- [ ] 10.3 Reconcile the `data-as-json-scenario` base spec: `openspec/specs/data-as-json-scenario/spec.md` still mandates the typed `.:Float64` accessor and a frozen baseline wrapper chain, which the numeric queries already diverge from (they moved to untyped `toString(...)` for the correctness fix). The MODIFIED delta in this change updates that requirement to mandate the untyped-root form for *all* meter queries — apply it so the spec matches the shipped queries.
