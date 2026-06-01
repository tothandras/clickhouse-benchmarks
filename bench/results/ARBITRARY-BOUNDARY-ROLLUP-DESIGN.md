# Billing-exact rollups under ARBITRARY from/to boundaries

**Constraint (from the user):** total-period meter queries use arbitrary
`time >= from AND time < to` boundaries (e.g. signup-anniversary billing at
14:37:02), NOT calendar-aligned. This rules out a plain time-bucketed rollup —
a coarse bucket straddling `from`/`to` would over/under-count → unacceptable for
billing.

## The only exact design: rollup interior + raw base-table tails (hybrid)

Decompose `[from, to)` into three disjoint, complete pieces:

```
[from, from_ceil)   ← raw base table   (head: partial first bucket)
[from_ceil, to_floor) ← rollup sumMerge (interior: whole buckets)
[to_floor, to)      ← raw base table   (tail: partial last bucket)
```
where `from_ceil = ceil(from to grain)`, `to_floor = floor(to to grain)`.
**If `from_ceil >= to_floor`** (no whole bucket between them): skip the rollup,
do a single raw read over `[from, to)`.

The rollup is **dims-free**: `(namespace, type, subject, window_start) →
sumState/countState`. No high-card dims (that was the earlier 1× mistake) —
the total-period query groups by `subject` only.

## Correctness — VERIFIED EXACT on all boundary edge cases (local seed)

Tested hybrid vs base `sum`, hour-grain rollup, llm_request tokens. **All match
to the unit:**

| case | base = hybrid | result |
|------|--------------:|--------|
| mid-mid (normal) | 1,688,166,180 | ✅ MATCH |
| from on bucket boundary | 1,476,270,352 | ✅ MATCH |
| to on bucket boundary | 688,619,696 | ✅ MATCH |
| same bucket (interior empty) | 27,826,613 | ✅ MATCH |
| adjacent / interior empty | 27,705,271 | ✅ MATCH |
| spans exactly one boundary | 83,530,745 | ✅ MATCH |
| empty range | 0 | ✅ MATCH |
| whole dataset | 3,005,145,740 | ✅ MATCH |

**Two correctness requirements found by testing the edges (not the happy path):**
1. **Interior-empty fallback.** When no whole bucket fits, you MUST do a single
   raw read of `[from,to)`. A naive head+tail split double-counts or drops the
   middle (my first attempt dropped half a range: 13.8M vs 27.7M). Gate on
   `from_ceil < to_floor`.
2. **NULL = 0.** `sum`/`sumMerge` over an empty head/tail/interior returns NULL,
   and `NULL + x = NULL`. Wrap every part in `ifNull(..., 0)` or the boundary
   sums silently become NULL.

## Compression (interior, artifact-free) — measured on seed

Dims-free rollup, both meters:

| grain | llm (SUM) | api (COUNT) |
|-------|----------:|------------:|
| hour | 206× | 343× |
| day | 3,754× | 6,254× |

Both meters compress massively (this is the corrected verdict — api_request is
NOT 1× for the total-period query; that earlier result was from grouping by its
near-unique dims).

## Minute grain: shrinks the tails (user's refinement) — verified exact

The hybrid is **grain-generic** — the decomposition math doesn't depend on bucket
width. Verified billing-exact at **minute grain** too, across sub-minute boundary
cases (the regime the user named): sub-minute-within-one-minute, spans-one-minute-
boundary, sub-second range, mid-minute interior, on-boundary — **all match base to
the unit** (whole dataset 3,005,145,740). So a minute rollup + raw sub-minute tails
is correct.

What minute grain buys: the two raw tails shrink from "up to a full bucket of
events" to "events in one partial minute" — on the seed, avg **3.6**, p99 **8**,
max **17** events per (subject, minute). The interior covers essentially the whole
period from a pre-aggregate.

**Caveat — tail benefit is capped by the granule floor (measured reality):** a
filtered base-table read still reads ≥1 granule (~8192 rows). On the seed (3.6
events/min/subject) a sub-minute tail AND a sub-hour tail both read ~1 granule, so
minute grain buys **zero measurable tail reduction here while adding 60× more
interior rows** — the seed favors hour. Minute grain only wins when a partial-hour
slice for the queried subject spans *multiple* granules (high-frequency subjects,
≫8192 events/hour). That threshold is unmeasurable on this sparse seed — it's the
prod-density question.

