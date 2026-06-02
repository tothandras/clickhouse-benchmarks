package cache

import (
	"testing"
	"time"

	"github.com/alpacahq/alpacadecimal"
)

func dec(i int64) alpacadecimal.Decimal { return alpacadecimal.NewFromInt(i) }

// TestDedupeRows_BugClassImpossibleWithDecimals documents the fix for Bug #1 in
// REVIEW-query-result-cache.md. The original cache stored a Float64 NaN "no
// value" sentinel for gap windows, then on the next query read those rows back
// and ran them through a dedupe whose check was `row.Value != seen.Value`.
// Because NaN != NaN is ALWAYS true in IEEE 754, a duplicated sentinel row
// (expected under parallel double-caching, which the dedupe exists to tolerate)
// took the error branch and failed the whole query — the most likely reason the
// cache never shipped.
//
// Here values are exact Decimals (alpacadecimal), which have NO NaN at all, and
// there is no sentinel (absent windows are simply absent rows). So the failure
// mode is STRUCTURALLY IMPOSSIBLE, not merely guarded: equal duplicates — the
// parallel-double-cache case the dedupe must tolerate — collapse cleanly.
func TestDedupeRows_BugClassImpossibleWithDecimals(t *testing.T) {
	w := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := []Row{
		{WindowStart: w, Subject: "s1", GroupBy: []string{"us-west-2", "free"}, Value: dec(42)},
		{WindowStart: w, Subject: "s1", GroupBy: []string{"us-west-2", "free"}, Value: dec(42)},
	}
	out, err := dedupeRows(rows)
	if err != nil {
		t.Fatalf("equal duplicates must not error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected the duplicate to collapse to 1 row, got %d", len(out))
	}
	if !out[0].Value.Equal(dec(42)) {
		t.Fatalf("expected the surviving row to keep value 42, got %s", out[0].Value.String())
	}
}

// TestDedupeRows_GenuineConflictStillErrors confirms the fix is narrow: a
// duplicate key with genuinely DIFFERENT values is still a real inconsistency
// and must still error (the behavior the original intended).
func TestDedupeRows_GenuineConflictStillErrors(t *testing.T) {
	w := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := []Row{
		{WindowStart: w, Subject: "s1", GroupBy: []string{"a"}, Value: dec(10)},
		{WindowStart: w, Subject: "s1", GroupBy: []string{"a"}, Value: dec(20)},
	}
	if _, err := dedupeRows(rows); err == nil {
		t.Fatal("expected an error for a duplicate key with different values, got nil")
	}
}

// TestDedupeRows_DistinctGroupsKeptSeparate confirms that rows sharing a window
// but differing in group-by are NOT collapsed — the parity/dedupe key is the
// full tuple, not the window alone.
func TestDedupeRows_DistinctGroupsKeptSeparate(t *testing.T) {
	w := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := []Row{
		{WindowStart: w, Subject: "s1", GroupBy: []string{"us-west-2"}, Value: dec(10)},
		{WindowStart: w, Subject: "s1", GroupBy: []string{"eu-west-1"}, Value: dec(20)},
	}
	out, err := dedupeRows(rows)
	if err != nil {
		t.Fatalf("distinct groups must not error: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("distinct group-by values must stay separate, got %d rows", len(out))
	}
}

// TestParamsHash_OrderIndependent confirms the cache key is stable regardless of
// subject ordering (the original sorts multi-valued fields before hashing).
func TestParamsHash_OrderIndependent(t *testing.T) {
	a := Params{Namespace: "ns", Type: "t", Subjects: []string{"s1", "s2", "s3"}, GroupByPaths: []string{"group1", "group2"}}
	b := Params{Namespace: "ns", Type: "t", Subjects: []string{"s3", "s1", "s2"}, GroupByPaths: []string{"group1", "group2"}}
	if a.Hash() != b.Hash() {
		t.Fatalf("subject order must not change the hash: %s vs %s", a.Hash(), b.Hash())
	}
	// Different group-by paths MUST produce a different key (they change the query).
	d := Params{Namespace: "ns", Type: "t", Subjects: []string{"s1"}, GroupByPaths: []string{"group1"}}
	e := Params{Namespace: "ns", Type: "t", Subjects: []string{"s1"}, GroupByPaths: []string{"group2"}}
	if d.Hash() == e.Hash() {
		t.Fatal("different group-by paths must produce different hashes")
	}
}
