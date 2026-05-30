package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// WriteReport renders run as a human-readable markdown report next to its JSON
// result file, at bench/results/<scenario>/<timestamp>.md (same basename as the
// JSON). It is derived from the same Run record Write serializes, so the two
// renderings cannot diverge. queries supplies the SQL text rendered in each
// query's collapsed details section, matched against BenchResult.Name; pass nil
// to omit the SQL sections (the table is still rendered). Returns the absolute
// path written.
func WriteReport(resultsRoot string, run Run, queries []Query) (string, error) {
	dir := filepath.Join(resultsRoot, run.Scenario)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	name := run.StartedAt.UTC().Format("2006-01-02T15-04-05Z") + ".md"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(renderReport(run, queries)), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func renderReport(run Run, queries []Query) string {
	sqlByName := make(map[string]string, len(queries))
	for _, q := range queries {
		sqlByName[q.Name] = q.SQL
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Benchmark: %s\n\n", run.Scenario)
	renderHeader(&b, run)
	b.WriteString("\n")
	renderIngest(&b, run.Ingest)
	b.WriteString("\n")
	renderQueries(&b, run.Queries, sqlByName, run.Repeat)
	return b.String()
}

func renderHeader(b *strings.Builder, run Run) {
	fp := run.ClusterFingerprint
	topology := "multi-node"
	if fp.IsSingleNode {
		topology = "single-node"
	}
	clusters := "—"
	if len(fp.Clusters) > 0 {
		clusters = strings.Join(fp.Clusters, ", ")
	}
	b.WriteString("| Field | Value |\n")
	b.WriteString("| --- | --- |\n")
	fmt.Fprintf(b, "| Harness commit | `%s` |\n", run.HarnessCommit)
	fmt.Fprintf(b, "| ClickHouse | %s (%s) |\n", fp.Version, topology)
	fmt.Fprintf(b, "| Clusters | %s |\n", clusters)
	fmt.Fprintf(b, "| Sweep id | `%s` |\n", run.SweepID)
	fmt.Fprintf(b, "| Concurrency | %s |\n", joinInts(run.Concurrency))
	if run.Repeat > 1 {
		fmt.Fprintf(b, "| Repeats | %d |\n", run.Repeat)
	}
	fmt.Fprintf(b, "| Started | %s |\n", run.StartedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(b, "| Finished | %s |\n", run.FinishedAt.UTC().Format(time.RFC3339))
}

func renderIngest(b *strings.Builder, ing *IngestResult) {
	b.WriteString("## Ingest\n\n")
	if ing == nil {
		b.WriteString("Seeding skipped (queries ran against pre-existing data).\n")
		return
	}
	fmt.Fprintf(b, "Source `%s`: %d rows in %.2fs (%.0f events/sec)",
		ing.Source, ing.Rows, ing.DurationSeconds, ing.EventsPerSecond)
	if ing.BatchSize > 0 {
		fmt.Fprintf(b, ", batch=%d", ing.BatchSize)
	}
	if ing.AsyncInsert {
		b.WriteString(", async_insert=1")
	}
	b.WriteString(".\n")
}

func renderQueries(b *strings.Builder, queries []BenchResult, sqlByName map[string]string, repeat int) {
	b.WriteString("## Queries\n\n")
	if len(queries) == 0 {
		b.WriteString("No queries run.\n")
		return
	}
	b.WriteString("| Query | c | cache | p50 ms | p95 ms | p99 ms | QPS | CPU p50 ms | CPU src | Mem MB |\n")
	b.WriteString("| --- | ---: | --- | ---: | ---: | ---: | ---: | ---: | --- | ---: |\n")
	for _, q := range queries {
		cache := q.CacheState
		if cache == "" {
			cache = "warm"
		}
		if q.Error != "" {
			fmt.Fprintf(b, "| %s | %d | %s | error: %s |\n", q.Name, q.Concurrency, cache, q.Error)
			continue
		}
		cpu, mem := "n/a", "n/a"
		src := "n/a"
		if q.CPUp50Us != nil {
			cpu = fmt.Sprintf("%.1f", *q.CPUp50Us/1000)
		}
		if q.MemAvgBytes != nil {
			mem = fmt.Sprintf("%.1f", *q.MemAvgBytes/(1024*1024))
		}
		if q.CPUSource != "" {
			src = q.CPUSource
		}
		fmt.Fprintf(b, "| %s | %d | %s | %.1f | %.1f | %.1f | %.1f | %s | %s | %s |\n",
			q.Name, q.Concurrency, cache, q.P50Sec*1000, q.P95Sec*1000, q.P99Sec*1000, q.QPS, cpu, src, mem)
	}

	renderRepeatSpread(b, repeat, queries)
	renderIndexPruning(b, queries)

	// Collapsed SQL once per distinct query name, so a reader can audit what was
	// actually measured (the table above may list a query several times across
	// concurrency / cache / repeat variants).
	seen := map[string]bool{}
	for _, q := range queries {
		if seen[q.Name] {
			continue
		}
		seen[q.Name] = true
		sql, ok := sqlByName[q.Name]
		if !ok || strings.TrimSpace(sql) == "" {
			continue
		}
		fmt.Fprintf(b, "\n<details>\n<summary><code>%s</code></summary>\n\n```sql\n%s\n```\n\n</details>\n",
			q.Name, strings.TrimRight(sql, "\n"))
	}
}

// renderRepeatSpread summarizes run-to-run variance when a scenario was run
// with --repeat > 1. For each (query, concurrency, cache) group it reports the
// median p50 and the min/max p50 across repeats, so a reported delta can be
// judged against the noise floor. Reuse-seed repeats (the harness default)
// surface query-time variance (cache, scheduler, contention).
func renderRepeatSpread(b *strings.Builder, repeat int, queries []BenchResult) {
	if repeat <= 1 {
		return
	}
	type key struct {
		name  string
		conc  int
		cache string
	}
	groups := map[key][]float64{}
	var order []key
	for _, q := range queries {
		if q.Error != "" {
			continue
		}
		k := key{q.Name, q.Concurrency, q.CacheState}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], q.P50Sec*1000)
	}
	b.WriteString("\n### Run-to-run spread (p50 ms across repeats)\n\n")
	b.WriteString("| Query | c | cache | median | min | max |\n")
	b.WriteString("| --- | ---: | --- | ---: | ---: | ---: |\n")
	for _, k := range order {
		vs := append([]float64(nil), groups[k]...)
		sort.Float64s(vs)
		cache := k.cache
		if cache == "" {
			cache = "warm"
		}
		fmt.Fprintf(b, "| %s | %d | %s | %.1f | %.1f | %.1f |\n",
			k.name, k.conc, cache, median(vs), vs[0], vs[len(vs)-1])
	}
}

func median(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// renderIndexPruning emits the EXPLAIN granule-pruning evidence for any query
// that carries it (lookup_by_id against a bloom_filter on id). Reported once;
// the signal is a property of the table+index, not the timed iterations.
func renderIndexPruning(b *strings.Builder, queries []BenchResult) {
	seen := map[string]bool{}
	wrote := false
	for _, q := range queries {
		p := q.IndexPruning
		if p == nil || seen[q.Name] {
			continue
		}
		seen[q.Name] = true
		if !wrote {
			b.WriteString("\n### Index pruning (`EXPLAIN indexes=1`, literal id)\n\n")
			b.WriteString("| Query | granules (no index → index) | parts (no index → index) |\n")
			b.WriteString("| --- | ---: | ---: |\n")
			wrote = true
		}
		fmt.Fprintf(b, "| %s | %d → %d | %d → %d |\n",
			q.Name, p.GranulesWithout, p.GranulesWith, p.PartsWithout, p.PartsWith)
	}
}

func joinInts(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprintf("%d", x)
	}
	return strings.Join(parts, ", ")
}
