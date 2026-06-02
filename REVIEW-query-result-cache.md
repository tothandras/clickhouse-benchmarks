# Review: meter query-result cache experiment

Reviewing the meter query-result caching experiment at openmeter commit
[`ad17d09c`](https://github.com/openmeterio/openmeter/tree/ad17d09cfbcfd900714a9cf6f9b24a27b5879348/openmeter/streaming/clickhouse)
— `cachematerialize.go`, `cachemerge.go`, `cachequery.go` and their tests, plus
the orchestration in `cache.go` / `cache_util.go` and the call site in
`connector.go` (fetched to follow the wiring). The note "there was a bug
somewhere, we never shipped this" framed the read as a bug hunt.

**Bottom line.** The architecture is sound — a per-query-hash row cache, day
granularity, gap-fill, merge-back into the requested window, fall back to a live
query for uncached periods, invalidate by namespace on late events. It did not
ship because of a small number of concrete, demonstrable defects, and the test
suite was structured in a way that **could not** exercise the exact path where
they meet. The single most likely shipping-blocker is a `NaN != NaN` comparison
in the cache read-back dedupe; a pointer-keyed map is the deterministic trigger
that makes it fire. Both are fixable in a few lines. The caching idea is worth
resurrecting; the implementation needs the fixes below and tests that cover the
store→reread round-trip.

I could not see the git history or the original bug report, so I can't *prove*
which defect was the one that got it shelved. Both below are demonstrable from
the source; either is a plausible trigger. I lead with the one that fails
without any special conditions.

---

## How it's wired (so the bugs below have context)

`connector.go:214`: a query is cacheable only if `canQueryBeCached` passes. That
gate (`cache.go:121`) admits **only SUM / COUNT / MIN / MAX**, requires a `From`,
a minimum usage age, and a minimum period. Cacheable queries go through
`executeQueryWithCaching` (`cache.go:129`):

1. Fetch cached rows for the hash (`fetchCachedMeterRows` → `dedupeQueryRows`).
2. Query the live meter for the uncached head/tail periods (in parallel).
3. `materializeCacheRows` fills gap windows with a **NaN sentinel**.
4. Store new + materialized rows in the cache.
5. Strip NaN and zero rows, sort, return.

Then back in `connector.go:225`, `mergeMeterQueryRows` collapses the day-window
cached rows into the caller's requested window size (or into a single total when
`WindowSize == nil`). So merge *is* wired — one level up from the cache function,
not inside it.

The cache table (`cachequery.go:24`) is a plain `MergeTree`
`ORDER BY (namespace, hash, window_start, window_end)`, partitioned by month,
`TTL created_at + 30 DAY`. Reasonable.

---

## Bug #1 (primary): `NaN != NaN` turns a tolerated duplicate into a hard error

`cachematerialize.go:14` marks gap windows with a NaN sentinel:

```go
// We use NaN to avoid storing 0 values in the cache which impacts min and max aggregations
var cacheNoValue = math.NaN()
```

Those NaN rows are **written to the cache** (`cache.go:277-286`) and only
stripped from the *current* response by `filterOutNaNValues` (`cache.go:321`). So
the sentinel lives in the cache table and is read back on the next query.

On read-back, `fetchCachedMeterRows` immediately runs `dedupeQueryRows`
(`cache.go:393`). That function exists specifically to absorb duplicate cached
rows — the code says so:

```go
// Results can be double cached in the case of parallel queries     (cache.go:281)
// At insert time we can have duplicates for the same window due to parallel queries (cache.go:392)
```

Its tolerance rule (`cachemerge.go:115-122`):

```go
if _, ok := seen[key]; !ok {
    deduplicatedValues = append(deduplicatedValues, row)
    seen[key] = row
} else {
    if row.Value != seen[key].Value {
        return nil, fmt.Errorf("duplicate row found with different value: %s", key)
    }
}
```

A duplicate with an **equal** value is silently dropped — the intended,
harmless path. But for a materialized gap window the value is `NaN`, and in IEEE
754 **`NaN != NaN` is always true**. So two cached rows for the same gap window
take the `return nil, error` branch, and the error propagates all the way out
(`cache.go:393-395` → `cache.go:222` → the whole `QueryMeter` call fails).

