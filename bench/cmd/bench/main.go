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
	concurrency     []int
	coldPaired      bool
	repeat          int
	requireClean    bool
	rows            int
	batchSize       int
	asyncInsert     bool
	waitAsyncInsert bool
	rngSeed         uint64
	namespaces      int
	mixedValue      bool
	skipSeed        bool
	timeEnd         string // optional RFC3339 pin for seed TimeEnd; shared across scenarios for byte-identical time windows
}

// resolveTimeEnd returns the pinned TimeEnd (if --time-end set) or the seeder
// default. Parsed once and shared by every scenario in a run so their event
// time windows are byte-identical (required for cross-scenario per-row parity).
func (f *flags) resolveTimeEnd() (time.Time, error) {
	if f.timeEnd == "" {
		return seed.DefaultConfig().TimeEnd, nil
	}
	t, err := time.Parse(time.RFC3339, f.timeEnd)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse --time-end %q (want RFC3339 like 2026-06-01T00:00:00Z): %w", f.timeEnd, err)
	}
	return t.UTC().Truncate(time.Minute), nil
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
	cmd.Flags().IntSliceVar(&f.concurrency, "concurrency", []int{1}, "concurrency level(s), e.g. 1,8,16; each measured separately (clickhouse-benchmark -c)")
	cmd.Flags().BoolVar(&f.coldPaired, "cold-paired", false, "measure each query warm AND cold (enable_filesystem_cache=0) in one run")
	cmd.Flags().IntVar(&f.repeat, "repeat", 1, "run each scenario's query set N times (reuses seeded data) to gauge run-to-run variance")
	cmd.Flags().BoolVar(&f.requireClean, "require-clean", false, "refuse to run if the harness git tree is dirty (results would be unreproducible)")
	cmd.Flags().IntVar(&f.rows, "rows", 1_000_000, "events to seed per scenario")
	cmd.Flags().IntVar(&f.batchSize, "batch-size", 10_000, "INSERT batch size")
	cmd.Flags().BoolVar(&f.asyncInsert, "async-insert", false, "enable async_insert SETTING on seed batches")
	cmd.Flags().BoolVar(&f.waitAsyncInsert, "wait-async", false, "set wait_for_async_insert=1 when async-insert is true")
	cmd.Flags().Uint64Var(&f.rngSeed, "seed", 42, "RNG seed for deterministic data generation")
	cmd.Flags().IntVar(&f.namespaces, "namespaces", 1, "number of namespaces to spread seeded rows across (multi-tenant table)")
	cmd.Flags().BoolVar(&f.mixedValue, "mixed-value", false, "emit baseline `value` in mixed JSON storage (number/string/bigint) to exercise the type-agnostic correctness fix")
	cmd.Flags().BoolVar(&f.skipSeed, "skip-seed", false, "skip seeding (run queries against existing data)")
	cmd.Flags().StringVar(&f.timeEnd, "time-end", "", "pin seed TimeEnd (RFC3339, e.g. 2026-06-01T00:00:00Z); shared across scenarios so their event time windows are byte-identical. Default: time.Now() truncated to the minute (per-scenario).")
	cmd.SilenceUsage = true
	cmd.AddCommand(newCompareCmd(), newCogsCmd())
	return cmd
}

