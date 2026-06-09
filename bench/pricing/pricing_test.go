package pricing

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scaleProfile mirrors pricing/clickhouse-cloud-scale-aws-us-east-1.json:
// 2 replicas x 16 GiB / 8 GiB-per-unit = 4 compute units, 8 vCPUs total.
func scaleProfile() Profile {
	return Profile{
		Name:     "test-scale",
		Currency: "USD",
		Service:  Service{Replicas: 2, GiBPerReplica: 16, VCPUsPerReplica: 4, GiBPerComputeUnit: 8},
		Rates: Rates{
			ComputeUnitHour:   0.2985,
			StorageTBMonth:    25.30,
			BackupMultiplier:  1.0,
			EgressPerGBPublic: 0.1152,
		},
		AsOf:   "2026-06-09",
		Source: "test",
	}
}

func approx(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9*math.Max(1, math.Abs(want)) {
		t.Fatalf("%s = %.12f, want %.12f", name, got, want)
	}
}

func TestServiceDerivedShape(t *testing.T) {
	s := scaleProfile().Service
	approx(t, "compute units", s.ComputeUnits(), 4)
	approx(t, "total vcpus", s.TotalVCPUs(), 8)
}

func TestUSDPerCPUSec(t *testing.T) {
	// 8 vCPUs / 4 units = 2 vCPU per unit; 0.2985 / (2*3600).
	approx(t, "$/cpu_sec", scaleProfile().USDPerCPUSec(), 0.2985/7200)
}

func TestBilledShapeHandComputed(t *testing.T) {
	p := scaleProfile()
	// One hour window: 4 units x 1 h x 0.2985 = 1.194.
	cpu := map[string]float64{"insert": 1000, "merge": 500, "query": 2500}
	available := 8.0 * 3600 // 28800 cpu-sec in the window
	window, per, idle := BilledShape(p, 3600, cpu, available)

	approx(t, "window", window, 1.194)
	approx(t, "insert", per["insert"], 1.194*1000/28800)
	approx(t, "merge", per["merge"], 1.194*500/28800)
	approx(t, "query", per["query"], 1.194*2500/28800)
	// Idle = remainder; components + idle must sum to the window cost
	// (the invariant that reconciles with the invoice).
	approx(t, "idle", idle, 1.194*(28800-4000)/28800)
	approx(t, "sum", per["insert"]+per["merge"]+per["query"]+idle, window)
}

func TestBilledShapeOverAttributed(t *testing.T) {
	// Attribution above capacity (clock skew, estimate error) must floor idle
	// at zero, never go negative.
	p := scaleProfile()
	_, _, idle := BilledShape(p, 3600, map[string]float64{"query": 30000}, 28800)
	approx(t, "idle floored", idle, 0)
}

func TestCPULinearHandComputed(t *testing.T) {
	p := scaleProfile()
	out := CPULinear(p, map[string]float64{"insert": 7200, "query": 3600})
	approx(t, "insert", out["insert"], 7200*0.2985/7200)
	approx(t, "query", out["query"], 3600*0.2985/7200)
}

func TestStorageEgressIdleFloor(t *testing.T) {
	p := scaleProfile()
	// 66 bytes/event x 1e6 events = 66 MB -> 66e6/1e12 TB x 25.30 x 1.0.
	approx(t, "storage", StorageUSDMonth(p, 66e6), 66e6/1e12*25.30)
	p.Rates.BackupMultiplier = 2.0
	approx(t, "storage with backups", StorageUSDMonth(p, 66e6), 2*66e6/1e12*25.30)
	approx(t, "egress", EgressUSD(p, 5e9), 5*0.1152)
	p.Rates.BackupMultiplier = 1.0
	approx(t, "idle floor / month", IdleFloorUSDMonth(p), 4*730*0.2985)
}

func TestLoadShippedProfiles(t *testing.T) {
	root := filepath.Join("..", "..", "pricing")

	scale, err := Load(filepath.Join(root, "clickhouse-cloud-scale-aws-us-east-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	approx(t, "scale compute units", scale.Service.ComputeUnits(), 4)
	if scale.Rates.ClickPipes != nil {
		t.Fatal("clickpipes is reserved and must stay null")
	}

	zero, err := Load(filepath.Join(root, "local-zero.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Zero rates price everything to zero while accounting stays decoupled.
	window, per, idle := BilledShape(zero, 3600, map[string]float64{"query": 100}, 8*3600)
	if window != 0 || per["query"] != 0 || idle != 0 {
		t.Fatalf("local-zero must price to $0, got window=%v per=%v idle=%v", window, per, idle)
	}
	if USD := CPULinear(zero, map[string]float64{"query": 100})["query"]; USD != 0 {
		t.Fatalf("local-zero cpu-linear must be $0, got %v", USD)
	}
}

func TestLoadRejectsUnknownFieldAndBadShape(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	writeFile(t, bad, `{"name":"x","currency":"USD","tier":"scale",
	  "service":{"replicas":1,"gib_per_replica":8,"vcpus_per_replica":2,"gib_per_compute_unit":8},
	  "rates":{"compute_unit_hour":0,"storage_tb_month":0,"backup_multiplier":0,"egress_per_gb_public":0,"clickpipes":null},
	  "as_of":"2026-06-09","source":"test"}`)
	if _, err := Load(bad); err == nil || !strings.Contains(err.Error(), "tier") {
		t.Fatalf("unknown field must be rejected by name, got: %v", err)
	}

	noReplicas := filepath.Join(dir, "no-replicas.json")
	writeFile(t, noReplicas, `{"name":"x","currency":"USD",
	  "service":{"replicas":0,"gib_per_replica":8,"vcpus_per_replica":2,"gib_per_compute_unit":8},
	  "rates":{"compute_unit_hour":0,"storage_tb_month":0,"backup_multiplier":0,"egress_per_gb_public":0,"clickpipes":null},
	  "as_of":"2026-06-09","source":"test"}`)
	if _, err := Load(noReplicas); err == nil || !strings.Contains(err.Error(), "replicas") {
		t.Fatalf("zero replicas must be rejected, got: %v", err)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