This is the headline failure: the dedupe layer that was built to make parallel
double-caching *harmless* instead makes any duplicated gap window *fatal*. It
does not need the pointer bug below — it fires whenever a NaN sentinel row gets
duplicated, which the code itself says happens under normal parallel load.

**The sentinel-choice irony.** The comment at `cachematerialize.go:13` chose NaN
*over 0* precisely because a 0 "impacts min and max aggregations." But NaN is the
one value that breaks an equality-based dedupe — `0 == 0` would have deduped
cleanly. The sentinel picked to protect MIN/MAX is exactly what breaks the cache
read path. A non-NaN, out-of-band "no value" representation (a separate boolean
column / a sentinel that compares equal to itself, or filtering materialized rows
*before* they enter the dedupe set) resolves it.

**Fix:** don't compare sentinels by float equality. Either (a) exclude NaN rows
from `dedupeQueryRows` (they're filtered before return anyway, so they don't need
to survive dedupe), or (b) make the equality check NaN-aware
(`both NaN → equal`), or (c) carry "missing" as a typed flag instead of a NaN
float.

---

## Bug #2 (the trigger): pointer-keyed `subjectMap` over-enumerates subjects

`cachematerialize.go:30`:

```go
subjectMap := map[*string]struct{}{}
...
if _, ok := subjectMap[row.Subject]; !ok {   // keyed by the *pointer*, not the string
    subjectMap[row.Subject] = struct{}{}
}
```

The map key is `*string`. Two rows with the *same subject string* but different
`*string` addresses are distinct keys. And distinct addresses are exactly what
the inputs produce: cached rows come from `cacheRow.toMeterQueryRow`
(`cachequery.go:66`, `currentRow.Subject = &row.Subject` — a fresh address per
scanned row) and fresh rows come from the live `queryMeter`. So for one logical
subject appearing in N input rows, `subjectMap` holds N entries, and the gap-fill
loop (`cachematerialize.go:78-79`) emits **one NaN row per gap window per
duplicate pointer** — duplicate materialized rows for the same `(window,
subject)`.

On its own this is mostly benign: those duplicate NaN rows are stripped by
`filterOutNaNValues` before the response, so the only direct cost is cache bloat.
Its real damage is that it **deterministically manufactures the duplicate NaN
rows that trip Bug #1** on the next query. Fix Bug #1 and the system survives
this; fix only this and parallel double-caching still triggers Bug #1. So: rank
this as the trigger, not an independent correctness failure.

**Fix:** key the map by subject value (`map[string]struct{}`, with a separate
nil/empty flag), as the rest of the code already does for group keys.

---

## Why the tests didn't catch it (the useful through-line)

Each defect sits in a blind spot of the suite, and none of the tests exercises
the path where the two meet — the store→reread NaN round-trip:

- **`TestMaterializeCacheRows` reuses `&subject1` / `&subject2`** (stable
  addresses across all rows in a case), so the pointer-keyed map dedupes
  "correctly" in-test. The over-enumeration **structurally cannot manifest**
  with shared-address fixtures; it only appears with the distinct-per-row
  pointers the real scan path produces.
- **The comparison loop in that test checks `result[i]` against `want[i]` by
  index** (`cachematerialize_test.go:394`) and `assert.Fail`s on `i >= len(want)`
  — so *extra* rows are caught, but if `result` is *shorter* than `want` the tail
  is never checked. (Extra-row blindness is not the gap here; missing-row
  blindness is — worth tightening with an explicit `len` assert regardless.)
- **`TestDedupeQueryRows` only uses non-NaN values**, so the `NaN != NaN` branch
  — the actual blocker — is never exercised. The test even asserts the
  "different value → error" path, but with `20 != 10`, never with NaN.
- **`TestAggregateRowsByAggregationType` / `TestMergeMeterQueryRows` cover only
  SUM/COUNT/MIN/MAX on non-NaN inputs**, matching the gate, so a NaN reaching the
  aggregator (which `math.Min`/`math.Max`/`+=` would propagate to a NaN result)
  is never tested.
- **No test stores rows and reads them back through the cache**, which is the
  only place the sentinel survives into `dedupeQueryRows`. Both bugs are
  invisible to single-pass unit tests and only appear on the second query.

A single integration test — "query a window with a gap, then re-query an
overlapping window" — would have caught both.

---