**Elegant invariant:** minute compression ratio = events ÷ distinct(subject,minute)
= avg events per subject-minute = the logical tail size. Same quantity. So fine
grain pays off in compression *exactly* in the regime where tails would otherwise
be large — self-balancing, but the granule floor caps the realized tail benefit
until logical tail ≫ 8192.

## Grain has a real optimum (not "pick day")

- Coarser grain → interior compresses more, BUT the two raw tails span more events
  (a day-grain tail can read ~a full day of raw rows per boundary).
- Finer grain → smaller tails, larger interior.
- Sweet spot depends on per-subject event rate and period length. **Hour is a
  reasonable default**; measure on prod.

## The unavoidable cost: NON-TRANSPARENT (resolver codegen)

This design CANNOT be transparent. The resolver must, for the two known meters,
emit the 3-part query (compute boundaries, sumMerge interior, raw-sum two tails,
ifNull-add). That's a real change in OpenMeter's query builder — the unavoidable
consequence of "arbitrary boundaries + pre-aggregation + billing-exact." There is
no transparent variant (projections proven out three ways: raw-`time` filter
can't bind a bucketed projection).

## Dedup consistency

Interior rollup and base-table tails must share dedup semantics — both derived
from the *deduped* event stream — or the boundary arithmetic is exact over
inconsistent populations. (Seed/prod-copy are already deduped; this is a
prod-pipeline requirement.)

## Recommendation

- Build dims-free `(namespace, type, subject, window_start)` rollups at **hour
  grain** for both `kong.llm_request` (sumState tokens) and `kong.api_request`
  (countState). Both compress 100s× and the hybrid query is billing-exact for
  arbitrary boundaries (verified).
- Resolver routes these two types' total-period queries to the 3-part hybrid;
  everything else uses the base table.
- Non-transparent by necessity — accept the resolver codegen cost.

## PROD VALIDATION — measured on live data (2026, `proposal_events`, 4.59B rows)

Tested against real meter `completion` (SUM `total_tokens`, 27M events) in the
highest-volume namespace (`org_2wj4…`, 8.5M completion events). `data` is the JSON
type; native `data.total_tokens` access.

**Compression (dims-free rollup, REAL data — artifact-free):**

| meter | minute | hour | day |
|-------|-------:|-----:|----:|
| completion (SUM tokens, 27M) | 61.7× | **1,212×** | **7,853×** |
| api.request (COUNT, 3.4M, 60k subjects) | 12.5× | 45× | 55× |

This confirms the per-meter divergence the seed hid: **LLM-token meters compress
massively** (steep grain curve, 62×→1212×→7853×); the **high-subject-cardinality
COUNT meter plateaus** (45×→55×, the 60k subjects barely co-occur in a window).

**Correctness — billing-exact on real data (verified):**
- rollup total == base total: **63,497,760,180 tokens, exact.**
- Hybrid arbitrary-boundary cases all exact: mid-mid **29,273,247,088**, on-hour
  **25,924,675,038**, sub-hour ranges exact. Decomposition proven at prod scale.

**Rows-read, real scale — base vs hybrid, arbitrary 25-day range, whole namespace:**

| component | rows read |
|-----------|----------:|
| BASE (raw total-period) | **10,010,000** (766 ms, 113 MiB) |
| hybrid interior (rollup, ~25 days) | **18,740** |
| hybrid head tail (36 min raw) | 863,120 |
| hybrid tail-end (41 min raw) | 768,680 |
| **HYBRID total** | **~1,650,540 (≈6× fewer)** |

**Key real-scale findings:**
1. **The interior rollup is a massive win — 18,740 rows for 25 days vs 10M raw
   (~530×).** The rollup concept is validated on real data.
2. **At HOUR grain the two sub-hour tails dominate** (~1.6M of the 1.65M hybrid
   rows) — this namespace is bursty enough that a 36-min slice holds ~860K events.
3. **This is exactly where minute grain pays off** (the user's refinement): minute
   tails would be ≤1 minute of events instead of ≤1 hour, shrinking the 1.6M tail
   cost ~60×, while the interior (minute grain still 61.7× compressed = ~440K rows
   for 25 days) stays small. **At this real volume, minute grain likely beats hour
   end-to-end** — the opposite of the sparse seed, where the granule floor made
   minute pointless. Confirms: grain optimum is density-dependent, and prod density
   favors fine grain for bursty high-volume namespaces.

Net: design validated billing-exact at 4.59B-row scale; interior compression
1212×/7853× (hour/day) for LLM tokens; hybrid ≈6× fewer rows even at hour grain,
and minute grain should push that much further by collapsing the tail cost.
