package accounting

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// testConn connects to the disposable test instance. Gated on
// CLICKHOUSE_TEST_DSN — deliberately NOT the harness's CLICKHOUSE_DSN, which
// may point at a shared/production service.
func testConn(t *testing.T) driver.Conn {
	t.Helper()
	dsn := os.Getenv("CLICKHOUSE_TEST_DSN")
	if dsn == "" {
		t.Skip("CLICKHOUSE_TEST_DSN not set (use the devenv instance: clickhouse://default:@127.0.0.1:9000/default)")
	}
	if strings.Contains(dsn, "clickhouse.cloud") && os.Getenv("CLICKHOUSE_TEST_ALLOW_CLOUD") != "1" {
		t.Fatal("refusing to run integration tests against a ClickHouse Cloud host; point CLICKHOUSE_TEST_DSN at a disposable instance, or set CLICKHOUSE_TEST_ALLOW_CLOUD=1 if this Cloud service IS disposable")
	}
	opts, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := clickhouse.Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := conn.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	return conn
}

func currentDatabase(t *testing.T, conn driver.Conn) string {
	t.Helper()
	var db string
	if err := conn.QueryRow(context.Background(), "SELECT currentDatabase()").Scan(&db); err != nil {
		t.Fatal(err)
	}
	return db
}

func execTagged(t *testing.T, conn driver.Conn, comment, sql string) {
	t.Helper()
	ctx := clickhouse.Context(context.Background(),
		clickhouse.WithSettings(clickhouse.Settings{"log_comment": comment}))
	rows, err := conn.Query(ctx, sql)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
	}
	rows.Close()
}

// TestCollectorsEndToEnd drives a tiny tagged workload through every
// collector: tagged queries + an INSERT-heavy table that merges, then asserts
// nonzero CPU attribution.
func TestCollectorsEndToEnd(t *testing.T) {
	conn := testConn(t)
	ctx := context.Background()
	db := currentDatabase(t, conn)

	target, err := DetectTarget(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("target: cluster=%v replicas=%d", target.UseCluster, target.Replicas)

	const table = "cogs_accounting_it"
	if err := conn.Exec(ctx, "DROP TABLE IF EXISTS "+table+" SYNC"); err != nil {
		t.Fatal(err)
	}
	if err := conn.Exec(ctx, "CREATE TABLE "+table+" (k UInt64, v String) ENGINE = MergeTree ORDER BY k"); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(ctx, "DROP TABLE IF EXISTS "+table+" SYNC")

	runID := fmt.Sprintf("it-%d", time.Now().UnixNano())
	start := time.Now().Add(-5 * time.Second) // tolerate clock skew vs server event_time

	// Tagged workload: a query-class SELECT, an ingest-class INSERT, and a
	// failing statement for the errors block.
	queryTag := fmt.Sprintf(`{"cogs_run":%q,"component":"query","class":"meter_agg","query":"sum_hour","cache":"warm"}`, runID)
	execTagged(t, conn, queryTag, "SELECT sum(number) FROM numbers(10000000)")

	ingestTag := fmt.Sprintf(`{"cogs_run":%q,"component":"ingest"}`, runID)
	ictx := clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{"log_comment": ingestTag}))
	for i := range 5 { // several small inserts -> several parts -> a merge has something to do
		if err := conn.Exec(ictx, fmt.Sprintf(
			"INSERT INTO %s SELECT number + %d, toString(number) FROM numbers(100000)", table, i*100_000)); err != nil {
			t.Fatal(err)
		}
	}
	badTag := fmt.Sprintf(`{"cogs_run":%q,"component":"query","class":"meter_agg","query":"broken","cache":"warm"}`, runID)
	func() {
		bctx := clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{"log_comment": badTag}))
		rows, err := conn.Query(bctx, "SELECT throwIf(1, 'cogs-it intentional failure')")
		if err == nil {
			for rows.Next() {
			}
			rows.Close()
		}
	}()

	// Force merges so part_log has MergeParts rows in the window.
	if err := conn.Exec(ctx, "OPTIMIZE TABLE "+table+" FINAL"); err != nil {
		t.Fatal(err)
	}

	if mode := FlushLogs(ctx, conn, target, 3*time.Second); mode != "cluster" && mode != "local-only" {
		t.Fatalf("unexpected flush mode %q", mode)
	}
	w := Window{Start: start, End: time.Now().Add(5 * time.Second)}

	t.Run("query_log", func(t *testing.T) {
		stats, err := CollectQueryLog(ctx, conn, target, runID, w)
		if err != nil {
			t.Fatal(err)
		}
		var query, ingest *QueryGroup
		for i := range stats.Groups {
			g := &stats.Groups[i]
			switch g.Component {
			case "query":
				query = g
			case "ingest":
				ingest = g
			}
		}
		if query == nil || ingest == nil {
			t.Fatalf("expected query and ingest groups, got %+v", stats.Groups)
		}
		if query.CPUSec <= 0 || query.N != 1 || query.Class != "meter_agg" || query.Cache != "warm" {
			t.Fatalf("query group wrong: %+v", *query)
		}
		if query.ResultBytes == 0 {
			t.Fatal("result_bytes must be collected (egress estimate input)")
		}
		if ingest.WrittenRows != 500_000 || ingest.N != 5 {
			t.Fatalf("ingest group wrong: %+v", *ingest)
		}
		if ingest.CPUSec <= 0 {
			t.Fatal("insert CPU must be nonzero")
		}
		if len(stats.Errors) != 1 || stats.Errors[0].Query != "broken" || stats.Errors[0].N != 1 {
			t.Fatalf("errors block wrong: %+v", stats.Errors)
		}
	})

	t.Run("part_log", func(t *testing.T) {
		// Merges are asynchronous: on SharedMergeTree (Cloud) the MergeParts
		// row can land on another replica's part_log after OPTIMIZE returns.
		// The real runner has a whole drain phase for this; the test polls.
		var m MergeStats
		deadline := time.Now().Add(60 * time.Second)
		for {
			FlushLogs(ctx, conn, target, time.Second)
			mw := Window{Start: w.Start, End: time.Now().Add(5 * time.Second)}
			var err error
			m, err = CollectPartLog(ctx, conn, target, db, table, mw)
			if err != nil {
				t.Fatal(err)
			}
			if m.Merges > 0 || time.Now().After(deadline) {
				break
			}
			time.Sleep(2 * time.Second)
		}
		if m.Merges == 0 {
			t.Fatal("OPTIMIZE FINAL must produce MergeParts events within the drain window")
		}
		if m.CPUSec <= 0 {
			t.Fatalf("merge CPU must be nonzero (estimated=%v): %+v", m.CPUEstimated, m)
		}
		if m.NewParts < 5 {
			t.Fatalf("expected >=5 NewPart events, got %d", m.NewParts)
		}
	})

	t.Run("storage", func(t *testing.T) {
		s, err := SnapshotStorage(ctx, conn, db, table)
		if err != nil {
			t.Fatal(err)
		}
		if s.Rows != 500_000 || s.CompressedBytes == 0 {
			t.Fatalf("storage snapshot wrong: %+v", s)
		}
		n, err := ActivePartCount(ctx, conn, db, table)
		if err != nil {
			t.Fatal(err)
		}
		if n < 1 || uint64(n) != s.Parts {
			t.Fatalf("part count %d inconsistent with snapshot %d", n, s.Parts)
		}
	})

	t.Run("capacity", func(t *testing.T) {
		c, err := DetectCapacity(ctx, conn, target)
		if err != nil {
			t.Fatal(err)
		}
		if c.TotalVCPUs <= 0 || c.Replicas < 1 || c.Source == "" {
			t.Fatalf("capacity detection incomplete: %+v", c)
		}
		if c.TotalVCPUs != float64(c.Replicas)*c.VCPUsPerReplica {
			t.Fatalf("total vcpus must be replicas x per-replica: %+v", c)
		}
	})

	t.Run("foreign_databases", func(t *testing.T) {
		// Own database excluded: the test table must not flag itself.
		own, err := ForeignDatabases(ctx, conn, db, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range own {
			if d.Database == db {
				t.Fatalf("own database flagged as foreign: %+v", own)
			}
		}
	})
}

