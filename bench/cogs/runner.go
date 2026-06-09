package cogs

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/oklog/ulid/v2"

	"github.com/openmeterio/ch-playground/bench/accounting"
	"github.com/openmeterio/ch-playground/bench/ingest"
	"github.com/openmeterio/ch-playground/bench/pricing"
	"github.com/openmeterio/ch-playground/bench/replay"
	"github.com/openmeterio/ch-playground/bench/runner"
	"github.com/openmeterio/ch-playground/bench/seed"
)

// Opts configures one cogs run.
type Opts struct {
	CellPath     string // path to the cell manifest
	RunProfile   string // "" (manifest durations) or "ci"
	SkipInit     bool   // reuse existing table + data (skips init.sql AND preload)
	ResultsDir   string
	ScenariosDir string
	PricingDir   string
	Database     string // database of the connection (for system-table filters)
	HarnessCommit string

	// UsageExport, when set, reconciles the run against a Cloud usage export.
	UsageExport string

	// PricingOverride replaces the cell's pricing_profile (e.g. local-zero for
	// resource-accounting-only runs); recorded in the result via the embedded
	// profile, so results stay self-describing.
	PricingOverride string
	// PreloadOverride replaces the cell's preload_rows when >= 0 (smoke runs).
	PreloadOverride int
	// PreloadWorkers seeds the preload over N parallel connections, each
	// writing a disjoint generator index range — byte-identical data to a
	// sequential seed (the generator is a pure function of (seed, index));
	// only insertion order differs. <= 1 means sequential.
	PreloadWorkers int

	// Logf logs phase progress to stderr.
	Logf func(format string, args ...any)
}

// foreignDBThreshold is the size above which another database on the service
// triggers the shared-service contamination warning.
const foreignDBThreshold = 100 << 20 // 100 MiB

// logFlushSettle covers the default query_log flush interval (7.5s) when only
// a local flush is available.
const logFlushSettle = 8 * time.Second

