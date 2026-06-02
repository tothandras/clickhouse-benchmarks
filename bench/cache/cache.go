// Package cache is a minimal, bug-fixed reimplementation of OpenMeter's
// application-layer meter query-result cache — enough to demonstrate and
// benchmark the one thing that makes it valuable: partial-range reuse. It reads
// the stable history from a small pre-aggregated hourly cache table and runs the
// expensive raw-event aggregation only over the fresh, uncacheable tail, then
// merges the two by re-aggregating.
//
// Like the reference cache, it caches at the grain of (window, subject, group-by
// values): one cache row per distinct hourly window × subject × group-by combo,
// so it serves grouped meter queries (e.g. SUM by region+tier), not just totals.
// The reference stores group-by values as a Map(String,String); here they are an
// ordered list aligned to GroupByPaths (same information, no map-key-order parity
// trap). It deliberately drops everything not needed to show the win: incremental
// head/tail goroutines, namespace invalidation, the TTL table, and the per-window
// NaN "gap" sentinel. Scope is the four MERGEABLE aggregations (SUM, COUNT, MIN,
// MAX); AVG and UNIQUE_COUNT are non-mergeable across partial windows and out of
// scope by design (see REVIEW-query-result-cache.md).
//
// Values are carried as Decimal128(19) end-to-end — billing-exact, matching the
// proposal queries (toDecimal128OrNull(toString(data.<path>), 19)) with no
// Float64 hop anywhere. The Go side uses alpacadecimal.Decimal (the same decimal
// type OpenMeter uses).
//
// The two bugs found in the original cache (REVIEW-query-result-cache.md) are
// fixed here and called out at their sites:
//   - Bug #1 (NaN != NaN in dedupe-on-read): the original stored a Float64 NaN
//     "no value" sentinel and compared it by `!=`; NaN != NaN always true →
//     duplicated sentinel rows errored. Two structural fixes here: there is no
//     sentinel (absent windows are simply absent rows), and values are exact
//     Decimals, which have NO NaN at all — so the bug class cannot exist.
//   - Bug #2 (pointer-keyed subject map): all grouping keys are string values.
package cache

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/alpacahq/alpacadecimal"
	"github.com/cespare/xxhash/v2"
	"github.com/shopspring/decimal"
)

// nullDecimal scans a ClickHouse Decimal128 (Nullable or not) into an
// alpacadecimal.Decimal. It mirrors OpenMeter's own meter_query.go wrapper,
// which exists because the clickhouse-go driver hands back a shopspring
// decimal.Decimal value, and shopspring's stock Scan rejects a decimal.Decimal
// source (its default branch only handles string/[]byte). We special-case that
// type — exactly as upstream does — then convert to alpacadecimal.
type nullDecimal struct {
	Decimal alpacadecimal.Decimal
	Valid   bool
}

func (n *nullDecimal) Scan(src any) error {
	if src == nil {
		n.Valid = false
		return nil
	}
	// The driver's native type for Decimal128 is shopspring decimal.Decimal.
	if d, ok := src.(decimal.Decimal); ok {
		n.Decimal = alpacadecimal.NewFromDecimal(d)
		n.Valid = true
		return nil
	}
	// Fallback for any other representation (string/float/int).
	var d alpacadecimal.Decimal
	if err := d.Scan(src); err != nil {
		return err
	}
	n.Decimal = d
	n.Valid = true
	return nil
}

// Aggregation is the meter aggregation type. Only the mergeable four are valid.
type Aggregation string

const (
	AggSum   Aggregation = "SUM"
	AggCount Aggregation = "COUNT"
	AggMin   Aggregation = "MIN"
	AggMax   Aggregation = "MAX"
)

// Mergeable reports whether an aggregation can be reconstructed from partial
// per-window aggregates — the precondition for this cache to serve it.
func (a Aggregation) Mergeable() bool {
	switch a {
	case AggSum, AggCount, AggMin, AggMax:
		return true
	default:
		return false
	}
}

// Params describes one meter query. Subjects is the explicit subject allow-list
// the meter filters to. GroupByPaths are the meter's group-by JSON paths within
// `data` (e.g. ["group1", "group2"] → data.group1, data.group2); the query
// groups by these dimensions and the cache stores one row per combo. All of
// (namespace, type, subjects, group-by paths) are part of the cache key, and all
// are applied identically when populating the cache, so cache and query cover
// the same rows and produce the same numbers.
type Params struct {
	Namespace    string
	Type         string
	Subjects     []string
	GroupByPaths []string
	From         time.Time
	To           time.Time
}