func run(ctx context.Context, f *flags) error {
	if err := runner.EnsureBinary(); err != nil {
		return err
	}
	if _, err := f.resolveTimeEnd(); err != nil { // fail fast on a bad --time-end
		return err
	}

	commit := runner.HarnessCommit()
	if strings.HasSuffix(commit, "-dirty") {
		if f.requireClean {
			return fmt.Errorf("harness tree is dirty (%s); commit changes or drop --require-clean", commit)
		}
		fmt.Fprintf(os.Stderr, "warning: harness tree is dirty (%s); results will be tagged -dirty and are not reproducible from the commit alone\n", commit)
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
		fmt.Printf(" queries: %d (iterations=%d, concurrency=%v, repeat=%d, cold-paired=%v)\n",
			len(queries), f.iterations, f.concurrency, f.repeat, f.coldPaired)

		// Cache states to measure per query: warm always; cold too when paired.
		cacheStates := []bool{false}
		if f.coldPaired {
			cacheStates = append(cacheStates, true)
		}

		// Capture the index-pruning signal once (it's a property of the table +
		// index, independent of repeat/concurrency/cache). Attached to the
		// matching query's results below.
		pruning := captureIndexPruning(ctx, conn, sc, table, defaultParams(f))

		// Capture each query's normalized result digest once (a property of
		// (scenario, query, params), independent of the timed variants). Stored in
		// the run JSON so `bench compare` can check value parity across scenarios
		// with no extra DB round-trip.
		valueParity := captureValueDigests(ctx, conn, sc, f)

		var results []runner.BenchResult
		for rep := 0; rep < f.repeat; rep++ {
			for _, conc := range f.concurrency {
				for _, cold := range cacheStates {
					benchOpts := runner.BenchOpts{
						Host:        host,
						Port:        port,
						Secure:      opts.TLS != nil,
						Database:    opts.Auth.Database,
						User:        opts.Auth.Username,
						Password:    opts.Auth.Password,
						Iterations:  f.iterations,
						Concurrency: conc,
						ColdCache:   cold,
						Params:      defaultParams(f),
						Scenario:    sc.Name,
						SweepID:     sweepID,
						CPUProbe:    cpuProbe,
					}
					for _, q := range queries {
						label := q.Name
						if len(f.concurrency) > 1 || f.coldPaired || f.repeat > 1 {
							label = fmt.Sprintf("%s [c=%d %s r%d]", q.Name, conc, cacheTag(cold), rep+1)
						}
						fmt.Printf("  %-52s", label)
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
						if pruning != nil && res.Name == lookupByIDQuery {
							res.IndexPruning = pruning
						}
						results = append(results, res)
					}
				}
			}
		}

		runRecord := runner.Run{
			Scenario:           sc.Name,
			HarnessCommit:      commit,
			SweepID:            sweepID,
			StartedAt:          startedAt,
			FinishedAt:         time.Now(),
			ClusterFingerprint: fingerprint,
			Concurrency:        f.concurrency,
			Repeat:             f.repeat,
			Ingest:             ingest,
			Queries:            results,
			ValueParity:        valueParity,
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

// lookupByIDQuery is the query name whose bloom_filter pruning we capture via
// EXPLAIN. Only scenarios that ship this query (with a bloom on id) get a
// pruning signal; others get nil.
const lookupByIDQuery = "lookup_by_id"

func cacheTag(cold bool) string {
	if cold {
		return "cold"
	}
	return "warm"
}

// captureIndexPruning runs `EXPLAIN indexes=1` for a literal-id lookup on table,
// once with skip indexes disabled and once enabled, and returns the granule/part
// counts each scans. Returns nil (no error surfaced) when the scenario has no
// lookup_by_id query, the table is empty, or EXPLAIN fails — the signal is
// best-effort enrichment, never a reason to abort the run.
func captureIndexPruning(ctx context.Context, conn driver.Conn, sc runner.Scenario, table string, params map[string]string) *runner.IndexPruning {
	hasLookup := false
	for _, q := range runner.LoadQueries(sc) {
		if q.Name == lookupByIDQuery {
			hasLookup = true
			break
		}
	}
	if !hasLookup {
		return nil
	}

	ns := strings.Trim(params["namespace"], "'")
	var literalID string
	idQ := fmt.Sprintf("SELECT id FROM %s WHERE namespace = ? ORDER BY namespace, type, subject, time LIMIT 1 OFFSET 1000", table)
	if err := conn.QueryRow(ctx, idQ, ns).Scan(&literalID); err != nil {
		fmt.Fprintf(os.Stderr, "warning: index-pruning capture skipped (resolve id: %v)\n", err)
		return nil
	}

	explain := func(useSkip int) (granules, parts int, err error) {
		q := fmt.Sprintf(
			"EXPLAIN indexes = 1 SELECT count() FROM %s WHERE namespace = ? AND id = ? SETTINGS use_skip_indexes = %d",
			table, useSkip)
		rows, err := conn.Query(ctx, q, ns, literalID)
		if err != nil {
			return 0, 0, err
		}
		defer rows.Close()
		// EXPLAIN indexes=1 prints a Granules:/Parts: line per index (MinMax,
		// Partition, PrimaryKey, then each Skip index), in application order; each
		// numerator is the running survivor count, so the LAST line is the
		// granules/parts actually scanned. Keeping the last match is therefore
		// correct. Assumes ≤1 *pruning* skip index (here only the id bloom prunes
		// an id-lookup; the stored_at minmax is pass-through), so ordering can't
		// pick the wrong index. Verified against EXPLAIN: bloom prunes 26→1.
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return 0, 0, err
			}
			if g, ok := parseExplainCount(line, "Granules:"); ok {
				granules = g
			}
			if p, ok := parseExplainCount(line, "Parts:"); ok {
				parts = p
			}
		}
		return granules, parts, rows.Err()
	}

	gWithout, pWithout, err := explain(0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: index-pruning capture skipped (explain no-index: %v)\n", err)
		return nil
	}
	gWith, pWith, err := explain(1)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: index-pruning capture skipped (explain index: %v)\n", err)
		return nil
	}
	return &runner.IndexPruning{
		LiteralID:       literalID,
		GranulesWithout: gWithout,
		GranulesWith:    gWith,
		PartsWithout:    pWithout,
		PartsWith:       pWith,
	}
}