// RunCell executes one workload cell end to end and writes its result files.
// Errors that still allow a useful partial report are recorded in the result
// rather than aborting (same convention as the perf path); a returned error
// means no report could be produced.
func RunCell(ctx context.Context, conn driver.Conn, opts Opts) (Result, error) {
	logf := opts.Logf
	if logf == nil {
		logf = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
	}

	// ---- Prepare -----------------------------------------------------------
	cell, err := LoadCell(opts.CellPath)
	if err != nil {
		return Result{}, err
	}
	if err := cell.ApplyProfile(opts.RunProfile); err != nil {
		return Result{}, err
	}
	if opts.PricingOverride != "" {
		cell.PricingProfile = opts.PricingOverride
	}
	if opts.PreloadOverride >= 0 {
		cell.PreloadRows = opts.PreloadOverride
	}

	profile, err := pricing.Load(filepath.Join(opts.PricingDir, cell.PricingProfile+".json"))
	if err != nil {
		return Result{}, err
	}

	scenarios, err := runner.Discover(opts.ScenariosDir, []string{cell.Scenario})
	if err != nil {
		return Result{}, err
	}
	if len(scenarios) != 1 {
		return Result{}, fmt.Errorf("cogs: scenario %q not found in %s", cell.Scenario, opts.ScenariosDir)
	}
	sc := scenarios[0]
	table := runner.ScenarioTable(cell.Scenario)
	scenarioDir := filepath.Join(opts.ScenariosDir, cell.Scenario)

	res := Result{
		Kind:          "cogs/v1",
		RunID:         ulid.Make().String(),
		Cell:          cell,
		Scenario:      cell.Scenario,
		HarnessCommit: opts.HarnessCommit,
		StartedAt:     time.Now().UTC(),
		Pricing:       profile,
		Errors:        []string{},
	}
	res.Phases.Profile = opts.RunProfile
	recordErr := func(stage string, err error) {
		logf("  warning: %s: %v", stage, err)
		res.Errors = append(res.Errors, stage+": "+err.Error())
	}

	logf("== cogs cell %s on %s (run %s) ==", cell.Name, cell.Scenario, res.RunID)

	target, err := accounting.DetectTarget(ctx, conn)
	if err != nil {
		return Result{}, err
	}
	capacity, err := accounting.DetectCapacity(ctx, conn, target)
	if err != nil {
		return Result{}, err
	}
	res.Accounting.Capacity = capacity
	if fp, err := runner.Fingerprint(ctx, conn); err == nil {
		res.Cluster = fp
	} else {
		recordErr("fingerprint", err)
	}

	// Shape cross-check: pricing on the wrong shape must never be silent.
	if capacity.Replicas != profile.Service.Replicas ||
		math.Abs(capacity.VCPUsPerReplica-profile.Service.VCPUsPerReplica) > 0.01 {
		res.Flags.ShapeMismatch = true
		logf("  WARNING: detected shape (%d replicas x %.1f vCPU) != pricing profile %q (%d x %.1f); costs will be priced on the profile shape",
			capacity.Replicas, capacity.VCPUsPerReplica, profile.Name,
			profile.Service.Replicas, profile.Service.VCPUsPerReplica)
	}

	if foreign, err := accounting.ForeignDatabases(ctx, conn, opts.Database, foreignDBThreshold); err == nil {
		res.Flags.ForeignDatabases = len(foreign)
		for _, d := range foreign {
			logf("  WARNING: foreign database %q holds %d bytes on this service; COGS methodology requires a dedicated service", d.Database, d.Bytes)
		}
	} else {
		recordErr("foreign-database scan", err)
	}

	// Mix + queries (only when the cell replays).
	var mix replay.Mix
	var queryTemplates map[string]string
	if cell.Query.QPS > 0 {
		queries := runner.LoadQueries(sc)
		names := make([]string, len(queries))
		queryTemplates = make(map[string]string, len(queries))
		for i, q := range queries {
			names[i] = q.Name
			queryTemplates[q.Name] = q.SQL
		}
		mix, err = replay.LoadMix(scenarioDir, cell.Query.Mix, names)
		if err != nil {
			return Result{}, err
		}
		res.Flags.MixNotes = mix.Notes
	}

	if !opts.SkipInit {
		logf(" init.sql ...")
		if err := conn.Exec(ctx, "DROP TABLE IF EXISTS "+table+" SYNC"); err != nil {
			return Result{}, fmt.Errorf("cogs: drop %s: %w", table, err)
		}
		if err := runner.ApplyInit(ctx, conn, sc, opts.Database); err != nil {
			return Result{}, err
		}
		if cell.PreloadRows > 0 {
			workers := max(1, opts.PreloadWorkers)
			logf(" preload: %d rows (%d workers) ...", cell.PreloadRows, workers)
			if err := parallelPreload(ctx, conn, seedConfig(cell, table), cell.PreloadRows, workers); err != nil {
				return Result{}, fmt.Errorf("cogs: preload: %w", err)
			}
		}
	}

	if snap, err := accounting.SnapshotStorage(ctx, conn, opts.Database, table); err == nil {
		res.Accounting.Storage.Prepare = snap
	} else {
		recordErr("prepare storage snapshot", err)
	}

	// ---- Soak --------------------------------------------------------------
	soakStart := time.Now().UTC()
	if cell.Soak.D() > 0 {
		logf(" soak: %s (plateau gate: ±10%% over %d polls) ...", cell.Soak.D(), accounting.PlateauWindow)
		res.Phases.PartsPlateau = runSoak(ctx, conn, cell, table, opts.Database, logf)
	} else {
		// No soak phase: nothing to gate on.
		res.Phases.PartsPlateau = true
	}
	res.Phases.Soak = phase(soakStart)
	if snap, err := accounting.SnapshotStorage(ctx, conn, opts.Database, table); err == nil {
		res.Accounting.Storage.SoakEnd = snap
	} else {
		recordErr("soak storage snapshot", err)
	}

	// ---- Measure -----------------------------------------------------------
	logf(" measure: %s (ingest %d eps, replay %.2f qps) ...", cell.Measure.D(), cell.Ingest.EventsPerSec, cell.Query.QPS)
	measureStart := time.Now().UTC()

	type ingestOut struct {
		res ingest.Result
		err error
	}
	type replayOut struct {
		res replay.Result
		err error
	}
	ingestCh := make(chan ingestOut, 1)
	replayCh := make(chan replayOut, 1)

	if cell.Ingest.EventsPerSec > 0 {
		go func() {
			gen, err := seed.NewGenerator(seedConfig(cell, table))
			if err != nil {
				ingestCh <- ingestOut{err: err}
				return
			}
			r, err := ingest.Run(ctx, ingest.Config{
				EventsPerSec:  cell.Ingest.EventsPerSec,
				BatchMaxRows:  cell.Ingest.BatchMaxRows,
				FlushInterval: cell.Ingest.FlushInterval.D(),
				Duration:      cell.Measure.D(),
				Gen:           gen,
				Inserter: &ingest.CHInserter{
					Conn:       conn,
					Table:      table,
					LogComment: fmt.Sprintf(`{"cogs_run":%q,"component":"ingest"}`, res.RunID),
					Settings:   ingestSettings(cell),
				},
			})
			ingestCh <- ingestOut{res: r, err: err}
		}()
	} else {
		ingestCh <- ingestOut{}
	}

	if cell.Query.QPS > 0 {
		go func() {
			r, err := replay.Run(ctx, replay.Config{
				QPS:            cell.Query.QPS,
				Arrival:        cell.Query.Arrival,
				ColdFraction:   cell.Query.ColdFraction,
				ConcurrencyCap: cell.Query.ConcurrencyCap,
				Duration:       cell.Measure.D(),
				Mix:            mix,
				Queries:        queryTemplates,
				Params:         replayParams(),
				TimeSpan:       seed.DefaultConfig().TimeSpan,
				Settings:       cell.Query.Settings,
				RunID:          res.RunID,
				Seed:           cell.Ingest.Seed,
				Executor:       &chExecutor{conn: conn},
			})
			replayCh <- replayOut{res: r, err: err}
		}()
	} else {
		replayCh <- replayOut{}
	}

	iOut := <-ingestCh
	rOut := <-replayCh
	measureEnd := time.Now().UTC()
	res.Phases.Measure = PhaseInfo{Start: measureStart, End: measureEnd, Seconds: measureEnd.Sub(measureStart).Seconds()}

	if ctx.Err() != nil {
		// SIGINT mid-measure: the truncated window still gets collected,
		// priced, and reported — flagged.
		res.Flags.Truncated = true
		logf("  interrupted: reporting over the truncated %.0fs window", res.Phases.Measure.Seconds)
	}
	if cell.Ingest.EventsPerSec > 0 {
		if iOut.err != nil && ctx.Err() == nil {
			recordErr("ingest", iOut.err)
		}
		res.Ingest = &iOut.res
		res.Flags.Saturated = !iOut.res.RateSatisfied
	}
	if cell.Query.QPS > 0 {
		if rOut.err != nil && ctx.Err() == nil {
			recordErr("replay", rOut.err)
		}
		res.Replay = &rOut.res
	}

	// ---- Drain -------------------------------------------------------------
	drainStart := time.Now().UTC()
	if cell.Drain.D() > 0 && ctx.Err() == nil {
		logf(" drain: %s (catching merges from measure-window inserts) ...", cell.Drain.D())
		select {
		case <-ctx.Done():
		case <-time.After(cell.Drain.D()):
		}
	}
	drainEnd := time.Now().UTC()
	res.Phases.Drain = PhaseInfo{Start: drainStart, End: drainEnd, Seconds: drainEnd.Sub(drainStart).Seconds()}

	// ---- Collect -----------------------------------------------------------
	// Collection runs on a fresh context: a SIGINT-cancelled ctx must not
	// prevent the truncated report.
	cctx := context.WithoutCancel(ctx)
	logf(" collect ...")
	res.Flags.LogFlush = accounting.FlushLogs(cctx, conn, target, logFlushSettle)

	measureWindow := accounting.Window{Start: measureStart, End: measureEnd}
	mergeWindow := accounting.Window{Start: measureStart, End: drainEnd}

	if ql, err := accounting.CollectQueryLog(cctx, conn, target, res.RunID, measureWindow); err == nil {
		res.Accounting.QueryLog = ql
		res.Flags.CPUSource = ql.CPUSource
	} else {
		recordErr("query_log collection", err)
	}
	if m, err := accounting.CollectPartLog(cctx, conn, target, opts.Database, table, mergeWindow); err == nil {
		res.Accounting.Merges = m
		res.Flags.MergeCPUEstimated = m.CPUEstimated
	} else {
		recordErr("part_log collection", err)
	}
	if cell.Ingest.AsyncInsert {
		if as, err := accounting.CollectAsyncInserts(cctx, conn, target, opts.Database, table, measureWindow); err == nil {
			res.Accounting.Async = &as
			res.Flags.AsyncAttributionPartial = as.Partial
		} else {
			recordErr("async insert collection", err)
		}
	}
	if snap, err := accounting.SnapshotStorage(cctx, conn, opts.Database, table); err == nil {
		res.Accounting.Storage.DrainEnd = snap
	} else {
		recordErr("drain storage snapshot", err)
	}

	res.Accounting.MeasureSeconds = res.Phases.Measure.Seconds
	if res.Ingest != nil {
		res.Accounting.EventsIngested = res.Ingest.Events
	}

	// ---- Price + report ----------------------------------------------------
	res.Attribution = Attribute(res.Accounting)
	res.Costs = Price(profile, res.Accounting, res.Attribution)

	if opts.UsageExport != "" {
		modelStorageTB := float64(res.Accounting.Storage.DrainEnd.CompressedBytes) / 1e12
		rec, err := ReconcileFile(opts.UsageExport, profile, res.Phases.Measure, modelStorageTB)
		if err != nil {
			recordErr("reconciliation", err)
		} else {
			res.Reconciliation = &rec
		}
	}

	res.FinishedAt = time.Now().UTC()
	return res, nil
}

