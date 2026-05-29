package runner

import (
	"strings"
	"testing"
)

func TestSplitStatements(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "single statement, no trailing semicolon",
			in:   "CREATE TABLE t (a Int)",
			want: []string{"CREATE TABLE t (a Int)"},
		},
		{
			name: "two statements",
			in:   "DROP TABLE t; CREATE TABLE t (a Int)",
			want: []string{"DROP TABLE t", "CREATE TABLE t (a Int)"},
		},
		{
			name: "leading comment block is stripped",
			in:   "-- a comment\n-- another\nSELECT 1",
			want: []string{"SELECT 1"},
		},
		{
			// Regression: a comment line containing a semicolon must not be
			// split into a bogus statement. Both init.sql headers and query
			// file headers carry prose with semicolons (and EXPLAIN examples),
			// which LoadQueries also relies on this splitter to handle.
			name: "semicolon inside a comment does not split",
			in:   "-- the team converged on this; it bakes in per-meter knowledge\nCREATE TABLE t (a Int)",
			want: []string{"CREATE TABLE t (a Int)"},
		},
		{
			name: "blank statements between semicolons are dropped",
			in:   "SELECT 1;; ;\nSELECT 2",
			want: []string{"SELECT 1", "SELECT 2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitStatements(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d statements %q, want %d %q", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if strings.TrimSpace(got[i]) != tc.want[i] {
					t.Errorf("statement %d: got %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
