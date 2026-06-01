# Total-period rollups — the corrected picture (overturns prior "1×" verdicts)

**Trigger:** the dominant meter query is **total-period** — `WHERE time >= from
AND time < to` aggregating over the whole range, `GROUP BY subject` (no time
buckets, no dim group-by). Confirmed by `scenarios/proposal/queries/sum_no_window.sql`.
This overturns the "MVs/projections give 1× / are useless" conclusions from the
two prior turns, which were measured with the high-cardinality dims in the GROUP BY.

## The error in the prior verdicts

Prior turns built rollups/projections grouped by all 13 meter dims (`route_id`,
`service_id`, `client_ip`, …) and measured 1× compression. **That 1× was caused
by including those dims** — and on the seed they're unique-per-event. But the
dominant query **doesn't group by them** — it groups by `subject` only. A rollup
that matches the query (drop the dims) compresses enormously, and this is
**artifact-independent** (depends only on subjects × time-buckets, both real).

## Measured: dims-free rollup `(namespace, type, subject, window_start)` (seed, artifact-free)

| Meter | grain | rollup rows | compression |
|-------|-------|------------:|------------:|
| llm_request (SUM) | hour | 7.3K | **206×** |
| llm_request (SUM) | day | 400 | **3,754×** |
| kong_api_request (COUNT) | hour | 7.3K | **343×** |
| kong_api_request (COUNT) | day | 400 | **6,254×** |

**Both meters compress massively** for the total-period query. The earlier
"api_request is intrinsically 1× / unoptimizable" was WRONG — that only held for
queries grouping by its near-unique dims. For COUNT-by-subject-over-period, a
`countState` rollup compresses 343×–6254×.

## The two gates that decide usability

**Gate 1 — what do the dominant queries GROUP BY?**
"Most queries are total-period" → `GROUP BY subject` only (per `sum_no_window`).
If so, the dims-free rollup is the right structure and the compression above
applies. (Queries that DO slice by a high-card dim can't use it and fall through
to the base table — fine, they're the minority.)

**Gate 2 — billing-period boundary alignment (the exactness gate, a PRODUCT fact).**
A time-bucketed rollup answers `time >= from AND time < to` **exactly** only if
`from`/`to` align to the bucket grain:
- Calendar-aligned periods (month/day boundaries) → exact. ✅
- Arbitrary instants (e.g. signup-anniversary billing at 14:37:02) → the boundary
  bucket straddles `from`/`to` → wrong sum → **unacceptable for billing.** ❌

**This is the determinant and it's not in this repo** — it's OpenMeter's query
builder semantics (`meter_query.go`). Must confirm: do total-period `from`/`to`
align to a grain (day/month), or are they arbitrary timestamps? Pick the rollup
grain = the billing-period grain so boundaries always align.

## Vehicle: MV, not projection (for this query)

The total-period query filters **raw `time`**. A time-bucketed projection can't
bind a raw-`time` filter transparently (proven three ways in prior turns → `[]`).
The **MV wins precisely because the resolver rewrites** the query to filter
`window_start` directly (and, with grain ≤ period and aligned boundaries, exactly).
That's the move a projection can't make. So: **MV + resolver routing** for the two
known meters; projection path is closed for the total-period query.

Correctness stays exact via `sumState`/`countState` (integer tokens / counts) —
verified exact in the prior MV turn (3,005,145,740 tokens to the unit).

## Recommendation

- Build **dims-free** rollups `(namespace, type, subject, window_start)` at the
  **billing-period grain** for both `kong.llm_request` (sumState tokens) and
  `kong.api_request` (countState) — both compress 100s–1000s× for the total-period
  query. This reverses the prior "don't build api_request."
- Resolver routes these two types' total-period queries to the rollups; everything
  else (other meters, dim-sliced queries, sub-grain windows) uses the base table.
- **Blocking product fact:** confirm OpenMeter's `from`/`to` alignment before
  committing a grain. If arbitrary instants, either (a) rollup at fine grain
  (minute) accepting less compression but bucket-exact for minute-aligned periods,
  or (b) combine rollup (interior buckets) + base-table tails for the partial
  boundary buckets — more complex but exact for any from/to.

## Still to validate on prod
- Real subject count × period length (drives actual compression; seed has 100
  subjects — prod has far more, but the per-subject-per-bucket structure holds).
- `from`/`to` alignment semantics (the exactness gate).
