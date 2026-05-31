# Known-meter rollup investigation — current conclusions

This is the **authoritative summary**. The other `*.md` files here are dated
investigation notes; where they disagree with this file, this file wins (several
were written before the seeder was fixed and carry superseded numbers — they
each now carry a "SUPERSEDED" banner pointing here).

Local scratch notes — not committed to `main` (some reference partial customer
namespaces from the prod cluster). The committed artifact is the `proposal`
scenario itself (`scenarios/proposal/`).

## Latest run — fresh 10M-event seed, all scenarios, --iterations 10

Local ClickHouse 26.2.5, 10,000,000 events seeded per scenario (1 namespace),
all results below are warm p50 / CPU p50. Re-run with:
`bin/bench --scenarios-dir scenarios --rows 10000000 --iterations 10`.

**Scenario medians across the 20 shared meter queries (lower = better):**

| scenario | median p50 | median CPU p50 |
|----------|-----------:|---------------:|
| baseline-openmeter (`data String`, JSON_VALUE) | ~73 ms | ~1230 ms |
| data-as-map (`data Map`) | ~57 ms | ~960 ms |
| **proposal** (`data JSON` + ZSTD3 + bloom) | **~43 ms** | **~700 ms** |

Proposal is the fastest table design — ~40% lower p50 and ~45% lower CPU than the
String baseline on the meter-aggregation sweep, consistent with prior runs.

**Proposal's Kong-meter rollups vs the base-table equivalent (10M):** the MV
rollups are the dramatic win — same query, served from the pre-aggregate:

| query | p50 | CPU p50 | vs base-table equivalent |
|-------|----:|--------:|--------------------------|
| `sum_hour` (base-table SUM sweep) | 45 ms | 729 ms | — |
| **`kong_llm_tokens_total_hybrid`** (llm rollup) | **17 ms** | **167 ms** | ~2.6× faster, ~4.4× less CPU |
| `count_hour` (base-table COUNT) | 14 ms | 189 ms | — |
| **`kong_api_request_total_hybrid`** (api rollup) | **11 ms** | **68 ms** | ~1.3× faster, ~2.8× less CPU |

Both hybrids are billing-exact for arbitrary boundaries (verified rollup == base:
llm 3,005,145,740 tokens; api 2,501,717 count). Compression at 10M:
api dims-free **2.5M → 7.3K rows (~343×)**; llm dims-full 1.5M → 1.5M (1× at this
seed's dim cardinality — its win is typed columns / no JSON parse, not row count).

## The seeder is fixed (this was the source of most stale numbers)

Earlier notes show the Kong meters' id dimensions as **unique-per-event** (e.g.
`route_id` = 1.5M distinct = event count), which made every grouped/rollup
aggregation look like 1× "cannot compress". That was a synthetic-seed artifact,
**not** real behavior. The seeder now draws bounded dims from fixed id pools.

Measured on the current 1M seed (`kong.llm_request`, 150k rows):

| dimension | OLD (stale notes) | CURRENT |
|-----------|------------------:|--------:|
| route_id | 1.50M | **60** |
| service_id | 1.50M | **40** |
| ai_plugin_id | 1.50M | **12** |
| control_plane_id | 1.50M | **8** |
| model / provider | 4 / 3 | 5 / 3 |

Genuinely high-cardinality dims (`client_ip` ~250k, `request_uri` ~50k,
`request_user_agent`) are deliberately **kept** near-unique — real gateway traffic.

## Canonical meters (see scenarios/proposal/meters.yaml)

- **kong_konnect_llm_tokens** — SUM `$.tokens`, eventType `kong.llm_request`, 14 groupBy dims.
- **kong_konnect_api_request** — COUNT, eventType `kong.api_request`, 19 groupBy dims.

## Rollup design in `proposal` (asymmetric, from measurement)

- **llm: DIMS-FULL rollup** (`proposal_llm_tokens_rollup`) — all 14 dims as typed
  columns + `sumState(tokens)` (UInt64 state, cast to `toDecimal128(…,19)` at
  query time). Serves total-period SUM **and** dim-filtered queries.
- **api: DIMS-FREE rollup** (`proposal_api_request_rollup`) — `countState` keyed
  only `(namespace, subject, window)`. Even with the seeder fixed, the api meter's
  19-dim combination (incl. high-card `client_ip`/`request_uri`/`user_agent`)
  stays ~unique per event, so a dims-full api rollup is still ~1× = pure cost.
  Dims-free compresses **~34× hourly / ~625× daily** for the dominant total-period
  COUNT-by-subject query. Dim-filtered api queries run on the base table.

## Correctness (verified on fresh 1M seed)

- Both rollups == base, exact: llm `300,040,901` tokens; api `250,141` count.
- Total-period reads use the **3-part hybrid** (raw head + rollup interior + raw
  tail) → billing-exact for **arbitrary** (non-hour-aligned) from/to. Verified
  hybrid == base across edge cases (mid-hour, on-boundary, same-hour/interior-
  empty fallback, spans-one-boundary). Count is exact integer; tokens cast to
  Decimal at query time.

## Projections (the earlier "rejected" verdict was a misdiagnosis — corrected)

Aggregate projections were first reported as unable to serve filtered meter
queries. Per ClickHouse #33678/#33587 the real rule is that every WHERE/GROUP-BY
column must EXIST in the projection. A correctly-built projection DOES route and
prune; but for OpenMeter's already-well-pruned per-subject queries the base sort
key reads ~1–2 granules already, so a projection's marginal benefit is small, and
it can't transparently serve the raw-`time` arbitrary-boundary filter. The
**MV-rollup + 3-part hybrid** (what `proposal` ships) is the chosen mechanism.

## Doc status

| file | status |
|------|--------|
| (this README) | current / authoritative |
| KNOWN-SCHEMA-METER-OPTIMIZATION.md | early options survey — superseded |
| KNOWN-METER-MV-FINDINGS.md | pre-seeder-fix (shows 1× artifact) — superseded |
| PROJECTIONS-FOR-KNOWN-METERS.md | pre-fix + pre-correction — superseded |
| PROJECTIONS-AND-EXTENDED-TIME-FINDINGS.md | corrected-banner already; superseded |
| PROJECTIONS-CORRECTED-FINDINGS.md | projection correction — folded in above |
| TOTAL-PERIOD-ROLLUP-FINDINGS.md | total-period analysis — folded in above |
| ARBITRARY-BOUNDARY-ROLLUP-DESIGN.md | hybrid design — implemented in proposal |
