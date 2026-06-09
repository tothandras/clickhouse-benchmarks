package accounting

import (
	"context"
	"fmt"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// AsyncInsertStats books background flush-query cost to the insert component.
// With async_insert=1 the tagged INSERT statements in query_log do not carry
// the parse/write cost — it lands in untagged flush queries. Correlation goes
// through system.asynchronous_insert_log (flush_query_id per buffered insert).
type AsyncInsertStats struct {
	Flushes        uint64  `json:"flushes"`         // distinct flush queries observed
	FlushesMatched uint64  `json:"flushes_matched"` // of those, found in query_log
	CPUSec         float64 `json:"cpu_sec"`
	WallSec        float64 `json:"wall_sec"`
	WrittenRows    uint64  `json:"written_rows"`
	WrittenBytes   uint64  `json:"written_bytes"`
	// Partial means some flush queries could not be correlated (log lag,
	// version gaps, missing asynchronous_insert_log): CPUSec undercounts and
	// the report must carry the caveat.
	Partial bool `json:"partial"`
}

// CollectAsyncInserts correlates the table's async-insert flushes with
// query_log across all replicas. When asynchronous_insert_log is unavailable
// the result is empty with Partial=true rather than an error: the cell ran,
// its insert CPU is just not fully attributable.
func CollectAsyncInserts(ctx context.Context, conn driver.Conn, target Target, database, table string, w Window) (AsyncInsertStats, error) {
	var s AsyncInsertStats

	var exists uint8
	if err := conn.QueryRow(ctx, "EXISTS TABLE system.asynchronous_insert_log").Scan(&exists); err != nil || exists == 0 {
		s.Partial = true
		return s, nil
	}

	sql := fmt.Sprintf(`
WITH flushes AS (
  SELECT DISTINCT flush_query_id
  FROM %s
  WHERE database = ? AND table = ?
    AND event_time >= ? AND event_time < ?
    AND flush_query_id != ''
)
SELECT
  (SELECT count() FROM flushes)                                        AS flushes,
  count()                                                              AS matched,
  toFloat64(sum(ProfileEvents['OSCPUVirtualTimeMicroseconds']) / 1e6)  AS os_cpu_sec,
  toFloat64(sum(ProfileEvents['RealTimeMicroseconds']) / 1e6)          AS rt_cpu_sec,
  toFloat64(sum(query_duration_ms) / 1e3)                              AS wall_sec,
  toUInt64(sum(written_rows))                                          AS written_rows,
  toUInt64(sum(written_bytes))                                         AS written_bytes
FROM %s
WHERE type = 'QueryFinish'
  AND query_id IN (SELECT flush_query_id FROM flushes)%s`,
		target.table("asynchronous_insert_log"), target.table("query_log"), target.settingsSuffix())

	var rtCPU float64
	if err := conn.QueryRow(ctx, sql, database, table, w.Start, w.End).Scan(
		&s.Flushes, &s.FlushesMatched, &s.CPUSec, &rtCPU, &s.WallSec, &s.WrittenRows, &s.WrittenBytes,
	); err != nil {
		return s, fmt.Errorf("accounting: async insert correlation: %w", err)
	}
	if s.CPUSec == 0 && rtCPU > 0 {
		s.CPUSec = rtCPU // real_time proxy, same convention as the other collectors
	}
	s.Partial = s.FlushesMatched < s.Flushes
	return s, nil
}
