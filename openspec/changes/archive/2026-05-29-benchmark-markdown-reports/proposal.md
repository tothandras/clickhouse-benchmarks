## Why

Benchmark results are written only as machine-readable JSON under `bench/results/`, which is `.gitignore`d — so a run leaves nothing human-readable or reviewable in the repo, and the accumulated history is local-only noise. At the same time the one durable human artifact, `bench/results/ANALYSIS-optimal-table.md`, lives in that same ignored directory and is invisible to anyone reading the repo on GitHub. We want each run to emit a readable markdown report that is committed alongside the JSON, fold the standing analysis into the top-level README, and start the results history clean.

## What Changes

- **Delete all existing benchmark results.** Remove the accumulated per-scenario JSON files under `bench/results/<scenario>/` so the history starts clean (the `.gitkeep` stays).
- **Fold the analysis into the README.** Move the content of `bench/results/ANALYSIS-optimal-table.md` into `README.md` (as a "Findings" section) and delete the standalone analysis file, so the optimal-table conclusion is visible on the repo landing page.
- **Track `bench/results/`.** Remove the `bench/results/` (and `bench/results/*.tsv`) entries from `.gitignore` so result JSON **and** the new markdown reports are committed going forward.
- **Generate a markdown report per run.** After each scenario run, alongside the existing `<timestamp>.json`, the harness writes a sibling `<timestamp>.md` summarizing that run: scenario, harness commit, ClickHouse version / topology, ingest stats, and a per-query table (latency percentiles, QPS, CPU, memory). The report is derived from the same `Run` record the JSON is built from — one source of truth, two renderings.

## Capabilities

### New Capabilities
- `benchmark-reports`: rendering a per-run human-readable markdown report from a completed `Run` record, written next to the JSON result file.

### Modified Capabilities
- `benchmark-harness`: the result-persistence requirement gains the markdown report as a second output of each run, and the requirement that result files (JSON + markdown) are tracked in git rather than ignored.

## Impact

- **Code**: new `bench/runner/report.go` (markdown renderer) + test; `bench/runner/results.go` / `bench/cmd/bench/main.go` call it after `Write`.
- **Repo**: `bench/results/<scenario>/*.json` deleted; `bench/results/ANALYSIS-optimal-table.md` deleted (content merged into `README.md`); `.gitignore` loses the `bench/results/` lines; `README.md` gains a Findings section.
- **No behavioral change to measurement** — reports are an additive rendering of data already captured; latency/CPU numbers are unchanged.
- **Risk**: low. The only cross-cutting change is that `bench/results/` is now tracked, so future runs produce committable artifacts (and a noticed diff) rather than ignored files.
