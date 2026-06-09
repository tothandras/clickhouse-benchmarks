package accounting

import (
	"context"
	"fmt"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// MergeStats aggregates the scenario table's merges over the attribution
// window (measure start .. drain end). Merge CPU carries no log_comment; it is
// attributed to ingest by construction: on a dedicated service the table only
// receives this run's inserts during the window.
type MergeStats struct {
	Merges  uint64  `json:"merges"`
	CPUSec  float64 `json:"cpu_sec"`
	WallSec float64 `json:"wall_sec"`
	// CPUEstimated is set when part_log carried no usable ProfileEvents CPU
	// (version-dependent); CPUSec then falls back to wall time, a single-thread
	// lower bound.
	CPUEstimated bool `json:"cpu_estimated"`

	ReadRows     uint64 `json:"read_rows"`
	ReadBytes    uint64 `json:"read_bytes"`
	WrittenRows  uint64 `json:"written_rows"`
	WrittenBytes uint64 `json:"written_bytes"`
	PeakMemBytes uint64 `json:"peak_mem_bytes"`

	// Diagnostics only. NewPart CPU is already inside the tagged INSERTs in
	// query_log; summing it here would double-count insert work.
	NewParts uint64 `json:"new_parts"`
	Mutates  uint64 `json:"mutates"`
}

// CollectPartLog aggregates MergeParts events for the table across all
// replicas — the component query_log structurally cannot see.
func CollectPartLog(ctx context.Context, conn driver.Conn, target Target, database, table string, w Window) (MergeStats, error) {
	var m MergeStats

	mergeSQL := fmt.Sprintf(`
SELECT
  count()                                                              AS merges,
  toFloat64(sum(ProfileEvents['OSCPUVirtualTimeMicroseconds']) / 1e6)  AS os_cpu_sec,
  toFloat64(sum(ProfileEvents['RealTimeMicroseconds']) / 1e6)          AS rt_cpu_sec,
  toFloat64(sum(duration_ms) / 1e3)                                    AS wall_sec,
  toUInt64(sum(read_rows))                                             AS read_rows,
  toUInt64(sum(read_bytes))                                            AS read_bytes,
  toUInt64(sum(rows))                                                  AS written_rows,
  toUInt64(sum(size_in_bytes))                                         AS written_bytes,
  toUInt64(max(peak_memory_usage))                                     AS peak_mem_bytes
FROM %s
WHERE event_type = 'MergeParts'
  AND database = ? AND table = ?
  AND event_time >= ? AND event_time < ?%s`, target.table("part_log"), target.settingsSuffix())

	var rtCPU float64
	if err := conn.QueryRow(ctx, mergeSQL, database, table, w.Start, w.End).Scan(
		&m.Merges, &m.CPUSec, &rtCPU, &m.WallSec,
		&m.ReadRows, &m.ReadBytes, &m.WrittenRows, &m.WrittenBytes, &m.PeakMemBytes,
	); err != nil {
		return m, fmt.Errorf("accounting: part_log merges: %w", err)
	}

	// ProfileEvents availability in part_log varies by version and host:
	// prefer the OS CPU counter, fall back to the real_time proxy (hosts where
	// OS thread counters read 0, e.g. macOS devenv), and as a last resort
	// estimate from merge wall time — a single-thread lower bound is better
	// than silently booking merge work as idle. Both fallbacks are flagged.
	if m.Merges > 0 && m.CPUSec == 0 {
		switch {
		case rtCPU > 0:
			m.CPUSec = rtCPU
			m.CPUEstimated = true
		case m.WallSec > 0:
			m.CPUSec = m.WallSec
			m.CPUEstimated = true
		}
	}

	diagSQL := fmt.Sprintf(`
SELECT
  toUInt64(countIf(event_type = 'NewPart'))    AS new_parts,
  toUInt64(countIf(event_type = 'MutatePart')) AS mutates
FROM %s
WHERE database = ? AND table = ?
  AND event_time >= ? AND event_time < ?%s`, target.table("part_log"), target.settingsSuffix())

	if err := conn.QueryRow(ctx, diagSQL, database, table, w.Start, w.End).Scan(&m.NewParts, &m.Mutates); err != nil {
		return m, fmt.Errorf("accounting: part_log diagnostics: %w", err)
	}
	return m, nil
}
