package cogs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestShippedCellMatrix loads every manifest in cells/ through the strict
// loader: the shipped matrix must always validate.
func TestShippedCellMatrix(t *testing.T) {
	dir := filepath.Join("..", "..", "cells")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		eps   int
		qps   float64
		cold  float64
		async bool
	}{
		"idle":             {0, 0, 0, false},
		"ingest-1k":        {1000, 0, 0, false},
		"ingest-5k":        {5000, 0, 0, false},
		"ingest-25k":       {25000, 0, 0, false},
		"ingest-5k-async":  {5000, 0, 0, true},
		"query-1qps":       {0, 1, 0.1, false},
		"query-4qps":       {0, 4, 0.1, false},
		"query-16qps":      {0, 16, 0.1, false},
		"query-4qps-cold":  {0, 4, 1.0, false},
		"mixed-5keps-4qps": {5000, 4, 0.1, false},
	}
	seen := 0
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		seen++
		c, err := LoadCell(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Errorf("cell %s: %v", e.Name(), err)
			continue
		}
		exp, ok := want[name]
		if !ok {
			t.Errorf("unexpected cell %s (add it to the matrix test)", name)
			continue
		}
		if c.Name != name {
			t.Errorf("cell %s: manifest name %q must match the filename", name, c.Name)
		}
		if c.Ingest.EventsPerSec != exp.eps || c.Query.QPS != exp.qps ||
			c.Query.ColdFraction != exp.cold || c.Ingest.AsyncInsert != exp.async {
			t.Errorf("cell %s: rates diverge from the documented matrix: %+v", name, c)
		}
		if c.Scenario != "proposal" || c.PreloadRows != 10_000_000 || c.Ingest.Seed != 42 {
			t.Errorf("cell %s: matrix defaults violated (scenario/preload/seed)", name)
		}
	}
	if seen != len(want) {
		t.Fatalf("cells/ has %d manifests, matrix documents %d", seen, len(want))
	}
}
