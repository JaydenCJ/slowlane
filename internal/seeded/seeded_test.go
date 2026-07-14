// Tests for the deterministic decision source. The golden-value tests are
// the most important file in the suite: they pin the hash algorithm, which
// is part of the scenario file format contract (docs/determinism.md). If
// one of them fails, the change is breaking and needs a format version bump.
package seeded

import (
	"math"
	"testing"
)

func TestDrawStaysInHalfOpenUnitIntervalAndFinite(t *testing.T) {
	for c := uint64(0); c < 5000; c++ {
		v := Draw(99, "proxy", "rule", "rate", c)
		if v < 0 || v >= 1 {
			t.Fatalf("Draw out of [0,1) at counter %d: %v", c, v)
		}
	}
	// Extreme seed must not produce NaN/Inf either.
	for c := uint64(0); c < 1000; c++ {
		v := Draw(math.MaxUint64, "p", "r", "x", c)
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Fatalf("non-finite draw at counter %d", c)
		}
	}
}

// The pinned constants below were produced by this implementation and are
// now frozen: they guarantee scenarios replay identically across releases
// and platforms. A failure here is a format-breaking change.
func TestDrawGoldenValues(t *testing.T) {
	cases := []struct {
		seed                 uint64
		proxy, rule, purpose string
		counter              uint64
		want                 float64
	}{
		{42, "api", "brownout", "rate", 1, 0.77062817083341006},
		{42, "api", "brownout", "rate", 2, 0.36894825553352428},
		{42, "api", "brownout", "rate", 3, 0.83571265025676944},
		{0, "p", "r", "jitter", 10, 0.017442580445358624},
	}
	for _, c := range cases {
		got := Draw(c.seed, c.proxy, c.rule, c.purpose, c.counter)
		if got != c.want {
			t.Errorf("Draw(%d, %s, %s, %s, %d) = %.17g, want %.17g",
				c.seed, c.proxy, c.rule, c.purpose, c.counter, got, c.want)
		}
		if again := Draw(c.seed, c.proxy, c.rule, c.purpose, c.counter); again != got {
			t.Errorf("Draw is not deterministic: %v then %v", got, again)
		}
	}
}

func TestDrawIntGoldenValues(t *testing.T) {
	want := []int64{64, 79, 43}
	for i, w := range want {
		got := DrawInt(42, "api", "slow", "jitter", uint64(i+1), 100)
		if got != w {
			t.Errorf("DrawInt(counter=%d) = %d, want %d", i+1, got, w)
		}
	}
}

func TestDrawVariesWithEveryCoordinate(t *testing.T) {
	base := Draw(42, "api", "rule", "rate", 5)
	variants := map[string]float64{
		"seed":    Draw(43, "api", "rule", "rate", 5),
		"proxy":   Draw(42, "api2", "rule", "rate", 5),
		"rule":    Draw(42, "api", "rule2", "rate", 5),
		"purpose": Draw(42, "api", "rule", "jitter", 5),
		"counter": Draw(42, "api", "rule", "rate", 6),
	}
	for coord, v := range variants {
		if v == base {
			t.Errorf("changing the %s coordinate did not change the draw", coord)
		}
	}
}

// Concatenation ambiguity would let two distinct rules share a decision
// stream; the NUL separators must prevent ("ab","c") == ("a","bc").
func TestLabelSeparationPreventsConcatenationCollisions(t *testing.T) {
	if Draw(42, "ab", "c", "rate", 1) == Draw(42, "a", "bc", "rate", 1) {
		t.Fatal("label parts must be separated, not concatenated")
	}
}

// DrawInt must stay inside [0, n], reach every bucket (or jitter would be
// systematically biased), and collapse to 0 for empty ranges.
func TestDrawIntBoundsCoverageAndDegenerateRanges(t *testing.T) {
	seen := map[int64]bool{}
	for c := uint64(0); c < 500; c++ {
		v := DrawInt(3, "p", "r", "jitter", c, 3)
		if v < 0 || v > 3 {
			t.Fatalf("DrawInt out of [0,3] at counter %d: %d", c, v)
		}
		seen[v] = true
	}
	for v := int64(0); v <= 3; v++ {
		if !seen[v] {
			t.Errorf("bucket %d never drawn in 500 counters", v)
		}
	}
	if v := DrawInt(9, "p", "r", "jitter", 1, 0); v != 0 {
		t.Fatalf("DrawInt(…, 0) = %d, want 0", v)
	}
	if v := DrawInt(9, "p", "r", "jitter", 1, -3); v != 0 {
		t.Fatalf("DrawInt(…, -3) = %d, want 0", v)
	}
}

// Rates should fire close to their nominal share over many counters. The
// draw sequence is fixed, so these assert exact pinned counts — the tests
// document the real behavior rather than a tolerance.
func TestRatesFireThePinnedShareOfCounters(t *testing.T) {
	fires := func(seed uint64, rule string, rate float64) int {
		n := 0
		for c := uint64(1); c <= 1000; c++ {
			if Draw(seed, "api", rule, "rate", c) < rate {
				n++
			}
		}
		return n
	}
	if got := fires(42, "flaky", 0.5); got != 481 {
		t.Fatalf("rate-0.5 fired %d/1000 times, want the pinned 481", got)
	}
	if got := fires(7, "outage", 0.25); got != 265 {
		t.Fatalf("rate-0.25 fired %d/1000 times, want the pinned 265", got)
	}
}
