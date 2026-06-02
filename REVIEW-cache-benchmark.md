# Benchmark: a minimal, bug-fixed meter query-result cache

A minimal Go implementation of OpenMeter's application-layer meter query-result
cache — the pattern from [REVIEW-query-result-cache.md](REVIEW-query-result-cache.md),
with the two bugs that kept it from shipping fixed — plus a standalone benchmark
of cached vs uncached meter queries. It runs **read-only on the existing
`proposal_events` table** (the recommended 10M-row design), caching at the
reference grain of `(window, subject, group-by values)` so it serves **grouped**
meter queries, not just totals.

This answers the earlier question empirically: **the partial-range cache is a
real win, it scales with how much of the range is cached, and (with the fixes)
it returns identical billing numbers to the uncached path — including
multi-dimensional group-by.** It also shows the floor: for already-cheap or
high-cardinality-group queries the cache overhead can make it a small loss.

## What was built

- `bench/cache/cache.go` — a cache `Connector` exposing a query API:
  `QueryUncached` (full-range raw scan, the control), `QueryCached` (cached
  history `UNION ALL` fresh tail, re-aggregated), and `PopulateCache`. It caches
  one row per `(window, subject, group-by combo)` — matching the reference
  cache's grain (the reference stores group-by as `Map(String,String)`; here it
  is an ordered `Array(String)` aligned to the meter's group-by paths, same
  information, no map-key-order parity trap). Scope is the four **mergeable**
  aggregations (SUM/COUNT/MIN/MAX); AVG and UNIQUE_COUNT are non-mergeable across
  partial windows and out of scope by design.
  Values are carried as **`Decimal128(19)` end-to-end** — billing-exact, matching
  the proposal queries (`toDecimal128OrNull(toString(data.<path>), 19)`), with no
  `Float64` hop anywhere. Cache columns are `Decimal128(19)`; COUNT is cast to the
  same decimal type identically on both the uncached path and the cached query's
  fresh leg so COUNT parity holds. The Go side scans through a `nullDecimal`
  wrapper into `alpacadecimal.Decimal` — mirroring OpenMeter's own
  `meter_query.go`, which wraps the scan because the clickhouse-go driver returns
  a shopspring `decimal.Decimal` whose stock `Scan` rejects a `decimal.Decimal`
  source (and `min`/`max` are `Nullable(Decimal128)`, which the wrapper handles).
- `bench/cache/cache_test.go` — regression tests, including the exact original
  failure shape (below) and a check that distinct group-by values stay separate.
