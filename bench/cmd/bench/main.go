// Command bench runs benchmark scenarios against a ClickHouse instance.
//
// Per-query measurement is delegated to the official `clickhouse-benchmark`
// CLI (which must be on PATH; the devenv provides it). The Go binary handles
// scenario lifecycle: init.sql apply, seeding via bench/seed,
// parameter rendering, and result file assembly.
//
// Usage:
//
//	export CLICKHOUSE_DSN="clickhouse://default:@localhost:9000/default"
//	bench --scenario baseline-openmeter --iterations 10 --concurrency 1
//
// Each scenario writes one JSON result file to bench/results/<scenario>/.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/spf13/cobra"

	"github.com/openmeterio/ch-playground/bench/runner"
	"github.com/openmeterio/ch-playground/bench/seed"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type flags struct {
	dsn             string
	scenarios       []string
	scenariosDir    string
	resultsDir      string
	iterations      int
	concurrency     int
	rows            int
	batchSize       int
	asyncInsert     bool
	waitAsyncInsert bool
	rngSeed         uint64
	skipSeed        bool
}

func newRootCmd() *cobra.Command {
	f := &flags{}
	cmd := &cobra.Command{
		Use:   "bench",
		Short: "Run ClickHouse benchmark scenarios via clickhouse-benchmark",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return run(cmd.Context(), f)
		},
	}
	cmd.Flags().StringVar(&f.dsn, "dsn", "", "ClickHouse DSN (overrides $CLICKHOUSE_DSN)")
	cmd.Flags().StringSliceVar(&f.scenarios, "scenario", nil, "scenario name(s); default = all discovered")
	cmd.Flags().StringVar(&f.scenariosDir, "scenarios-dir", "scenarios", "directory holding scenarios")
	cmd.Flags().StringVar(&f.resultsDir, "results-dir", "bench/results", "directory to write result JSON files")
	cmd.Flags().IntVar(&f.iterations, "iterations", 10, "measured iterations per query")
	cmd.Flags().IntVar(&f.concurrency, "concurrency", 1, "concurrent query streams (passed to clickhouse-benchmark -c)")
	cmd.Flags().IntVar(&f.rows, "rows", 1_000_000, "events to seed per scenario")
	cmd.Flags().IntVar(&f.batchSize, "batch-size", 10_000, "INSERT batch size")
	cmd.Flags().BoolVar(&f.asyncInsert, "async-insert", false, "enable async_insert SETTING on seed batches")
	cmd.Flags().BoolVar(&f.waitAsyncInsert, "wait-async", false, "set wait_for_async_insert=1 when async-insert is true")
	cmd.Flags().Uint64Var(&f.rngSeed, "seed", 42, "RNG seed for deterministic data generation")
	cmd.Flags().BoolVar(&f.skipSeed, "skip-seed", false, "skip seeding (run queries against existing data)")
	cmd.SilenceUsage = true
	return cmd
}

func run(ctx context.Context, f *flags) error {
	if err := runner.EnsureBinary(); err != nil {
		return err
	}

	dsn := f.dsn
	if dsn == "" {
		dsn = os.Getenv("CLICKHOUSE_DSN")
	}
	if dsn == "" {
		return fmt.Errorf("no ClickHouse DSN: set --dsn or $CLICKHOUSE_DSN")
	}

	opts, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		return fmt.Errorf("parse DSN: %w", err)
	}
	conn, err := clickhouse.Open(opts)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()
	if err := conn.Ping(ctx); err != nil {
		return fmt.Errorf("ping: %w", err)
	}

	host, port, err := hostPort(opts)
	if err != nil {
		return err
	}

	scenarios, err := runner.Discover(f.scenariosDir, f.scenarios)
	if err != nil {
		return err
	}
	if len(scenarios) == 0 {
		return fmt.Errorf("no scenarios found in %s", f.scenariosDir)
	}

	fingerprint, err := runner.Fingerprint(ctx, conn)
	if err != nil {
		return fmt.Errorf("fingerprint: %w", err)
	}
	commit := runner.HarnessCommit()

	// Per-invocation sweep id correlates query_log rows to this run's queries.
	sweepID := time.Now().UTC().Format("20060102T150405Z")
	var cpuProbe runner.CPUProbe
	if runner.QueryLogAvailable(ctx, conn) {
		cpuProbe = runner.NewCPUProbe(conn)
	} else {
		fmt.Fprintln(os.Stderr, "warning: system.query_log unavailable on target; per-query CPU will be null")
	}

	for _, sc := range scenarios {
		fmt.Printf("\n== scenario: %s ==\n", sc.Name)
		startedAt := time.Now()
		table := scenarioTable(sc.Name)

		// Drop first when we're about to reseed so a stale table from an earlier
		// run of THIS scenario doesn't shadow the new init.sql with the wrong
		// schema. Each scenario uses its own table name so scenarios coexist
		// without clobbering each other; --skip-seed reuses whatever is there.
		if !f.skipSeed {
			if err := conn.Exec(ctx, "DROP TABLE IF EXISTS "+table+" SYNC"); err != nil {
				return fmt.Errorf("drop %s before %s: %w", table, sc.Name, err)
			}
		}

		fmt.Println(" init.sql ...")
		if err := runner.ApplyInit(ctx, conn, sc, opts.Auth.Database); err != nil {
			return err
		}

		var ingest *runner.IngestResult
		if !f.skipSeed {
			ingest, err = doSeed(ctx, conn, sc, table, f)
			if err != nil {
				return err
			}
		}

		queries := runner.LoadQueries(sc)
		fmt.Printf(" queries: %d (iterations=%d, concurrency=%d)\n", len(queries), f.iterations, f.concurrency)

		benchOpts := runner.BenchOpts{
			Host:        host,
			Port:        port,
			Database:    opts.Auth.Database,
			User:        opts.Auth.Username,
			Password:    opts.Auth.Password,
			Iterations:  f.iterations,
			Concurrency: f.concurrency,
			Params:      defaultParams(f),
			Scenario:    sc.Name,
			SweepID:     sweepID,
			CPUProbe:    cpuProbe,
		}

		var results []runner.BenchResult
		for _, q := range queries {
			fmt.Printf("  %-40s", q.Name)
			res := runner.Bench(ctx, benchOpts, q)
			if res.Error != "" {
				fmt.Printf("ERR: %s\n", res.Error)
			} else {
				cpu := "cpu=n/a"
				if res.CPUp50Us != nil {
					cpu = fmt.Sprintf("cpu_p50=%.1fms", *res.CPUp50Us/1000)
				}
				fmt.Printf("p50=%.1fms p95=%.1fms %s QPS=%.1f\n",
					res.P50Sec*1000, res.P95Sec*1000, cpu, res.QPS)
			}
			results = append(results, res)
		}

		runRecord := runner.Run{
			Scenario:           sc.Name,
			HarnessCommit:      commit,
			SweepID:            sweepID,
			StartedAt:          startedAt,
			FinishedAt:         time.Now(),
			ClusterFingerprint: fingerprint,
			Concurrency:        f.concurrency,
			Ingest:             ingest,
			Queries:            results,
		}
		path, err := runner.Write(f.resultsDir, runRecord)
		if err != nil {
			return fmt.Errorf("write result: %w", err)
		}
		rel, _ := filepath.Rel(".", path)
		fmt.Printf(" wrote: %s\n", rel)

		reportPath, err := runner.WriteReport(f.resultsDir, runRecord, queries)
		if err != nil {
			return fmt.Errorf("write report: %w", err)
		}
		relReport, _ := filepath.Rel(".", reportPath)
		fmt.Printf(" wrote: %s\n", relReport)
	}

	return nil
}