// Hash returns a deterministic cache key for the query shape. Multi-valued
// fields are sorted before hashing so key order doesn't matter (mirrors
// QueryParamsHash in the original). GroupByPaths are NOT sorted — their order is
// significant (it aligns the stored value list), but it's stable per meter.
func (p Params) Hash() string {
	h := xxhash.New()
	_, _ = h.WriteString(p.Namespace)
	_, _ = h.WriteString("\x00")
	_, _ = h.WriteString(p.Type)
	_, _ = h.WriteString("\x00")
	subs := append([]string(nil), p.Subjects...)
	sort.Strings(subs)
	_, _ = h.WriteString(strings.Join(subs, ","))
	_, _ = h.WriteString("\x00")
	_, _ = h.WriteString(strings.Join(p.GroupByPaths, ","))
	return fmt.Sprintf("%x", h.Sum64())
}

// Row is one result row: the aggregated value for a (window, subject, group-by)
// tuple. GroupBy holds the group-by values in the same order as
// Params.GroupByPaths. Value is an exact Decimal128(19), never a float.
// WindowEnd mirrors the proposal query's tumbleEnd column; it is a pure function
// of WindowStart (= WindowStart + 1h), so it can't independently break parity.
type Row struct {
	WindowStart time.Time
	WindowEnd   time.Time
	Subject     string
	GroupBy     []string
	Value       alpacadecimal.Decimal
}

// Key returns the full grouping key for parity comparison and dedupe — the
// tuple the query actually groups by (matching the proposal query's GROUP BY:
// windowstart, windowend, subject, group-by...). Comparing on anything less
// (e.g. window alone) would line up different groups' rows against each other.
func (r Row) Key() string {
	return r.WindowStart.UTC().Format(time.RFC3339) + "\x00" +
		r.WindowEnd.UTC().Format(time.RFC3339) + "\x00" +
		r.Subject + "\x00" + strings.Join(r.GroupBy, "\x01")
}

// The hourly tumble window, IDENTICAL to the proposal queries
// (tumbleStart/tumbleEnd over toIntervalHour(1) in UTC). Used at all three SQL
// sites — uncached control, cache population, cached query's fresh leg — so the
// windowstart/windowend values line up and parity holds.
const (
	windowStartExpr = "tumbleStart(time, toIntervalHour(1), 'UTC')"
	windowEndExpr   = "tumbleEnd(time, toIntervalHour(1), 'UTC')"
	// proposalSettings is the SETTINGS clause every proposal query carries; the
	// uncached control must include it too or the baseline is unfairly slow.
	proposalSettings = "SETTINGS optimize_move_to_prewhere = 1, allow_reorder_prewhere_conditions = 1"
)

// Connector runs cached and uncached meter queries against ClickHouse.
type Connector struct {
	conn        driver.Conn
	eventsTable string
	cacheTable  string
	valueExpr   string // billing-exact value extraction, identical in both paths
}

// New builds a Connector. valueExpr is the SQL expression that extracts a row's
// numeric value (e.g. toDecimal128OrNull(toString(data.value), 19)); it must be
// byte-identical between the cache-population INSERT and the uncached query, or
// values won't match.
func New(conn driver.Conn, eventsTable, cacheTable, valueExpr string) *Connector {
	return &Connector{conn: conn, eventsTable: eventsTable, cacheTable: cacheTable, valueExpr: valueExpr}
}

// groupExpr is the SQL that extracts one group-by path's value from `data`,
// IDENTICAL in cache population and query (matches the proposal scenario's
// sum_hour_group1_group2.sql). Missing path → empty string on both sides.
func groupExpr(path string) string {
	return fmt.Sprintf("toString(data.%s.:String)", path)
}

// groupSelectAliases returns the "expr AS g0, expr AS g1, ..." select list and
// the matching "g0, g1, ..." name list for the configured group-by paths.
func groupSelectAliases(paths []string) (selectList []string, names []string) {
	for i, p := range paths {
		name := fmt.Sprintf("g%d", i)
		selectList = append(selectList, fmt.Sprintf("%s AS %s", groupExpr(p), name))
		names = append(names, name)
	}
	return selectList, names
}

// liveValueExpr is the per-window aggregate SQL over RAW events for an
// aggregation, matching the proposal queries exactly: SUM/MIN/MAX over
// toDecimal128OrNull(...) (Decimal128(19), no Float64 hop), and COUNT as a bare
// count(*) (UInt64, as the proposal emits it — the decimal-rounded parity check
// absorbs the int-vs-decimal type, and the scan handles uint64). The expression
// is IDENTICAL on the uncached control and the cached query's fresh leg.
func (c *Connector) liveValueExpr(agg Aggregation) string {
	if agg == AggCount {
		return "count(*)"
	}
	return fmt.Sprintf("%s(%s)", strings.ToLower(string(agg)), c.valueExpr)
}

