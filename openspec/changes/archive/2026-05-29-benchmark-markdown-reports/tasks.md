## 1. Repo cleanup

- [x] 1.1 Delete all existing result JSON under `bench/results/<scenario>/` (keep `bench/results/.gitkeep`).
- [x] 1.2 Remove the `bench/results/` and `bench/results/*.tsv` lines from `.gitignore`; keep the `/data/`, Go, and editor entries.
- [x] 1.3 Move the content of `bench/results/ANALYSIS-optimal-table.md` into `README.md` as a top-level "Findings" section, then delete the standalone analysis file. Verify no other file links to the old path (`git grep ANALYSIS-optimal-table`).

## 2. Markdown report renderer

- [x] 2.1 Add `bench/runner/report.go` with `WriteReport(resultsRoot string, run Run) (string, error)` that renders the `Run` to `bench/results/<scenario>/<timestamp>.md` (same basename as the JSON; reuse `run.StartedAt.UTC().Format("2006-01-02T15-04-05Z")`).
- [x] 2.2 Render the header block: scenario, harness commit (with `-dirty`), ClickHouse version + `is_single_node` + clusters, started/finished, sweep id, concurrency.
- [x] 2.3 Render the ingest summary line (source, rows, duration, events/sec, batch/async settings) or an explicit "seeding skipped" marker when `run.Ingest` is nil.
- [x] 2.4 Render the per-query markdown table: name, p50/p95/p99 ms, QPS, CPU p50 ms + source, memory MB. Render `n/a` for nil CPU/memory pointers; render the `error` string in place of metrics for an errored query.
- [x] 2.5 Call `WriteReport` from `bench/cmd/bench/main.go` right after `runner.Write`, and print the report path next to the JSON path.

## 3. Tests + verification

- [x] 3.1 Add `bench/runner/report_test.go`: render a representative `Run` (including one nil-CPU query and one errored query) and assert the header, ingest line, and table rows are present and that `n/a` / the error message appear correctly.
- [x] 3.2 `go build ./bench/...` and `go test ./bench/...` pass.
- [x] 3.3 Run one scenario end-to-end against a local ClickHouse and confirm both `<timestamp>.json` and `<timestamp>.md` are written, the `.md` is well-formed, and neither is git-ignored (`git status` shows them).
- [x] 3.4 `openspec validate benchmark-markdown-reports` reports valid.
