# Design: COGS Measurement

## Context

The harness measures *which table design is fastest* (per-query CPU/latency via `clickhouse-benchmark`, value parity via digests). This change adds a parallel result type measuring *what OpenMeter's ClickHouse workload costs*. Reused as-is: the deterministic seeded event generator (`bench/seed`, already a pure function of `(Seed, idx)` via `genCtx.genEvent`), the `proposal`/`baseline-openmeter` scenarios, the production-shaped query files, result-file conventions (`harness_commit` with `-dirty`, `--require-clean`, self-describing JSON+md), and the `compare` workflow.

Capability gaps being filled: merge accounting (`system.part_log`; merges are invisible to `query_log`), idle-floor accounting, per-query-class cost attribution, and pricing.

Constraints carried over from the project: no per-meter knowledge in any schema, exact aggregation for billing, and the perf path must remain bit-for-bit unaffected.

**Deployment reality that shapes the design:** the measurement target is ClickHouse Cloud, where a Scale service has ≥2 replicas, connections are load-balanced across them, and `system.query_log`/`system.part_log` are **per-replica** tables. Any collector that reads the bare local tables undercounts nondeterministically and the error lands in the idle residual. All accounting is therefore multi-replica by construction (Decision 2).

## Goals / Non-Goals

**Goals:**

- Unit-cost card per workload cell: `$/1M events` (insert + merge split), `$/1k queries` per class warm/cold, `$/GB-month` storage, idle floor.
- Attribution coverage metric (`attributed_cpu / available_cpu`) so unaccounted work is visible, not silently mispriced.
- Both billed-shape (reconciles with the invoice) and cpu-linear (marginal what-if) pricing.
- Linearity/interference answer via `cogs validate`.
- Same reproducibility standard as perf runs (seeded data, self-describing results, compare).

**Non-Goals:**

- Cloud service provisioning/autoscaling automation; ClickPipes; measured egress; measured backup bytes; GitHub CI (repo has none — smoke test is a devenv script).

## Decisions

### D1. Run lifecycle: prepare → soak → measure → drain → collect → price → report

`bench/cogs/runner.go` executes one cell:

1. **Prepare.** Resolve cell manifest, scenario, pricing profile. Apply `init.sql` (unless `--skip-init`). Optionally pre-seed `preload_rows` of history with the existing seeder, with `TimeEnd` pinned to run start (see D4). Record `system.parts` baseline, detected service shape (D8), and a `run_id` (ULID).
2. **Soak.** Run the cell's ingest at target rate for `soak` (default 30m; `--profile ci` 2m). Gate: poll active part count every 30s; end early once stable within ±10% over 5 consecutive polls. Record `parts_plateau: true|false`; never fail on it.
3. **Measure.** Ingest driver + query replayer concurrently for `measure` (default 60m; ci 3m). Everything in this window is tagged (D6) and attributed.
4. **Drain.** Stop ingest; keep collecting `part_log` for `drain` (default 15m) so merges triggered by measure-window inserts are captured and attributed back to that ingest. (Symmetrically, soak-triggered merges completing during measure are counted — at steady state the two leakages net out; the methodology README states this.)
5. **Collect.** Flush logs on all replicas, run collectors (D2/D6).
6. **Price + report.** Apply pricing (D7/D9), write JSON + markdown, optionally reconcile (D11).

SIGINT during measure still produces a report over the truncated window, flagged. Runs are resumable only as a whole.

### D2. Multi-replica accounting (Cloud correctness)

*Why:* per-replica log tables + load-balanced connections (Context). The single-node devenv path must not mask this: the same code runs everywhere.

- All log collectors read `clusterAllReplicas(default, system.query_log)` / `clusterAllReplicas(default, system.part_log)`. On the single-node devenv, `default` cluster resolves to the one node — same query works.
- Collect phase issues `SYSTEM FLUSH LOGS` on all replicas (`ON CLUSTER default`; fallback to local flush + 10s settle if the statement is not permitted, recorded as `log_flush: "local-only"`).
- Capacity: per-replica vCPUs detected from the connected node (`CGroupMaxCPU` from `system.asynchronous_metrics`, fallbacks to `nproc`-equivalents), replica count from `system.clusters`; `available_cpu_sec = replicas × vcpus_per_replica × measure_window_sec`.
- Runner cross-checks detected shape against `pricing_profile.service` (replicas, vCPUs) and warns loudly on mismatch, recording `shape_mismatch: true` — otherwise unit costs get priced on the wrong shape with no signal.
- Acceptance for this decision: on a 2-replica Cloud service, an ingest-only cell's merge count from the collector matches a manual `clusterAllReplicas` query; coverage does not systematically drop versus a single-replica run of the same cell.

