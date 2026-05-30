package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/openmeterio/ch-playground/bench/seed"
)

// IngestResult records the seeding phase outcome.
type IngestResult struct {
	Source          string  `json:"source"` // "go-seeder" or "seed.sql" or "none"
	Rows            int     `json:"rows"`
	DurationSeconds float64 `json:"duration_seconds"`
	EventsPerSecond float64 `json:"events_per_second"`
	BatchSize       int     `json:"batch_size,omitempty"`
	AsyncInsert     bool    `json:"async_insert,omitempty"`
}

// ClusterFingerprint captures enough cluster metadata to read a result file
// in isolation later.
type ClusterFingerprint struct {
	Version      string   `json:"version"`
	IsSingleNode bool     `json:"is_single_node"`
	Clusters     []string `json:"clusters,omitempty"`
}

// Run captures one full benchmark run for one scenario.
type Run struct {
	Scenario           string             `json:"scenario"`
	HarnessCommit      string             `json:"harness_commit"`
	SweepID            string             `json:"sweep_id"`
	StartedAt          time.Time          `json:"started_at"`
	FinishedAt         time.Time          `json:"finished_at"`
	ClusterFingerprint ClusterFingerprint `json:"cluster"`
	Concurrency        IntList            `json:"concurrency"`
	Repeat             int                `json:"repeat,omitempty"`
	Ingest             *IngestResult      `json:"ingest,omitempty"`
	Queries            []BenchResult      `json:"queries"`
}

// IntList marshals as a JSON array but unmarshals from either a JSON array
// (new multi-level runs) or a bare number (result files written before the
// concurrency sweep, when `concurrency` was a scalar). This keeps `bench
// compare` and any other reader working against the pre-sweep committed files.
type IntList []int

func (l *IntList) UnmarshalJSON(b []byte) error {
	var arr []int
	if err := json.Unmarshal(b, &arr); err == nil {
		*l = arr
		return nil
	}
	var n int
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("concurrency: want number or array, got %s", b)
	}
	*l = []int{n}
	return nil
}

// QueryLogAvailable reports whether system.query_log exists and is queryable
// on the target. CPU capture is skipped (fields left nil) when it isn't.
func QueryLogAvailable(ctx context.Context, conn driver.Conn) bool {
	var n uint8
	// EXISTS returns one row, 0/1. A driver/permission error means "treat as
	// unavailable" rather than aborting the run.
	if err := conn.QueryRow(ctx, "EXISTS TABLE system.query_log").Scan(&n); err != nil {
		return false
	}
	return n == 1
}

// NewCPUProbe returns a CPUProbe bound to conn. It flushes logs, then reads
// OSCPUVirtualTimeMicroseconds p50/p95 + avg memory_usage for the rows tagged
// with logComment. Returns (nil, nil) when no matching rows are found after a
// brief retry — the run still completes, the query just has no CPU data.
func NewCPUProbe(conn driver.Conn) CPUProbe {
	return func(ctx context.Context, logComment string) (*CPUStats, error) {
		read := func() (*CPUStats, bool, error) {
			if err := conn.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
				return nil, false, fmt.Errorf("flush logs: %w", err)
			}
			// Read both the OS CPU counter and the RealTime fallback. OS thread
			// CPU counters are unavailable on some hosts (ClickHouse inside
			// Docker on macOS reports them as 0); RealTimeMicroseconds (summed
			// thread wall-time) is the proxy there and equals CPU time for the
			// CPU-bound aggregation queries this harness runs.
			const q = `SELECT
			    count() AS n,
			    quantile(0.5)(ProfileEvents['OSCPUVirtualTimeMicroseconds']) AS os_p50,
			    quantile(0.95)(ProfileEvents['OSCPUVirtualTimeMicroseconds']) AS os_p95,
			    toFloat64(max(ProfileEvents['OSCPUVirtualTimeMicroseconds'])) AS os_max,
			    quantile(0.5)(ProfileEvents['RealTimeMicroseconds']) AS rt_p50,
			    quantile(0.95)(ProfileEvents['RealTimeMicroseconds']) AS rt_p95,
			    avg(memory_usage) AS mem_avg
			  FROM system.query_log
			  WHERE log_comment = ? AND type = 'QueryFinish'`
			var (
				n                          uint64
				osP50, osP95, osMax        float64
				rtP50, rtP95, memAvg       float64
			)
			if err := conn.QueryRow(ctx, q, logComment).Scan(&n, &osP50, &osP95, &osMax, &rtP50, &rtP95, &memAvg); err != nil {
				return nil, false, fmt.Errorf("query_log read: %w", err)
			}
			if n == 0 {
				return nil, false, nil
			}
			if osMax > 0 {
				return &CPUStats{CPUp50Us: osP50, CPUp95Us: osP95, MemAvgBytes: memAvg, Source: "os_cpu"}, true, nil
			}
			return &CPUStats{CPUp50Us: rtP50, CPUp95Us: rtP95, MemAvgBytes: memAvg, Source: "real_time"}, true, nil
		}

		stats, ok, err := read()
		if err != nil {
			return nil, err
		}
		if ok {
			return stats, nil
		}
		// query_log flushes asynchronously; one retry covers the race.
		time.Sleep(200 * time.Millisecond)
		stats, ok, err = read()
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, nil
		}
		return stats, nil
	}
}

