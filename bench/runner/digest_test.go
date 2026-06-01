package runner

import "testing"

// normalizeCell is the heart of the digest: it must render a value the same way
// regardless of whether the access path produced a bare or a Nullable column,
// and must round floats/decimals so summation-order differences don't matter.
func TestNormalizeCellNullableEqualsBare(t *testing.T) {
	s := "coder-agent"
	// JSON_VALUE → Nullable(String) scans to *string; native .:String → string.
	if bare, ptr := normalizeCell(s, false, false), normalizeCell(&s, false, false); bare != ptr {
		t.Fatalf("bare %q != pointer %q — Nullable and non-null columns must digest equal", bare, ptr)
	}

	f := 1.234567891
	bf := normalizeCell(f, true, false)   // bare float64
	pf := normalizeCell(&f, true, false)  // *float64 (Nullable)
	if bf != pf {
		t.Fatalf("bare float %q != pointer float %q", bf, pf)
	}
	if bf != "1.234568" { // rounded to DigestDecimals=6
		t.Fatalf("float not rounded to %d dp: got %q", DigestDecimals, bf)
	}

	// A nil pointer is SQL NULL and must not format as a Go nil/address.
	var np *string
	if got := normalizeCell(np, false, false); got != "∅" {
		t.Fatalf("nil pointer should be NULL sentinel, got %q", got)
	}

	// Float summation-order jitter below the rounding precision must collapse.
	a := normalizeCell(485.0483524995147, true, false)
	b := normalizeCell(485.0483521234999, true, false)
	if a != b {
		t.Fatalf("floats equal at 6dp should normalize equal: %q vs %q", a, b)
	}
}
