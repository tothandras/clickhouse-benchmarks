package accounting

import (
	"context"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// FlushLogs flushes system logs on all replicas before collection. Tries the
// cluster-wide statement first (required on multi-replica Cloud services so
// the other replicas' query_log/part_log buffers reach their tables); on
// targets where it is unavailable (devenv single node has no coordination
// config, Cloud may deny it) it falls back to a local flush plus a settle
// delay covering the default log flush interval, and reports "local-only" so
// the result records which guarantee it got.
func FlushLogs(ctx context.Context, conn driver.Conn, target Target, settle time.Duration) (mode string) {
	if target.UseCluster && target.Replicas > 1 {
		if err := conn.Exec(ctx, "SYSTEM FLUSH LOGS ON CLUSTER default"); err == nil {
			return "cluster"
		}
	}
	_ = conn.Exec(ctx, "SYSTEM FLUSH LOGS")
	if settle > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(settle):
		}
	}
	return "local-only"
}
