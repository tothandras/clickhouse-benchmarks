package accounting

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// UsageExport is the versioned reconciliation input. ClickHouse Cloud's own
// usage/billing export format may drift, so the harness pins its own schema
// (version cogs-usage/v1) and fails loudly on anything else instead of
// misparsing; the operator converts the Cloud export into this shape (or a
// future parser version adds the native format).
type UsageExport struct {
	Version   string        `json:"version"` // must be "cogs-usage/v1"
	ServiceID string        `json:"service_id,omitempty"`
	Records   []UsageRecord `json:"records"`
}

// UsageRecord is one billed interval. Compute is either unit-hours (JSON v1)
// or dollars (Cloud statement CSV); dollars convert via the pricing profile's
// rate at reconcile time.
type UsageRecord struct {
	From             time.Time `json:"from"`
	To               time.Time `json:"to"`
	ComputeUnitHours float64   `json:"compute_unit_hours"`
	StorageTB        float64   `json:"storage_tb"`
	ComputeUSD       float64   `json:"compute_usd,omitempty"`
	ServiceID        string    `json:"service_id,omitempty"`
}

// usageVersion is the only JSON export schema this parser accepts.
const usageVersion = "cogs-usage/v1"

// Granularity reports the coarsest record duration, so reconciliation can
// flag daily statements (a 1h measure window pro-rated out of a daily dollar
// total is indicative, not billing-grade).
func (u UsageExport) Granularity() time.Duration {
	var g time.Duration
	for _, r := range u.Records {
		g = max(g, r.To.Sub(r.From))
	}
	return g
}

// LoadUsageExport parses and validates a usage export file. Two formats, both
// versioned to fail loudly on drift:
//   - cogs-usage/v1 JSON (interval records in compute-unit-hours)
//   - the ClickHouse Cloud usage statement CSV (daily dollar rows; header
//     pinned in statementColumns)
func LoadUsageExport(path string) (UsageExport, error) {
	if strings.HasSuffix(strings.ToLower(path), ".csv") {
		return loadStatementCSV(path)
	}
	f, err := os.Open(path)
	if err != nil {
		return UsageExport{}, err
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var u UsageExport
	if err := dec.Decode(&u); err != nil {
		return UsageExport{}, fmt.Errorf("usage export %s: %w", path, err)
	}
	if u.Version != usageVersion {
		return UsageExport{}, fmt.Errorf("usage export %s: unsupported version %q (this parser reads %q or the Cloud statement CSV)", path, u.Version, usageVersion)
	}
	for i, r := range u.Records {
		if !r.To.After(r.From) {
			return UsageExport{}, fmt.Errorf("usage export %s: record %d window is empty or inverted", path, i)
		}
	}
	return u, nil
}

// statementColumns are the Cloud usage-statement CSV columns this parser
// reads. The full header is matched loosely (these names must exist), so
// added columns don't break parsing but a renamed/removed one fails loudly —
// the CSV equivalent of the versioned JSON schema.
var statementColumns = []string{"Date", "Entity Type", "Service ID", "Service Compute ($)", "Warehouse Storage ($)"}

// loadStatementCSV parses a ClickHouse Cloud usage statement: one row per day
// per entity, amounts in dollars. Each service row becomes a UsageRecord
// spanning that UTC day with ComputeUSD set (unit-hours derived at reconcile
// time from the pricing profile's rate).
func loadStatementCSV(path string) (UsageExport, error) {
	f, err := os.Open(path)
	if err != nil {
		return UsageExport{}, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	header, err := r.Read()
	if err != nil {
		return UsageExport{}, fmt.Errorf("usage statement %s: read header: %w", path, err)
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.TrimSpace(h)] = i
	}
	for _, want := range statementColumns {
		if _, ok := col[want]; !ok {
			return UsageExport{}, fmt.Errorf("usage statement %s: column %q missing — statement format drifted, update the parser (have: %s)", path, want, strings.Join(header, ", "))
		}
	}

	parseUSD := func(row []string, name string) float64 {
		s := strings.TrimSpace(row[col[name]])
		if s == "" {
			return 0
		}
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0
		}
		return v
	}

	u := UsageExport{Version: "cloud-statement-csv/v1"}
	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return UsageExport{}, fmt.Errorf("usage statement %s: %w", path, err)
		}
		if strings.TrimSpace(row[col["Entity Type"]]) != "service" {
			continue
		}
		day, err := time.Parse("2006-01-02", strings.TrimSpace(row[col["Date"]]))
		if err != nil {
			return UsageExport{}, fmt.Errorf("usage statement %s: bad date %q: %w", path, row[col["Date"]], err)
		}
		u.Records = append(u.Records, UsageRecord{
			From:       day,
			To:         day.Add(24 * time.Hour),
			ComputeUSD: parseUSD(row, "Service Compute ($)"),
			// Warehouse Storage ($) is a daily proration of the monthly rate;
			// deriving TB from it needs day-count assumptions, so CSV inputs
			// reconcile compute only. Recorded as 0.
			ServiceID: strings.TrimSpace(row[col["Service ID"]]),
		})
	}
	if len(u.Records) == 0 {
		return UsageExport{}, fmt.Errorf("usage statement %s: no service rows", path)
	}
	return u, nil
}

// ServiceIDs lists the distinct non-empty service ids in the export.
func (u UsageExport) ServiceIDs() []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range u.Records {
		if r.ServiceID != "" && !seen[r.ServiceID] {
			seen[r.ServiceID] = true
			out = append(out, r.ServiceID)
		}
	}
	sort.Strings(out)
	return out
}

// FilterService keeps only the given service's records.
func (u UsageExport) FilterService(id string) UsageExport {
	out := UsageExport{Version: u.Version, ServiceID: id}
	for _, r := range u.Records {
		if r.ServiceID == id || r.ServiceID == "" {
			out.Records = append(out.Records, r)
		}
	}
	return out
}

// BilledUsage pro-rates the export's records over the window: compute
// unit-hours by overlap fraction (dollar records convert at usdPerUnitHour),
// storage as the overlap-weighted average.
func (u UsageExport) BilledUsage(w Window, usdPerUnitHour float64) (computeUnitHours, storageTB float64) {
	var weightSum float64
	for _, r := range u.Records {
		start := maxTime(r.From, w.Start)
		end := minTime(r.To, w.End)
		if !end.After(start) {
			continue
		}
		overlap := end.Sub(start).Seconds()
		frac := overlap / r.To.Sub(r.From).Seconds()
		cuh := r.ComputeUnitHours
		if cuh == 0 && r.ComputeUSD > 0 && usdPerUnitHour > 0 {
			cuh = r.ComputeUSD / usdPerUnitHour
		}
		computeUnitHours += cuh * frac
		storageTB += r.StorageTB * overlap
		weightSum += overlap
	}
	if weightSum > 0 {
		storageTB /= weightSum
	}
	return computeUnitHours, storageTB
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
