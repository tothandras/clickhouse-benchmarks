> **⚠️ CORRECTION (see PROJECTIONS-CORRECTED-FINDINGS.md):** This document's
> claim that a WHERE filter on subject/time *inherently* defeats aggregate-
> projection routing is **WRONG**. Per ClickHouse issue #33678/#33587, the real
> rule is that every WHERE/GROUP-BY column must EXIST in the projection; the
> failing tests here used `tumbleStart(time)` only, so the raw-`time` WHERE had
> no column to bind. A namespace-leading windowed projection DOES route and prune
> (measured: 1 granule vs base's 2) — but the win is small because the base sort
> key already prunes to ~1-2 granules. See the corrected report for the full
> tension (route-vs-compress) and verdict.

# Projections, extended-time ORDER BY, and the always-present keys

Tested empirically on the local seeded cluster (CH 26.2.5.45, `proposal_events`,
10M rows). Answers two questions:
1. Can we find a projection design that works, given `namespace, type, subject,
   time` are always in the meter query?
2. Should we try the extended-time ORDER BY?

## The key realization: the always-present keys are already exploited — by the SORT KEY, not projections

The user's instinct is right and **already realized**. The base table's
`ORDER BY (namespace, type, subject, toStartOfHour(time))` IS the always-present
filter columns. Measured: a real filtered meter query
(`namespace + type + subject + time range`) prunes via the primary key to:

```
PrimaryKey: type, subject, toStartOfHour(time)  →  Granules: 11 / 1225   (111× prune)
filtered base scan: 90K rows read, 8 ms, 16 MiB
```

So filtering is **already near-optimal**. There is no projection to "make work"
here — the sort key does exactly what the user wanted projections to do.

## Can projections work? No — and they'd be slower anyway

Exhaustively tested (CH 26.2; each = run + `system.query_log.projections`):

| Predicate on the projection | Routes? |
|-----------------------------|:-------:|
| `type=X` only (bare rollup) | ✅ — but reads **575K** rows |
| `+ subject=` / `+ subject IN` / `+ time range` | ❌ full scan |
| `+ subject` even with subject in the projection's GROUP BY prefix | ❌ full scan |

Two confirmed CH-26.2 limitations: (1) a raw-`time` range predicate can't be
satisfied by a projection storing only `tumbleStart(time)` — raw `time` isn't a
projection column; (2) dimension-filter routing on aggregate projections doesn't
fire here even when the dim is a GROUP BY key.

**The clincher:** the only projection that *did* route (the bare, unfiltered one)
read **575K rows** — *more* than the filtered base scan's **90K**. A projection
cannot beat what the sort key already achieves for the real (always-filtered)
meter query. So even ignoring the transparency problem, projections lose here.

(Plus the maintenance hazards from the prior writeup: no WHERE in projection
defs, byte-exact expression matching, silent deopt on SQL drift, one projection
per window×tz combination.)

## Extended-time ORDER BY: measured, and it's a wash

`PRIMARY KEY (namespace, type, subject, toStartOfHour(time))` +
`ORDER BY (…, time)` — built a 10M copy and ran the filtered meter query
head-to-head, 5 runs each:

| Table | p50 | rows read | marks |
|-------|----:|----------:|------:|
| base proposal | 8 ms | 90.1K | 11 |
| extended-time | 6 ms | 73.7K | 9 |

The **PRIMARY KEY is unchanged**, so granule pruning is essentially identical —
the extra raw `time` only reorders rows *within* granules. The 8→6 ms / 90K→74K
difference is at the noise floor and is exactly the run-to-run sign-flip the
prior analysis documented (one run wins, the next regresses). It cannot touch the
cost that actually dominates after pruning: **per-row JSON extraction + sum over
the ~90K already-pruned rows.** Consistent with the earlier verdict — not a
dependable win, and it costs ingest. Don't stack it.

## What this leaves: the one real remaining lever

After granule pruning, the dominant cost is **per-row JSON parsing on the pruned
rows.** Neither projections nor ORDER BY can reduce it (proven three ways now).
The only thing that can, for the two known-schema meters, is **pre-extracting
their paths into typed columns** — a dedicated structure for the 2 meters, routed
at the OpenMeter resolver. That is the non-transparent tradeoff already documented
in KNOWN-SCHEMA-METER-OPTIMIZATION.md and PROJECTIONS-FOR-KNOWN-METERS.md:

- **llm_tokens (SUM, 17× group compression):** typed columns OR an
  AggregatingMergeTree rollup. Both require resolver routing (interface changes
  for these 2 meters), which is the price of a win the transparent options can't
  deliver.
- **api_request (COUNT, 1× — group combos ≈ row count):** no pre-aggregation
  helps; at most typed columns for a cheaper per-row scan.

## Bottom line

- The always-present keys are already fully exploited by the sort key (90K rows,
  111× granule prune). 
- Projections can't beat that for filtered queries and aren't transparent — rejected.
- Extended-time ORDER BY is a measured wash (PK unchanged) — rejected.
- The only further win requires per-meter typed extraction + resolver routing,
  scoped to the 2 known meters — not transparent, but it's the sole lever left.