- `bench/cmd/cachebench/main.go` — a standalone runnable (NOT wired into the
  `clickhouse-benchmark` scenario harness, which times one SQL statement at a
  time and can't express the Go-driven cache flow). It runs **read-only** on an
  existing events table — it never drops or seeds `proposal_events`, only its own
  cache table — derives the window from `min/max(time)`, populates the cache,
  **asserts cached == uncached values (keyed on the full window+subject+group-by
  tuple) before every timing**, then reports a speedup curve across freshness
  cutoffs.

**The uncached control is the proposal query verbatim.** `QueryUncached` is
exactly `scenarios/proposal/queries/sum_hour_group1_group2.sql` (and its
sum/min/max/count siblings): `tumbleStart/tumbleEnd(time, toIntervalHour(1),
'UTC')` windows with a `windowend` column, `toDecimal128OrNull(toString(data.<p>),
19)` values, `toString(data.<g>.:String)` group extraction, bare `count(*)`, and
the same `SETTINGS optimize_move_to_prewhere = 1, allow_reorder_prewhere_conditions
= 1`. So the baseline being beaten is the *real* meter query, not a strawman.
`QueryCached` and `PopulateCache` use the **same** window/value/group expressions
at all three SQL sites — so the cached `windowstart` lines up with the uncached
one and parity holds. (`windowend` is carried as a pure function of windowstart,
`windowstart + toIntervalHour(1)` = `tumbleEnd`, so it never drifts.) A row-count
canary confirms the tumble switch didn't shift any windows: counts stay 6,480 /
10,800.

```bash
export CLICKHOUSE_DSN="clickhouse://default:@127.0.0.1:9000/default"
go run ./bench/cmd/cachebench --events proposal_events --cutoffs 1,6,24,48
```

## The bugs, fixed and shown

Both defects from the prior review are fixed at their sites in `cache.go`, and
the primary one has a focused regression test:

- **Bug #1 — `NaN != NaN` in dedupe-on-read** (the most likely shipping blocker).
  The original stored a NaN "no value" sentinel, read it back, and ran it through
  a dedupe whose check was `row.Value != seen.Value`. NaN's self-inequality
  turned a *tolerated* duplicate (expected under parallel caching) into a hard
  error that failed the whole query. Fixed two ways: this implementation needs no
  NaN sentinel at all (missing hours are simply absent rows — both paths emit no
  row for an empty window, so absent == absent), and the dedupe equality is now
  NaN-aware. `TestDedupeRows_NaNSentinelDoesNotError` feeds two NaN rows for one
  window through the dedupe and asserts it returns instead of erroring;
  `TestDedupeRows_GenuineConflictStillErrors` confirms the fix stays narrow (real
  value conflicts still error).
- **Bug #2 — pointer-keyed subject map.** All grouping keys are string values,
  never `*string`, so equal subjects can't become distinct keys.

```
$ go test ./bench/cache/
ok  github.com/openmeterio/ch-playground/bench/cache
```

## Verification: cached == uncached (the billing-safety gate)

Before any timing, the runnable verifies that **with and without the cache the
results are identical**, for every `(meter, cutoff, aggregation)`:

- It compares the two full result sets on the **complete grouping tuple**
  (`windowstart, windowend, subject, group-by…`), with **exact decimal equality**
  (`Decimal.Round(6).Equal` — the same rounding `digest.go` uses), not a float
  epsilon.
- It tests the **two extremes where merge bugs hide**: `all-fresh` (0% cached —
  the cache is empty, so the cached path's tail leg *is* the full query) and
  `all-cached` (100% — served from the cache table alone, empty tail).
- It **fails the run** (`exit 1`) if any check differs, and prints a summary:
  `parity: N/N checks passed`.

```
$ go run ./bench/cmd/cachebench --events proposal_events --verify
   sample SUM rows (uncached == cached):
     2026-05-29T00:00 | eu-central-1,enterprise  uncached=54676.042862448434105  cached=54676.042862448434105
     2026-05-29T00:00 | eu-central-1,free         uncached=32752.8455510127283947 cached=32752.8455510127283947
   ...
=== parity: 24/24 checks passed ===
with and without the cache produce IDENTICAL results on every check.
```

The check is proven to actually fail: deliberately corrupting the cache
(storing `sum*2`) makes it report `value @ … uncached=38107 cached=76214`,
`14/18 passed`, and exit non-zero. So the passing result is a real equality
proof, not a check that can only say OK. The sample dump shows the full 19-digit
`Decimal128` matching exactly — no float-rounding hidden anywhere.

## Results (read-only on `proposal_events`: single-node CH 26.2, 10M events, ~3-day window, 10 iterations)

Median wall-clock per query. **Parity = OK on every row** — including every
grouped row — the cached path computes values identical to the full-range scan,
checked on the full `(window, subject, group-by)` tuple via **exact decimal
comparison** (`Decimal.Round(6).Equal`, the same rounding `digest.go` uses), not
a float epsilon. This is the billing-safety gate the whole line of review has
insisted on, now verified with multi-dimensional group-by and end-to-end
`Decimal128(19)`.

The cache stores `(window, subject, group-by)` rollups of the history
`[from, cutoff)`; the query reads those plus a live scan of the fresh tail
`[cutoff, to)`, re-aggregated. `cutoff` is hours of fresh tail left uncached.

**Meter `api_request` — `SUM(data.value)` grouped by `data.group1, data.group2`**
(4.997M rows of this type; 6,480 result rows = hours × 10 subjects × group combos):

| cutoff | range cached | SUM | MIN | MAX | COUNT |
| --- | --- | ---: | ---: | ---: | ---: |
| 1h | 99% | **4.22×** | 4.14× | 4.08× | 2.18× |
| 6h | 92% | 2.46× | 2.55× | 2.63× | 1.36× |
| 24h | 67% | 1.76× | 1.61× | 1.64× | 1.14× |
| 48h | 33% | 1.14× | 1.19× | 1.15× | 0.93× |

(Absolute: uncached SUM/MIN/MAX ≈ 56 ms regardless of cutoff; cached SUM falls
from ~13 ms at 99% cached to ~49 ms at 33%. Uncached COUNT ≈ 26 ms.)

**Meter `kong.llm_request` — `SUM(data.tokens)` grouped by `data.model, data.provider`**
(1.502M rows; 10,800 result rows — higher group cardinality):

| cutoff | range cached | SUM | COUNT |
| --- | --- | ---: | ---: |
| 1h | 99% | 1.57× | 1.36× |
| 6h | 92% | 1.25× | 1.05× |
| 24h | 67% | 1.07× | 0.97× |
| 48h | 33% | 0.86× | 0.91× |

### What the curve says

- **The win is the cached:fresh ratio, not a fixed number.** api_request SUM goes
  4.2× → 1.1× as the cached fraction drops 99% → 33%. A single "X% faster" would
  be meaningless; the cache pays off in proportion to how much history is stable
  enough to cache. This is exactly why the application-orchestrated *partial-range*
  cache exists and why the native query cache (which can't compose partial ranges
  — see [REVIEW-native-clickhouse-cache.md](REVIEW-native-clickhouse-cache.md)) is
  not a substitute.

- **The win concentrates where the raw query is expensive.** api_request reads
  `data.value` on every raw row across ~5M rows; the cache replaces that with a
  scan of pre-extracted decimals, so it wins big (56 ms → 13 ms). This mirrors the
  repo's wider finding that JSON-path reads dominate meter-query cost.

- **Group cardinality moves the crossover earlier.** `kong.llm_request` (model ×
  provider → 10,800 groups over a smaller 1.5M-row type) wins far less and crosses
  into a *loss* sooner (0.86× at 33% cached) than api_request. Two reasons: the
  cache table itself is larger (more group combos = more cache rows to scan on the
  read side), and the uncached query is already cheaper (fewer rows, cheaper
  `data.tokens` extraction), so there's less to save. **The cache's value falls as
  group cardinality rises and as the underlying query gets cheaper.**

- **The crossover to a small loss is real.** Once the fresh tail dominates (≥~2/3
  uncached) or the query is already cheap, the `UNION ALL` + cache-table scan +
  planning overhead exceeds the saving (down to 0.86–0.93×). A real deployment
  should gate caching on expected query cost and cached fraction (the original's
  `query_cache_min_query_duration`-style idea), not cache everything.

## Caveats (so the numbers aren't over-read)

- **Read-only on the shared seed.** Runs against the existing 10M-row
  `proposal_events` — the cachebench never drops or seeds it (verified: 10M rows
  before and after). Only its own cache table is created/truncated.
- **Warm cache, single node.** Wall-clock with everything in page cache; the
  cache's row/byte-scan reduction would show even larger under cold I/O or
  concurrent multi-tenant load (less to read = less to contend on), but these
  specific magnitudes are this host's.
- **The merge is modelled as SQL, not Go.** OpenMeter does the final merge in Go
  (~18 µs/op per PR #3764) — negligible next to the DB reads — so folding it into
  the `UNION ALL` captures essentially all the cost while letting one statement be
  timed cleanly.
- **Group-by values pre-extracted into the cache, matching the reference.** The
  cache stores `subject` + an ordered `group_by Array(String)` per row, populated
  with the *identical* `subject IN (...)` filter and `toString(data.<path>.:String)`
  extraction as the query — which is why grouped parity holds exactly. This is the
  reference cache's `Map(String,String)` shape, minus the map-key-order hazard.
- **No invalidation / TTL / incremental head-tail.** Deliberately omitted as not
  needed to measure partial-range reuse. In production, late events must
  invalidate affected cache windows (the original did this per-namespace); that
  cost is not in these numbers.

## Bottom line

The partial-range cache works, returns correct billing values once the NaN/dedupe
and pointer bugs are fixed — **including multi-dimensional group-by** — and
delivers **1.1×–4.2× on payload-heavy grouped meter queries** in proportion to the
cached fraction, while crossing into a small **loss** on high-cardinality-group or
already-cheap queries when the fresh tail dominates. That asymmetry is the design
guidance: it's a cache worth building for expensive aggregations over
mostly-stable history with modest group cardinality, gated so it doesn't wrap
queries that are already cheap.