// aggCol maps an aggregation to its cache column and how partials recombine.
func aggCol(a Aggregation) (cacheColumn, recombine string) {
	switch a {
	case AggSum:
		return "sum_value", "sum"
	case AggCount:
		return "count_value", "sum" // counts recombine by summing
	case AggMin:
		return "min_value", "min"
	case AggMax:
		return "max_value", "max"
	}
	return "", ""
}

// QueryUncached runs the meter query over the full [from, to) range against raw
// events — the no-cache control. This is the PROPOSAL QUERY VERBATIM (e.g.
// scenarios/proposal/queries/sum_hour_group1_group2.sql): tumbleStart/tumbleEnd
// hourly windows in UTC, toDecimal128OrNull value, toString(data.<p>.:String)
// group-by, the same WHERE, GROUP BY, ORDER BY, and SETTINGS. It is the real
// query the cache must beat.
func (c *Connector) QueryUncached(ctx context.Context, p Params, agg Aggregation) ([]Row, error) {
	valSQL := c.liveValueExpr(agg)

	gSel, gNames := groupSelectAliases(p.GroupByPaths)
	selCols := append([]string{
		windowStartExpr + " AS windowstart",
		windowEndExpr + " AS windowend",
		"subject",
	}, gSel...)
	groupCols := append([]string{"windowstart", "windowend", "subject"}, gNames...)

	sql := fmt.Sprintf(`
SELECT %s, %s AS value
FROM %s
WHERE namespace = ? AND type = ? AND subject IN (?)
  AND time >= ? AND time < ?
GROUP BY %s
ORDER BY windowstart
%s`,
		strings.Join(selCols, ", "), valSQL, c.eventsTable,
		strings.Join(groupCols, ", "), proposalSettings)

	rows, err := c.conn.Query(ctx, sql, p.Namespace, p.Type, p.Subjects, p.From, p.To)
	if err != nil {
		return nil, fmt.Errorf("uncached query: %w", err)
	}
	defer rows.Close()
	return scanRows(rows, len(p.GroupByPaths))
}

// QueryCached serves [from, to) by reading pre-aggregated rows from the cache for
// the history (windowstart < cutoff) and aggregating raw events only for the
// fresh tail (time >= cutoff), then re-aggregating the union per
// (window, subject, group-by). cutoff is the freshness boundary.
func (c *Connector) QueryCached(ctx context.Context, p Params, agg Aggregation, cutoff time.Time) ([]Row, error) {
	col, recombine := aggCol(agg)
	tailVal := c.liveValueExpr(agg)

	gSel, gNames := groupSelectAliases(p.GroupByPaths)
	// Inner column lists shared by both legs: windowstart, windowend, subject,
	// g0, g1, ... — the same output shape as the proposal query.
	innerGroupCols := append([]string{"windowstart", "windowend", "subject"}, gNames...)

	// Cached leg: read pre-aggregated value + stored group columns. windowend is
	// reconstructed from windowstart (= windowstart + 1h = tumbleEnd), so it never
	// needs storing and can't drift from the fresh leg.
	cacheGroupRead := make([]string, len(p.GroupByPaths))
	for i := range p.GroupByPaths {
		cacheGroupRead[i] = fmt.Sprintf("group_by[%d] AS g%d", i+1, i) // CH arrays are 1-based
	}
	cacheSel := append([]string{
		"windowstart",
		"windowstart + toIntervalHour(1) AS windowend",
		"subject",
	}, cacheGroupRead...)

	// Fresh leg: aggregate raw events into the SAME shape, with the SAME tumble
	// window expressions as the uncached control and the cache population.
	tailSel := append([]string{
		windowStartExpr + " AS windowstart",
		windowEndExpr + " AS windowend",
		"subject",
	}, gSel...)

	sql := fmt.Sprintf(`
SELECT %[1]s, %[2]s(value) AS value
FROM (
  SELECT %[3]s, %[4]s AS value
  FROM %[5]s
  WHERE namespace = ? AND type = ? AND windowstart >= ? AND windowstart < ?

  UNION ALL

  SELECT %[6]s, %[7]s AS value
  FROM %[8]s
  WHERE namespace = ? AND type = ? AND subject IN (?)
    AND time >= ? AND time < ?
  GROUP BY %[1]s
)
GROUP BY %[1]s
ORDER BY windowstart
%[9]s`,
		strings.Join(innerGroupCols, ", "), // [1] grouping key
		recombine,                          // [2] outer recombine
		strings.Join(cacheSel, ", "),       // [3] cached leg select
		col,                                // [4] cached value column
		c.cacheTable,                       // [5]
		strings.Join(tailSel, ", "),        // [6] fresh leg select
		tailVal,                            // [7] fresh value agg
		c.eventsTable,                      // [8]
		proposalSettings,                   // [9]
	)

	rows, err := c.conn.Query(ctx, sql,
		p.Namespace, p.Type, p.From, cutoff, // cached history bounds [from, cutoff)
		p.Namespace, p.Type, p.Subjects, cutoff, p.To, // fresh tail [cutoff, to)
	)
	if err != nil {
		return nil, fmt.Errorf("cached query: %w", err)
	}
	defer rows.Close()
	return scanRows(rows, len(p.GroupByPaths))
}

