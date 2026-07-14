// Package seeded implements slowlane's deterministic decision source.
//
// Every probabilistic choice in a scenario (does a rule with rate < 1 fire?
// how much jitter does this request get?) is answered by hashing a fixed
// coordinate — (seed, proxy, rule, purpose, request counter) — through
// SplitMix64. There is no mutable RNG state: the decision for request N is a
// pure function of the scenario seed and N, independent of every other
// request. That is what makes `slowlane plan` output and live proxy behavior
// byte-identical, and it is why the algorithm below is part of the scenario
// file format contract (see docs/determinism.md). Changing it is a breaking
// change.
package seeded

// fnv1a64 hashes a label with 64-bit FNV-1a. It is used only to fold the
// textual parts of a coordinate (proxy name, rule name, purpose) into a
// single word before mixing.
func fnv1a64(s string) uint64 {
	const (
		offset = 0xcbf29ce484222325
		prime  = 0x00000100000001b3
	)
	h := uint64(offset)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime
	}
	return h
}

// mix is one SplitMix64 output step. It is a strong 64-bit finalizer: every
// input bit affects every output bit, so nearby counters map to unrelated
// draws.
func mix(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

// word computes the raw 64-bit draw for a coordinate. The label parts are
// joined with NUL separators so that ("ab","c") and ("a","bc") cannot
// collide.
func word(seed uint64, proxy, rule, purpose string, counter uint64) uint64 {
	label := fnv1a64(proxy + "\x00" + rule + "\x00" + purpose)
	return mix(seed ^ mix(label^mix(counter)))
}

// Draw returns a uniform float64 in [0, 1) for the coordinate. It uses the
// top 53 bits of the hashed word, the standard exact-double construction.
func Draw(seed uint64, proxy, rule, purpose string, counter uint64) float64 {
	return float64(word(seed, proxy, rule, purpose, counter)>>11) / (1 << 53)
}

// DrawInt returns a deterministic integer in [0, n] for the coordinate.
// n must be >= 0; DrawInt(…, 0) is always 0. The mapping floors a uniform
// draw across n+1 buckets; for the millisecond-scale ranges slowlane uses,
// the bias of this construction is far below one part per billion.
func DrawInt(seed uint64, proxy, rule, purpose string, counter uint64, n int64) int64 {
	if n <= 0 {
		return 0
	}
	f := Draw(seed, proxy, rule, purpose, counter)
	v := int64(f * float64(n+1))
	if v > n { // guard the f≈1 edge; Draw is < 1 but be explicit
		v = n
	}
	return v
}