// parallelPreload splits [0, rows) into per-worker generator index ranges and
// seeds them concurrently. base.TimeEnd is already pinned by the caller, so
// every worker draws event times from the identical window.
func parallelPreload(ctx context.Context, conn driver.Conn, base seed.Config, rows, workers int) error {
	if workers <= 1 {
		base.Rows = rows
		_, err := seed.Run(ctx, conn, base)
		return err
	}
	chunk := rows / workers
	errs := make(chan error, workers)
	for w := range workers {
		cfg := base
		cfg.StartRow = w * chunk
		cfg.Rows = chunk
		if w == workers-1 {
			cfg.Rows = rows - cfg.StartRow // remainder to the last worker
		}
		go func() {
			_, err := seed.Run(ctx, conn, cfg)
			errs <- err
		}()
	}
	var firstErr error
	for range workers {
		if err := <-errs; err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// runSoak drives ingest (when the cell ingests) while polling the parts
// plateau; it may end early once the plateau is reached.
func runSoak(ctx context.Context, conn driver.Conn, cell Cell, table, database string, logf func(string, ...any)) bool {
	soakCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan struct{})
	if cell.Ingest.EventsPerSec > 0 {
		go func() {
			defer close(done)
			gen, err := seed.NewGenerator(seedConfig(cell, table))
			if err != nil {
				logf("  warning: soak generator: %v", err)
				return
			}
			_, _ = ingest.Run(soakCtx, ingest.Config{
				EventsPerSec:  cell.Ingest.EventsPerSec,
				BatchMaxRows:  cell.Ingest.BatchMaxRows,
				FlushInterval: cell.Ingest.FlushInterval.D(),
				Duration:      cell.Soak.D(),
				Gen:           gen,
				Inserter: &ingest.CHInserter{
					Conn:     conn,
					Table:    table,
					Settings: ingestSettings(cell),
					// Soak inserts are tagged harness, not ingest: they are
					// outside the measure window and must not be attributed.
					LogComment: `{"component":"harness","cogs_run":"soak"}`,
				},
			})
		}()
	} else {
		close(done)
	}

	// Poll interval scales down for short (CI) soaks so the 5-poll rule can
	// actually fire.
	interval := min(30*time.Second, max(time.Second, cell.Soak.D()/(2*accounting.PlateauWindow)))
	deadline := time.Now().Add(cell.Soak.D())
	plateau := &accounting.Plateau{}
	reached := false
	for time.Now().Before(deadline) && ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case <-time.After(interval):
		}
		n, err := accounting.ActivePartCount(ctx, conn, database, table)
		if err != nil {
			continue
		}
		if plateau.Observe(n) {
			reached = true
			break
		}
	}
	cancel()
	<-done
	return reached
}

