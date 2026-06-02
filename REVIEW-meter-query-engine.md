# Review: Meter Query Engine proposal

Reviewing the "Meter Query Engines" proposal (the experimentation-framework /
[PR #3764](https://github.com/openmeterio/openmeter/pull/3764) direction) — an
async per-meter overlay that leaves `om_events` frozen and accelerates meter
queries from purpose-built side tables, with a streaming-query fallback.

**Bottom line.** Sound direction, well risk-engineered as an *experiment*. The
lifecycle + fallback + consistency-check design genuinely neutralizes the
materialized-view-rebuild fear that has historically blocked this team. But the
headline — **"50% savings on CH costs"** — is a *query-side* number presented as
a *total-cost* number. CH cost = ingest + storage + query; this proposal's cost
accounting omits the write side, and the write side is where this design's
characteristic risk lives. The verdict should be: **build it as a de-risked
experiment; do not claim the cost savings until write-side amplification is
measured at production meters-per-event.**

This repo independently reproduced the *query* win (see below), so the query
claim is credible. The review's value is the cost-accounting gap and the
operational assessment, not new benchmarks.

---

## What's genuinely good

These are real strengths and should be credited plainly:

- **The lifecycle neutralizes the MV-rebuild fear.** `created → reconciling →
  active → failed → deleting`, plus a streaming-query fallback whenever an
  instance isn't `active`, plus a 0.01% dual-query consistency check
  (`stored_at < now()−5min`) that flips an instance to `failed` on divergence.
  This is the right shape of risk engineering. The thing that has historically
  blocked experimentation here — "rebuilding a materialized view is a pain, and
  if it's wrong we've corrupted billing" — is directly addressed: a bad engine
  degrades to the known-correct streaming path instead of returning wrong
  numbers. That is the proposal's best idea.

- **`om_events` stays the source of truth.** Always written, always queryable,
  always the fallback. The experiment cannot break the system of record. This is
  what makes the whole thing safe to try, and it's the correct call given the
  stated fear/overhead.

- **Decimal128 ≈ Float64 at ~1–2%.** This validates exact-billing arithmetic at
  essentially zero query cost — a useful, non-obvious result that removes a
  long-standing tension (exact decimal vs. performance). Worth keeping
  regardless of whether the rest ships.

- **The query wins are real and independently reproduced.** This repo benched a
  per-meter overlay shape (native pre-cast `value Decimal128` +
  `group_by_filters Map(LowCardinality(String), String)`, meter-scoped ORDER BY)
  head-to-head against the generic raw JSON table on a fresh single-node CH,
  10 iterations, seeded: **−58% query CPU, −81% memory, ~2× fewer rows/bytes
  read**. That *reproduces and exceeds* the PR's synthetic ~50%/~50% claim. The
  biggest win is on the decimal path (−71%), because the pre-cast column erases
  the per-row JSON→Decimal128 cast that is the single most expensive thing a
  query-time design does. So: the query-side mechanism works, and the magnitude
  is defensible.

- **Honest about its own benchmarks.** The proposal flags that numbers are
  synthetic, local, single-node, and that the `SELECT *` comparison is a full
  scan. That honesty is the right posture and should be preserved.

---

## The load-bearing gap: total-cost accounting

> "This should result in at least 50% savings on CH costs."

This is the claim to scrutinize, because it's the justification for the whole
effort. It does not hold as stated, for a structural reason.

**ClickHouse cost has three components: ingest, storage, query.** All three
appear on the bill. The benchmarks measure *query* (time, reads, memory). The
proposal's cost section nets in nothing else. So "50% savings on CH costs" is
really "≈50% savings on the *query portion* of CH cost" — and the query portion
is not the whole bill.

**Why the omission matters here specifically:** this design's write path is its
characteristic cost, and it scales the wrong way.

A single CloudEvent of type `T` feeds *every meter whose event type is `T`*.
Each meter is a separate `query_engine_id` with its own side table (the table is
keyed/ordered by `query_engine_id`, and a table has one native `value` column —
so it physically cannot serve two meters that read different paths from the same
row). Therefore one ingested event of type `T` becomes **N overlay rows**, where
N = number of active meters on type `T`. Each of those N rows re-stores
`subject`, `time`, `stored_at`, a freshly extracted `value`, and its own copy of
the `group_by_filters` map.

So **overlay write volume and storage scale with `Σ(active meters per event
type)`, not with event count.** This is precisely the per-meter fan-out that the
raw-table design rules exist to avoid — except it's been moved off the raw table
(legitimately, since `om_events` is frozen and this is async) onto a new
subsystem, where it's now unaccounted for.

To be fair: because each event type typically has only a *few* meters (a
`request` event might feed COUNT + SUM(duration) + SUM(bytes) → N≈3), this is
not catastrophic. But "a few×" write/storage amplification against a claimed
"50% query savings" is exactly the trade that has to be netted out before the
cost headline is earned. The honest framing is a **sizing question**, not a
verdict of doom:

> What is the average (and p99) number of active meters per event type in
> production? At that N, what is the overlay's net effect on total CH cost once
> ingest CPU and incremental storage are added to the query savings?

Until that's answered with production-shaped numbers, the defensible claim is
"**−50% on query CPU/memory/reads**," not "−50% on CH costs." Those are
different sentences and only one of them is supported.

(Storage detail worth measuring: the `group_by_filters` map is duplicated on
every overlay row, and `Map(LowCardinality(String), String)` keys help but the
values don't compress as well as a sorted native column. The proposal's own
`SELECT *` comparison shows 142 MB vs 502 MB for the overlay *per meter* — but
that's one meter's slice, not the sum across all meters on that event stream.)

---

## Specific items

### "A single materialized view on all of the meters"

> "this allows us to use a single materialized view on all of the meters as the
> data is now organized in a way that is aligned with the materialized view's
> capabilities."

This sentence conflates two mechanisms and undersells the actual one. The real
mechanism described in the Ingest section is **not** a materialized view — it's
an async **query-engine worker** consuming `om_sys.ingest_events` from Kafka and
issuing per-`query_engine_id` inserts. That's good (a worker is far more
flexible and debuggable than an MV, and sidesteps the per-insert-block MV
fan-out that collapses ingest). But it means the data is **N rows per event**
written by application code, not "one MV." The "single MV" framing hides the
fan-out discussed above. Recommend dropping the MV phrasing from the proposal —
it's both inaccurate and it's the phrasing that makes the cost look free.

(The Dashboarding section's "2–3 MVs per side table for hourly/daily rollups" is
a *different* and legitimate use of MVs — a handful per single-meter table, not
thousands fanning off the raw table. That one is fine.)

### "What about duplicates? We can just use insert dedupe."

This is known to be insufficient. ClickHouse's `insert_deduplicate` catches only
**identical block re-insertion** (the same INSERT replayed) — it does not catch
two *logically equal* events (same `(source, id)`) arriving in **different**
batches, which is the real duplicate mode OpenMeter dedups against. OpenMeter's
own dedup is ingest-side, keep-first, windowed on `(namespace, source, id)` in
Redis precisely because CH's primitives can't do this synchronously. Since the
overlay worker consumes from `om_sys.ingest_events` (which the proposal states is
*already* the deduped stream out of the sink worker), this may be moot — but then
the answer is "the upstream stream is already deduped," not "we use insert
dedupe." The casual answer should be replaced with the real one, because if the
overlay ever consumes a non-deduped stream, `insert_deduplicate` will silently
let cross-batch duplicates double-count.

### Backfill throughput is unsized

The Create flow backfills history in daily chunks, **explicitly non-parallel**
"to make sure we are not DoSing ClickHouse," at ~10 min per instance. That's a
fine default for one instance. But the system has thousands of meters, and every
*new* meter (or every engine code change that forces a rebuild) creates a new
instance that needs a serial backfill. The throughput of "create a new
query-engine instance" under that serialization is unstated. Concretely: if a
deploy invalidates engine logic and all instances go `failed → deleting →
rebuild`, what's the wall-clock to bring the fleet back to `active`, and what's
the CH load during it? During that window everything correctly falls back to
streaming queries (good — that's the safety net working), but that's also the
window where the slow/DoS-prone path the proposal exists to escape is carrying
100% of load. Worth sizing.

### fd-exhaustion is the load-bearing motivation and it's unmeasured

Peter Marton's native-JSON file-descriptor-exhaustion concern is doing a lot of
work in this proposal. It's the implicit reason *not* to take the simpler path —
migrating `om_events` to `data JSON CODEC(ZSTD(3))`, which this repo measured at
−42% p50 / −44.5% query CPU / −31% disk with 30/30 value parity, on the *raw*
table, no new subsystem, no write amplification. The overlay sidesteps fd
exhaustion (native typed columns + Map, no JSON column), so the concern cuts
*for* the overlay and *against* the simpler JSON migration. But the concern is
**asserted, not measured**. Before committing to the heavier overlay subsystem
on these grounds, the fd-exhaustion risk on `data JSON` should be reproduced (or
not) under production-shaped concurrency. If it's not real, a one-column raw-table
migration gets most of the query win with none of the write amplification,
backfill machinery, or lifecycle bookkeeping. This is the single most important
thing to verify, because it decides whether the new subsystem is necessary at
all or whether a far cheaper migration would do.

### Consistency check: 0.01% sample, asymmetric failure cost

The dual-query consistency check (0.01% of queries, both engines, `stored_at <
now()−5min`, flip to `failed` on mismatch) is good. Two refinements:

- **0.01% is a low sample for billing.** A bug that affects 1-in-10,000 query
  *shapes* (e.g. a specific groupBy combination, a specific value type) could
  take a very long time to be sampled, during which the engine serves wrong
  billing numbers for the affected queries. Consider biasing the sample toward
  query diversity (new meter, new groupBy set) rather than uniform random, and
  consider a one-time full-parity gate before an instance first goes `active`.
- **The check compares two live queries** — it catches *divergence*, not
  *shared* bugs. If both engines are wrong the same way (unlikely given different
  code paths, but possible for shared upstream issues like value-type coercion),
  the check passes. The repo's own `latest_hour` finding is the cautionary tale:
  `argMax(value, time)` diverged by physical read order across two layouts — a
  *query-determinism* gap, not a data gap. The overlay's different ORDER BY
  (`query_engine_id` in the key) will resolve such ties differently from the
  streaming path, so expect the consistency check to flag tie-ordering
  differences that are not real billing differences. Use a deterministic
  tiebreaker (`argMax(value, (time, store_row_id))`) on *both* paths, or the
  check will produce false `failed` flips on legitimately-equal data.

### Async insert latency / "store_row_id"

`store_row_id` is generated per overlay row (`generateUUIDv4()` in the backfill
draft). Note that for the dual-write determinism above and for any future
exactly-once story, the overlay's `store_row_id` will *not* equal `om_events`'
`store_row_id` for the same logical event (different generation site), so it
can't be used to correlate an overlay row back to its source event. If that
correlation is ever needed (debugging a parity failure: "which event produced
this wrong overlay row?"), carry the source `id`/`store_row_id` through instead
of minting a fresh UUID.

---

## Scope notes (agreeing with the proposal's own framing)

The proposal explicitly defers ClickHouse migrations, sink-worker batch
restructuring, and entitlement/balance-worker performance. Those deferrals are
correct for an *experiment*, with one caveat: the "if async insert is too slow,
refactor sink worker to stage batches + emit batch ids" fallback (the
kafka-large-message-serde pattern) is a substantial piece of work hiding behind
an "if it turns out to be an issue." If async single-row inserts into N side
tables per event *don't* hold up under production ingest rates, the fallback
isn't a tweak — it's a sink-worker redesign. Worth a spike to know which world
we're in before committing to the timeline.

The 1–2 week "production ready with pairing" estimate is for the *PoC SUM
engine*. The framework (lifecycle state machine, backfill cron, consistency
sampler, per-meter instance management, the worker, ops tooling for the
`failed`/`deleting` transitions) is the larger and riskier build, and the
"spread rollout over a month" line acknowledges this. Keep those two numbers
clearly separated so "1–2 weeks" isn't read as the cost of the system.

---

## Recommendation

1. **Approve as an experiment**, on the strength of the fallback-based risk
   engineering and the reproduced query wins. The design cannot corrupt the
   system of record, which is the bar for trying it.
2. **Reframe the headline** from "50% CH cost savings" to "−50% query
   CPU/memory/reads; net CH cost TBD pending write-side measurement." Don't ship
   the cost claim until ingest + storage at production meters-per-event are
   netted in.
3. **Measure first, in this order:**
   - Average/p99 active meters per event type in prod → the write-amplification
     factor.
   - fd-exhaustion on `data JSON` raw-table migration → decides whether the
     whole subsystem is necessary or a one-column migration suffices.
   - Async-insert latency under production ingest into N side tables → decides
     whether the sink-worker-redesign fallback is needed.
   - Fleet backfill wall-clock under the non-parallel constraint.
4. **Fix the dedup answer** (cite the deduped upstream stream, not
   `insert_deduplicate`) and the **"single MV" framing** (it's an N-row-per-event
   worker), and add a **deterministic tiebreaker on both query paths** so the
   consistency check doesn't false-flag.

The direction is good. The cost case is not yet made.
