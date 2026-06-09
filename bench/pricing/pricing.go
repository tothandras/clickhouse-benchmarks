// Package pricing loads ClickHouse Cloud pricing profiles and prices
// attributed resource consumption. Every priced number in a cogs report is
// derivable from a named profile checked into pricing/; rates and service
// shape live in profile data, never in code.
//
// Two modes, both always reported:
//
//   - billed-shape: prices the measure window the way Cloud actually bills
//     (compute units x active hours x rate) and splits that amount across
//     components proportionally by CPU seconds; the remainder is the idle
//     floor. This is the number that reconciles with the invoice.
//   - cpu-linear: prices CPU seconds directly at the marginal per-vCPU rate.
//     Answers "what would this workload cost if perfectly packed", i.e. the
//     marginal cost on an already-busy service.
package pricing

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Service is the pinned service shape a profile prices against. Cloud meters
// compute in RAM increments (gib_per_compute_unit, 8 GiB on current tiers),
// so compute units derive from RAM, not vCPUs.
type Service struct {
	Replicas          int     `json:"replicas"`
	GiBPerReplica     float64 `json:"gib_per_replica"`
	VCPUsPerReplica   float64 `json:"vcpus_per_replica"`
	GiBPerComputeUnit float64 `json:"gib_per_compute_unit"`
}

// ComputeUnits is the number of billable compute units the shape occupies.
func (s Service) ComputeUnits() float64 {
	return float64(s.Replicas) * s.GiBPerReplica / s.GiBPerComputeUnit
}

// TotalVCPUs is the vCPU capacity across all replicas.
func (s Service) TotalVCPUs() float64 {
	return float64(s.Replicas) * s.VCPUsPerReplica
}

// Rates are the billable rates. All zero (local-zero profile) is legal and
// yields $0 everywhere while resource accounting stays fully reported.
type Rates struct {
	ComputeUnitHour float64 `json:"compute_unit_hour"`
	StorageTBMonth  float64 `json:"storage_tb_month"`
	// BackupMultiplier scales storage cost for backup retention: 1.0 means
	// ignore backups, 2.0 means backups double the stored bytes. v1 estimate;
	// there is no system-table source for actual backup bytes on Cloud.
	BackupMultiplier  float64  `json:"backup_multiplier"`
	EgressPerGBPublic float64  `json:"egress_per_gb_public"` // estimate-only in v1, applied to result bytes
	ClickPipes        *float64 `json:"clickpipes"`           // reserved; OpenMeter's sink is not ClickPipes
}

// Profile is a named, checked-in pricing profile (pricing/<name>.json). The
// full profile is embedded in every result JSON so reports are self-describing.
type Profile struct {
	Name     string  `json:"name"`
	Currency string  `json:"currency"`
	Service  Service `json:"service"`
	Rates    Rates   `json:"rates"`
	AsOf     string  `json:"as_of"`
	Source   string  `json:"source"`
}

// Load reads and validates a pricing profile. Strict decode: unknown fields
// are an error.
func Load(path string) (Profile, error) {
	f, err := os.Open(path)
	if err != nil {
		return Profile{}, err
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var p Profile
	if err := dec.Decode(&p); err != nil {
		return Profile{}, fmt.Errorf("pricing profile %s: %w", path, err)
	}
	if err := p.Validate(); err != nil {
		return Profile{}, fmt.Errorf("pricing profile %s: %w", path, err)
	}
	return p, nil
}

// Validate checks profile invariants.
func (p *Profile) Validate() error {
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }

	if p.Name == "" {
		add("name is required")
	}
	if p.Currency == "" {
		add("currency is required")
	}
	if p.Service.Replicas <= 0 {
		add("service.replicas must be > 0")
	}
	if p.Service.GiBPerReplica <= 0 || p.Service.VCPUsPerReplica <= 0 || p.Service.GiBPerComputeUnit <= 0 {
		add("service gib_per_replica, vcpus_per_replica, gib_per_compute_unit must be > 0")
	}
	if p.Rates.ComputeUnitHour < 0 || p.Rates.StorageTBMonth < 0 || p.Rates.BackupMultiplier < 0 || p.Rates.EgressPerGBPublic < 0 {
		add("rates must be >= 0")
	}
	if len(errs) > 0 {
		return fmt.Errorf("invalid profile: %s", strings.Join(errs, "; "))
	}
	return nil
}

// USDPerCPUSec is the cpu-linear marginal rate: one compute unit's hourly
// rate spread over its vCPU share.
func (p Profile) USDPerCPUSec() float64 {
	vcpusPerUnit := p.Service.TotalVCPUs() / p.Service.ComputeUnits()
	return p.Rates.ComputeUnitHour / (vcpusPerUnit * 3600)
}

// BilledShape prices a window of windowSec seconds on the profile's shape and
// splits the window cost across components proportionally by attributed CPU
// seconds against availableCPUSec; the remainder is booked as idle floor.
// Components with zero CPU price to zero; insert+merge+query+idle always sums
// to the window cost (the invariant that reconciles with the invoice).
func BilledShape(p Profile, windowSec float64, cpuSec map[string]float64, availableCPUSec float64) (windowUSD float64, perComponent map[string]float64, idleFloorUSD float64) {
	windowUSD = p.Service.ComputeUnits() * (windowSec / 3600) * p.Rates.ComputeUnitHour
	perComponent = make(map[string]float64, len(cpuSec))
	attributed := 0.0
	for component, cpu := range cpuSec {
		share := 0.0
		if availableCPUSec > 0 {
			share = cpu / availableCPUSec
		}
		perComponent[component] = windowUSD * share
		attributed += cpu
	}
	idleShare := 0.0
	if availableCPUSec > 0 {
		idleShare = max(0, availableCPUSec-attributed) / availableCPUSec
	}
	idleFloorUSD = windowUSD * idleShare
	return windowUSD, perComponent, idleFloorUSD
}

// CPULinear prices attributed CPU seconds directly at the marginal rate.
func CPULinear(p Profile, cpuSec map[string]float64) map[string]float64 {
	rate := p.USDPerCPUSec()
	out := make(map[string]float64, len(cpuSec))
	for component, cpu := range cpuSec {
		out[component] = cpu * rate
	}
	return out
}

// StorageUSDMonth prices retained compressed bytes per month, including the
// backup-retention estimate via BackupMultiplier.
func StorageUSDMonth(p Profile, compressedBytes float64) float64 {
	return compressedBytes / 1e12 * p.Rates.StorageTBMonth * p.Rates.BackupMultiplier
}

// EgressUSD estimates egress cost for result bytes returned to clients.
func EgressUSD(p Profile, resultBytes float64) float64 {
	return resultBytes / 1e9 * p.Rates.EgressPerGBPublic
}

// IdleFloorUSDMonth is the 100%-active bound: what the pinned shape costs per
// month if it never idles (730 h). Shown next to the measured idle share.
func IdleFloorUSDMonth(p Profile) float64 {
	return p.Service.ComputeUnits() * 730 * p.Rates.ComputeUnitHour
}
