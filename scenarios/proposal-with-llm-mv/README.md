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

## Measured (local, seeded 1M rows, --iterations 5)

| query | p50 | cpu_p50 | QPS |
|-------|----:|--------:|----:|
| `api_request_count_base` (base, COUNT, no rollup) | 11.0 ms | 96.5 ms | 28.8 |
| `llm_total_base` (base, raw SUM tokens) | 14.0 ms | 201.5 ms | 23.4 |
| `llm_total_rollup` (MV-served) | **6.0 ms** | **29.2 ms** | **32.8** |

Rollup-served vs raw base for the same meter: **~2.3× faster p50, ~7× lower CPU.**
Compression at seed density: 150K llm events → 7.3K rollup rows (~20×). (Prod
density is far higher — measured 1212× hourly on real `completion` data.)

## Run

```bash
# local CH; isolate from any cloud .env first:
unset CLICKHOUSE_HOST CLICKHOUSE_PORT CLICKHOUSE_USER CLICKHOUSE_PASSWORD CLICKHOUSE_DATABASE CLICKHOUSE_SECURE CLICKHOUSE_VERIFY CLICKHOUSE_DSN
export CLICKHOUSE_DSN="clickhouse://default:@127.0.0.1:9000/default"
bin/bench --scenarios-dir scenarios --scenario proposal-with-llm-mv --rows 1000000 --iterations 5
```

## Scope / constraint note

This is the **sanctioned exception** to the "no per-meter MV fan-out" rule. That
rule governs PRODUCTION at 1000s of meters (per-meter MVs collapse ingest). A
dedicated MV for 1–2 known-schema meters does not trigger that — and the generic
`proposal` table remains meter-agnostic for everything else. In production the MV
must consume the **deduped** event stream (OpenMeter dedups ingest-side on
`(source,id)`); a raw-stream MV would bake in duplicates. The total-period query
served here is the aligned-boundary form (pure `sumMerge`); for arbitrary
boundaries use the 3-part hybrid from the prod-llm-rollup scenario.
