# Optimizing the two known-schema meters (kong.llm_request, kong.api_request)

For the hundreds of meters whose JSON paths/types are unknown at schema time,
the base table must stay generic (`data JSON`) — this is the project's core
constraint. **These two meters lift that constraint:** we know the event type,
the value path, the group-by paths, and the types. That unlocks optimizations
previously rejected *only* because they assumed per-meter knowledge.

The valid pattern is a **hot-path / cold-path split**: keep the base
`proposal_events` table uniform for all meters, and *add* dedicated structures
for the two known ones. This is NOT the forbidden per-meter MV fan-out — it's
two structures total, not one-per-meter-of-hundreds.

## The strongest lever: dedicated incremental pre-aggregation (one MV each)

Both meters are the **ideal pre-aggregation case**, for a specific reason:

| Meter | Agg | Mergeable exactly? |
|-------|-----|--------------------|
| kong_konnect_llm_tokens | SUM | ✅ `sumState` — exact, integer tokens, no Float64 drift |
| kong_konnect_api_request | COUNT | ✅ `countState` — exact |

SUM and COUNT are *associative* → `AggregatingMergeTree` pre-aggregation reads
thousands of pre-rolled rows instead of billions of raw events, with **zero loss
of billing exactness**. The `uniqExact`-must-be-exact constraint that would block
pre-aggregating a UNIQUE_COUNT meter does not apply here. These are the cleanest
pre-agg candidates in the whole project.

Sketch (tokens meter; consumes the *deduped* stream — see Gate 1):

```sql
CREATE TABLE llm_tokens_rollup (
  namespace String, window_start DateTime,
  ai_plugin_id String, ai_plugin_name String, api_id String,
  api_product_id String, api_product_version_id String, application_id String,
  cache_status String, consumer_id String, control_plane_id String,
  http_status String, model String, provider String, route_id String,
  service_id String,
  tokens_sum AggregateFunction(sum, UInt64)
) ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMM(window_start)
ORDER BY (namespace, model, provider, window_start);  -- low-card group dims first

CREATE MATERIALIZED VIEW llm_tokens_mv TO llm_tokens_rollup AS
SELECT namespace, toStartOfHour(time) AS window_start,
  data.ai_plugin_id::String AS ai_plugin_id, /* … */ data.model::String AS model,
  data.provider::String AS provider, /* … */
  sumState(toUInt64OrZero(toString(data.tokens))) AS tokens_sum
FROM <deduped_events>
WHERE type = 'kong.llm_request'
GROUP BY namespace, window_start, ai_plugin_id, /* …all groupBy dims… */;
```

Query: `sumMerge(tokens_sum)` over the rollup, filtered by window — orders of
magnitude fewer rows scanned than the raw meter query.

## Two gates that decide whether this is valid (not bypassable)

**Gate 1 — Dedup ordering.** OpenMeter dedups ingest-side on `(source,id)`. An
incremental MV fires per insert block and will **double-count** any duplicate
that reaches it. So the pre-agg MV must consume the *deduped* stream, not raw
ingest. This is exactly where the single, sanctioned meter-agnostic
dedup/routing MV fits: **dedup/route first → pre-agg downstream.** With that
ordering, the two pre-agg MVs are consistent with the "no per-meter MV fan-out"
rule (whose real concern is ingest write-amplification at 1000s of meters — two
MVs don't trigger it).

**Gate 2 — groupBy cardinality decides the win, and it DIFFERS between the two
meters.** Pre-agg only compresses if `distinct(groupBy combos × window) ≪ row
count`. This is THE decision variable, and it splits the recommendation:

- **llm_tokens (SUM, ~13 dims):** dims look bounded — `model`, `provider`,
  `ai_plugin_*`, `api_*`, `route_id`, `service_id`. Likely compresses well →
  **pre-agg is the right call.**
- **api_request (COUNT, 18 dims):** includes `client_ip`, `request_uri`,
  `request_user_agent`, `request_host` — **near-unique per event.** If most
  group-by combos are unique, the rollup has ~1 row per event and pre-agg
  **barely helps** (you pay MV write cost for ~no read win). For this meter the
  better lever is likely **write-time typed extraction** (below), or
  pre-aggregating only over a *subset* of the bounded dims if the product allows.

## Fallback lever (esp. for the high-cardinality requests meter): write-time typed columns

For a known meter you can extract its paths into typed columns at write time,
in a **dedicated per-meter table** (not the generic base table — that would
pollute the hundreds of other types with null columns and break genericity):

```sql
CREATE TABLE api_request_typed (
  namespace String, time DateTime, subject String,
  api_id String, route_id String, response_http_status UInt16,
  request_method LowCardinality(String), /* …typed groupBy dims… */
) ENGINE = MergeTree ORDER BY (namespace, route_id, toStartOfHour(time));
```

This keeps the scan O(rows) but removes per-row JSON parsing and gives typed,
LowCardinality-encoded group dims → cheaper scan + smaller storage. Strictly
worse than pre-agg when pre-agg applies, but it's the right answer when group-by
cardinality is too high for pre-agg to compress (the api_request case).

## NOT recommended

- **Typed columns in the base `proposal_events` table** — mostly-null pollution
  for the hundreds of other meter types; breaks genericity for no gain over the
  native `data.x` access already measured fast.
- **Per-meter MV for all meters** — the original forbidden fan-out; this proposal
  is explicitly only the *two* known meters.
- Typed JSON path hints (`data JSON(tokens UInt64, …)`) — marginal vs native
  access; not worth per-meter schema coupling on the shared table.

## Validation checklist (run when the cluster is reachable)

The cluster was idle/unreachable when this was written. Before building anything:

1. **Volume:** `count()` for `kong.llm_request` and `kong.api_request` in the
   copied partitions. If rare/absent here, this is architecturally sound for the
   real Kong workload but not benchmarkable on this data copy.
2. **The decision variable — compression ratio per meter:**
   `distinct(groupBy combos × hour) / count()`. Low ratio → pre-agg wins; ratio
   near 1 → pre-agg won't help (use typed-table fallback). Compute separately for
   each meter; expect them to diverge (tokens low, api_request high).
3. **Confirm types:** that `data.tokens` is integer and the group paths exist as
   the schema claims, on real rows.

## Deviation note

This proposal **deviates from the project's "no materialized views" guidance.**
That guidance's reasoning is ingest write-amplification when fanning out to 1000s
of per-meter MVs. Two MVs for two known-schema meters do not trigger that, and
the user's question explicitly scopes to "the two where we know the schema." This
is a sanctioned, scale-bounded exception — flagged here, not silently taken.
