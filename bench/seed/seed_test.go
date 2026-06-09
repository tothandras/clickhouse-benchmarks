package seed

import (
	"encoding/json"
	"testing"
)

// mustGenCtx builds the validated genCtx Run and Generator use, so tests
// exercise the real per-index generation path without a ClickHouse connection.
func mustGenCtx(cfg Config) *genCtx {
	g, err := newGenCtx(cfg)
	if err != nil {
		panic(err)
	}
	return g
}

// genTypesAndData reproduces Run's per-index generation for n rows so we can
// assert mix/determinism without a ClickHouse connection.
func genTypesAndData(cfg Config, n int) ([]string, []string) {
	g := mustGenCtx(cfg)
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

func TestSeedNamespaceDistribution(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Namespaces = 5
	g := mustGenCtx(cfg)
	const n = 50_000

	counts := map[string]int{}
	first := make([]string, n)
	for i := 0; i < n; i++ {
		ns := g.genEvent(i).Namespace
		counts[ns]++
		first[i] = ns
	}
	if len(counts) != 5 {
		t.Fatalf("expected 5 namespaces, saw %d: %v", len(counts), counts)
	}
	if counts[cfg.Namespace] == 0 {
		t.Errorf("primary namespace %q never generated", cfg.Namespace)
	}
	// Deterministic: a second pass yields identical per-row assignments.
	g2 := mustGenCtx(cfg)
	for i := 0; i < n; i++ {
		if got := g2.genEvent(i).Namespace; got != first[i] {
			t.Fatalf("row %d namespace not deterministic: %q vs %q", i, got, first[i])
		}
	}
}

func TestSeedSingleNamespaceUnchanged(t *testing.T) {
	// The namespace draw is skipped for n≤1, so single-namespace data (every
	// field) must be byte-identical whether Namespaces is 0 or 1.
	a := DefaultConfig()
	a.Namespaces = 0
	b := DefaultConfig()
	b.Namespaces = 1
	ga, gb := mustGenCtx(a), mustGenCtx(b)
	for i := 0; i < 2000; i++ {
		ea, eb := ga.genEvent(i), gb.genEvent(i)
		if ea.Namespace != a.Namespace || eb.Namespace != b.Namespace {
			t.Fatalf("row %d: single-namespace rows must carry the primary namespace", i)
		}
		if ea.ID != eb.ID || ea.Data != eb.Data || ea.Subject != eb.Subject || !ea.Time.Equal(eb.Time) {
			t.Fatalf("row %d: single-namespace data diverged between Namespaces=0 and 1", i)
		}
	}
}

func TestSeedMixedValueStorage(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MixedValueStorage = true
	cfg.EventTypes = withMixedBaseline(cfg.EventTypes, cfg.Group1, cfg.Group2)
	g := mustGenCtx(cfg)

	var sawNumber, sawString, sawBigint bool
	for i := 0; i < 20_000 && !(sawNumber && sawString && sawBigint); i++ {
		e := g.genEvent(i)
		if e.Type != "api_request" {
			continue
		}
		var p map[string]any
		if err := json.Unmarshal([]byte(e.Data), &p); err != nil {
			t.Fatalf("index %d not JSON: %v", i, err)
		}
		switch v := p["value"].(type) {
		case float64:
			sawNumber = true
		case string:
			sawString = true
			// A value > 2^53 stored as a string is the case the typed `.:Float64`
			// accessor cannot represent exactly but toDecimal128OrNull(toString)
			// can — the whole point of the mixed-storage stress.
			if len(v) >= 16 { // ~1e15+, the bigint branch (1<<60 ≈ 1.15e18)
				sawBigint = true
			}
		default:
			t.Fatalf("index %d: unexpected value type %T", i, v)
		}
	}
	if !sawNumber {
		t.Error("mixed storage never emitted a JSON number value")
	}
	if !sawString {
		t.Error("mixed storage never emitted a JSON string value")
	}
	if !sawBigint {
		t.Error("mixed storage never emitted a Float64-overflowing bigint value")
	}

	// Default (uniform) config must NOT produce string values — guards the gate.
	gu := mustGenCtx(DefaultConfig())
	for i := 0; i < 5_000; i++ {
		e := gu.genEvent(i)
		if e.Type != "api_request" {
			continue
		}
		var p map[string]any
		_ = json.Unmarshal([]byte(e.Data), &p)
		if _, isStr := p["value"].(string); isStr {
			t.Fatalf("index %d: uniform config emitted a string value; gate leaked", i)
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
	g := mustGenCtx(cfg)
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
	llm := samples["kong.llm_request"]
	if _, ok := llm["value"]; ok {
		t.Error("kong.llm_request payload should not carry baseline value")
	}
	if _, ok := llm["tokens"]; !ok {
		t.Error("kong.llm_request payload missing tokens")
	}
	// Numeric fields must be JSON strings (toDecimal128OrNull fidelity).
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