func phase(start time.Time) PhaseInfo {
	end := time.Now().UTC()
	return PhaseInfo{Start: start, End: end, Seconds: end.Sub(start).Seconds()}
}

// seedConfig builds the generator config the cell's preload, soak, and
// measure ingest share. TimeEnd pins to the current minute so preloaded
// history is contiguous up to run start and the replayer's sliding window
// covers it.
func seedConfig(cell Cell, table string) seed.Config {
	cfg := seed.DefaultConfig()
	cfg.Table = table
	cfg.Seed = cell.Ingest.Seed
	cfg.Namespaces = cell.Ingest.Namespaces
	cfg.MixedValueStorage = cell.Ingest.MixedValue
	cfg.TimeEnd = time.Now().UTC().Truncate(time.Minute)
	return cfg
}

func ingestSettings(cell Cell) map[string]any {
	s := map[string]any{}
	if cell.Ingest.AsyncInsert {
		s["async_insert"] = 1
		s["wait_for_async_insert"] = 1
	}
	return s
}

// replayParams is the v1 fixed parameter set (same values the perf path
// binds), minus from/to which the replayer binds per arrival.
func replayParams() map[string]string {
	cfg := seed.DefaultConfig()
	subjects := seed.Subjects(10)
	lit := make([]string, len(subjects))
	for i, s := range subjects {
		lit[i] = "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
	return map[string]string{
		"namespace": "'" + cfg.Namespace + "'",
		"type":      "'" + cfg.Types[0] + "'",
		"subjects":  "(" + strings.Join(lit, ", ") + ")",
		"group1":    "'" + cfg.Group1[0] + "'",
		"group2":    "'" + cfg.Group2[0] + "'",
		"model":     "'claude-haiku'",
	}
}

// chExecutor drains the query so the server does the full work query_log
// accounts.
type chExecutor struct {
	conn driver.Conn
}

func (e *chExecutor) Exec(ctx context.Context, sql string, settings map[string]any) error {
	sctx := clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings(settings)))
	rows, err := e.conn.Query(sctx, sql)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
	}
	return rows.Err()
}