// TestAsyncInsertAttribution exercises the async-insert flush correlation
// with async_insert=1 and wait_for_async_insert=1.
func TestAsyncInsertAttribution(t *testing.T) {
	conn := testConn(t)
	ctx := context.Background()
	db := currentDatabase(t, conn)

	target, err := DetectTarget(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}

	const table = "cogs_async_it"
	if err := conn.Exec(ctx, "DROP TABLE IF EXISTS "+table+" SYNC"); err != nil {
		t.Fatal(err)
	}
	if err := conn.Exec(ctx, "CREATE TABLE "+table+" (k UInt64) ENGINE = MergeTree ORDER BY k"); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(ctx, "DROP TABLE IF EXISTS "+table+" SYNC")

	start := time.Now().Add(-5 * time.Second)
	actx := clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"async_insert":          1,
		"wait_for_async_insert": 1,
	}))
	for i := range 3 {
		if err := conn.Exec(actx, fmt.Sprintf("INSERT INTO %s VALUES (%d)", table, i)); err != nil {
			t.Fatal(err)
		}
	}

	FlushLogs(ctx, conn, target, 3*time.Second)
	w := Window{Start: start, End: time.Now().Add(5 * time.Second)}

	s, err := CollectAsyncInserts(ctx, conn, target, db, table, w)
	if err != nil {
		t.Fatal(err)
	}
	if s.Flushes == 0 {
		t.Fatalf("async inserts must appear in asynchronous_insert_log: %+v", s)
	}
	if !s.Partial && s.WrittenRows == 0 {
		t.Fatalf("complete correlation must carry written rows: %+v", s)
	}
	if s.Partial {
		t.Logf("correlation partial (acceptable, flagged): %+v", s)
	}
}
