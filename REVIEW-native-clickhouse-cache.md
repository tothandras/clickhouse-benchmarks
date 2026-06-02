# Evaluation: could the meter query-result cache be built natively in ClickHouse?

Follow-up to [REVIEW-query-result-cache.md](REVIEW-query-result-cache.md). The
question: instead of the application-layer row cache (the one with the NaN/dedupe
bug), could equivalent caching be built directly in ClickHouse using
**refreshable materialized views** and **`system.query_log`**?

**Bottom line.** There is no clean native replica of that app-layer cache,
because its value comes from where it sits — *downstream* of query execution,
storing already-computed result rows keyed by an opaque hash. Refreshable MVs and
`system.query_log` both sit *upstream* and would force per-meter knowledge back
into the storage layer — exactly the fan-out the team has already rejected. But
the question points at something real: ClickHouse's **native query result cache**
(a third mechanism, not named in the question) *is* the right native analog for
part of this, and switching to it would erase every bug from the previous review
for free — at the cost of weaker semantics on moving time windows. The honest
answer is a split: native query cache for the simple case, refreshable MVs for
the *bounded* dashboard rollups (the "Dashboarding" section of the query-engine
proposal), and neither for the open-ended per-customer meter-query space.

---

## Why the app-layer cache works: it's downstream and schema-blind

The thing that makes the reviewed cache *generic across all meters* is its
position. It runs **after** `QueryMeter` has executed, takes the resulting rows,
and stores them keyed by `QueryParamsHash` — an opaque hash of (filters, groupBy,
window size, timezone). It never needs to know that meter X sums `$.tokens` and
meter Y counts `$.duration`. The aggregation has already happened; the cache just
remembers the answer.

That schema-blindness is the whole game, and it is precisely what neither
proposed native mechanism can reproduce, because both sit **upstream** of the
aggregation and must therefore bake the meter's aggregation function and JSON
value/groupBy path into their definition.

This is the same wall documented across this repo's investigation: OpenMeter's
`valueProperty`/`groupBy` are **per-meter JSON paths resolved at query time**, and
there are thousands of meters. Anything that pre-computes per meter at write time
collapses into per-meter fan-out.

---

## Mechanism 1: Refreshable materialized views — wrong layer

A refreshable MV re-executes a **fixed SELECT** on a schedule and overwrites (or
appends to) a target table (per `query-mv-refreshable`; CH docs). Its inputs are
fixed at `CREATE` time. That mismatches the meter-cache workload on every axis:

- **The query set is open-ended, not fixed.** Each customer queries arbitrary
  time ranges, arbitrary subjects, arbitrary groupBy subsets, arbitrary window
  sizes. A refreshable MV is one frozen query. You cannot enumerate the
  parameter space into a finite set of MVs — that's the open key space the
  app-layer hash exists to handle.

- **To make the MV's SELECT concrete, you must name the aggregation and the
  path.** `REFRESH … AS SELECT sumState(?)` requires knowing it's a SUM of
  *which* JSON path — i.e. one MV per meter. Thousands of meters → thousands of
  refreshable MVs, each re-scanning its slice of `om_events` on every refresh
  tick. That is the per-meter fan-out the team rejected, just moved from the
  insert path to a refresh schedule. (Same conclusion as the
  [query-engine overlay review](REVIEW-meter-query-engine.md): the moment storage
  must know the meter's path, write/compute cost scales with meter count, not
  event count.)

- **`REFRESH EVERY N` re-runs the whole query**; it does not incrementally
  extend a cached time range. The app cache's signature trick — "days 1–29 are
  cached, only query day 30 live, then merge" — has no refreshable-MV equivalent.
  A refresh recomputes the entire window from scratch.

**Where refreshable MVs *are* right:** a small, *bounded, known* query set — the
cross-customer dashboard rollups (the "Dashboarding" idea in the query-engine
proposal: hourly/daily aggregates over a handful of fixed shapes). There, the
query set is finite and operator-defined, so one MV per shape is fine and the
sub-millisecond reads are a real win. That's a genuinely good use — it's just a
different problem from per-customer meter queries.

### The incremental-MV variant hits the same wall

An incremental MV into an `AggregatingMergeTree` (per `query-mv-incremental`) is
the stronger native rollup pattern — it updates on insert instead of on a
schedule. But it has the *same* genericity ceiling: you can roll up `count()` or
route raw rows **generically** (no path needed), but you cannot maintain
`sumState(<arbitrary meter path>)` without naming the path at `CREATE` time. So
generic incremental rollups can accelerate COUNT-shaped meters only; everything
that reads a value path needs a per-meter MV → fan-out again. (This is also why
the only sanctioned MV on the raw table in this repo's design is a *single
meter-agnostic* dedup/routing MV — fixed cost regardless of meter count — not
per-meter aggregation.)

---

## Mechanism 2: `system.query_log` — observability, not a result store

`system.query_log` records *that* queries ran (text, duration, rows/bytes read,
memory, the normalized query hash) — it does **not** store result rows. You can't
read a prior answer out of it. So it cannot *be* the cache.

