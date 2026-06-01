# Projections for the known meters — corrected findings

**This supersedes the projection verdict in PROJECTIONS-FOR-KNOWN-METERS.md and
PROJECTIONS-AND-EXTENDED-TIME-FINDINGS.md, which contained a misdiagnosis.**

Prompted by ClickHouse issue [#33678](https://github.com/ClickHouse/ClickHouse/issues/33678)
(umbrella: "Boosting the Projection feature") and its sub-issue #33587
("projection not support the where clause"). Re-tested on local CH 26.2.5.45,
`proposal_events` (10M rows seed, 1 namespace).

## The earlier claim was WRONG

Earlier writeups said: *"a WHERE filter on subject/time inherently defeats
aggregate-projection routing."* **That is false.** The real rule (confirmed by
the issue + docs + re-test):

> An aggregate projection is used only if **every column referenced in WHERE and
> GROUP BY exists in the projection**, and the filter must hit the projection's
> sort prefix to prune.

My failing projections used `tumbleStart(time)` as the *only* time
representation. The meter query's `WHERE time >= … AND time < …` references **raw
`time`**, which wasn't a projection column → routing failed. Not "filters break
projections" — **a missing column.**

## The core tension (the real lesson)

| To... | you need... | which means... |
|-------|-------------|----------------|
| route against `WHERE time >= … < …` (raw time) | raw `time` in the projection | projection has ~1 row/event → **no compression** |
| compress (aggregate time → window) | no raw `time` column | raw-`time` WHERE can't bind → **won't route** |

For the *current* OpenMeter query shape (filters on raw `time`) these are
mutually exclusive. **Resolution:** make the meter query filter on the **windowed
column** — `WHERE toStartOfHour(time) >= F AND < U` — so a windowed aggregate
projection both routes and prunes. That's a generator change (uniform across all
meters, same class as tumbleStart→toStartOf*), semantically equal **only when
from/to are window-aligned** (state this caveat).

## Measured, the clean test (namespace-leading projection, merged, real shape)

Projection `GROUP BY (namespace, type, subject, toStartOfHour(time), model,
provider)` + `sum(...)`; query filters `namespace + type + subject + windowed
time`; `OPTIMIZE FINAL` first so parts are merged:

| | rows read | granules |
|--|----------:|---------:|
| BASE (sort key only) | 16,384 | 2 |
| PROJECTION (p_full) | 8,192 | 1 |

The projection **does route and does prune** — but the win is **1 granule vs 2**.
Both are already at the granule floor because the base sort key
`(namespace, type, subject, toStartOfHour(time))` — the always-present meter
filter columns — already prunes extremely well. The projection saves one granule.

## Why "17× compression" did NOT become "17× fewer rows read"

The projection holds **876 aggregated rows** for this ns+subject (vs 14,879 raw)
— a 17× *size* compression. But row-read savings were only 2×. **Projection size
compression ≠ row-read savings.** Read savings require the filter to prune the
projection's granules; once both base and projection are down to ~1 granule for a
selective filter, compression buys at most a granule. This is the key correction
to the earlier over-optimistic framing too.

## Scoping to ONLY the two meters without duplicating all events (measured)

A projection definition **cannot have a `WHERE`** (rejected in CH 26.2, confirmed).
The scoping mechanism is **GROUP BY**, not WHERE: an aggregate projection stores
one row per group-key *combo*, not per row. A projection grouped by the **llm
meter's dims** —
`(namespace, type, subject, toStartOfHour(time), model, provider)` — collapses
every *other* type to ~one row per `(namespace, type, subject, hour)` (their
`model`/`provider` are null). Measured on the 10M seed:

| | value |
|--|------|
| base events | 10,000,000 |
| projection rows | 116,794 |
| **collapse factor** | **85.6×** |
| projection size | 604 KiB (base: 711 MiB → ~0.08%) |

So a GROUP-BY-scoped projection does **NOT** duplicate all events — it's a tiny
aggregate. The llm meter query routes to it (read 8,192 = 1 granule). **This is
how you scope the optimization to the llm meter.**

**Critical: do NOT put api_request's dims in any projection.** Its 18 near-unique
fields don't collapse — measured 2.50M events → 2.50M distinct combos (1:1). An
api-dims projection would store ~one row per event = exactly the duplication to
avoid. One projection can't serve both meters; scope it to llm dims only, and
api_request (and all other types) collapse cheaply inside it.

### Projection vs MV for scoping (both avoid full duplication)

| | GROUP-BY-scoped projection | MV with WHERE type IN (...) |
|--|----------------------------|------------------------------|
| Interface | transparent (auto-routes, identical SQL) | non-transparent (resolver routes 2 meters) |
| Dedup | safe by construction (recomputed from committed rows) | reintroduces dedup gate (must consume deduped stream) |
| Other types | processed on every merge; stored at subject-hour grain (cheap but nonzero) | never touched — zero overhead |
| Cost driver | all-events merge overhead at ingest | dedup-ordering complexity |

The projection honors both earlier goals (transparent + dedup-safe + no full
dup); the MV is the fallback if all-types merge overhead is too costly at
4.59B-event ingest scale. **That tradeoff is prod-measurable, not a seed question.**

## Verdict

- Projections are **not the inherent dead-end** the earlier writeups claimed —
  routing works once the WHERE columns exist in the projection.
- **Scope to the llm meter via GROUP BY (not WHERE):** an llm-dims projection is
  85.6× smaller than the event count — it does not duplicate all events. api_request
  must stay out (1:1, can't collapse) and stays on the base table.
- But for OpenMeter's well-pruned meter queries (always filtered on the
  always-present sort-key columns), the **base table already reads ~1-2
  granules**, so a projection's marginal benefit is ~1 granule. Small.
- The win would matter only where the base *can't* prune to a granule — i.e.
  queries scanning many subjects / wide time ranges. The canonical per-subject,
  windowed meter query isn't that.
- Requires the generator to filter on the windowed column (window-aligned bounds
  caveat) for a windowed projection to route at all.

## Cardinality gate (unchanged, caps upside regardless)

- **llm_tokens (SUM):** ~17× group-size compression — the projection at least has
  something to compress.
- **api_request (COUNT):** group combos ≈ row count (1×) — a projection holds ~one
  row per event, so it can neither compress nor beat the base. Out, definitively.

## Must validate on prod (cluster was down)

The seed is 10M / 1 namespace / ~15K rows per subject — too small for the granule
math to reflect 4.59B-row reality. On prod, the base per-subject slice may span
many granules, where a windowed projection's pruning could matter more. Re-run the
clean test (namespace-leading projection, merged, windowed-column filter,
multi-namespace) on prod before any decision. The cardinality split (llm 17× /
api 1×) holds regardless of scale.
