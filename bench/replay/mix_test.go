package replay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realScenarioQueries lists the *.sql names in a real scenario dir, the same
// way harness discovery does.
func realScenarioQueries(t *testing.T, scenarioDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(scenarioDir, "queries"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".sql"); ok {
			names = append(names, name)
		}
	}
	return names
}

// TestShippedManifestsValidate is the contract test the review demanded:
// the shipped mix.json files must validate against each scenario's REAL
// on-disk query set (34 queries for proposal, 30 for baseline-openmeter).
func TestShippedManifestsValidate(t *testing.T) {
	for _, sc := range []struct {
		dir        string
		wantOnDisk int
		hasLookup  bool
	}{
		{filepath.Join("..", "..", "scenarios", "proposal"), 34, true},
		{filepath.Join("..", "..", "scenarios", "baseline-openmeter"), 30, false},
	} {
		t.Run(filepath.Base(sc.dir), func(t *testing.T) {
			queries := realScenarioQueries(t, sc.dir)
			if len(queries) != sc.wantOnDisk {
				t.Fatalf("scenario has %d queries on disk, test expects %d — update mix.json AND this test", len(queries), sc.wantOnDisk)
			}
			m, err := LoadMix(sc.dir, "production", queries)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := m.Classes["lookup"]; ok != sc.hasLookup {
				t.Fatalf("lookup class presence = %v, want %v", ok, sc.hasLookup)
			}
			if m.Notes == "" {
				t.Fatal("placeholder weights must carry a notes caveat")
			}
			if m.Replayable("sum_hour_group1_no_prewhere") {
				t.Fatal("excluded diagnostic query must not be replayable")
			}
			if !m.Replayable("sum_hour") {
				t.Fatal("classified query must be replayable")
			}
		})
	}
}

func writeMixDir(t *testing.T, mixJSON string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "queries"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "queries", "mix.json"), []byte(mixJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const tinyMix = `{
  "production": {
    "classes": {
      "a": {"weight": 3, "queries": ["q1", "q2"]},
      "b": {"weight": 1, "queries": ["q3"]}
    },
    "exclude": ["diag"]
  }
}`

func TestLoadMixHappyPath(t *testing.T) {
	dir := writeMixDir(t, tinyMix)
	m, err := LoadMix(dir, "production", []string{"q1", "q2", "q3", "diag"})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Classes) != 2 {
		t.Fatalf("want 2 classes, got %d", len(m.Classes))
	}
}

func TestLoadMixUnclassifiedOnDiskQuery(t *testing.T) {
	dir := writeMixDir(t, tinyMix)
	_, err := LoadMix(dir, "production", []string{"q1", "q2", "q3", "diag", "newly_added"})
	if err == nil || !strings.Contains(err.Error(), "newly_added") {
		t.Fatalf("unclassified on-disk query must be named in the error, got: %v", err)
	}
}

func TestLoadMixMissingFileForReference(t *testing.T) {
	dir := writeMixDir(t, tinyMix)
	_, err := LoadMix(dir, "production", []string{"q1", "q2", "diag"}) // q3 has no file
	if err == nil || !strings.Contains(err.Error(), "q3") {
		t.Fatalf("manifest reference without a .sql file must be named, got: %v", err)
	}
}

func TestLoadMixDoubleAssignment(t *testing.T) {
	const dup = `{"production": {"classes": {
	  "a": {"weight": 1, "queries": ["q1"]},
	  "b": {"weight": 1, "queries": ["q1"]}
	}}}`
	dir := writeMixDir(t, dup)
	_, err := LoadMix(dir, "production", []string{"q1"})
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("double class assignment must be rejected, got: %v", err)
	}

	const exclDup = `{"production": {"classes": {
	  "a": {"weight": 1, "queries": ["q1"]}
	}, "exclude": ["q1"]}}`
	dir2 := writeMixDir(t, exclDup)
	_, err = LoadMix(dir2, "production", []string{"q1"})
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("classified+excluded must be rejected, got: %v", err)
	}
}

func TestLoadMixUnknownMixName(t *testing.T) {
	dir := writeMixDir(t, tinyMix)
	_, err := LoadMix(dir, "staging", []string{"q1", "q2", "q3", "diag"})
	if err == nil || !strings.Contains(err.Error(), "production") {
		t.Fatalf("unknown mix must list available names, got: %v", err)
	}
}
