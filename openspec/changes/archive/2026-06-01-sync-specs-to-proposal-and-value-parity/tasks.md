# Tasks

This change is a spec-only reconciliation: the implementation it documents is
already built and committed. Tasks are the verification that each spec delta
matches the shipped reality, then the archive.

## 1. Verify the deltas against the code

- [x] 1.1 `proposal-scenario`: `scenarios/proposal/init.sql` defines
  `proposal_events` (`data JSON CODEC(ZSTD(3))`, `bloom_filter` on `id`), the
  `proposal_llm_tokens_rollup`/`_mv` (dims-full, `sumState` UInt64) and the
  `proposal_api_request_rollup`/`_mv` (dims-bounded countState, documented
  negative result); `scenarios/proposal/meters.yaml` holds the two canonical
  meter definitions; hybrid queries (`*_hybrid.sql`) and `lookup_by_id.sql`
  exist under `queries/`.
- [x] 1.2 `value-parity`: `bench/runner/digest.go` (`DigestResult`),
  `Run.ValueParity` in `bench/runner/results.go`, and `compareValues` in
  `bench/cmd/bench/compare.go` (window-gated digest diff, non-zero exit on
  mismatch).
- [x] 1.3 `baseline-openmeter-scenario`: `scenarios/baseline-openmeter/init.sql`
  creates `baseline_openmeter_events` with the upstream column set / engine /
  partition / order-by / minmax skip index.
- [x] 1.4 `cache-state-control`: `--cold-paired` flag in `bench/cmd/bench/main.go`;
  `CacheState` per `BenchResult` in `bench/runner/benchmark.go`.
- [x] 1.5 `result-comparison`: `compare.go` prints both the p50/CPU/ingest delta
  table and the value-parity table.
- [x] 1.6 `benchmark-harness`: `--time-end` flag and `resolveTimeEnd` in
  `main.go`; `cfg.TimeEnd` shared across scenarios when set.

## 2. Validate and archive

- [x] 2.1 `openspec validate 2026-06-01-sync-specs-to-proposal-and-value-parity --strict`
- [x] 2.2 Archive the change so the deltas fold into `openspec/specs/`.
