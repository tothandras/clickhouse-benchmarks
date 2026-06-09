// Package replay is the cogs weighted query replayer: it draws arrivals from
// a configured process, picks queries by class weight from a scenario's mix
// manifest, and issues them through the native client with cogs tagging.
package replay

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Class is one weighted query class in a mix.
type Class struct {
	Weight  int      `json:"weight"`
	Queries []string `json:"queries"`
}

// Mix is one named query mix: weighted classes plus explicit excludes for
// on-disk queries that are diagnostics, not production traffic (e.g. the
// *_no_prewhere variants).
type Mix struct {
	Notes   string           `json:"notes,omitempty"`
	Classes map[string]Class `json:"classes"`
	Exclude []string         `json:"exclude,omitempty"`
}

// MixFile is scenarios/<name>/queries/mix.json: mix name -> Mix.
type MixFile map[string]Mix

// LoadMix loads the named mix from the scenario's queries/mix.json and
// validates it against the query names actually on disk: every on-disk query
// must appear in exactly one class or in exclude, and every referenced name
// must exist on disk. This forces conscious classification when queries are
// added to a scenario.
func LoadMix(scenarioDir, mixName string, onDiskQueries []string) (Mix, error) {
	path := filepath.Join(scenarioDir, "queries", "mix.json")
	f, err := os.Open(path)
	if err != nil {
		return Mix{}, fmt.Errorf("mix manifest: %w", err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var mf MixFile
	if err := dec.Decode(&mf); err != nil {
		return Mix{}, fmt.Errorf("mix manifest %s: %w", path, err)
	}
	m, ok := mf[mixName]
	if !ok {
		names := make([]string, 0, len(mf))
		for k := range mf {
			names = append(names, k)
		}
		sort.Strings(names)
		return Mix{}, fmt.Errorf("mix %q not found in %s (have: %s)", mixName, path, strings.Join(names, ", "))
	}
	if err := m.Validate(onDiskQueries); err != nil {
		return Mix{}, fmt.Errorf("mix %q in %s: %w", mixName, path, err)
	}
	return m, nil
}

// Validate enforces the exactly-one-class-or-exclude contract over the
// on-disk query set.
func (m Mix) Validate(onDiskQueries []string) error {
	if len(m.Classes) == 0 {
		return fmt.Errorf("no classes defined")
	}

	assigned := map[string]string{} // query -> class (or "exclude")
	for _, q := range m.Exclude {
		assigned[q] = "exclude"
	}
	classNames := make([]string, 0, len(m.Classes))
	for name := range m.Classes {
		classNames = append(classNames, name)
	}
	sort.Strings(classNames)

	for _, name := range classNames {
		c := m.Classes[name]
		if c.Weight <= 0 {
			return fmt.Errorf("class %q: weight must be > 0", name)
		}
		if len(c.Queries) == 0 {
			return fmt.Errorf("class %q: no queries", name)
		}
		for _, q := range c.Queries {
			if prev, dup := assigned[q]; dup {
				return fmt.Errorf("query %q assigned to both %q and %q (must be exactly one)", q, prev, name)
			}
			assigned[q] = name
		}
	}

	onDisk := map[string]bool{}
	for _, q := range onDiskQueries {
		onDisk[q] = true
	}
	for q := range assigned {
		if !onDisk[q] {
			return fmt.Errorf("query %q referenced in the manifest has no .sql file on disk", q)
		}
	}
	var unclassified []string
	for _, q := range onDiskQueries {
		if _, ok := assigned[q]; !ok {
			unclassified = append(unclassified, q)
		}
	}
	if len(unclassified) > 0 {
		sort.Strings(unclassified)
		return fmt.Errorf("on-disk queries not classified (add to a class or to exclude): %s", strings.Join(unclassified, ", "))
	}
	return nil
}

// Replayable reports whether the mix selects q (i.e. it is classified, not
// excluded).
func (m Mix) Replayable(q string) bool {
	for _, c := range m.Classes {
		if slices.Contains(c.Queries, q) {
			return true
		}
	}
	return false
}
