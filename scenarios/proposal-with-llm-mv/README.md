# proposal-with-llm-mv — proposal table + live materialized view for one known meter

A **separate** scenario (the generic `proposal` table is unchanged). It adds a
dedicated rollup + **live MATERIALIZED VIEW** for a single known-schema meter
(`llm_request`, SUM tokens), demonstrating the production shape the prod
backfill-only scenario could not: **incremental rollup maintenance on insert.**

Seedable (synthetic data, no customer namespaces) → lives in `scenarios/`.

## Objects (`init.sql`)

- `proposal_with_llm_mv_events` — base table, identical to the generic proposal
  schema (`MergeTree`, `data JSON CODEC(ZSTD(3))`, id bloom, hour-bucket ORDER BY).
- `llm_tokens_rollup` — `AggregatingMergeTree`, **dims-free** key
  `(namespace, subject, window_start)`, `tokens AggregateFunction(sum, UInt64)`.
- `llm_tokens_mv` — `MATERIALIZED VIEW … TO llm_tokens_rollup`, fires on every
  insert of `type='llm_request'`, `sumState(toUInt64OrZero(toString(data.tokens)))`.
- guarded one-time backfill (no-op when seeded after init; never overlaps the MV's
  forward coverage → no double-count).

## What it proves (the NEW result vs the backfill-only prod scenario)

**Incremental maintenance is billing-exact.** Prior results were all backfill
(`INSERT … SELECT … GROUP BY`). The MV is a different path: it fires per insert
block and AggregatingMergeTree merges partial `sumState`s asynchronously.

Verified on local CH:
- **Two-batch / same-window test:** 5 llm rows inserted in TWO batches into the
  same (ns, subject, hour) → MV made 2 partial rollup parts → `sumMerge` = 800 =
  base. No double-count, no drop (the naive-MV failure mode).
- **Full seeded run (1M rows, 10K/batch → 100s of MV firings):** rollup total ==
  base total == **300,040,901 tokens**, exact.

## Dims-full rollup (groups by all the meter's groupBy dims)

The rollup carries every meter `groupBy` dimension as a typed column
(`model`, `provider`, `http_status`, `route_id`, `service_id`, `ai_plugin_id`,
`control_plane_id`) so meter queries can **filter/group on them**. ORDER BY leads
with the always-present filter prefix `(namespace, subject, window_start)` then
dims low-card-first.

**Key point: the dim-filtered win does NOT depend on row compression.** Including
the high-card ID dims means the rollup is ~1 row/event on this seed (1× rows —
the synthetic route_id/service_id/ai_plugin_id are unique-per-event). It still
beats the base table because the rollup has typed dim columns (no per-row JSON
parse) and carries no `data` blob (far narrower rows scanned). Real Kong IDs are
bounded, so prod row-compression will be much better than 1×.

## Measured (local, seeded 10M rows, --iterations 10)

| query | p50 | cpu_p50 | QPS |
|-------|----:|--------:|----:|
| `api_request_count_base` (base, COUNT, no rollup) | 9.0 ms | 117.9 ms | 46.4 |
| `llm_total_base` (base, raw SUM tokens) | 23.0 ms | 353.0 ms | 25.9 |
| `llm_total_rollup` (MV-served total) | **9.0 ms** | **109.1 ms** | 42.2 |
| `llm_by_provider_filtered_base` (base, WHERE model= GROUP BY provider) | 26.0 ms | 414.6 ms | 24.0 |
| `llm_by_provider_filtered_rollup` (MV-served, dim-filtered) | **8.0 ms** | **87.6 ms** | 43.6 |

- **Dim-filtered query** (the use case the dims exist for): rollup **~3× faster
  p50, ~4.7× lower CPU** than base — even at 1× row compression.
- **Total query**: rollup ~2.5× faster, ~3× lower CPU. (Less than the dims-free
  rollup's gap, because dims-full carries 1× rows; for pure totals a dims-free
  rollup is better — the real answer may be both.)

**Correctness @ 10M (1000+ MV firings, 10K/batch), exact:**
- total: rollup re-agg == base == **3,005,145,740 tokens**.
- dim-filtered (model=claude-haiku by provider): rollup == base to the token
  (anthropic 249,667,755 / google 249,359,738 / openai 248,845,286).

Dims-full rollup rows at this seed: 1,501,796 == llm event count (1×, seed's
unique IDs). The CPU/latency win comes from typed columns + no JSON parse, not
compression.

## Run

```bash
# local CH; isolate from any cloud .env first:
unset CLICKHOUSE_HOST CLICKHOUSE_PORT CLICKHOUSE_USER CLICKHOUSE_PASSWORD CLICKHOUSE_DATABASE CLICKHOUSE_SECURE CLICKHOUSE_VERIFY CLICKHOUSE_DSN
export CLICKHOUSE_DSN="clickhouse://default:@127.0.0.1:9000/default"
bin/bench --scenarios-dir scenarios --scenario proposal-with-llm-mv --rows 1000000 --iterations 5
```

## Arbitrary (non-hour-aligned) from/to — the 3-part hybrid

A time-bucketed rollup is only directly correct when from/to land on bucket
boundaries. For ARBITRARY boundaries (real billing periods start at a signup
instant, not on the hour), `llm_total_hybrid_arbitrary.sql` splits `[from,to)`
into three disjoint, complete pieces and uses the rollup ONLY for whole buckets
fully inside the range:

```
[from, from_ceil)     raw base table   (partial first hour)
[from_ceil, to_floor) rollup sumMerge  (whole hours)
[to_floor, to)        raw base table   (partial last hour)
```

`from_ceil`/`to_floor` are computed inline from {from}/{to} (no precomputed
param). A self-guarding 4th UNION branch handles the interior-empty case (no
whole bucket between the boundaries → single raw read of [from,to)). Every slice
is `ifNull(...,0)`.

**Why it's exact:** the rollup never covers a partial bucket — the partial edges
always come from raw events, and the three ranges partition `[from,to)` exactly.
Verified on local 10M data, hybrid == raw base to the token across edge cases:

| from / to | hybrid = base |
|-----------|--------------:|
| mid-hour both ends | 1,686,335,313 |
| from on boundary | 1,823,920,059 |
| to on boundary | 862,614,378 |
| same hour (interior empty → fallback) | 21,167,933 |
| spans one boundary | 41,692,593 |
| both aligned, 2h interior | 82,795,526 |

**Measured (10M, --iterations 10):** `llm_total_hybrid_arbitrary` 16 ms p50 /
166 ms CPU vs raw base 27 ms / 427 ms — ~1.7× faster, ~2.6× lower CPU. (Slower
than the aligned `llm_total_rollup` at 8 ms because the two raw partial-hour
tails still scan the base table; that's the cost of arbitrary boundaries.)

## Scope / constraint note

This is the **sanctioned exception** to the "no per-meter MV fan-out" rule. That
rule governs PRODUCTION at 1000s of meters (per-meter MVs collapse ingest). A
dedicated MV for 1–2 known-schema meters does not trigger that — and the generic
`proposal` table remains meter-agnostic for everything else. In production the MV
must consume the **deduped** event stream (OpenMeter dedups ingest-side on
`(source,id)`); a raw-stream MV would bake in duplicates. The total-period query
served here is the aligned-boundary form (pure `sumMerge`); for arbitrary
boundaries use the 3-part hybrid from the prod-llm-rollup scenario.
