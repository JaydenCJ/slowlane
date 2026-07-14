// Tests for request selectors: method sets, segment globs, and header
// values. The glob cases mirror the examples promised in the scenario
// format doc — if one of these fails, the docs are lying.
package match

import (
	"net/http"
	"testing"
)

func TestZeroCriteriaMatchesEverything(t *testing.T) {
	var c Criteria
	if !c.Matches("GET", "/", nil) {
		t.Fatal("zero criteria must match any request")
	}
	if !c.Matches("DELETE", "/deep/nested/path", http.Header{"X-Anything": {"v"}}) {
		t.Fatal("zero criteria must match any request shape")
	}
}

func TestMethodMatchIsCaseInsensitiveOnRequestSide(t *testing.T) {
	c := Criteria{Methods: []string{"GET", "POST"}}
	if !c.Matches("get", "/", nil) {
		t.Fatal("lowercase request method should match uppercase criteria")
	}
	if c.Matches("PUT", "/", nil) {
		t.Fatal("PUT is not in the method set")
	}
}

func TestPathCriterionRejectsNonMatching(t *testing.T) {
	c := Criteria{Path: "/users/*"}
	if !c.Matches("GET", "/users/42", nil) {
		t.Fatal("/users/42 should match /users/*")
	}
	if c.Matches("GET", "/orders/42", nil) {
		t.Fatal("/orders/42 must not match /users/*")
	}
}

func TestHeaderCriterionExactValueCaseInsensitiveName(t *testing.T) {
	c := Criteria{Headers: map[string]string{"x-tenant": "acme"}}
	h := http.Header{}
	h.Set("X-TENANT", "acme")
	if !c.Matches("GET", "/", h) {
		t.Fatal("header names are case-insensitive")
	}
	h.Set("X-Tenant", "acme-corp")
	if c.Matches("GET", "/", h) {
		t.Fatal("header matching is exact, not prefix")
	}
}

func TestHeaderCriterionAnyOccurrenceButNotAbsence(t *testing.T) {
	c := Criteria{Headers: map[string]string{"Accept": "application/json"}}
	h := http.Header{}
	h.Add("Accept", "text/html")
	h.Add("Accept", "application/json")
	if !c.Matches("GET", "/", h) {
		t.Fatal("any occurrence of the header may carry the value")
	}
	if c.Matches("GET", "/", http.Header{}) {
		t.Fatal("absent header must not match")
	}
}

func TestAllCriteriaMustHold(t *testing.T) {
	c := Criteria{Methods: []string{"POST"}, Path: "/api/**"}
	if c.Matches("POST", "/other", nil) {
		t.Fatal("path criterion must also hold")
	}
	if c.Matches("GET", "/api/v1", nil) {
		t.Fatal("method criterion must also hold")
	}
	if !c.Matches("POST", "/api/v1", nil) {
		t.Fatal("both criteria hold; must match")
	}
}

func TestMatchPathLiteralAndSegmentCounts(t *testing.T) {
	if !MatchPath("/users/42", "/users/42") {
		t.Fatal("literal paths must match themselves")
	}
	if MatchPath("/users/42", "/users/43") {
		t.Fatal("literal paths must not match different paths")
	}
	if MatchPath("/Orders/42", "/orders/42") {
		t.Fatal("path matching is case-sensitive")
	}
	if MatchPath("/a/b/c", "/a/b") {
		t.Fatal("pattern with more segments than the path must not match")
	}
	if MatchPath("/a", "/a/b") {
		t.Fatal("path with more segments than the pattern must not match")
	}
}

func TestMatchPathSingleStarIsOneWholeSegment(t *testing.T) {
	if !MatchPath("/users/*", "/users/42") {
		t.Fatal("* should match one segment")
	}
	if MatchPath("/users/*", "/users/42/posts") {
		t.Fatal("* must not cross a slash")
	}
	if MatchPath("/users/*", "/users") {
		t.Fatal("* requires the segment to exist")
	}
}

func TestMatchPathDoubleStarSpansSegments(t *testing.T) {
	for _, p := range []string{"/api", "/api/v1", "/api/v1/items/9"} {
		if !MatchPath("/api/**", p) {
			t.Errorf("/api/** should match %s", p)
		}
	}
	if MatchPath("/api/**", "/admin") {
		t.Fatal("/api/** must not match /admin")
	}
	// Interior "**" absorbs zero or more segments but keeps the suffix.
	if !MatchPath("/api/**/delete", "/api/v1/items/delete") {
		t.Fatal("** should absorb interior segments")
	}
	if !MatchPath("/api/**/delete", "/api/delete") {
		t.Fatal("** should absorb zero segments")
	}
	if MatchPath("/api/**/delete", "/api/v1/items") {
		t.Fatal("suffix after ** must still be required")
	}
}

func TestMatchPathWithinSegmentWildcards(t *testing.T) {
	if !MatchPath("/api/v*/items", "/api/v1/items") || !MatchPath("/api/v*/items", "/api/v22/items") {
		t.Fatal("v* should match v1 and v22")
	}
	if MatchPath("/api/v*/items", "/api/beta/items") {
		t.Fatal("v* must not match beta")
	}
	if !MatchPath("/files/*.tar.*", "/files/backup.tar.gz") {
		t.Fatal("*.tar.* should match backup.tar.gz")
	}
	if MatchPath("/files/*.tar.*", "/files/backup.zip") {
		t.Fatal("*.tar.* must not match backup.zip")
	}
	if !MatchPath("/v*/x", "/v/x") {
		t.Fatal("* may match an empty run within a segment")
	}
}

func TestMatchPathTrailingSlashInsensitive(t *testing.T) {
	// Both sides are trimmed before splitting, so a trailing slash on
	// either side does not change the outcome.
	if !MatchPath("/users/*/", "/users/42") {
		t.Fatal("trailing slash on the pattern is ignored")
	}
	if !MatchPath("/users/*", "/users/42/") {
		t.Fatal("trailing slash on the path is ignored")
	}
}

func TestMatchPathRootPattern(t *testing.T) {
	if !MatchPath("/", "/") {
		t.Fatal("/ should match /")
	}
	if MatchPath("/", "/users") {
		t.Fatal("/ must not match /users")
	}
	if !MatchPath("/**", "/anything/at/all") {
		t.Fatal("/** should match every path")
	}
	if !MatchPath("/**", "/") {
		t.Fatal("/** should match the root")
	}
}