## Secondary findings

These don't block by themselves but are real and worth fixing before a revival.

- **`value != 0` filter conflates "zero usage" with "no data"
  (`cache.go:295-297`).** Cached rows with `Value == 0` are dropped from the
  result. A window where usage is *genuinely* zero (a real, complete, billed-as-0
  window) is indistinguishable here from an absent one, so it silently vanishes
  from the response. For SUM/COUNT that's a correctness smell independent of the
  NaN bugs — a legitimately-zero day should be returned as 0, not omitted.

- **`aggregateRowsByAggregationType` has no `default` case
  (`cachemerge.go:148`).** AVG and UNIQUE_COUNT are correctly fenced out today by
  `canQueryBeCached` (`cache.go:121`), so this is *latent, correctly gated*, not a
  live bug. But the gate is the **sole** guard: if it's ever loosened, an AVG
  query falls through to `aggregatedRow.Value == 0` (the zero value) **silently**,
  not loudly. Note too that these two aggregations are unmergeable *in principle*,
  not merely unimplemented: averaging per-window averages ≠ the global average,
  and you cannot union exact distinct-counts by combining two scalars (this
  collides directly with the `uniqExact`-for-billing requirement). So the gate
  isn't a temporary limitation to lift later — it's a permanent design boundary
  for this cache shape. Make the missing case an explicit error, and document
  that AVG/UNIQUE_COUNT are structurally non-cacheable here.

- **Invalidation is whole-namespace and coarse (`cache.go:460`).** Any late event
  older than the min usage age wipes the entire namespace's cache (the code's own
  TODO at `cache.go:456` acknowledges this). Functionally safe, but on a busy
  namespace with a trickle of late events the hit rate could collapse to near
  zero — worth measuring before claiming a cache win, because a cache that's
  continually invalidated costs storage + insert traffic without returning
  reads.

---

## What's good

Credit where due — the bones are right:

- **The decomposition is clean and correct in shape:** fetch cache → query only
  the uncached head/tail in parallel → gap-fill → store → merge back to the
  requested window. The head/tail split (`cache.go:193-234`) correctly queries
  only the periods the cache doesn't cover.
- **The cacheability gate is conservative in the right ways:** min usage age (so
  fresh, still-mutating data isn't cached), min period, window alignment,
  aggregation allow-list. It refuses to cache what it can't cache correctly.
- **Incomplete-window guarding** (`cache.go:256-260`) keeps partial windows out
  of the cache — the right instinct.
- **`QueryParamsHash`** sorts every multi-valued field before hashing
  (`cache.go:27-79`), so cache keys are order-independent. Correct and
  easy to get wrong.
- **The dedupe-on-read design is the right answer** to parallel double-caching —
  it just needs to not explode on the sentinel value.

(One earlier suspicion I want to retract explicitly so it isn't re-litigated:
`cachequery.go:71` takes `&groupValue` of a range variable. That is the classic
pre-Go-1.22 loop-capture bug — but `go.mod` at this commit is **Go 1.24.1**, where
each iteration gets a fresh variable. Not a bug here.)

---

## Recommendation

The caching direction is worth resurrecting — the architecture is sound and the
gate is appropriately conservative. Before a revival:

1. **Fix the NaN sentinel handling** (Bug #1) — stop comparing "no value" by
   float equality. Prefer a typed "missing" flag over a NaN float so it never
   reaches an equality check, a `math.Min/Max`, or a `+=`.
2. **Key `subjectMap` by string value** (Bug #2), removing the duplicate-NaN
   generator.
3. **Add a store→reread integration test** (query a gapped window, then re-query
   an overlapping one) — the one test shape that exercises where the two bugs
   meet. Add an explicit `len(result) == len(want)` assert to the materialize
   test.
4. **Decide the zero-vs-missing semantics** (`value != 0` filter) — a genuinely
   zero window should be returned, not dropped.
5. **Make the missing aggregation case an explicit error** and document that
   AVG/UNIQUE_COUNT are non-cacheable by design, not pending implementation.
6. **Measure hit rate under realistic late-event traffic** before claiming a cost
   win — whole-namespace invalidation may erode it.

The exact defect that caused the shelving isn't recoverable from source alone,
but the NaN/dedupe interaction is the most probable culprit and is the first
thing to fix.
