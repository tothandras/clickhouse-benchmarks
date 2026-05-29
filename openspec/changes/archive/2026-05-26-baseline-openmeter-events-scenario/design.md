## Context

OpenMeter today stores all events in a single ClickHouse MergeTree table `om_events` and answers meter queries by aggregating on the fly with `JSON_VALUE` extraction over a `String` data column — no per-meter materialized views, no projections. This design is the upstream baseline (`openmeter/streaming/clickhouse/event_query.go:20-49`, `meter_query.go:108-362`). It works, but every alternative we want to evaluate (materialized aggregates, narrowed columns, AggregatingMergeTree, projections, JSON-typed columns, partition strategies) needs to be measured against *this* shape under *these* query patterns, or numbers are meaningless.

ch-playground has no infrastructure yet. There is no cluster bring-up, no scenario runner, and no benchmark code — only the devenv toolchain and the openspec workflow. This change introduces all three at the minimum needed to make the baseline runnable, and stops there. Cluster orchestration (kind + Altinity operator) is a separate upcoming change; until it lands, the harness will target any reachable ClickHouse via a DSN env var so this work isn't blocked.

Stakeholders: OpenMeter engineering (downstream consumer of any "the new schema is faster" claim), this repo's maintainers (who add variants over time).

## Goals / Non-Goals

**Goals:**
- Reproduce `om_events` schema byte-for-byte against the upstream definition, so any divergence is a bug not a design choice.
- Emit meter queries in the exact shape `meter_query.go` produces — `tumbleStart`/`tumbleEnd` windowing, `JSON_VALUE` extraction with `ifNotFinite`/`nullIf` null-safety, namespace/type/subject/time filtering — covering every supported aggregation (SUM, COUNT, AVG, MIN, MAX, UNIQUE_COUNT, LATEST).
- Define a `scenarios/<name>/` contract that the harness can re-use unchanged for every future variant.
- Ship a Go-based harness that does the minimum: discover scenarios, seed, run queries N times after a warm-up, write structured results. No web UI, no comparison reports yet.
- Make the harness cluster-agnostic via `CLICKHOUSE_DSN`, so progress doesn't block on the kind/operator change.

**Non-Goals:**
- Cluster bring-up (kind, Altinity operator, sharding/replication strategy) — separate proposal.
- Statistical comparison tooling (regression detection, A/B reports) — separate proposal; this change just lays down the data.
- Reproducing OpenMeter's *full* schema universe (entitlements, grants, billing tables, subjects/customers). Just `om_events` + the meter query shape, which is the hot path.
- Drift detection against upstream OpenMeter — flagged in tasks as a future concern.
- A scenario manifest format. The first scenario fits in one directory; we'll introduce a manifest only when a second variant needs to declare what it modifies vs. inherits.

## Decisions

**Decision: Reproduce the schema with raw DDL, not by generating it from OpenMeter's Go code.**
The upstream DDL is a Go string template (`event_query.go`). We considered importing OpenMeter as a Go dep and calling its DDL generator, which would guarantee parity. Rejected: it pulls in OpenMeter's full dep tree (Ent, Watermill, etc.) for one string, and it couples scenario evolution to upstream releases. Instead, the baseline scenario's `init.sql` contains a verbatim copy of the DDL, with a comment citing the upstream file:line and commit SHA at the time of capture. Drift is a future concern (a periodic CI check that diffs upstream's generated DDL against ours) but is out of scope here.

**Decision: Seed with a Go program, not pure SQL `generateRandom`.**
ClickHouse's `generateRandom` table function can fill a table fast, but it can't produce a *structured* JSON `data` column with controllable cardinality on group-by fields. Meter queries' performance hinges on JSON-extraction cost and group-by cardinality, both of which would be degenerate under random JSON. The seeder will be a small Go program under `bench/seed/` that emits batched INSERTs (or uses the native protocol's batch insert) with: deterministic namespaces/types, a configurable subject pool (default 100), and a `data` payload of `{"value": <float>, "group1": <enum>, "group2": <enum>}`. Determinism (seedable RNG) matters so re-runs across variants compare against the same data.

**Decision: One scenario directory per variant, no shared DDL.**
Considered factoring common DDL into `scenarios/_common/`. Rejected for now: the whole point is that variants change DDL, so sharing it is the opposite of what we want. If shared seed *data* becomes useful later (e.g., snapshot a populated table once, attach to variants), we'll add a separate `fixtures/` mechanism then.