The plausible intent behind pairing it with MVs is **key discovery / cache
warming**: mine `query_log` for which normalized queries are hot and which
parameter values recur, then materialize those. That's a reasonable instinct —
but it's the hand-rolled version of a knob ClickHouse already exposes natively
(see below). And it doesn't solve the core problem: even once you *know* the hot
queries, you still need somewhere to put their results that composes partial time
ranges — which is exactly what MVs can't do. So `query_log` is at best an input
to a warming heuristic, not a building block of the cache itself.

---

## Mechanism 3 (the one the question is circling): the native query result cache

ClickHouse has a built-in **query result cache** — the actual native analog of
the app-layer cache, and the thing worth evaluating seriously. Turn it on
per-query with `use_query_cache = 1`; results are reused within a TTL
(`query_cache_ttl`, default 60s). (CH docs: *Query Cache*.)

**What it gets right for free** — it erases the entire bug surface of the
reviewed implementation:

- No NaN sentinel, no gap-fill, no `dedupeQueryRows`, no `NaN != NaN` hazard.
- No cache table to create, partition, TTL, or invalidate by namespace.
- Eviction and memory are the engine's problem (lazy eviction on pressure).
- **Schema-blind, like the app cache** — it caches the *result* of whatever
  query ran, so it's automatically generic across all meters. This is the one
  native mechanism that shares the app cache's downstream position.
- Built-in warming controls that subsume the `query_log` idea:
  `query_cache_min_query_runs` (only cache after N executions) and
  `query_cache_min_query_duration` (only cache slow queries). That's the
  "discover hot queries, then cache them" loop, native and automatic.

**Where it is genuinely weaker than the app-layer cache** — and why it's not a
drop-in:

- **It keys on the whole query AST, including the time literals.** It does **not**
  compose partial time ranges. The app cache's core trick — reuse cached days
  1–29 and only run day 30 — is impossible here: change `to` and it's a different
  AST → a full cache miss → the whole range recomputed. (The docs' phrase
  "multiple partial result blocks" is about `max_block_size` chunking of a
  *single* result, **not** partial-time-range reuse — easy to misread.)

- **Moving windows defeat it.** Metering queries are overwhelmingly
  `to = now()`-shaped. Every call has a different time bound → a different AST →
  a miss. And `now()` is non-deterministic, so the result isn't cached by default
  at all (`query_cache_nondeterministic_function_handling`). To get hits you must
  round/snap `to` to a coarse boundary at the **application** layer before
  issuing the query — at which point you're doing the app-side period-alignment
  work anyway, just feeding it to the native cache instead of a hand-rolled
  table.

- **TTL-only invalidation, no event-driven busting.** The reviewed cache
  invalidates a namespace when a late event lands (`invalidateCache`). The native
  cache only expires on TTL — so a late/backfilled event serves stale numbers for
  up to `query_cache_ttl`. For billing-grade reads that staleness window has to
  be acceptable (it may be, given the cache only ever applies to data older than
  the min-usage-age cutoff — but that's a policy call to make explicitly).

- **Per-user isolation by default** (`query_cache_share_between_users`) — fine
  here since cache scope is already per-namespace.

---

## Verdict and recommendation

There is **no single native feature that replicates the reviewed app-layer
cache**, because that cache's value is in the *partial-time-range composition +
event-driven invalidation* — application-level orchestration logic that lives
above ClickHouse. Refreshable MVs and `system.query_log` are the wrong tools for
it: MVs because they sit upstream and force per-meter fan-out, `query_log`
because it's a log, not a store.

The native **query result cache** is the closest analog and the most pragmatic
move, but it is a *coarser* cache, not an equivalent one.

Concrete recommendation, in increasing effort:

1. **Quick win — turn on the native query result cache** for the same
   SUM/COUNT/MIN/MAX subset the app cache already gated to, with `to` snapped to a
   coarse boundary (e.g. start-of-hour/day) at the app layer to create cacheable
   ASTs, plus `query_cache_min_query_runs` for warming. This recovers a real
   chunk of the benefit with **none** of the bug surface from the prior review,
   and almost no code. Measure hit rate on production-shaped traffic first — with
   moving windows it could be low, and a cache that never hits is pure overhead.

2. **For cross-customer dashboards — use refreshable (or incremental
   `AggregatingMergeTree`) MVs** for the *bounded, operator-defined* rollup
   shapes. Right tool, right place; credit the proposal's Dashboarding section
   here.

3. **If partial-range composition is the actual requirement** (cache the stable
   history, only query the fresh tail), that orchestration has to live in the
   application — that's what the reviewed code was, and ClickHouse offers no
   native substitute. If it's revived, fix the NaN/dedupe and pointer bugs from
   the prior review; don't expect a native feature to remove the need for it.

The decision hinges on one measurement: **what fraction of meter queries use a
snappable (non-moving) time window?** High → the native query cache is most of
the win for almost no work. Low → only the application-orchestrated partial-range
cache helps, and the native features can't stand in for it.
