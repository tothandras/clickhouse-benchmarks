package accounting

import (
	"context"
	"fmt"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// Capacity is the detected compute capacity the coverage denominator uses:
// available_cpu_sec = TotalVCPUs x window seconds. Cross-checked by the
// runner against the pricing profile's declared shape.
type Capacity struct {
	Replicas        int     `json:"replicas"`
	VCPUsPerReplica float64 `json:"vcpus_per_replica"`
	TotalVCPUs      float64 `json:"total_vcpus"`
	// Source names the probe that yielded VCPUsPerReplica:
	// "CGroupMaxCPU" (cgroup-limited, the Cloud case) or "max_threads"
	// (server default resolves to the core count).
	Source string `json:"source"`
}

// DetectCapacity probes per-replica vCPUs on the connected node and scales by
// the target's replica count. The probes are per-replica values; multiplying
// by replicas is what makes the coverage denominator correct on multi-replica
// Cloud services.
func DetectCapacity(ctx context.Context, conn driver.Conn, target Target) (Capacity, error) {
	c := Capacity{Replicas: target.Replicas}

	// Preferred: the cgroup CPU limit, present on Cloud (and any
	// container-limited deployment), where core count would over-report.
	var cg float64
	err := conn.QueryRow(ctx,
		"SELECT toFloat64(value) FROM system.asynchronous_metrics WHERE metric = 'CGroupMaxCPU'").Scan(&cg)
	if err == nil && cg > 0 {
		c.VCPUsPerReplica = cg
		c.Source = "CGroupMaxCPU"
	} else {
		// Fallback: max_threads defaults to the machine's core count
		// ('auto(N)' resolves to N via getSetting).
		var mt uint64
		if err := conn.QueryRow(ctx, "SELECT toUInt64(getSetting('max_threads'))").Scan(&mt); err != nil {
			return c, fmt.Errorf("accounting: detect vcpus (no CGroupMaxCPU, max_threads failed): %w", err)
		}
		if mt == 0 {
			return c, fmt.Errorf("accounting: detect vcpus: max_threads resolved to 0")
		}
		c.VCPUsPerReplica = float64(mt)
		c.Source = "max_threads"
	}

	c.TotalVCPUs = float64(c.Replicas) * c.VCPUsPerReplica
	return c, nil
}
