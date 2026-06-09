package accounting

import "testing"

func feed(p *Plateau, counts ...int) bool {
	last := false
	for _, c := range counts {
		last = p.Observe(c)
	}
	return last
}

func TestPlateauNeedsFiveObservations(t *testing.T) {
	p := &Plateau{}
	if feed(p, 100, 100, 100, 100) {
		t.Fatal("4 observations must not satisfy a 5-poll rule")
	}
	if !p.Observe(100) {
		t.Fatal("5th identical observation must reach the plateau")
	}
}

func TestPlateauWithinTenPercentBand(t *testing.T) {
	// 100..110: max == 1.1 x min, inside the band.
	if !feed(&Plateau{}, 100, 104, 110, 102, 106) {
		t.Fatal("±10% band must count as stable")
	}
	// 100..112: outside.
	if feed(&Plateau{}, 100, 104, 112, 102, 106) {
		t.Fatal(">10% spread must not count as stable")
	}
}

func TestPlateauSlidesOverGrowth(t *testing.T) {
	p := &Plateau{}
	// Monotone growth never plateaus...
	if feed(p, 10, 20, 40, 80, 160, 320) {
		t.Fatal("doubling part counts must not plateau")
	}
	// ...until the tail stabilizes: the window slides past the growth phase.
	if !feed(p, 330, 332, 328, 331) {
		t.Fatal("stable tail after growth must plateau (window slides)")
	}
}

func TestPlateauEmptyTable(t *testing.T) {
	if !feed(&Plateau{}, 0, 0, 0, 0, 0) {
		t.Fatal("a table that stays empty is stable")
	}
	if feed(&Plateau{}, 0, 0, 0, 0, 5) {
		t.Fatal("growth from empty is not stable")
	}
}