*Alternative considered:* reading local tables and assuming sticky routing — rejected; Cloud load-balancing makes even the harness's own tagged statements land on different replicas.

### D3. Workload cell manifests: `cells/<name>.json`

```jsonc
{
  "name": "mixed-5keps-4qps",
  "scenario": "proposal",
  "preload_rows": 10000000,
  "soak": "30m", "measure": "60m", "drain": "15m",
  "ingest": {
    "events_per_sec": 5000,            // 0 = no ingest (query-only / idle cells)
    "batch_max_rows": 5000,            // flush on either threshold,
    "flush_interval": "1s",            //   mirroring the OpenMeter sink semantics
    "async_insert": false,
    "namespaces": 32, "mixed_value": true, "seed": 42
  },
  "query": {
    "qps": 4.0,                        // 0 = ingest-only / idle cells
    "arrival": "poisson",              // "poisson" | "uniform"
    "mix": "production",               // key into scenarios/<scenario>/queries/mix.json
    "cold_fraction": 0.1,
    "concurrency_cap": 16,
    "settings": { "max_threads": 4 }
  },
  "pricing_profile": "clickhouse-cloud-scale-aws-us-east-1"
}
```

Strict decode (unknown fields error); durations parse as Go durations; `events_per_sec == 0 && qps == 0` is legal (idle cell); `mix` must resolve for the chosen scenario.

**Default cell matrix** (shipped in `cells/`; `--profile ci` overrides durations to 2m/3m/1m):

| Cell | eps | qps | purpose |
|---|---|---|---|
| `idle` | 0 | 0 | awake-but-unloaded floor (see D12) |
| `ingest-1k` / `ingest-5k` / `ingest-25k` | 1k/5k/25k | 0 | $/1M events; rate linearity; merge share |
| `query-1qps` / `query-4qps` / `query-16qps` | 0 | 1/4/16 | $/1k queries per class; qps linearity |
| `query-4qps-cold` | 0 | 4 | `cold_fraction: 1.0`; cold-read multiplier |
| `mixed-5keps-4qps` | 5k | 4 | interference; input to `cogs validate` |
| `ingest-5k-async` | 5k | 0 | async_insert vs sink-style batching (carries the async accounting caveat, D6) |

All default `scenario: "proposal"`, `preload_rows: 10000000`, `seed: 42`.

### D4. Event-time policy for live ingest

*Problem:* `genEvent` draws event time uniformly over `[TimeEnd−3d, TimeEnd)`. Reusing that for live ingest backfills the past (query scan size drifts through the window); stamping nothing means queries never see fresh data.

*Decision:*

