package runner

import (
	"fmt"
	"os"
	"path/filepath"
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
	renderQueries(&b, run.Queries, sqlByName)
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
	fmt.Fprintf(b, "| Concurrency | %d |\n", run.Concurrency)
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

func renderQueries(b *strings.Builder, queries []BenchResult, sqlByName map[string]string) {
	b.WriteString("## Queries\n\n")
	if len(queries) == 0 {
		b.WriteString("No queries run.\n")
		return
	}
	b.WriteString("| Query | p50 ms | p95 ms | p99 ms | QPS | CPU p50 ms | CPU src | Mem MB |\n")
	b.WriteString("| --- | ---: | ---: | ---: | ---: | ---: | --- | ---: |\n")
	for _, q := range queries {
		if q.Error != "" {
			fmt.Fprintf(b, "| %s | error: %s |\n", q.Name, q.Error)
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
		fmt.Fprintf(b, "| %s | %.1f | %.1f | %.1f | %.1f | %s | %s | %s |\n",
			q.Name, q.P50Sec*1000, q.P95Sec*1000, q.P99Sec*1000, q.QPS, cpu, src, mem)
	}

	// Collapsed SQL per query, so a reader can audit what was actually measured.
	for _, q := range queries {
		sql, ok := sqlByName[q.Name]
		if !ok || strings.TrimSpace(sql) == "" {
			continue
		}
		fmt.Fprintf(b, "\n<details>\n<summary><code>%s</code></summary>\n\n```sql\n%s\n```\n\n</details>\n",
			q.Name, strings.TrimRight(sql, "\n"))
	}
}