// hostPort extracts the first host:port from the parsed DSN. clickhouse-benchmark
// takes --host/--port (singular) and we don't try to span multiple shards from a
// single bench process.
func hostPort(opts *clickhouse.Options) (string, int, error) {
	if len(opts.Addr) == 0 {
		return "", 0, errors.New("DSN has no addresses")
	}
	addr := opts.Addr[0]
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, fmt.Errorf("split host:port from %q: %w", addr, err)
	}
	port, err := strconv.Atoi(p)
	if err != nil {
		return "", 0, fmt.Errorf("parse port from %q: %w", p, err)
	}
	return h, port, nil
}

func doSeed(ctx context.Context, conn driver.Conn, sc runner.Scenario, table string, f *flags) (*runner.IngestResult, error) {
	// Pure-SQL seed wins if scenario provides one (e.g. generateRandom-based).
	if sc.HasSeed {
		fmt.Println(" seed.sql ...")
		start := time.Now()
		if err := runner.ApplySeedSQL(ctx, conn, sc); err != nil {
			return nil, err
		}
		return &runner.IngestResult{
			Source:          "seed.sql",
			DurationSeconds: time.Since(start).Seconds(),
		}, nil
	}

	cfg := seed.DefaultConfig()
	cfg.Table = table
	cfg.Rows = f.rows
	cfg.BatchSize = f.batchSize
	cfg.AsyncInsert = f.asyncInsert
	cfg.WaitAsyncInsert = f.waitAsyncInsert
	cfg.Seed = f.rngSeed

	fmt.Printf(" seed: %d rows (batch=%d, async=%v) ...\n",
		cfg.Rows, cfg.BatchSize, cfg.AsyncInsert)
	res, err := seed.Run(ctx, conn, cfg)
	if err != nil {
		return nil, err
	}
	fmt.Printf(" seed done: %d events in %s (%.0f events/sec)\n",
		res.Rows, res.Duration.Round(time.Millisecond), res.EventsPerSecond)
	return runner.FromSeedResult(res), nil
}

// defaultParams returns the v1 fixed parameter set documented in
// scenarios/README.md as already-rendered SQL literals (clickhouse-benchmark
// does not support bound parameters; we substitute them ourselves). Per-scenario
// manifests are a future change.
func defaultParams(_ *flags) map[string]string {
	cfg := seed.DefaultConfig()
	subjects := seed.Subjects(10)
	from := cfg.TimeEnd.Add(-cfg.TimeSpan)
	const chTimeFmt = "2006-01-02 15:04:05"

	subjectsLit := make([]string, len(subjects))
	for i, s := range subjects {
		subjectsLit[i] = sqlString(s)
	}

	return map[string]string{
		"namespace": sqlString(cfg.Namespace),
		"type":      sqlString(cfg.Types[0]),
		"from":      sqlString(from.UTC().Format(chTimeFmt)),
		"to":        sqlString(cfg.TimeEnd.UTC().Format(chTimeFmt)),
		"subjects":  "(" + strings.Join(subjectsLit, ", ") + ")",
		"group1":    sqlString(cfg.Group1[0]),
		"group2":    sqlString(cfg.Group2[0]),
	}
}

// sqlString quotes s as a ClickHouse string literal. Values used here come
// from the seeder's own config (not user input), so escaping just needs to
// handle the apostrophe case.
func sqlString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// scenarioTable derives the table name for a scenario by replacing `-` with
// `_` and appending `_events`. The scenario directory name is the single
// source of truth: scenarios/data-as-json/ → data_as_json_events. Each
// scenario uses its own table so scenarios coexist without clobbering.
func scenarioTable(name string) string {
	return strings.ReplaceAll(name, "-", "_") + "_events"
}
