package runner

import (
	"strings"
	"testing"
	"time"
)

func TestRenderReport(t *testing.T) {
	cpu := 12345.6
	mem := 7.0 * 1024.0 * 1024.0 // 7 MiB, as float64
	run := Run{
		Scenario:      "data-as-json",
		HarnessCommit: "abc1234-dirty",
		SweepID:       "20260529T120000Z",
		StartedAt:     time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC),
		FinishedAt:    time.Date(2026, 5, 29, 12, 0, 30, 0, time.UTC),
		ClusterFingerprint: ClusterFingerprint{
			Version:      "26.2.5.45",
			IsSingleNode: true,
			Clusters:     []string{"default", "test_cluster_one_shard_three_replicas"},
		},
		Concurrency: 1,
		Ingest: &IngestResult{
			Source:          "go-seeder",
			Rows:            1_000_000,
			DurationSeconds: 4.2,
			EventsPerSecond: 238_095,
			BatchSize:       10_000,
		},
		Queries: []BenchResult{
			{
				Name:        "sum_hour",
				P50Sec:      0.018, P95Sec: 0.024, P99Sec: 0.030,
				QPS:         55.5,
				CPUp50Us:    &cpu,
				MemAvgBytes: &mem,
				CPUSource:   "os_cpu",
			},
			{
				Name:   "lookup_by_id",
				P50Sec: 0.007, P95Sec: 0.012, P99Sec: 0.041,
				QPS:    600,
				// CPU* nil → query_log unavailable
			},
			{
				Name:  "broken_query",
				Error: "syntax error near FOO",
			},
		},
	}

	queries := []Query{
		{Name: "sum_hour", SQL: "SELECT 1 -- sum_hour body"},
		{Name: "lookup_by_id", SQL: "SELECT 1 -- lookup body"},
		{Name: "broken_query", SQL: "FOO"},
	}
	out := renderReport(run, queries)

	mustContain := []string{
		"# Benchmark: data-as-json",
		"abc1234-dirty",
		"26.2.5.45 (single-node)",
		"default, test_cluster_one_shard_three_replicas",
		"20260529T120000Z",
		"2026-05-29T12:00:00Z",
		"## Ingest",
		"go-seeder",
		"1000000 rows",
		"batch=10000",
		"## Queries",
		"| sum_hour | 18.0 | 24.0 | 30.0 | 55.5 | 12.3 | os_cpu | 7.0 |",
		"| lookup_by_id | 7.0 | 12.0 | 41.0 | 600.0 | n/a | n/a | n/a |",
		"| broken_query | error: syntax error near FOO |",
		"<details>",
		"<summary><code>sum_hour</code></summary>",
		"SELECT 1 -- sum_hour body",
		"</details>",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Errorf("report missing expected substring %q\n---report---\n%s", want, out)
		}
	}
}

func TestRenderReportSeedingSkipped(t *testing.T) {
	run := Run{
		Scenario:   "baseline-openmeter",
		StartedAt:  time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC),
		FinishedAt: time.Date(2026, 5, 29, 12, 0, 10, 0, time.UTC),
		Ingest:     nil,
		Queries:    nil,
	}
	out := renderReport(run, nil)
	if !strings.Contains(out, "Seeding skipped") {
		t.Errorf("missing seeding-skipped marker:\n%s", out)
	}
	if !strings.Contains(out, "No queries run.") {
		t.Errorf("missing empty-queries marker:\n%s", out)
	}
}