// PopulateCache fills the cache table with hourly rollups of the history window
// [from, cutoff), using the IDENTICAL filter (namespace, type, subject allow-
// list) and value/group extraction as the queries — so cache and query cover the
// same rows and produce the same numbers. The group-by values are stored as an
// ordered array aligned to GroupByPaths.
func (c *Connector) PopulateCache(ctx context.Context, p Params, cutoff time.Time) error {
	// Build the group_by array and the GROUP BY directly from the extraction
	// EXPRESSIONS (not output aliases), so the SELECT emits exactly the 9 target
	// columns (4 keys + group_by + 4 aggs) and the gN values don't leak as extra
	// output columns. Extraction is IDENTICAL to the query path.
	groupExprs := make([]string, len(p.GroupByPaths))
	for i, path := range p.GroupByPaths {
		groupExprs[i] = groupExpr(path)
	}
	groupArray := "[]"
	if len(groupExprs) > 0 {
		groupArray = "[" + strings.Join(groupExprs, ", ") + "]"
	}
	// windowstart uses the SAME tumble expression as the query paths, so the
	// cached windowstart lines up exactly with the uncached/fresh-leg windowstart.
	groupByCols := append([]string{"namespace", "type", windowStartExpr, "subject"}, groupExprs...)

	sql := fmt.Sprintf(`
INSERT INTO %[1]s (namespace, type, windowstart, subject, group_by, sum_value, count_value, min_value, max_value)
SELECT
  namespace, type, %[6]s AS windowstart, subject,
  %[2]s AS group_by,
  sum(%[3]s)  AS sum_value,
  count(*)    AS count_value,
  min(%[3]s)  AS min_value,
  max(%[3]s)  AS max_value
FROM %[4]s
WHERE namespace = ? AND type = ? AND subject IN (?)
  AND time >= ? AND time < ?
GROUP BY %[5]s`,
		c.cacheTable,
		groupArray,
		c.valueExpr,
		c.eventsTable,
		strings.Join(groupByCols, ", "),
		windowStartExpr,
	)

	return c.conn.Exec(ctx, sql, p.Namespace, p.Type, p.Subjects, p.From, cutoff)
}

func scanRows(rows driver.Rows, nGroups int) ([]Row, error) {
	var out []Row
	for rows.Next() {
		var (
			r      Row
			groups []string
		)
		// Scan the value into a NullDecimal: min()/max() are Nullable(Decimal128)
		// (no aggregation identity → NULL on empty groups), and the UNION ALL
		// reconciles the cached + live legs to Nullable, so the driver presents a
		// nullable scan type. This mirrors OpenMeter's own meter_query.go, which
		// scans the meter value through a NullDecimal for exactly this reason. A
		// NULL value (empty group) leaves the zero value — but GROUP BY only emits
		// non-empty groups, so this is defensive.
		var val nullDecimal
		dest := []any{&r.WindowStart, &r.WindowEnd, &r.Subject}
		gvals := make([]string, nGroups)
		for i := range gvals {
			dest = append(dest, &gvals[i])
		}
		dest = append(dest, &val)
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		if val.Valid {
			r.Value = val.Decimal
		}
		groups = append(groups, gvals...)
		r.GroupBy = groups
		out = append(out, r)
	}
	return out, rows.Err()
}

// dedupeRows removes duplicate rows for the same group key, tolerating
// duplicates with EQUAL values (as the original cache does for parallel
// double-caching) and erroring only on a genuine value conflict.
//
// This is where the original's Bug #1 (REVIEW-query-result-cache.md) lived: it
// stored a Float64 NaN sentinel and compared `row.Value != seen.Value`; because
// NaN != NaN is always true, a duplicated sentinel row hit the error branch and
// failed the whole query. Here values are exact Decimals — which have NO NaN —
// so that failure mode is structurally impossible, not merely guarded. Decimal
// equality is exact, so equal duplicates collapse cleanly. The map is keyed by
// the full (window, subject, group-by) string tuple, not a pointer (Bug #2 fix).
func dedupeRows(rows []Row) ([]Row, error) {
	seen := map[string]Row{}
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		key := r.Key() // full string-value tuple (Bug #2 fix)
		if prev, ok := seen[key]; !ok {
			seen[key] = r
			out = append(out, r)
		} else if !r.Value.Equal(prev.Value) {
			return nil, fmt.Errorf("duplicate row for key %q with different value: %s vs %s", key, r.Value.String(), prev.Value.String())
		}
	}
	return out, nil
}
