> **⚠️ CORRECTION (see PROJECTIONS-CORRECTED-FINDINGS.md):** This document's
> claim that a WHERE filter on subject/time *inherently* defeats aggregate-
> projection routing is **WRONG**. Per ClickHouse issue #33678/#33587, the real
> rule is that every WHERE/GROUP-BY column must EXIST in the projection; the
> failing tests here used `tumbleStart(time)` only, so the raw-`time` WHERE had
> no column to bind. A namespace-leading windowed projection DOES route and prune
> (measured: 1 granule vs base's 2) — but the win is small because the base sort
> key already prunes to ~1-2 granules. See the corrected report for the full
> tension (route-vs-compress) and verdict.

# Can projections accelerate the two known-schema meters? (empirical, local 10M)

**Question:** for `type IN ('kong.api_request','kong.llm_request')` where we know
the JSON paths/types, can a **projection** transparently speed up queries while
keeping the query interface identical for both known and unknown meters?

**Tested on the local seeded cluster** (CH 26.2.5.45, `proposal_events`, 10M rows,
seed types `llm_request` (SUM/tokens) and `kong_api_request` (COUNT)). The prod
cloud cluster was unreachable (idle), so numbers are from the local seed; the
qualitative findings are structural and version-specific to 26.2.

## Verdict: projections do NOT work for the real meter query shape

The appeal was real — a projection lives *inside* the table, so the optimizer
routes transparently and the query interface never changes (unknown meters fall
through to the base table). It's also **dedup-consistent by construction**
(recomputed from committed rows), unlike an MV. But the matching constraints kill
it for OpenMeter's actual queries.

### What was measured (each line = run + `system.query_log.projections`)

| Query predicate (type=llm_request, +…) | Routed to projection? | rows read |
|----------------------------------------|:---------------------:|----------:|
| *(no extra predicate)* — bare aggregation | ✅ yes | 575K (proj) |
| `+ subject = 'x'` (single equality) | ❌ no | full scan |
| `+ subject IN (…literal list…)` | ❌ no | full scan |
| `+ time >= … AND time < …` (window range) | ❌ no | full scan |
| `+ subject` even when subject is in the projection GROUP BY prefix | ❌ no | full scan |

**Every real OpenMeter meter query carries a `time` range AND a `subject` filter.**
Both independently prevent the aggregate projection from being selected. So the
projection routes only for a bare full-population roll-up — which no meter query
is. **Not viable as a transparent accelerator here.**

### Secondary constraints found along the way (each cost a real debugging cycle)

1. **No `WHERE` in projection definitions.** Can't scope a projection to "only
   these two types" — `ALTER … ADD PROJECTION … WHERE …` is a syntax error. You
   put `type` in the GROUP BY instead; non-kong rows collapse cheaply.
2. **Exact-expression matching.** A projection over `toFloat64OrZero(toString(
   data.tokens))` only routes if the query emits the **byte-identical**
   expression. A query using a different cast/extraction silently full-scans. JSON
   subcolumn extraction matched only when query and projection were textually
   identical.
3. **Window function must match exactly too.** `tumbleStart(…)` in the query
   matched only a projection also defined with `tumbleStart(…)`; mixing with
   `toStartOfHour` broke it. (Both forms are individually allowed in a projection.)

### The maintenance hazard, even if you forced a query-shape change

Matching is exact and failure is **silent**: any drift in OpenMeter's generated
SQL (an added cast, changed null-handling, a tz variant) drops back to full scan —
correct results, lost performance, no error. Plus you'd need **one projection per
distinct (window expression × timezone × window size)** because matching is exact:
hour/day/minute/month × UTC/non-UTC = many projections per meter, each rebuilt on
every insert/merge.

## The cardinality split still decides everything (measured)

Even setting routing aside, pre-aggregation only helps if it compresses:

| Meter | rows | distinct(groups × hour) | ratio |
|-------|-----:|------------------------:|------:|
| `llm_request` (SUM tokens) | 1.50M | 87.6K | **17× — would compress** |
| `kong_api_request` (COUNT) | 2.50M | 2.50M | **1× — cannot compress** |

The api_request meter has ~one group-combo per row (`client_ip`, `request_uri`,
`request_user_agent` near-unique) — pre-aggregation in any form (projection or MV)
gives **zero** benefit. (Seed-generated; confirm on prod, but the qualitative
split is intrinsic to the meter's group-by dimensions.)

## Recommendation

- **Projections: rejected** for this use case. They cannot accelerate the
  real meter query shape (time + subject filters defeat routing), and the
  exact-match + silent-deopt maintenance cost is high.
- **llm_tokens (SUM, 17× compressible):** the viable accelerator is the
  dedicated **incremental AggregatingMergeTree MV** from the prior analysis
  (KNOWN-SCHEMA-METER-OPTIMIZATION.md) — `sumState`, exact, consuming the
  *deduped* stream. The tradeoff vs projections: you query the rollup table, so
  the **interface is NOT identical** — OpenMeter's meter resolver would route
  the two known meters to the rollup. That's a routing-layer change, not a
  query-shape change, and it's the price of the win projections can't deliver
  transparently.
- **api_request (COUNT, 1× ):** no pre-aggregation helps. If anything, a
  dedicated typed-column table (cheaper per-row scan, still O(rows)).

**Interface-identical AND faster is not achievable for these meters via
projections.** You can keep the interface identical (projections) and get no
speedup on real queries, or get the speedup (MV rollup, llm only) and route the
two known meters at the resolver. The data forces that choice.

## Validation checklist (when prod cluster is reachable)

1. Re-run the routing table above on prod CH version (matching may differ by
   version).
2. Confirm the cardinality ratios on real `kong.llm_request` / `kong.api_request`
   data (seed is synthetic).
3. If pursuing the llm MV: measure `sumMerge` rollup query vs raw, and the
   per-insert MV cost.
