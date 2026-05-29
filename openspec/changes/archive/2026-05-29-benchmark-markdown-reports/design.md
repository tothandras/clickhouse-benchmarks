## Context

Today a run produces one `bench/results/<scenario>/<RFC3339-timestamp>.json` via `runner.Write`, built from a single `runner.Run` record (`bench/runner/results.go`). That directory is `.gitignore`d, so nothing reviewable lands in the repo, and the standing conclusions live in a sibling `bench/results/ANALYSIS-optimal-table.md` that is equally invisible on GitHub. This change makes `bench/results/` tracked, replaces the standalone analysis with a README section, clears the stale JSON history, and adds a per-run markdown rendering of the same `Run` record.

## Goals / Non-Goals

**Goals:**
- One human-readable markdown report per scenario run, written next to the JSON, from the same `Run` record (no second data path, no risk of the two disagreeing).
- `bench/results/` tracked in git so JSON + markdown are committable.
- The optimal-table findings visible from `README.md`.
- A clean results history (old JSON removed).

**Non-Goals:**
- No change to how measurements are taken (latency, CPU, memory capture all unchanged).
- No aggregation/comparison report across runs or scenarios — one report describes exactly one run. Cross-run analysis stays a manual/README concern.
- No HTML, no charts — plain GitHub-flavored markdown only.
- No new CLI flag; report generation is unconditional (it is cheap and additive).

## Decisions

**1. Render from the `Run` record, after `Write`, in the runner package.**
Add `runner.WriteReport(resultsRoot string, run Run) (string, error)` in a new `bench/runner/report.go`, called immediately after `runner.Write` in `bench/cmd/bench/main.go`. The markdown file shares the JSON's basename (`<timestamp>.md`) in the same `bench/results/<scenario>/` directory. Rationale: the `Run` struct already carries everything a report needs (scenario, commit, fingerprint, ingest, per-query results); rendering from it guarantees JSON and markdown never diverge. Alternative considered — render inside `Write` and return both paths — rejected to keep `Write` single-responsibility (JSON encoding) and make the report independently testable.

**2. Report content = the JSON, formatted.** Header block (scenario, harness commit, ClickHouse version + `is_single_node` + clusters, started/finished, sweep id, concurrency), an ingest line (rows, duration, events/sec, batch settings — or "skipped" when absent), and one query table. Table columns: query name, p50/p95/p99 ms, QPS, CPU p50 ms + source (`os_cpu`/`real_time`), memory MB. CPU/memory cells render `n/a` when the pointer fields are nil (query_log unavailable). Errored queries render their `error` string in place of metrics. Rationale: mirrors the existing console output and the JSON fields a reviewer cares about; the full percentile spread stays in the JSON for anyone who needs it.

**3. Fold the analysis into README under a "Findings" heading, delete the standalone file.** The current `bench/results/ANALYSIS-optimal-table.md` content moves verbatim (already condensed) into `README.md`. Rationale: the proposal wants the conclusion on the landing page; keeping two copies would drift. The per-run reports describe individual runs; the README Findings section is the synthesized conclusion — distinct roles, no overlap.

**4. Un-ignore `bench/results/` wholesale.** Remove both `bench/results/` and `bench/results/*.tsv` lines from `.gitignore`. Rationale: the point of the change is that these artifacts are now committable. The `.gitkeep` is retained so the directory exists on a fresh clone before any run.

## Risks / Trade-offs

- **Committed results grow the repo over time** → Reports + JSON are small (one scenario run ≈ a few KB). If volume ever becomes a problem, a retention policy (keep latest N per scenario) is a separate future change; out of scope here.
- **A dirty working tree taints `harness_commit`** → already handled: `HarnessCommit()` appends `-dirty`, and the report surfaces it, so a non-reproducible run is visible in the committed artifact rather than silently misleading.
- **Markdown renderer drifts from the JSON schema if `Run`/`BenchResult` fields change** → renderer reads the same struct the JSON encoder does; a unit test asserts a representative `Run` (including a nil-CPU query and an errored query) renders the expected sections, catching field omissions.
- **Deleting old JSON loses local history** → intended (proposal asks for a clean start); the history was untracked/local-only, so nothing committed is lost.
