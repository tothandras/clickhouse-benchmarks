// Command cachebench is a standalone manual benchmark for the application-layer
// meter query-result cache (bench/cache). It is intentionally NOT wired into the
// clickhouse-benchmark scenario harness: that harness times one SQL statement
// per query, while the cache's value is partial-range reuse across a cached
// history table and a live tail scan, driven from Go.
//
// It runs READ-ONLY on an existing events table (default: proposal_events, the
// recommended 10M-row design). It NEVER drops or seeds that table — it only
// creates and truncates its own cache table. The events table is input.
//
// What it does, end to end, per meter:
//  1. Read the seeded window from min/max(time) on the events table.
//  2. For each freshness cutoff on the --cutoffs curve:
//     a. (Re)populate the cache with hourly × subject × group-by rollups of the
//        history [from, cutoff), using the IDENTICAL filter + extraction as the
//        query.
//     b. VERIFY cached == uncached values for every aggregation, keyed on the
//        FULL (window, subject, group-by) tuple — billing-safety gate.
//     c. Time cached vs uncached over --iterations runs; report median.
//
// Usage (from inside the devenv shell):
//
//	export CLICKHOUSE_DSN="clickhouse://default:@127.0.0.1:9000/default"
//	go run ./bench/cmd/cachebench --events proposal_events --cutoffs 1,6,24,48
//
// --cutoffs are HOURS of fresh tail to leave uncached.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/openmeterio/ch-playground/bench/cache"
	"github.com/openmeterio/ch-playground/bench/seed"
)

const cacheTable = "cachebench_cache"

// meter is one concrete grouped meter to benchmark against real seeded data.
type meter struct {
	name         string
	typ          string
	valueExpr    string   // billing-exact value extraction; "" => count-only meter
	groupByPaths []string // data JSON paths to group by
	aggs         []cache.Aggregation
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		eventsTable string
		iterations  int
		cutoffsCSV  string
		verifyOnly  bool
	)
	flag.StringVar(&eventsTable, "events", "proposal_events", "EXISTING events table to read (never dropped/seeded)")
	flag.IntVar(&iterations, "iterations", 10, "timed iterations per query (median reported)")
	flag.StringVar(&cutoffsCSV, "cutoffs", "1,6,24,48", "fresh-tail sizes in HOURS, comma-separated (curve points)")
	flag.BoolVar(&verifyOnly, "verify", false, "only verify cached==uncached parity (skip timing); exits non-zero on any mismatch")
	flag.Parse()

	dsn := os.Getenv("CLICKHOUSE_DSN")
	if dsn == "" {
		dsn = "clickhouse://default:@127.0.0.1:9000/default"
	}
	opts, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		return fmt.Errorf("parse DSN: %w", err)
	}
	conn, err := clickhouse.Open(opts)
	if err != nil {
		return fmt.Errorf("open clickhouse: %w", err)
	}
	defer conn.Close()

	ctx := context.Background()
	if err := conn.Ping(ctx); err != nil {
		return fmt.Errorf("ping clickhouse (is it running at %s?): %w", dsn, err)
	}

	// Read-only guard: the events table must already exist. We only ever write to
	// our own cache table; the events table is never created, dropped, or seeded.
	from, to, err := eventsWindow(ctx, conn, eventsTable)
	if err != nil {
		return fmt.Errorf("read events window from %s (does it exist?): %w", eventsTable, err)
	}

	cfg := seed.DefaultConfig()
	subjects := seed.Subjects(10)

	cutoffHours, err := parseCutoffs(cutoffsCSV)
	if err != nil {
		return err
	}

	// Meters chosen for real group-by paths present in the seeded data.
	decimal := "toDecimal128OrNull(toString(data.value), 19)"
	meters := []meter{
		{
			name: "api_request (SUM value BY group1,group2)", typ: "api_request",
			valueExpr: decimal, groupByPaths: []string{"group1", "group2"},
			aggs: []cache.Aggregation{cache.AggSum, cache.AggCount, cache.AggMin, cache.AggMax},
		},
		{
			name: "kong.llm_request (SUM tokens BY model,provider)", typ: "kong.llm_request",
			valueExpr: "toDecimal128OrNull(toString(data.tokens), 19)", groupByPaths: []string{"model", "provider"},
			aggs: []cache.Aggregation{cache.AggSum, cache.AggCount},
		},
	}

	fmt.Printf("\n=== cachebench (read-only on %s) ===\n", eventsTable)
	fmt.Printf("window=[%s .. %s]  iterations=%d\n",
		from.UTC().Format("2006-01-02 15:04"), to.UTC().Format("2006-01-02 15:04"), iterations)
	fmt.Printf("value parity (keyed on window+windowend+subject+group-by, exact decimal) is\n")
	fmt.Printf("asserted for every (meter, cutoff, aggregation) BEFORE timing — including the\n")
	fmt.Printf("two extremes (whole range cached, nothing cached). Any mismatch fails the run.\n")

	if verifyOnly {
		fmt.Printf("(--verify: parity only, no timing)\n")
	}

	stats := &parityStats{}
	for _, m := range meters {
		if err := runMeter(ctx, conn, eventsTable, m, cfg.Namespace, subjects, from, to, cutoffHours, iterations, verifyOnly, stats); err != nil {
			return fmt.Errorf("meter %q: %w", m.name, err)
		}
	}

	fmt.Printf("\n=== parity: %d/%d checks passed ===\n", stats.passed, stats.passed+stats.failed)
	if stats.failed > 0 {
		return fmt.Errorf("PARITY VERIFICATION FAILED: %d of %d checks differ between cached and uncached",
			stats.failed, stats.passed+stats.failed)
	}
	fmt.Println("with and without the cache produce IDENTICAL results on every check.")
	return nil
}

