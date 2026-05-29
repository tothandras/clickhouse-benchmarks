## 1. Event-type catalog in the seeder

- [x] 1.1 Add an `EventType` struct to `bench/seed/seed.go` (`Name string`, `Weight int`, `BuildData func(rng *rand.Rand) map[string]any`) and an `EventTypes []EventType` field on `Config`.
- [x] 1.2 Implement payload builders for five types: a retained baseline `api_request` carrying `{value, group1, group2}`; an HTTP/API type with request/response fields (request_method, response_http_status weighted to "200", route/service ids); an LLM type with `tokens`/`model`/`provider`/`http_status`; a workload type with `duration_seconds`/`region`/`zone`/`instance_type`; an agent type with `agent_name`. Numeric fields emitted as JSON strings.
- [x] 1.3 Populate `DefaultConfig().EventTypes` with the five builders and realistic weights (baseline `api_request` dominant ≥~50%, others sharing the rest). Drop the now-unused flat `Types` list (or keep it only as the source of `Name`s) so `type` written per row comes from the chosen `EventType`.

## 2. Weighted, deterministic selection

- [x] 2.1 In `Run`, build a cumulative-weight table from `cfg.EventTypes` once before the row loop; per row draw `rng.IntN(totalWeight)` to pick the type, then call its `BuildData(rng)`. Write the chosen type's `Name` into the `type` column and `json.Marshal` of its payload into `data`.
- [x] 2.2 Preserve draw-order determinism: type-pick draw then payload draws happen in a fixed sequence per row so the RNG stream is stable. Validate `cfg.EventTypes` non-empty and total weight > 0.
- [x] 2.3 Add/extend a seeder test asserting two runs with the same seed + catalog produce byte-identical (type, data) sequences, and that weighted shares roughly track the configured weights over a large N.

## 3. Per-type benchmark queries

- [x] 3.1 Author per-type queries in `scenarios/baseline-openmeter/queries/`: e.g. `llm_tokens_by_model.sql` (sum `toFloat64OrNull(JSON_VALUE(data,'$.tokens'))` group by `$.model`/`$.provider`, `type='llm_request'`), `kong_status_by_route.sql` (count group by `$.response_http_status`/`$.route_name`, `type='api_request_http'` or the HTTP type's name), `workload_seconds_by_region.sql` (sum `$.duration_seconds` group by `$.region`), `agent_runs_by_name.sql` (count group by `$.agent_name`). Embed the literal `type` filter where it differs from the default param; keep `tumbleStart`/`tumbleEnd` windowing and time/namespace filters in the OpenMeter shape.
- [x] 3.2 Copy the new per-type query files byte-for-byte into the other swept scenarios that use file-based queries (`data-as-json` adapting JSON access syntax as that scenario already does; `time-desc`, `with-projections`, `materialized-columns` unchanged — materialized-columns intentionally still calls `JSON_VALUE` for the non-materialized types).
- [x] 3.3 Confirm every new query parses and binds under the harness's fixed param set (run one scenario at `--rows 100000 --iterations 3` and verify the new queries appear in the result file with non-null latency).

## 4. Docs

- [x] 4.1 Update `bench/seed/` package doc comment and `scenarios/README.md` to describe the heterogeneous event-type mix and that one baseline type retains `value/group1/group2`.

## 5. Re-sweep + re-rank

- [x] 5.1 Rebuild the harness. Start single-node ClickHouse (port 9100) with `query_log` enabled.
- [x] 5.2 Run the 10M sweep across all single-node candidates (`baseline-openmeter`, `data-as-json`, `time-desc`, `with-projections`, `materialized-columns`) at `--rows 10000000 --iterations 10 --seed 42`, dropping `om_events` between each — now seeding heterogeneous data.
- [x] 5.3 Aggregate the new result files: per scenario, per query, tabulate `cpu_p50_us`, `p50_sec`, `mem_avg_bytes`; also record the per-type row-count mix the seeder produced (the denominator for interpreting per-type query CPU).
- [x] 5.4 Update `bench/results/ANALYSIS-optimal-table.md`: add a "Heterogeneous-data re-rank" section keeping the prior uniform-data ranking for contrast, report the new winner and the per-type coverage of `materialized-columns`, and state honestly whether/why materialized-columns still wins under realistic data.
- [x] 5.5 Run `openspec validate heterogeneous-event-types-seeder` and confirm it is valid.
