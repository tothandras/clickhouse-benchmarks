# Two MVs for kong.llm_request and kong.api_request — findings

Built per directive: two AggregatingMergeTree rollups (one per known meter type),
minute grain, on local `proposal_events` (10M seed). Routing = OpenMeter's resolver
sends queries for these two types to the rollup tables; all other types use the
base table unchanged.

## What was built and proven solid

- **llm_rollup** (SUM tokens): `AggregatingMergeTree`, `sumState(toUInt64OrZero(
  data.tokens))`, GROUP BY all 13 meter dims at `toStartOfMinute` grain. MV +
  explicit backfill (not POPULATE).
- **Correctness verified EXACTLY (billing-critical):** `sumMerge(llm_rollup)` =
  base `sum` = **3,005,145,740** tokens, exact integer match. The rollup is
  arithmetically correct.
- Routing demonstrated: query the rollup with `sumMerge` instead of the base
  `sum(JSON_VALUE)`.

## The grain/dimensionality trap (why an earlier "17×" was wrong)

SUM/COUNT roll *up* (minute→hour→day) but never *down*, so the rollup must be at
the **finest grain any meter query uses** (minute — OpenMeter emits minute/hour/
day/month). An earlier "17× compression" for llm was measured at **hour grain with
only 2 dims (model, provider)** — not representative. The real meter groups by 13
dims at minute grain.

## ⚠️ The seed CANNOT tell us llm's real compressibility (measured)

At the real dim set, the local rollup is **1× (1.5M events → 1.5M rollup rows)** —
no compression. **But this is a seed artifact, not a finding.** Per-dimension
cardinality of the seed's `llm_request`:

| dim | distinct values | realistic? |
|-----|----------------:|-----------|
| model | 4 | ✅ low-card (real) |
| provider | 3 | ✅ low-card (real) |
| http_status | 3 | ✅ low-card (real) |
| route_id | **1.50M** (= event count) | ❌ seed makes it unique-per-event |
| service_id | **1.50M** | ❌ |
| ai_plugin_id | **1.50M** | ❌ |
| control_plane_id | **1.50M** | ❌ |

The seed generates `route_id`/`service_id`/`ai_plugin_id`/`control_plane_id` as
independent random hex — one distinct value per event — so the group-by
combination is unique per row → 1×. **In real Kong data these are bounded and
correlated:** a namespace has a few control planes, dozens–thousands of routes/
services, a handful of configured plugins, and a given subject hits one small set
of endpoints. So real `(subject, minute, dims)` combos are far below event count,
and a high-volume consumer collapses heavily. **llm compressibility is
indeterminate on this seed — it could be substantial in production.**

## Per-meter verdict (they diverge — this is the key result)

- **kong.api_request (COUNT): MV is useless, and this is ROBUST.** Measured 1×
  (2.5M → 2.5M). Its dims (`client_ip`, `request_uri`, `request_user_agent`) are
  genuinely near-unique **in production too**, not just in the seed. A rollup
  stores ~one row per event = duplicates the type for zero read benefit. In prod,
  "API Gateway requests" is plausibly a top-volume type → large ingest + storage
  cost for nothing. **Recommendation: leave api_request on the base table.** The
  sort key already prunes its per-subject windowed query to 1–2 granules.

- **kong.llm_request (SUM): MV may well be worth it — INDETERMINATE on this seed.**
  Its real group dims are mostly low-card (model, provider, http_status) plus
  bounded IDs in real data. The 1× here is purely the seed's manufactured ID
  uniqueness. **This is the #1 thing to measure on prod** (or with a
  realistic-cardinality seed). If real `(subject, minute, model, provider,
  route, service, …)` combos are ≪ event count, the rollup compresses and the MV
  is a real win; `sumState` keeps it billing-exact.

## MV vs projection (both were tested)

The GROUP-BY-scoped **projection** (PROJECTIONS-CORRECTED-FINDINGS.md) does llm
*transparently* (auto-routes, identical SQL, dedup-safe by construction). The
**MV** built here is the explicit, non-transparent alternative (resolver routes
the 2 types; reintroduces the dedup-ordering gate). Both avoid duplicating *all*
events. Pick by: transparency (projection) vs ingest-scoping to only the 2 types
(MV). Both share the same open question — does llm actually compress at prod
dim-cardinality.

## Dedup gate (production note, not a benchmark blocker)

An incremental MV bakes in any duplicate `(source,id)` that reaches ClickHouse
beyond OpenMeter's ingest-side dedup window → double-count. The MV must consume the
*deduped* stream (the sanctioned single meter-agnostic dedup/routing MV → pre-agg
downstream). The seed and prod copy are already deduped (`(source,id)` unique), so
this doesn't affect the benchmark — it's a prod-pipeline requirement.

## Bottom line

- Both MVs build and are arithmetically exact (llm sum verified to the token).
- **api_request: don't build it in prod** — intrinsically 1×, pure cost.
- **llm_request: promising but unproven** — the seed's synthetic ID cardinality
  hides the real answer. Measure on prod before deciding. Do not conclude "MVs
  don't help" from this seed — that conclusion is a synthetic-data artifact for
  llm.