// parityStats accumulates pass/fail across every parity check so the run can
// fail hard if cached and uncached ever differ.
type parityStats struct {
	passed int
	failed int
}

// cutoffPoint is one freshness boundary to test, with a human label.
type cutoffPoint struct {
	label  string
	cutoff time.Time
}

func runMeter(ctx context.Context, conn driver.Conn, eventsTable string, m meter, namespace string, subjects []string, from, to time.Time, cutoffHours []int, iterations int, verifyOnly bool, stats *parityStats) error {
	if err := recreateCacheTable(ctx, conn); err != nil {
		return err
	}
	c := cache.New(conn, eventsTable, cacheTable, m.valueExpr)
	p := cache.Params{
		Namespace:    namespace,
		Type:         m.typ,
		Subjects:     subjects,
		GroupByPaths: m.groupByPaths,
		From:         from,
		To:           to,
	}

	// Build the cutoff list, including the two EXTREMES where merge bugs hide:
	//   - all-fresh  (cutoff = from): the cached path's tail leg IS the full query,
	//     so the cache table is empty and the result must trivially equal uncached.
	//   - all-cached (cutoff = to):   the entire range is served from the cache
	//     table alone (empty fresh tail) — exercises the cache leg in isolation.
	points := []cutoffPoint{{label: "all-fresh (0% cached)", cutoff: from}}
	for _, ch := range cutoffHours {
		cutoff := to.Add(-time.Duration(ch) * time.Hour)
		if cutoff.After(from) && cutoff.Before(to) {
			points = append(points, cutoffPoint{label: fmt.Sprintf("cutoff=%dh", ch), cutoff: cutoff})
		}
	}
	points = append(points, cutoffPoint{label: "all-cached (100% cached)", cutoff: to})

	fmt.Printf("\n############ %s ############\n", m.name)

	dumpedSample := false
	for _, pt := range points {
		if err := truncateCache(ctx, conn); err != nil {
			return err
		}
		if err := c.PopulateCache(ctx, p, pt.cutoff); err != nil {
			return fmt.Errorf("populate cache (%s): %w", pt.label, err)
		}

		cachedFrac := 100.0 * pt.cutoff.Sub(from).Seconds() / to.Sub(from).Seconds()
		fmt.Printf("\n-- %s  (history cached: %.0f%%, fresh tail: %.0f%%) --\n",
			pt.label, cachedFrac, 100-cachedFrac)
		fmt.Printf("%-7s  %12s  %12s  %9s  %s\n", "agg", "uncached", "cached", "speedup", "parity")

		for _, agg := range m.aggs {
			un, err := c.QueryUncached(ctx, p, agg)
			if err != nil {
				return fmt.Errorf("%s uncached: %w", agg, err)
			}
			ca, err := c.QueryCached(ctx, p, agg, pt.cutoff)
			if err != nil {
				return fmt.Errorf("%s cached: %w", agg, err)
			}

			if diff := compareRows(un, ca); diff != "" {
				stats.failed++
				fmt.Printf("%-7s  %12s  %12s  %9s  PARITY FAIL: %s\n", agg, "-", "-", "-", diff)
				continue
			}
			stats.passed++

			// Show a few concrete row pairs once, so "they're the same" is visible
			// to a human, not just asserted.
			if !dumpedSample && agg == cache.AggSum && len(un) > 0 {
				dumpSample(agg, un, ca)
				dumpedSample = true
			}

			if verifyOnly {
				fmt.Printf("%-7s  %12s  %12s  %9s  %s (%d rows)\n", agg, "-", "-", "-", "OK", len(un))
				continue
			}

			unMed := timeIt(iterations, func() { _, _ = c.QueryUncached(ctx, p, agg) })
			caMed := timeIt(iterations, func() { _, _ = c.QueryCached(ctx, p, agg, pt.cutoff) })

			speedup := float64(unMed) / float64(caMed)
			fmt.Printf("%-7s  %12s  %12s  %8.2fx  %s (%d rows)\n",
				agg, unMed.Round(time.Microsecond), caMed.Round(time.Microsecond), speedup, "OK", len(un))
		}
	}
	return nil
}

