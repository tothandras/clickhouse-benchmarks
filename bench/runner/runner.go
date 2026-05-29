// Package runner discovers benchmark scenarios on disk and executes them
// against a ClickHouse cluster. Each scenario is a directory under scenarios/
// containing at minimum init.sql and a queries/ subdirectory.
//
// Per-query measurement is delegated to the official `clickhouse-benchmark`
// CLI (see benchmark.go) — the runner only handles scenario lifecycle:
// discovery, init.sql apply, seed integration, and result file assembly.
package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// Scenario describes one variant discovered on disk.
type Scenario struct {
	Name      string   // directory basename
	Dir       string   // absolute path
	InitSQL   string   // contents of init.sql
	SeedSQL   string   // contents of seed.sql; "" if no seed.sql present
	HasSeed   bool     // true if seed.sql exists
	QueryDirs []string // absolute paths to queries/ directory entries
}

// Query is one parsed benchmark query.
type Query struct {
	Name string // filename without .sql extension
	Path string
	SQL  string
}

// Discover walks the scenarios directory and returns every directory that
// contains an init.sql. Directories without init.sql are skipped with a
// warning printed to stderr.
func Discover(scenariosDir string, only []string) ([]Scenario, error) {
	entries, err := os.ReadDir(scenariosDir)
	if err != nil {
		return nil, fmt.Errorf("read scenarios dir: %w", err)
	}
	onlySet := map[string]bool{}
	for _, n := range only {
		onlySet[n] = true
	}
	var scenarios []Scenario
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".") {
			continue
		}
		if len(onlySet) > 0 && !onlySet[name] {
			continue
		}
		dir := filepath.Join(scenariosDir, name)
		initPath := filepath.Join(dir, "init.sql")
		initBytes, err := os.ReadFile(initPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping %s (no init.sql or unreadable: %v)\n", name, err)
			continue
		}
		s := Scenario{
			Name:    name,
			Dir:     dir,
			InitSQL: string(initBytes),
		}
		if seedBytes, err := os.ReadFile(filepath.Join(dir, "seed.sql")); err == nil {
			s.SeedSQL = string(seedBytes)
			s.HasSeed = true
		}
		queriesDir := filepath.Join(dir, "queries")
		queryEntries, err := os.ReadDir(queriesDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: scenario %s has no queries/ directory\n", name)
		}
		for _, qe := range queryEntries {
			if !strings.HasSuffix(qe.Name(), ".sql") {
				continue
			}
			s.QueryDirs = append(s.QueryDirs, filepath.Join(queriesDir, qe.Name()))
		}
		sort.Strings(s.QueryDirs)
		scenarios = append(scenarios, s)
	}
	sort.Slice(scenarios, func(i, j int) bool {
		return scenarios[i].Name < scenarios[j].Name
	})
	return scenarios, nil
}

// LoadQueries reads and validates the query files for a scenario.
// Each file must contain exactly one SELECT statement (multiple statements
// produce an error logged to stderr and the query is skipped).
func LoadQueries(scenario Scenario) []Query {
	var queries []Query
	for _, path := range scenario.QueryDirs {
		b, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: cannot read %s: %v\n", path, err)
			continue
		}
		// Split with the same comment-aware splitter used for init/seed, so a
		// `;` inside a `--` comment (e.g. an EXPLAIN example in the file header)
		// does not get miscounted as a second statement. Exactly one real
		// statement must remain.
		stmts := splitStatements(string(b))
		if len(stmts) != 1 {
			fmt.Fprintf(os.Stderr, "warning: skipping %s (expected exactly one statement, got %d)\n", path, len(stmts))
			continue
		}
		name := strings.TrimSuffix(filepath.Base(path), ".sql")
		queries = append(queries, Query{Name: name, Path: path, SQL: stmts[0]})
	}
	return queries
}

// ApplyInit runs every statement in init.sql against conn. Splits on `;` at
// the top level (no escape-aware parsing — scenarios should not include
// semicolons inside string literals in DDL). Substitutes `{{database}}` with
// the database name from the connection.
func ApplyInit(ctx context.Context, conn driver.Conn, scenario Scenario, database string) error {
	sql := scenario.InitSQL
	if database != "" {
		sql = strings.ReplaceAll(sql, "{{database}}", database)
	}
	for _, stmt := range splitStatements(sql) {
		if stmt == "" {
			continue
		}
		if err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("init %s: %w", scenario.Name, err)
		}
	}
	return nil
}

// ApplySeedSQL runs the scenario's seed.sql if present.
func ApplySeedSQL(ctx context.Context, conn driver.Conn, scenario Scenario) error {
	if !scenario.HasSeed {
		return nil
	}
	for _, stmt := range splitStatements(scenario.SeedSQL) {
		if stmt == "" {
			continue
		}
		if err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("seed %s: %w", scenario.Name, err)
		}
	}
	return nil
}

func splitStatements(s string) []string {
	// Strip `--` line comments BEFORE splitting on `;`. A comment may itself
	// contain a semicolon (prose like "converged on; it..."), and splitting
	// first would cut the comment in half and feed the tail to the parser.
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		lines = append(lines, line)
	}
	parts := strings.Split(strings.Join(lines, "\n"), ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		stmt := strings.TrimSpace(p)
		if stmt != "" {
			out = append(out, stmt)
		}
	}
	return out
}