// captureValueDigests computes a normalized result digest for every query in
// the scenario, once, using the same params the timed run binds. The digests +
// the resolved time window go into the run JSON so `bench compare` can verify
// value parity across scenarios (same events → same meter output) by diffing
// digests, gated on an identical window. Best-effort: a per-query failure is
// recorded as a digest error, never aborts the run; returns nil only if params
// can't be built at all.
func captureValueDigests(ctx context.Context, conn driver.Conn, sc runner.Scenario, f *flags) *runner.ValueParity {
	params := defaultParams(f)
	// Record the window as a readable UTC wall-clock string (params["from"]/["to"]
	// are now Unix-second integers — TZ-independent for the SQL, but opaque in the
	// report and the compare-gate). Derived from the same resolved TimeEnd.
	cfg0 := seed.DefaultConfig()
	te := cfg0.TimeEnd
	if t, err := f.resolveTimeEnd(); err == nil {
		te = t
	}
	const utcFmt = "2006-01-02 15:04:05"
	vp := &runner.ValueParity{
		From:    te.Add(-cfg0.TimeSpan).UTC().Format(utcFmt),
		To:      te.UTC().Format(utcFmt),
		TimeEnd: f.timeEnd,
		Digests: map[string]runner.QueryDigest{},
	}
	for _, q := range runner.LoadQueries(sc) {
		sql, err := runner.RenderParams(q.SQL, params)
		if err != nil {
			vp.Digests[q.Name] = runner.QueryDigest{Error: err.Error()}
			continue
		}
		d, err := runner.DigestResult(ctx, conn, strings.TrimRight(strings.TrimSpace(sql), ";"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: value digest for %s/%s skipped: %v\n", sc.Name, q.Name, err)
		}
		vp.Digests[q.Name] = d
	}
	return vp
}

// parseExplainCount pulls the surviving count from an EXPLAIN line of the form
// "<label> <surviving>/<total>" (e.g. "Granules: 12/1224" → 12). The label may
// be indented and appear mid-line.
func parseExplainCount(line, label string) (int, bool) {
	idx := strings.Index(line, label)
	if idx < 0 {
		return 0, false
	}
	rest := strings.TrimSpace(line[idx+len(label):])
	slash := strings.Index(rest, "/")
	if slash < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(rest[:slash]))
	if err != nil {
		return 0, false
	}
	return n, true
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
	cfg.Namespaces = f.namespaces
	cfg.MixedValueStorage = f.mixedValue
	te, err := f.resolveTimeEnd()
	if err != nil {
		return nil, err
	}
	cfg.TimeEnd = te // shared across scenarios when --time-end is set → identical time windows

	fmt.Printf(" seed: %d rows (batch=%d, async=%v, namespaces=%d) ...\n",
		cfg.Rows, cfg.BatchSize, cfg.AsyncInsert, max(f.namespaces, 1))
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
func defaultParams(f *flags) map[string]string {
	cfg := seed.DefaultConfig()
	// Use the same TimeEnd the seeder used so query from/to cover the seeded span
	// exactly. With --time-end pinned this is shared across scenarios; without it,
	// it falls back to the seeder default (run() has already validated the flag).
	if te, err := f.resolveTimeEnd(); err == nil {
		cfg.TimeEnd = te
	}
	subjects := seed.Subjects(10)
	from := cfg.TimeEnd.Add(-cfg.TimeSpan)

	subjectsLit := make([]string, len(subjects))
	for i, s := range subjects {
		subjectsLit[i] = sqlString(s)
	}

	return map[string]string{
		"namespace": sqlString(cfg.Namespace),
		"type":      sqlString(cfg.Types[0]),
		// from/to are rendered as Unix-second integers, NOT wall-clock string
		// literals: a bare 'YYYY-MM-DD hh:mm:ss' passed to toDateTime() is parsed
		// in the server/session timezone (e.g. Europe/Budapest), which shifts the
		// scan window off the UTC instants the seeder wrote and silently drops the
		// edge hours. A Unix timestamp is timezone-independent, so toDateTime({from})
		// resolves to exactly the intended instant on any server.
		"from":     strconv.FormatInt(from.UTC().Unix(), 10),
		"to":       strconv.FormatInt(cfg.TimeEnd.UTC().Unix(), 10),
		"subjects": "(" + strings.Join(subjectsLit, ", ") + ")",
		"group1":   sqlString(cfg.Group1[0]),
		"group2":   sqlString(cfg.Group2[0]),
		// LLM-meter dim filter (kong.llm_request model groupBy): a model value the
		// seeder emits, so dim-filtered queries render against real seed data.
		"model": sqlString("claude-haiku"),
	}
}

// sqlString quotes s as a ClickHouse string literal. Values used here come
// from the seeder's own config (not user input), so escaping just needs to
// handle the apostrophe case.
func sqlString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// scenarioTable derives the table name for a scenario; see runner.ScenarioTable.
func scenarioTable(name string) string {
	return runner.ScenarioTable(name)
}