// dumpSample prints a few uncached/cached value pairs side by side so the
// equality is visible, not just asserted. Rows are already sorted by compareRows.
func dumpSample(agg cache.Aggregation, un, ca []cache.Row) {
	n := 3
	if len(un) < n {
		n = len(un)
	}
	fmt.Printf("   sample %s rows (uncached == cached):\n", agg)
	for i := 0; i < n; i++ {
		fmt.Printf("     %s | %s  uncached=%s  cached=%s\n",
			un[i].WindowStart.UTC().Format("2006-01-02T15:04"),
			strings.Join(un[i].GroupBy, ","),
			un[i].Value.String(), ca[i].Value.String())
	}
}

// eventsWindow returns [min(time), max(time)+1s) for the default namespace span.
func eventsWindow(ctx context.Context, conn driver.Conn, table string) (time.Time, time.Time, error) {
	var tmin, tmax time.Time
	row := conn.QueryRow(ctx, fmt.Sprintf("SELECT min(time), max(time) FROM %s", table))
	if err := row.Scan(&tmin, &tmax); err != nil {
		return time.Time{}, time.Time{}, err
	}
	// half-open [from, to): bump max by 1s so the newest event is included.
	return tmin.UTC(), tmax.UTC().Add(time.Second), nil
}

func recreateCacheTable(ctx context.Context, conn driver.Conn) error {
	if err := conn.Exec(ctx, "DROP TABLE IF EXISTS "+cacheTable+" SYNC"); err != nil {
		return fmt.Errorf("drop cache: %w", err)
	}
	return conn.Exec(ctx, fmt.Sprintf(`CREATE TABLE %s (
  namespace String, type LowCardinality(String), windowstart DateTime,
  subject String, group_by Array(String),
  sum_value Decimal128(19), count_value UInt64,
  min_value Decimal128(19), max_value Decimal128(19)
) ENGINE = MergeTree
ORDER BY (namespace, type, windowstart, subject)`, cacheTable))
}

func truncateCache(ctx context.Context, conn driver.Conn) error {
	return conn.Exec(ctx, "TRUNCATE TABLE IF EXISTS "+cacheTable)
}

// compareRows returns "" if the two result sets are value-identical, keyed on the
// FULL (window, subject, group-by) tuple — NOT window alone, which would line up
// different groups' rows against each other and make the parity check hollow.
func compareRows(a, b []cache.Row) string {
	if len(a) != len(b) {
		return fmt.Sprintf("row count differs: uncached=%d cached=%d", len(a), len(b))
	}
	sortRows(a)
	sortRows(b)
	for i := range a {
		if a[i].Key() != b[i].Key() {
			return fmt.Sprintf("key[%d] differs: uncached=%q cached=%q", i, a[i].Key(), b[i].Key())
		}
		// Exact decimal comparison, rounded to 6 places like digest.go — no float
		// epsilon (the float round-trip the proposal exists to avoid).
		if !a[i].Value.Round(6).Equal(b[i].Value.Round(6)) {
			return fmt.Sprintf("value @ %q differs: uncached=%s cached=%s",
				a[i].Key(), a[i].Value.String(), b[i].Value.String())
		}
	}
	return ""
}

func sortRows(r []cache.Row) {
	sort.Slice(r, func(i, j int) bool { return r[i].Key() < r[j].Key() })
}

// timeIt runs fn n times and returns the median wall-clock.
func timeIt(n int, fn func()) time.Duration {
	ds := make([]time.Duration, n)
	for i := range ds {
		start := time.Now()
		fn()
		ds[i] = time.Since(start)
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	return ds[n/2]
}

func parseCutoffs(csv string) ([]int, error) {
	parts := strings.Split(csv, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("bad cutoff %q: %w", p, err)
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no cutoffs parsed from %q", csv)
	}
	return out, nil
}