**Decision: Queries are plain `.sql` files with `{name:Type}` placeholders, not Go templates.**
ClickHouse's native protocol supports `{name:Type}` parameter binding. Using SQL files keeps queries readable, lets engineers run them ad-hoc via `clickhouse-client`, and avoids a templating layer. The harness reads each `.sql`, binds parameters from a fixed default set (extensible later via per-scenario `params.json` if needed), executes, times.

**Decision: Async-insert + batch-size are bench-level flags, not part of the scenario.**
The scenario defines *what* gets inserted. *How* it's inserted (batch size, `async_insert=0|1`, `wait_for_async_insert`) is a benchmark dimension the harness varies. This lets one scenario produce a matrix of ingest results without DDL duplication. Initial defaults: batch size 10,000, `async_insert=0` (sync) — matches OpenMeter's typical sink path before any tuning.

**Decision: Results in JSON, one file per scenario per run.**
JSON keeps the door open to programmatic comparison later. Path: `bench/results/<scenario>/<RFC3339-timestamp>.json`. Includes scenario, harness commit, ClickHouse `version()`, ingest block, per-query timings + percentiles.

**Decision: Default to OpenMeter's PREWHERE-on by default, with a no-PREWHERE variant for comparison.**
`meter_query.go` exposes `EnablePrewhere` as a per-query flag and OpenMeter typically runs with it on. We mirror that: the default query files use PREWHERE; sibling `*_no_prewhere.sql` files exist so the optimizer's contribution is measurable.

**Decision: Delegate per-query measurement to `clickhouse-benchmark` (the official CLI), not a hand-rolled Go loop.**
First implementation timed queries in-process via `time.Now()` around `conn.Query()`. That measures client-side wall time, which includes Go driver deserialization overhead — for our SUM/COUNT meter queries, that overhead is a non-trivial slice of the recorded number (e.g. ~6ms client-side vs ~4ms server-side in early local runs). It also can't measure concurrent load, and reinvents percentile computation. `clickhouse-benchmark` is the ClickHouse-team CLI shipped in the same binary distribution; it reports server-side time, supports `--concurrency N` for realistic load shapes, emits p50/p95/p99/p99.9/p99.99 plus QPS/RPS/MiB/s, and includes T-test infrastructure for cluster-vs-cluster comparison (useful once the kind+operator change lands). Rejected the "keep both runners" path because two harnesses producing two different numbers for the same query means every disagreement is a debugging session. The Go harness keeps everything around query execution (scenario discovery, `init.sql` apply, the seeder, parameter binding, result file writer, cluster fingerprint) and shells out for the measurement itself. Output is text on stderr (the docs page references `--json` but it's not in 26.2.5; the percentile block is a stable TSV-shaped format, ~14 lines, trivial to regex-parse). The trade-off: we depend on `clickhouse-benchmark` being on PATH, which the devenv provides and any production CI image including `clickhouse-server` ships by default.

## Risks / Trade-offs

- **Schema drift against upstream OpenMeter** → mitigated short-term by citing the source commit SHA in `init.sql` so a reader can verify. Long-term mitigation (a CI job that diffs) is a separate change.
- **Synthetic event distribution may not match real OpenMeter traffic** → the seeder is configurable (subject pool size, value distribution, group-by cardinality, time spread). The defaults are explicit and documented so anyone comparing variants is comparing against the same fixture. A second seed profile based on a real OpenMeter sample is a follow-up.
- **`CLICKHOUSE_DSN` against a single-node local install will produce numbers that don't reflect production-scale cluster behavior** → acknowledged. Once the kind + operator scenario lands, the same harness runs unchanged against the cluster and we re-baseline. Until then results carry an `is_single_node: true` annotation in the cluster fingerprint so they aren't mistakenly compared to cluster numbers.
- **JSON_VALUE behavior differs subtly between ClickHouse versions** → the result file captures `version()`; comparisons across versions are caller-aware.
- **Warm-up of 1 iteration may not flush the page cache or mark cache enough on cold runs** → trade-off: more warm-up iterations make runs slower; we start with 1 and revisit if variance in early measured iterations stays high.

## Migration Plan

Not applicable — no existing scenarios, no existing harness. The artifacts of this change are the first.

## Open Questions

- Should `queries/` files declare their expected parameter set (in a sidecar file or header comment) so the harness can validate at load time, or is "fail at execute" good enough for v1? Leaning toward fail-at-execute for v1 and revisit when adding the second scenario surfaces the need.
- Should the seed runner write to ClickHouse via the HTTP interface or the native protocol? Native is faster and supports proper batching; HTTP is debugger-friendlier. Leaning native via `clickhouse-go/v2`, but happy to flip if there's a reason.
