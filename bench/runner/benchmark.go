package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// BenchResult captures the metrics clickhouse-benchmark emits for a single query.
// Percentiles are in seconds, matching clickhouse-benchmark's text output.
// Throughput fields come from the per-host summary line.
type BenchResult struct {
	Name        string  `json:"name"`
	Concurrency int     `json:"concurrency"`
	Iterations  int     `json:"iterations"`
	Error       string  `json:"error,omitempty"`

	// Percentiles in seconds (clickhouse-benchmark reports to ms precision).
	P0Sec    float64 `json:"p0_sec"`
	P10Sec   float64 `json:"p10_sec"`
	P20Sec   float64 `json:"p20_sec"`
	P30Sec   float64 `json:"p30_sec"`
	P40Sec   float64 `json:"p40_sec"`
	P50Sec   float64 `json:"p50_sec"`
	P60Sec   float64 `json:"p60_sec"`
	P70Sec   float64 `json:"p70_sec"`
	P80Sec   float64 `json:"p80_sec"`
	P90Sec   float64 `json:"p90_sec"`
	P95Sec   float64 `json:"p95_sec"`
	P99Sec   float64 `json:"p99_sec"`
	P999Sec  float64 `json:"p99_9_sec"`
	P9999Sec float64 `json:"p99_99_sec"`

	// Throughput from the per-host summary line.
	QPS            float64 `json:"qps"`
	RPS            float64 `json:"rps"`
	MiBPerSec      float64 `json:"mib_per_sec"`
	ResultRPS      float64 `json:"result_rps"`
	ResultMiBPerSec float64 `json:"result_mib_per_sec"`

	// CPU and memory from system.query_log, correlated via log_comment.
	// Nil when query_log was unavailable on the target (see CPUProbe).
	// CPU is OSCPUVirtualTimeMicroseconds (total CPU across all query threads).
	CPUp50Us    *float64 `json:"cpu_p50_us"`
	CPUp95Us    *float64 `json:"cpu_p95_us"`
	MemAvgBytes *float64 `json:"mem_avg_bytes"`
	// CPUSource names the query_log counter behind the CPU figures:
	// "os_cpu" or "real_time" (fallback). Empty when CPU wasn't captured.
	CPUSource string `json:"cpu_source,omitempty"`

	// CacheState is "warm" (default, page cache reused across iterations) or
	// "cold" (measured with enable_filesystem_cache=0). In a paired run a query
	// produces two BenchResults with the same Name and differing CacheState.
	CacheState string `json:"cache_state,omitempty"`

	// IndexPruning, when non-nil, holds the EXPLAIN indexes=1 granule-pruning
	// signal for an index-sensitive query (e.g. lookup_by_id against a
	// bloom_filter on id). Captured separately from the timed run because the
	// query's own wall-clock is dominated by its id-resolver subquery.
	IndexPruning *IndexPruning `json:"index_pruning,omitempty"`
}

// IndexPruning records granules/parts a query scans with skip indexes disabled
// vs enabled, for a literal-id lookup. The ratio is the bloom filter's real,
// diffable evidence (the query's wall-clock is not — see lookup_by_id.sql).
type IndexPruning struct {
	LiteralID         string `json:"literal_id"`
	GranulesWithout   int    `json:"granules_without"` // use_skip_indexes=0
	GranulesWith      int    `json:"granules_with"`    // index active
	PartsWithout      int    `json:"parts_without"`
	PartsWith         int    `json:"parts_with"`
}

// EnsureBinary checks that `clickhouse-benchmark` is on PATH.
// The harness depends on it for query measurement.
func EnsureBinary() error {
	if _, err := exec.LookPath("clickhouse-benchmark"); err != nil {
		return errors.New(
			"clickhouse-benchmark not found on PATH. " +
				"The dev shell provides it via the `clickhouse` devenv package; " +
				"run `direnv allow` or `devenv shell` and retry")
	}
	return nil
}