- The ingest driver stamps **event time = wall clock now** at generation. The generator still performs the time draw (first draw of the per-event PCG stream) and discards it, so all subsequent draws (type, payload, subject, id) stay byte-identical to the seeder for the same `(seed, idx)`. `store_row_id` is built from the wall-clock time + the stream's entropy, preserving its time-ordering property.
- The replayer binds a **sliding window per arrival**: `to = now`, `from = now − 3d` (the seeder's `TimeSpan`), rendered as UTC Unix seconds exactly like the perf path. Scan size is then stationary: 3 days of history exists from preload (whose `TimeEnd` is pinned to run start) and grows at the same rate it ages out of the window.
- The monthly partition key (`toYYYYMM(time)`) means a run straddling a month boundary writes two partitions; recorded in the result (`partitions_touched`) but not prevented.

*Alternative considered:* fixed window bound at run start — rejected: queries would progressively ignore the data being inserted, defeating the interference measurement (`cogs validate`) and not matching production meter queries, which scan recent data as it lands.

### D5. Drivers

**Ingest (`bench/ingest/driver.go`).** Token bucket targeting `events_per_sec`; batch flush on `batch_max_rows` OR `flush_interval`, whichever first (reproduces the real sink's part-creation pattern — the thing that drives merge amplification; the bulk seeder deliberately doesn't). Each INSERT carries the `log_comment` tag (D6) and cell `SETTINGS`. Backpressure: no catch-up bursting; falling >5% behind target over the measure window sets `rate_satisfied: false` and flags the cell `saturated: true` (itself a finding: the shape can't absorb the rate). Emits achieved eps, batches, rows, client-side insert latency percentiles, error count.

**Replay (`bench/replay/`).** Renders the scenario's queries with the existing v1 parameter binding, except `from`/`to` which follow D4. Arrivals: Poisson (exponential inter-arrival at `qps`) or uniform. Class selected by weight per arrival; uniform within class. Cold arrivals (probability `cold_fraction`) run with `enable_filesystem_cache = 0` and tag `cache:"cold"`. In-flight cap `concurrency_cap`; queue time recorded separately from server time. Queries go through the Go native client, not `clickhouse-benchmark`: the cogs path needs per-query tagging, weighted mixes, and arrival processes, and its latency/CPU source of truth is server-side `system.query_log`, so the perf path's reason to shell out (client-timing skew) does not apply.

### D6. Tagging and collectors (`bench/accounting/`)

Every harness statement during a run sets:

```
SETTINGS log_comment = '{"cogs_run":"<run_id>","component":"ingest|query|seed|harness","class":"<mix class>","query":"<query name>","cache":"warm|cold"}'
```

Collectors (run in Collect, after the D2 flush; all read via `clusterAllReplicas`):

1. **Queries + inserts** from `query_log`: grouped by `(component, class, query_name, cache)` — count, `sum(ProfileEvents['OSCPUVirtualTimeMicroseconds'])` CPU, wall, p50/p95/p99, `read_rows/bytes`, `written_rows/bytes`, **`result_bytes`** (feeds the egress estimate), `max(memory_usage)`, S3 gets, filesystem-cache hit/miss bytes. `WHERE type = 'QueryFinish' AND event_time` in the measure window `AND log_comment` matches the run. Missing ProfileEvents keys read as 0; `cluster.version` recorded. Failed statements (`type IN ('ExceptionBeforeStart','ExceptionWhileProcessing')`) are collected into an `errors` block — counted, not priced.
2. **Async inserts** from `system.asynchronous_insert_log` + the flush queries in `query_log`: when a cell sets `async_insert`, per-statement `query_log` entries do not carry the parse/write cost — it lands in flush queries with no harness `log_comment`. The collector correlates flushes via `asynchronous_insert_log` (query ids / table) and books their CPU to `insert`. If correlation is incomplete, the result sets `async_attribution_partial: true` and the report carries the caveat. (This is exactly the `ingest-5k-async` cell.)
3. **Merges** from `part_log`: `event_type = 'MergeParts'`, scenario table, `event_time BETWEEN measure_start AND drain_end` — count, CPU, wall, read/written rows/bytes, peak memory. Merge CPU has no `log_comment`; attributed to ingest by construction (dedicated service, table only receives this run's inserts). `Mutate`/`NewPart` counts recorded for diagnostics only (summing NewPart CPU would double-count insert work already in `query_log`). If `part_log` lacks ProfileEvents on the connected version, estimate merge CPU as `wall_sec × avg merge threads` and set `merge_cpu_estimated: true`.
4. **Storage** from `system.parts` (active parts, scenario table): rows, compressed/uncompressed bytes, part count — at prepare, soak-end, drain-end. Settled bytes/event = drain-end delta over events ingested.
5. **Capacity** per D2.

### D7. Pricing engine: `bench/pricing/` + `pricing/<profile>.json`

```jsonc
{
  "name": "clickhouse-cloud-scale-aws-us-east-1",
  "currency": "USD",
  "service": { "replicas": 2, "gib_per_replica": 16, "vcpus_per_replica": 4, "gib_per_compute_unit": 8 },
  "rates": {
    "compute_unit_hour": 0.2985,
    "storage_tb_month": 25.30,
    "backup_multiplier": 1.0,          // backup bytes estimated as multiplier × compressed bytes (v1 estimate; no system-table source)
    "egress_per_gb_public": 0.1152,    // applied to result_bytes; estimate only
    "clickpipes": null                 // reserved
  },
  "as_of": "2026-06-09",
  "source": "https://clickhouse.com/pricing"
}
```

Cloud billing facts encoded by the model (values always in config, never code): compute metered per active minute in 8 GiB increments; storage billed on compressed bytes; idling stops compute billing on Scale/Enterprise, so the floor is `active_minutes × service_units`, not wall-clock.

Two modes, both always reported:

- **`billed-shape`** (reconciles with the invoice): `window_cost = compute_units × (measure_seconds/3600) × rate`; split proportionally by CPU-seconds across `{insert, merge, query}`; remainder booked as `idle_floor`.
- **`cpu-linear`** (marginal what-if): `$/cpu_sec = rate / (vcpus_per_unit × 3600)`; prices CPU-seconds directly.

Every priced number derives from a named, checked-in profile; the full profile is embedded in the result JSON. A `local-zero` profile (all rates 0) ships for pure resource-accounting runs.

### D8. Attribution + unit costs (`bench/cogs/attribution.go`)

```
cpu = { insert: Σ query_log(component=ingest) + async flush CPU,
        merge:  Σ part_log MergeParts CPU,
        query:  Σ query_log(component=query) per class × cache }
available_cpu_sec  = replicas × vcpus_per_replica × measure_seconds     (detected; cross-checked vs profile)
coverage           = (insert + merge + query) / available_cpu_sec
idle_cpu_sec       = max(0, available_cpu_sec − attributed)

unit costs (both modes):
  usd_per_1m_events           = (cost.insert + cost.merge) / events × 1e6   (+ insert/merge split)
  usd_per_1k_queries[class][cache]
  bytes_per_event_settled     = Δcompressed(prepare → drain_end) / events
  usd_per_1m_events_month_storage = bytes_per_event_settled × 1e6 / 1e12 × storage_tb_month × backup_multiplier
  egress_usd_estimate         = Σ result_bytes / 1e9 × egress_per_gb_public   (marked estimate)
  idle_floor_usd_per_service_month = compute_units × 730 × compute_unit_hour  (100%-active bound, shown next to measured idle share)
```

Peak memory per class is reported next to CPU (Cloud sizes compute units by RAM; a class that forces a bigger shape drives COGS through `gib_per_replica`, not CPU) but does not enter the v1 formula.

### D9. Query mix manifests: `scenarios/<name>/queries/mix.json`

Contract: every `*.sql` in the scenario's `queries/` dir must appear in exactly one class **or** in `exclude`; loading errors otherwise (forces conscious classification when queries are added). `mix.json` is safe alongside the query files — discovery only globs `*.sql`. Weights are placeholders shaped like the slow-query-log mapping until measured production frequencies replace them; `notes` is rendered into the report header as a caveat.

`scenarios/proposal/queries/mix.json` (all 34 on-disk queries accounted for):

```jsonc
{
  "production": {
    "notes": "weights are placeholders pending measured production frequencies",
    "classes": {
      "key_only":      { "weight": 10, "queries": ["count_total", "count_hour", "agent_runs_by_name", "kong_api_request_total", "distinct_subjects"] },
      "meter_agg":     { "weight": 70, "queries": ["sum_hour", "sum_day", "sum_month", "sum_total", "sum_total_by_subject", "sum_no_window", "avg_hour", "max_hour", "min_hour", "sum_minute", "sum_hour_tz", "sum_day_by_subject", "unique_count_hour", "latest_hour"] },
      "payload_heavy": { "weight": 19, "queries": ["kong_status_by_route", "llm_tokens_by_model", "kong_llm_tokens_total", "kong_api_request_by_method", "kong_api_request_by_service", "kong_api_request_by_all_dims", "sum_day_by_dim", "workload_seconds_by_region", "sum_hour_group1", "sum_hour_group1_group2", "sum_total_filter1", "sum_total_filter2"] },
      "lookup":        { "weight": 1,  "queries": ["lookup_by_id"] }
    },
    "exclude": ["sum_hour_group1_no_prewhere", "sum_hour_group1_group2_no_prewhere"]   // PREWHERE diagnostics, not production queries
  }
}
```

`scenarios/baseline-openmeter/queries/mix.json` is per-scenario (baseline has 30 queries: no `lookup_by_id`, no `kong_api_request_by_*` variants): same classes minus the missing queries, **no `lookup` class**, same `exclude` for the two `_no_prewhere` diagnostics. Cross-scenario `cogs compare` therefore compares matching classes and reports the class-set difference explicitly rather than failing.

### D10. Compare + validate

- `bench cogs compare <run-a> <run-b>`: by path or `<scenario>` shorthand (latest in `bench/results/<scenario>/cogs/`). Diffs unit costs per line item; refuses cross-profile price comparison unless `--allow-profile-mismatch` (then resource lines only: cpu_sec, bytes/event, coverage). Primary uses: same cell across table designs (the COGS verdict the perf tables imply but cannot state); same cell across service shapes (right-sizing).
- `bench cogs validate`: given ingest-only, query-only, and mixed runs over the same scenario/rates, check `cpu_linear` additivity: `|mixed − (ingest + query + baseline_idle)| / mixed ≤ 0.15` per component. FAIL names the diverging component (merge pressure inflating query CPU, cache contention). This is the cheap experiment that decides whether per-tenant COGS can be a linear formula.

### D11. Reconciliation (input-driven)

`--usage-export <file>` (or `cogs reconcile <run.json> <usage.json>`): versioned parser (`bench/accounting/usagefile.go`) over a Cloud usage/billing export; select rows overlapping the measure window; emit billed vs model compute-unit-hours and storage TB with `delta_pct`; `|delta| > 20` flags the run. No Cloud API client. Known delta sources (autoscaling movement, shared service, background system work) documented in the methodology README.

### D12. Idle cell semantics

The idle cell measures the **awake-but-unloaded floor**, not Cloud idling behavior: the harness's own activity (plateau polling, collectors, live connection) can keep a service from idling, and the default idling delay exceeds the CI measure window anyway. During measure the driver issues nothing and holds no keepalive traffic beyond the client's own ping; the README defines the cell accordingly. Measuring actual idle→wake economics needs the usage export (D11) and is a follow-up.

### D13. Result schema: `cogs/v1`

`bench/results/<scenario>/cogs/<timestamp>.json` — self-describing: full cell manifest and pricing profile inline, `harness_commit`, cluster fingerprint, phases (with `parts_plateau`), ingest (`rate_satisfied`, `saturated`), replay (per-class counts, queue p95), accounting (cpu split, coverage, `merge_cpu_estimated`, `async_attribution_partial`, `shape_mismatch`, `log_flush`, memory peaks, storage, io), costs (`billed_shape`, `cpu_linear`, unit card), reconciliation (nullable), errors. Sibling `.md` leads with the unit-cost card, then attribution split, per-class warm/cold costs, storage, flags, reconciliation. Existing perf result schema untouched.

## Risks / Trade-offs

- [Production mix weights are placeholders] → manifest is data; `notes` caveat rendered in every report; owner maps real frequencies from the live slow-query log.
- [`part_log` ProfileEvents varies by version] → `merge_cpu_estimated` fallback, flagged in results.
- [Cloud usage export schema drift] → versioned parser + fixture; reconciliation is optional input, never a dependency.
- [Shared-service contamination] → methodology requires a dedicated service; runner warns when it detects non-harness databases of nontrivial size.
- [Async-insert attribution incomplete on some versions] → `async_attribution_partial` flag; the async cell is explicitly a comparative cell, not a billing-grade number.
- [Memory-bound pricing] → v1 reports peak memory per class but prices CPU shares; if all-dims-style queries drive service sizing, follow-up adds a `max(cpu_share, mem_share)` mode.
- [Autoscaling during a window] → v1 pins the shape (operator); shape cross-check (D2) catches drift; follow-up prices per-hour unit counts from the usage export.
- [Month-boundary runs double partitions] → recorded `partitions_touched`; acceptable for monthly partitioning.

## Open Questions

1. Measured production query-class frequencies to replace placeholder weights (source: live slow-query log).
2. Whether `SYSTEM FLUSH LOGS ON CLUSTER` is permitted on Cloud for the service user; if not, the local-flush fallback's settle time may need tuning (logs flush every ~7.5s by default).
3. Exact Cloud usage-export schema to pin in the v1 parser (depends on the org's billing export access).
