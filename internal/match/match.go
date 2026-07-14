// Package match implements the request selectors a scenario rule can carry:
// HTTP method lists, segment-aware path globs, and exact header values. All
// matching is pure and allocation-light — it runs on every proxied request.
package match

import (
	"net/http"
	"strings"
)

// Criteria is the compiled form of a rule's "match" block. Zero-value
// Criteria matches every request.
type Criteria struct {
	// Methods is the allowed method set, uppercase. Empty means any method.
	Methods []string
	// Path is a segment glob (see MatchPath). Empty means any path.
	Path string
	// Headers maps canonical header names to required exact values. A
	// request matches when every listed header carries the value in at
	// least one of its occurrences. Empty means no header constraint.
	Headers map[string]string
}

// Matches reports whether a request described by (method, path, header)
// satisfies every criterion. header may be nil when Headers is empty.
func (c Criteria) Matches(method, path string, header http.Header) bool {
	if len(c.Methods) > 0 {
		m := strings.ToUpper(method)
		ok := false
		for _, want := range c.Methods {
			if m == want {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if c.Path != "" && !MatchPath(c.Path, path) {
		return false
	}
	for name, want := range c.Headers {
		ok := false
		for _, got := range header.Values(name) {
			if got == want {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// MatchPath reports whether an URL path matches a segment glob pattern.
//
// The pattern language is deliberately small and has no escaping:
//
//   - the pattern and path are split on "/" (leading slashes trimmed);
//   - a segment equal to "**" matches zero or more whole segments;
//   - within a segment, "*" matches any run of characters (including none);
//   - everything else matches literally, case-sensitively.
//
// Examples: "/users/*" matches "/users/42" but not "/users/42/posts";
// "/api/**" matches "/api", "/api/v1" and "/api/v1/items/9";
// "/api/v*/items" matches "/api/v1/items" and "/api/v22/items".
func MatchPath(pattern, path string) bool {
	return matchSegs(splitPath(pattern), splitPath(path))
}

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// matchSegs matches pattern segments against path segments recursively.
// Recursion depth is bounded by the pattern length, which comes from the
// scenario file, not from the request.
func matchSegs(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			// "**" absorbs zero or more segments: try every split point.
			if matchSegs(pat[1:], segs) {
				return true
			}
			if len(segs) == 0 {
				return false
			}
			segs = segs[1:]
			continue
		}
		if len(segs) == 0 || !matchSeg(pat[0], segs[0]) {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}

// matchSeg matches one pattern segment (with "*" wildcards) against one
// path segment, iteratively to keep per-request work predictable.
func matchSeg(pat, seg string) bool {
	// Fast path: no wildcard.
	if !strings.Contains(pat, "*") {
		return pat == seg
	}
	parts := strings.Split(pat, "*")
	// The first and last parts are anchored; the middle parts float.
	if !strings.HasPrefix(seg, parts[0]) {
		return false
	}
	seg = seg[len(parts[0]):]
	last := parts[len(parts)-1]
	if !strings.HasSuffix(seg, last) {
		return false
	}
	seg = seg[:len(seg)-len(last)]
	for _, mid := range parts[1 : len(parts)-1] {
		idx := strings.Index(seg, mid)
		if idx < 0 {
			return false
		}
		seg = seg[idx+len(mid):]
	}
	return true
}