// Bench runs one query through clickhouse-benchmark and parses the result.
//
// Parameters in the query (`{name:Type}` placeholders) are rendered to literal
// SQL via `params` before invocation, since clickhouse-benchmark does not
// support bound parameters. host/port/database/user/password come from the DSN
// the harness already parsed.
func Bench(ctx context.Context, opts BenchOpts, q Query) BenchResult {
	sql, err := renderParams(q.SQL, opts.Params)
	if err != nil {
		return BenchResult{Name: q.Name, Concurrency: opts.Concurrency, Iterations: opts.Iterations, Error: err.Error()}
	}

	logComment := fmt.Sprintf("bench:%s:%s:%s", opts.SweepID, opts.Scenario, q.Name)
	if opts.CPUProbe != nil {
		sql = appendSetting(sql, "log_comment = '"+strings.ReplaceAll(logComment, "'", "''")+"'")
	}
	if opts.ColdCache {
		sql = appendSetting(sql, "enable_filesystem_cache = 0")
	}

	args := []string{
		"--host", opts.Host,
		"--port", strconv.Itoa(opts.Port),
		"--iterations", strconv.Itoa(opts.Iterations),
		"--concurrency", strconv.Itoa(opts.Concurrency),
		"--delay", "0",
		"--query", sql,
	}
	if opts.Secure {
		// Cloud / TLS clusters listen on the native-secure port (9440). Without
		// --secure, clickhouse-benchmark attempts a plaintext handshake and the
		// server resets the connection (NETWORK_ERROR: connection reset by peer).
		args = append(args, "--secure")
	}
	if opts.Database != "" {
		args = append(args, "--database", opts.Database)
	}
	if opts.User != "" {
		args = append(args, "--user", opts.User)
	}
	if opts.Password != "" {
		args = append(args, "--password", opts.Password)
	}

	cmd := exec.CommandContext(ctx, "clickhouse-benchmark", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = nil // clickhouse-benchmark writes its report to stderr
	if err := cmd.Run(); err != nil {
		return BenchResult{
			Name:        q.Name,
			Concurrency: opts.Concurrency,
			Iterations:  opts.Iterations,
			Error:       fmt.Sprintf("%v: %s", err, lastLine(stderr.String())),
		}
	}

	res, err := parseStderr(stderr.String())
	if err != nil {
		return BenchResult{Name: q.Name, Concurrency: opts.Concurrency, Iterations: opts.Iterations, Error: err.Error()}
	}
	res.Name = q.Name
	res.Concurrency = opts.Concurrency
	res.Iterations = opts.Iterations
	res.CacheState = "warm"
	if opts.ColdCache {
		res.CacheState = "cold"
	}

	if opts.CPUProbe != nil {
		if stats, perr := opts.CPUProbe(ctx, logComment); perr != nil {
			fmt.Fprintf(os.Stderr, "warning: CPU probe failed for %s: %v\n", q.Name, perr)
		} else if stats != nil {
			res.CPUp50Us = &stats.CPUp50Us
			res.CPUp95Us = &stats.CPUp95Us
			res.MemAvgBytes = &stats.MemAvgBytes
			res.CPUSource = stats.Source
		}
	}
	return res
}

// appendSetting adds `setting` to sql's trailing SETTINGS clause, merging into
// an existing one with a comma rather than producing a second clause
// (ClickHouse rejects two SETTINGS clauses). `setting` is a harness-built
// `key = value` fragment with no user input. Safe to call repeatedly to stack
// multiple settings (log_comment, enable_filesystem_cache, ...).
func appendSetting(sql, setting string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(sql), ";")
	// A trailing top-level SETTINGS keyword means merge; otherwise open a clause.
	if strings.Contains(strings.ToUpper(trimmed), "SETTINGS ") {
		return trimmed + ", " + setting
	}
	return trimmed + " SETTINGS " + setting
}

// CPUStats holds the per-query CPU/memory figures read from system.query_log.
// Source names which ProfileEvents counter backed the CPU figures: "os_cpu"
// (OSCPUVirtualTimeMicroseconds, true OS thread CPU) or "real_time"
// (RealTimeMicroseconds summed across query threads — the fallback when OS
// counters are unavailable, e.g. ClickHouse inside Docker on macOS). For the
// CPU-bound aggregation queries here, summed thread wall-time ≈ CPU time.
type CPUStats struct {
	CPUp50Us    float64
	CPUp95Us    float64
	MemAvgBytes float64
	Source      string
}

// CPUProbe reads system.query_log for the rows tagged with logComment and
// returns aggregated CPU/memory. Supplied by the caller so this package does
// not depend on the ClickHouse driver. Returns (nil, nil) when query_log is
// unavailable — that is not an error, it means "no CPU data for this run".
type CPUProbe func(ctx context.Context, logComment string) (*CPUStats, error)

