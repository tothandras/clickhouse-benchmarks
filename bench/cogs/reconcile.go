package cogs

import (
	"fmt"
	"math"
	"strings"

	"github.com/openmeterio/ch-playground/bench/accounting"
	"github.com/openmeterio/ch-playground/bench/pricing"
)

// reconcileFlagPct is the model-vs-billed delta magnitude that flags a run.
const reconcileFlagPct = 20.0

// Reconcile compares model-derived consumption against a usage export's
// billed values over the measure window. Input-file based by design: no Cloud
// API access, ever.
//
// Daily-granularity inputs (the Cloud statement CSV) are pro-rated by time
// overlap, which assumes uniform usage across the record — for a 1h measure
// window inside a daily dollar row that is indicative, not billing-grade;
// the result says so via Granularity and the report renders the caveat.
func Reconcile(export accounting.UsageExport, p pricing.Profile, measure PhaseInfo, modelStorageTB float64) (Reconciliation, error) {
	if ids := export.ServiceIDs(); len(ids) > 1 {
		return Reconciliation{}, fmt.Errorf("usage export covers %d services (%s); export a single service's statement or filter it first", len(ids), strings.Join(ids, ", "))
	}
	w := accounting.Window{Start: measure.Start, End: measure.End}
	billedCUH, billedStorageTB := export.BilledUsage(w, p.Rates.ComputeUnitHour)

	rec := Reconciliation{
		BilledComputeUnitHours: billedCUH,
		ModelComputeUnitHours:  p.Service.ComputeUnits() * measure.Seconds / 3600,
		BilledStorageTB:        billedStorageTB,
		ModelStorageTB:         modelStorageTB,
		Granularity:            export.Granularity().String(),
	}
	rec.DeltaPct = deltaPct(rec.ModelComputeUnitHours, rec.BilledComputeUnitHours)
	if billedStorageTB == 0 && modelStorageTB > 0 && export.Version == "cloud-statement-csv/v1" {
		// Statement CSVs carry storage as prorated dollars, not TB; compute-only.
		rec.StorageDeltaPct = 0
	} else {
		rec.StorageDeltaPct = deltaPct(rec.ModelStorageTB, rec.BilledStorageTB)
	}
	rec.Flagged = math.Abs(rec.DeltaPct) > reconcileFlagPct || math.Abs(rec.StorageDeltaPct) > reconcileFlagPct
	return rec, nil
}

// deltaPct is (model - billed) / billed in percent; 0 when billed is 0 and
// model is too, 100 x sign when only billed is 0.
func deltaPct(model, billed float64) float64 {
	if billed == 0 {
		if model == 0 {
			return 0
		}
		return 100
	}
	return (model - billed) / billed * 100
}

// ReconcileFile loads the export and reconciles it.
func ReconcileFile(path string, p pricing.Profile, measure PhaseInfo, modelStorageTB float64) (Reconciliation, error) {
	export, err := accounting.LoadUsageExport(path)
	if err != nil {
		return Reconciliation{}, err
	}
	return Reconcile(export, p, measure, modelStorageTB)
}
