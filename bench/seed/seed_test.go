package seed

import (
	"encoding/json"
	"testing"
)

// newGenCtx builds the same genCtx Run uses, so tests exercise the real
// per-index generation path without a ClickHouse connection.
func newGenCtx(cfg Config) *genCtx {
	cum := make([]int, len(cfg.EventTypes))
	total := 0
	for i, et := range cfg.EventTypes {
		total += et.Weight
		cum[i] = total
	}
	return &genCtx{
		cfg:         cfg,
		subjects:    Subjects(cfg.Subjects),
		cumWeights:  cum,
		totalWeight: total,
		timeStart:   cfg.TimeEnd.Add(-cfg.TimeSpan),
		spanNanos:   cfg.TimeSpan.Nanoseconds(),
	}
}

// genTypesAndData reproduces Run's per-index generation for n rows so we can
// assert mix/determinism without a ClickHouse connection.
func genTypesAndData(cfg Config, n int) ([]string, []string) {
	g := newGenCtx(cfg)
	types := make([]string, n)
	datas := make([]string, n)
	for i := 0; i < n; i++ {
		e := g.genEvent(i)
		types[i] = e.Type
		datas[i] = e.Data
	}
	return types, datas
}

func TestSeedDeterministic(t *testing.T) {
	cfg := DefaultConfig()
	const n = 5000
	t1, d1 := genTypesAndData(cfg, n)
	t2, d2 := genTypesAndData(cfg, n)
	for i := 0; i < n; i++ {
		if t1[i] != t2[i] {
			t.Fatalf("row %d type differs: %q vs %q", i, t1[i], t2[i])
		}
		if d1[i] != d2[i] {
			t.Fatalf("row %d data differs:\n %s\n %s", i, d1[i], d2[i])
		}
	}
}

func TestSeedWeightedMix(t *testing.T) {
	cfg := DefaultConfig()
	const n = 200_000
	types, _ := genTypesAndData(cfg, n)

	counts := map[string]int{}
	for _, ty := range types {
		counts[ty]++
	}
	total := 0
	for _, et := range cfg.EventTypes {
		total += et.Weight
	}
	for _, et := range cfg.EventTypes {
		want := float64(et.Weight) / float64(total)
		got := float64(counts[et.Name]) / float64(n)
		if counts[et.Name] == 0 {
			t.Errorf("type %q never generated", et.Name)
			continue
		}
		// 15% relative tolerance — plenty given N=200k and the smallest weight.
		if got < want*0.85 || got > want*1.15 {
			t.Errorf("type %q share %.3f, want ~%.3f", et.Name, got, want)
		}
	}
	// Baseline must dominate so the canonical value-queries scan a large pop.
	if counts["api_request"] < n/2*9/10 {
		t.Errorf("baseline api_request share too low: %d of %d", counts["api_request"], n)
	}
}

func TestSeedHeterogeneousPayloads(t *testing.T) {
	cfg := DefaultConfig()
	g := newGenCtx(cfg)
	// Collect one sample payload per type by scanning indices until every type
	// has appeared (each index is an independent event via genEvent).
	samples := map[string]map[string]any{}
	for i := 0; len(samples) < len(cfg.EventTypes) && i < 100_000; i++ {
		e := g.genEvent(i)
		if _, ok := samples[e.Type]; ok {
			continue
		}
		var p map[string]any
		if err := json.Unmarshal([]byte(e.Data), &p); err != nil {
			t.Fatalf("index %d data not JSON: %v", i, err)
		}
		samples[e.Type] = p
	}

	// Baseline carries value/group1/group2; LLM does not, and carries tokens.
	base := samples["api_request"]
	if _, ok := base["value"]; !ok {
		t.Error("api_request payload missing value")
	}
	llm := samples["llm_request"]
	if _, ok := llm["value"]; ok {
		t.Error("llm_request payload should not carry baseline value")
	}
	if _, ok := llm["tokens"]; !ok {
		t.Error("llm_request payload missing tokens")
	}
	// Numeric fields must be JSON strings (toFloat64OrNull fidelity).
	if _, isStr := llm["tokens"].(string); !isStr {
		t.Errorf("tokens must be a JSON string, got %T", llm["tokens"])
	}
	wl := samples["workload"]
	if _, isStr := wl["duration_seconds"].(string); !isStr {
		t.Errorf("duration_seconds must be a JSON string, got %T", wl["duration_seconds"])
	}
	// Sanity: every payload marshals to valid JSON.
	for name, p := range samples {
		if _, err := json.Marshal(p); err != nil {
			t.Errorf("type %q payload not JSON-marshalable: %v", name, err)
		}
	}
}

// TestSeedNoNestedPayloads is the contract Map(String,String) mode depends on:
// every event-type's payload must be a flat top-level object. If a future
// payload introduces a nested object or array, Map mode would lose data
// silently (flattenToStringMap doesn't recurse), so this test fails loudly.
func TestSeedNoNestedPayloads(t *testing.T) {
	cfg := DefaultConfig()
	g := newGenCtx(cfg)
	for i := 0; i < 50_000; i++ {
		e := g.genEvent(i)
		var p map[string]any
		if err := json.Unmarshal([]byte(e.Data), &p); err != nil {
			t.Fatalf("index %d not JSON: %v", i, err)
		}
		for k, v := range p {
			switch v.(type) {
			case map[string]any:
				t.Fatalf("type %q payload[%q] is a nested object; Map mode requires flat payloads", e.Type, k)
			case []any:
				t.Fatalf("type %q payload[%q] is an array; Map mode requires flat scalar values", e.Type, k)
			}
		}
	}
}

// TestSeedDataMapMatchesJSON pins the Map encoding to the JSON encoding: for
// every key in the JSON payload, DataMap[k] equals the text json.Marshal would
// produce for that value (with strings unquoted). This is what makes the Map
// scenario's queries produce the same answers as data-as-json down to the byte.
func TestSeedDataMapMatchesJSON(t *testing.T) {
	cfg := DefaultConfig()
	g := newGenCtx(cfg)
	for i := 0; i < 5_000; i++ {
		e := g.genEvent(i)
		var p map[string]any
		if err := json.Unmarshal([]byte(e.Data), &p); err != nil {
			t.Fatalf("index %d not JSON: %v", i, err)
		}
		if len(e.DataMap) != len(p) {
			t.Fatalf("index %d: DataMap has %d keys, JSON has %d", i, len(e.DataMap), len(p))
		}
		for k, v := range p {
			got, ok := e.DataMap[k]
			if !ok {
				t.Fatalf("index %d: DataMap missing key %q", i, k)
			}
			var want string
			if s, isStr := v.(string); isStr {
				want = s
			} else {
				b, _ := json.Marshal(v)
				want = string(b)
			}
			if got != want {
				t.Fatalf("index %d key %q: DataMap=%q, want %q", i, k, got, want)
			}
		}
	}
}