// BenchOpts collects the connection + sweep config used for one query.
type BenchOpts struct {
	Host        string
	Port        int
	Secure      bool
	Database    string
	User        string
	Password    string
	Iterations  int
	Concurrency int
	Params      map[string]string

	// Scenario + SweepID build the log_comment used to correlate query_log
	// rows back to this query. SweepID is unique per harness invocation.
	Scenario string
	SweepID  string

	// CPUProbe, when non-nil, is called after the benchmark completes to read
	// CPU/memory from query_log. Nil disables CPU capture entirely.
	CPUProbe CPUProbe

	// ColdCache, when true, runs the query with enable_filesystem_cache=0 so the
	// real I/O cost is measured instead of the warm page-cache best case. The
	// result records CacheState accordingly.
	ColdCache bool
}

// renderParams substitutes every `{name:Type}` placeholder in sql with the
// matching value from params. The value must already be a valid SQL literal
// (e.g. `'default'`, `1779456720`, `['s1', 's2']`); the caller is responsible
// for quoting. Missing params produce an error rather than a partial render.
func renderParams(sql string, params map[string]string) (string, error) {
	re := regexp.MustCompile(`\{([a-zA-Z0-9_]+):[^}]+\}`)
	var missing []string
	out := re.ReplaceAllStringFunc(sql, func(match string) string {
		// match is like {namespace:String}
		name := re.FindStringSubmatch(match)[1]
		v, ok := params[name]
		if !ok {
			missing = append(missing, name)
			return match
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("missing param values: %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// parseStderr extracts metrics from a clickhouse-benchmark stderr block.
//
// Two formats to parse:
//
//	127.0.0.1:9000, queries: 10, QPS: 63.615, RPS: 3065742.290, MiB/s: 257.340, result RPS: 9160.585, result MiB/s: 0.332.
//	0%		0.003 sec.
//	...
//	99.99%		0.007 sec.
//
// The percentile labels include both `0%` / `50%` and the special `99.9%` /
// `99.99%` forms. Tabs separate label from value (we accept any whitespace).
func parseStderr(s string) (BenchResult, error) {
	var r BenchResult
	var sawSummary, sawPercentiles bool

	hostRE := regexp.MustCompile(`queries:?\s+\d+,\s+QPS:\s+([0-9.]+),\s+RPS:\s+([0-9.]+),\s+MiB/s:\s+([0-9.]+),\s+result RPS:\s+([0-9.]+),\s+result MiB/s:\s+([0-9.]+)`)
	pctRE := regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)%\s+([0-9]+(?:\.[0-9]+)?)\s*sec\.?$`)

	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if m := hostRE.FindStringSubmatch(line); m != nil {
			r.QPS = mustFloat(m[1])
			r.RPS = mustFloat(m[2])
			r.MiBPerSec = mustFloat(m[3])
			r.ResultRPS = mustFloat(m[4])
			r.ResultMiBPerSec = mustFloat(m[5])
			sawSummary = true
			continue
		}
		if m := pctRE.FindStringSubmatch(line); m != nil {
			pct := mustFloat(m[1])
			sec := mustFloat(m[2])
			assignPercentile(&r, pct, sec)
			sawPercentiles = true
		}
	}

	if !sawSummary || !sawPercentiles {
		return r, fmt.Errorf("could not parse clickhouse-benchmark output (summary=%v, percentiles=%v); raw=%q", sawSummary, sawPercentiles, s)
	}
	return r, nil
}

func assignPercentile(r *BenchResult, pct, sec float64) {
	switch pct {
	case 0:
		r.P0Sec = sec
	case 10:
		r.P10Sec = sec
	case 20:
		r.P20Sec = sec
	case 30:
		r.P30Sec = sec
	case 40:
		r.P40Sec = sec
	case 50:
		r.P50Sec = sec
	case 60:
		r.P60Sec = sec
	case 70:
		r.P70Sec = sec
	case 80:
		r.P80Sec = sec
	case 90:
		r.P90Sec = sec
	case 95:
		r.P95Sec = sec
	case 99:
		r.P99Sec = sec
	case 99.9:
		r.P999Sec = sec
	case 99.99:
		r.P9999Sec = sec
	}
}

func mustFloat(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[len(lines)-1])
}