// FromSeedResult converts a seed.Result into the JSON-serialisable IngestResult.
func FromSeedResult(r seed.Result) *IngestResult {
	return &IngestResult{
		Source:          "go-seeder",
		Rows:            r.Rows,
		DurationSeconds: r.Duration.Seconds(),
		EventsPerSecond: r.EventsPerSecond,
		BatchSize:       r.BatchSize,
		AsyncInsert:     r.AsyncInsert,
	}
}

// Fingerprint probes the cluster and returns a fingerprint suitable for
// disambiguating result files later.
//
// IsSingleNode is determined by topology, not by cluster name. The official
// ClickHouse operator renders the operator-managed cluster as `default`
// (same name single-node installs use), so a name-only heuristic would
// false-positive. We instead probe shard/replica counts from system.clusters
// and call it single-node only when every cluster has exactly 1 shard with
// exactly 1 replica.
func Fingerprint(ctx context.Context, conn driver.Conn) (ClusterFingerprint, error) {
	var fp ClusterFingerprint
	if err := conn.QueryRow(ctx, "SELECT version()").Scan(&fp.Version); err != nil {
		return fp, fmt.Errorf("probe version: %w", err)
	}

	rows, err := conn.Query(ctx, "SELECT cluster, max(shard_num) AS shards, max(replica_num) AS replicas FROM system.clusters GROUP BY cluster ORDER BY cluster")
	if err != nil {
		return fp, nil
	}
	defer rows.Close()

	multiNode := false
	for rows.Next() {
		var (
			cluster      string
			shards, reps uint32
		)
		if err := rows.Scan(&cluster, &shards, &reps); err != nil {
			continue
		}
		fp.Clusters = append(fp.Clusters, cluster)
		// "test_*" clusters are server-side test fixtures that ship with
		// every CH install — skip them when deciding single-node-ness.
		if strings.HasPrefix(cluster, "test_") {
			continue
		}
		if shards > 1 || reps > 1 {
			multiNode = true
		}
	}
	fp.IsSingleNode = !multiNode
	return fp, nil
}

// HarnessCommit returns the current git HEAD short SHA, suffixed with
// "-dirty" if the working tree has uncommitted changes. Returns "unknown" if
// any git command fails (detached state, no git binary, run outside repo).
// The dirty suffix matters: results from a dirty tree are not reproducible
// from the recorded commit alone.
func HarnessCommit() string {
	shaCmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	out, err := shaCmd.Output()
	if err != nil {
		return "unknown"
	}
	sha := strings.TrimSpace(string(out))
	statusCmd := exec.Command("git", "status", "--porcelain")
	if status, err := statusCmd.Output(); err == nil && len(strings.TrimSpace(string(status))) > 0 {
		sha += "-dirty"
	}
	return sha
}

// Write persists a Run to bench/results/<scenario>/<RFC3339-timestamp>.json.
// Creates the per-scenario directory if needed. Returns the absolute path written.
func Write(resultsRoot string, run Run) (string, error) {
	dir := filepath.Join(resultsRoot, run.Scenario)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	name := run.StartedAt.UTC().Format("2006-01-02T15-04-05Z") + ".json"
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(run); err != nil {
		return "", err
	}
	return path, nil
}
