package accounting

import (
	"context"
	"fmt"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// QueryGroup aggregates one (component, class, query, cache) bucket of the
// run's tagged statements.
type QueryGroup struct {
	Component string `json:"component"`
	Class     string `json:"class,omitempty"`
	Query     string `json:"query,omitempty"`
	Cache     string `json:"cache,omitempty"`

	N       uint64  `json:"n"`
	CPUSec  float64 `json:"cpu_sec"`
	WallSec float64 `json:"wall_sec"`
	P50Ms   float64 `json:"p50_ms"`
	P95Ms   float64 `json:"p95_ms"`
	P99Ms   float64 `json:"p99_ms"`

	ReadRows     uint64 `json:"read_rows"`
	ReadBytes    uint64 `json:"read_bytes"`
	WrittenRows  uint64 `json:"written_rows"`
	WrittenBytes uint64 `json:"written_bytes"`
	// ResultBytes feeds the egress estimate (bytes returned to clients).
	ResultBytes  uint64 `json:"result_bytes"`
	PeakMemBytes uint64 `json:"peak_mem_bytes"`

	S3Get            uint64 `json:"s3_get"`
	FSCacheReadBytes uint64 `json:"fs_cache_read_bytes"`
	FSCacheMissBytes uint64 `json:"fs_cache_miss_bytes"`
}

// QueryErrorGroup counts failed statements per bucket; counted, never priced.
type QueryErrorGroup struct {
	Component string `json:"component"`
	Class     string `json:"class,omitempty"`
	Query     string `json:"query,omitempty"`
	Cache     string `json:"cache,omitempty"`
	N         uint64 `json:"n"`
	Sample    string `json:"sample"`
}

// QueryLogStats is the run's query_log aggregation.
type QueryLogStats struct {
	Groups []QueryGroup      `json:"groups"`
	Errors []QueryErrorGroup `json:"errors,omitempty"`
	// CPUSource names the counter behind CPUSec: "os_cpu"
	// (OSCPUVirtualTimeMicroseconds) or "real_time" (RealTimeMicroseconds,
	// summed thread wall-time — the proxy on hosts where OS thread CPU
	// counters read 0, e.g. macOS devenv; same convention as the perf path's
	// CPU probe).
	CPUSource string `json:"cpu_source"`
}

// CollectQueryLog aggregates the run's tagged statements (inserts AND queries)
// from query_log across all replicas. ProfileEvents map lookups on absent
// keys read as 0 in ClickHouse, which implements the missing-counter
// tolerance for free; version differences surface as zeros, not errors.
func CollectQueryLog(ctx context.Context, conn driver.Conn, target Target, runID string, w Window) (QueryLogStats, error) {
	var stats QueryLogStats

	groupSQL := fmt.Sprintf(`
SELECT
  JSONExtractString(log_comment, 'component')                          AS component,
  JSONExtractString(log_comment, 'class')                              AS class,
  JSONExtractString(log_comment, 'query')                              AS query_name,
  JSONExtractString(log_comment, 'cache')                              AS cache,
  count()                                                              AS n,
  toFloat64(sum(ProfileEvents['OSCPUVirtualTimeMicroseconds']) / 1e6)  AS os_cpu_sec,
  toFloat64(sum(ProfileEvents['RealTimeMicroseconds']) / 1e6)          AS rt_cpu_sec,
  toFloat64(sum(query_duration_ms) / 1e3)                              AS wall_sec,
  quantiles(0.5, 0.95, 0.99)(toFloat64(query_duration_ms))             AS p_ms,
  toUInt64(sum(read_rows))                                             AS read_rows,
  toUInt64(sum(read_bytes))                                            AS read_bytes,
  toUInt64(sum(written_rows))                                          AS written_rows,
  toUInt64(sum(written_bytes))                                         AS written_bytes,
  toUInt64(sum(result_bytes))                                          AS result_bytes,
  toUInt64(max(memory_usage))                                          AS peak_mem_bytes,
  toUInt64(sum(ProfileEvents['S3GetObject']))                          AS s3_get,
  toUInt64(sum(ProfileEvents['CachedReadBufferReadFromCacheBytes']))   AS fs_cache_read_bytes,
  toUInt64(sum(ProfileEvents['CachedReadBufferReadFromSourceBytes']))  AS fs_cache_miss_bytes
FROM %s
WHERE type = 'QueryFinish'
  AND event_time >= ? AND event_time < ?
  AND JSONExtractString(log_comment, 'cogs_run') = ?
GROUP BY component, class, query_name, cache
ORDER BY component, class, query_name, cache%s`, target.table("query_log"), target.settingsSuffix())

	rows, err := conn.Query(ctx, groupSQL, w.Start, w.End, runID)
	if err != nil {
		return stats, fmt.Errorf("accounting: query_log groups: %w", err)
	}
	defer rows.Close()
	var rtCPUs []float64
	totalOSCPU := 0.0
	for rows.Next() {
		var g QueryGroup
		var p []float64
		var rtCPU float64
		if err := rows.Scan(
			&g.Component, &g.Class, &g.Query, &g.Cache,
			&g.N, &g.CPUSec, &rtCPU, &g.WallSec, &p,
			&g.ReadRows, &g.ReadBytes, &g.WrittenRows, &g.WrittenBytes,
			&g.ResultBytes, &g.PeakMemBytes,
			&g.S3Get, &g.FSCacheReadBytes, &g.FSCacheMissBytes,
		); err != nil {
			return stats, fmt.Errorf("accounting: query_log scan: %w", err)
		}
		if len(p) == 3 {
			g.P50Ms, g.P95Ms, g.P99Ms = p[0], p[1], p[2]
		}
		totalOSCPU += g.CPUSec
		rtCPUs = append(rtCPUs, rtCPU)
		stats.Groups = append(stats.Groups, g)
	}
	if err := rows.Err(); err != nil {
		return stats, fmt.Errorf("accounting: query_log rows: %w", err)
	}

	// Counter selection is global per collection so components stay
	// comparable: os_cpu when the host populates it at all, else the
	// real_time proxy (same convention as the perf path's CPU probe).
	stats.CPUSource = "os_cpu"
	if totalOSCPU == 0 && len(stats.Groups) > 0 {
		stats.CPUSource = "real_time"
		for i := range stats.Groups {
			stats.Groups[i].CPUSec = rtCPUs[i]
		}
	}

	errSQL := fmt.Sprintf(`
SELECT
  JSONExtractString(log_comment, 'component') AS component,
  JSONExtractString(log_comment, 'class')     AS class,
  JSONExtractString(log_comment, 'query')     AS query_name,
  JSONExtractString(log_comment, 'cache')     AS cache,
  count()                                     AS n,
  any(exception)                              AS sample
FROM %s
WHERE type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing')
  AND event_time >= ? AND event_time < ?
  AND JSONExtractString(log_comment, 'cogs_run') = ?
GROUP BY component, class, query_name, cache
ORDER BY component, class, query_name, cache%s`, target.table("query_log"), target.settingsSuffix())

	erows, err := conn.Query(ctx, errSQL, w.Start, w.End, runID)
	if err != nil {
		return stats, fmt.Errorf("accounting: query_log errors: %w", err)
	}
	defer erows.Close()
	for erows.Next() {
		var e QueryErrorGroup
		if err := erows.Scan(&e.Component, &e.Class, &e.Query, &e.Cache, &e.N, &e.Sample); err != nil {
			return stats, fmt.Errorf("accounting: query_log error scan: %w", err)
		}
		stats.Errors = append(stats.Errors, e)
	}
	if err := erows.Err(); err != nil {
		return stats, fmt.Errorf("accounting: query_log error rows: %w", err)
	}
	return stats, nil
}
