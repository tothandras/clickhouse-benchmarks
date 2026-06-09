package accounting

// Plateau implements the soak gate's stability rule: the active part count
// has plateaued once 5 consecutive observations sit within a ±10% band
// (max <= 1.1 x min over the window). Pure state machine; the runner owns
// the polling loop.
type Plateau struct {
	window []int
}

// PlateauWindow is how many consecutive polls the rule evaluates.
const PlateauWindow = 5

// Observe records a poll and reports whether the plateau has been reached.
func (p *Plateau) Observe(parts int) bool {
	p.window = append(p.window, parts)
	if len(p.window) > PlateauWindow {
		p.window = p.window[1:]
	}
	if len(p.window) < PlateauWindow {
		return false
	}
	lo, hi := p.window[0], p.window[0]
	for _, v := range p.window[1:] {
		lo, hi = min(lo, v), max(hi, v)
	}
	if lo == 0 {
		// An empty table is vacuously stable only if it stays empty.
		return hi == 0
	}
	return float64(hi) <= 1.1*float64(lo)
}
