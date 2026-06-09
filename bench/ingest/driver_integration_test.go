package ingest

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/openmeterio/ch-playground/bench/seed"
)

// TestCHInserterIntegration drives a short paced run against a real ClickHouse.
// Gated on CLICKHOUSE_TEST_DSN — deliberately NOT the harness's CLICKHOUSE_DSN,
// which may point at a shared/production service; tests create and drop tables
// and must only ever run against a disposable instance (the devenv one).
func TestCHInserterIntegration(t *testing.T) {
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
	defer conn.Close()
	ctx := context.Background()
	if err := conn.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	const table = "cogs_ingest_driver_it"
	if err := conn.Exec(ctx, "DROP TABLE IF EXISTS "+table+" SYNC"); err != nil {
		t.Fatal(err)
	}
	if err := conn.Exec(ctx, `CREATE TABLE `+table+` (
		namespace String, id String, type LowCardinality(String), subject String,
		source String, time DateTime, data String, ingested_at DateTime,
		stored_at DateTime, store_row_id String
	) ENGINE = MergeTree PARTITION BY toYYYYMM(time) ORDER BY (namespace, type, subject, time)`); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(ctx, "DROP TABLE IF EXISTS "+table+" SYNC")

	gen, err := seed.NewGenerator(seed.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(ctx, Config{
		EventsPerSec:  500,
		BatchMaxRows:  100,
		FlushInterval: 200 * time.Millisecond,
		Duration:      2 * time.Second,
		Gen:           gen,
		Inserter: &CHInserter{
			Conn:       conn,
			Table:      table,
			LogComment: `{"cogs_run":"it-test","component":"ingest"}`,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Errors > 0 {
		t.Fatalf("insert errors: %d", res.Errors)
	}

	var rows uint64
	if err := conn.QueryRow(ctx, "SELECT count() FROM "+table).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if int(rows) != res.Events {
		t.Fatalf("table has %d rows, driver reports %d events", rows, res.Events)
	}
	if res.Events < 800 { // 2s at 500 eps, generous floor for slow CI boxes
		t.Fatalf("achieved only %d events in 2s at 500 eps", res.Events)
	}
}
