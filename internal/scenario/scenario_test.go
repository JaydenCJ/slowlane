// Tests for scenario parsing and validation. The stance under test:
// strict on input (unknown fields and half-broken files are errors, with
// locations), generous on output (all findings in one pass).
package scenario

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// minimal returns a valid single-proxy scenario document.
func minimal() string {
	return `{
	  "version": 1,
	  "seed": 42,
	  "proxies": [{
	    "name": "api",
	    "listen": "127.0.0.1:18080",
	    "upstream": "http://127.0.0.1:18081",
	    "rules": [{
	      "name": "brownout",
	      "rate": 0.5,
	      "fault": {"status": 503, "body": "injected outage"}
	    }]
	  }]
	}`
}

// mustParse parses or fails the test.
func mustParse(t *testing.T, doc string) *Scenario {
	t.Helper()
	sc, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse failed on a valid scenario: %v", err)
	}
	return sc
}

// errorsOf parses an invalid document and returns the ErrorList.
func errorsOf(t *testing.T, doc string) ErrorList {
	t.Helper()
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("Parse accepted an invalid scenario")
	}
	list, ok := err.(ErrorList)
	if !ok {
		t.Fatalf("expected ErrorList, got %T: %v", err, err)
	}
	return list
}

// hasError asserts that some finding is located at path and mentions frag.
func hasError(t *testing.T, list ErrorList, path, frag string) {
	t.Helper()
	for _, e := range list {
		if e.Path == path && strings.Contains(e.Msg, frag) {
			return
		}
	}
	t.Fatalf("no error at %q mentioning %q in:\n%s", path, frag, list.Error())
}

func TestParseMinimalScenario(t *testing.T) {
	sc := mustParse(t, minimal())
	if sc.Seed != 42 || len(sc.Proxies) != 1 || len(sc.Proxies[0].Rules) != 1 {
		t.Fatalf("parsed scenario has wrong shape: %+v", sc)
	}
	if sc.Proxies[0].Rules[0].EffectiveRate() != 0.5 {
		t.Fatalf("rate not preserved")
	}
	if sc.RuleCount() != 1 {
		t.Fatalf("RuleCount = %d, want 1", sc.RuleCount())
	}
}

func TestDefaultsAndNormalization(t *testing.T) {
	doc := strings.Replace(minimal(), `"rate": 0.5,`,
		`"match": {"methods": ["get", "Post"]},`, 1)
	sc := mustParse(t, doc)
	r := sc.Proxies[0].Rules[0]
	if got := r.EffectiveRate(); got != 1 {
		t.Fatalf("absent rate should default to 1, got %v", got)
	}
	if r.Match.Methods[0] != "GET" || r.Match.Methods[1] != "POST" {
		t.Fatalf("methods not normalized: %v", r.Match.Methods)
	}
	w := r.NormalizedWindow()
	if w.From != 1 || w.To != 0 || w.Every != 1 {
		t.Fatalf("window defaults wrong: %+v", w)
	}
}

// Strictness on input: typoed fields, trailing garbage, and malformed
// JSON must all fail loudly — a fault that silently never fires is the
// worst failure mode a fault injector can have.
func TestStrictParsingRejectsBrokenInput(t *testing.T) {
	typo := strings.Replace(minimal(), `"status": 503`, `"statas": 503`, 1)
	if _, err := Parse([]byte(typo)); err == nil || !strings.Contains(err.Error(), "statas") {
		t.Fatalf("typoed field must be rejected with its name; got %v", err)
	}
	if _, err := Parse([]byte(minimal() + `{"version": 1}`)); err == nil ||
		!strings.Contains(err.Error(), "trailing data") {
		t.Fatalf("trailing data must be rejected")
	}
	if _, err := Parse([]byte(`{"version": 1,`)); err == nil {
		t.Fatal("malformed JSON must be rejected")
	}
}

func TestVersionIsRequiredAndMustBeOne(t *testing.T) {
	list := errorsOf(t, strings.Replace(minimal(), `"version": 1,`, "", 1))
	hasError(t, list, "version", "must be 1")
	list = errorsOf(t, strings.Replace(minimal(), `"version": 1`, `"version": 2`, 1))
	hasError(t, list, "version", "must be 1")
}

func TestAtLeastOneProxyRequired(t *testing.T) {
	list := errorsOf(t, `{"version": 1, "seed": 1, "proxies": []}`)
	hasError(t, list, "proxies", "at least one proxy")
}

func TestProxyNameRules(t *testing.T) {
	dup := `{
	  "version": 1,
	  "proxies": [
	    {"name": "api", "listen": "127.0.0.1:1", "upstream": "http://127.0.0.1:2", "rules": []},
	    {"name": "api", "listen": "127.0.0.1:3", "upstream": "http://127.0.0.1:4", "rules": []}
	  ]
	}`
	hasError(t, errorsOf(t, dup), "proxies[1].name", "duplicate")
	bad := strings.Replace(minimal(), `"name": "api"`, `"name": "API prod"`, 1)
	hasError(t, errorsOf(t, bad), "proxies[0].name", "lowercase")
}

func TestListenValidation(t *testing.T) {
	noPort := strings.Replace(minimal(), `"listen": "127.0.0.1:18080"`, `"listen": "127.0.0.1"`, 1)
	hasError(t, errorsOf(t, noPort), "proxies[0].listen", "host:port")
	noHost := strings.Replace(minimal(), `"listen": "127.0.0.1:18080"`, `"listen": ":18080"`, 1)
	hasError(t, errorsOf(t, noHost), "proxies[0].listen", "explicit host")
}

func TestDuplicateListenAddressRejected(t *testing.T) {
	doc := `{
	  "version": 1,
	  "proxies": [
	    {"name": "a", "listen": "127.0.0.1:9000", "upstream": "http://127.0.0.1:1", "rules": []},
	    {"name": "b", "listen": "127.0.0.1:9000", "upstream": "http://127.0.0.1:2", "rules": []}
	  ]
	}`
	hasError(t, errorsOf(t, doc), "proxies[1].listen", "already used")
}

func TestPortZeroMayRepeat(t *testing.T) {
	// ":0" means "any free port" — two proxies may both ask for one.
	doc := `{
	  "version": 1,
	  "proxies": [
	    {"name": "a", "listen": "127.0.0.1:0", "upstream": "http://127.0.0.1:1", "rules": []},
	    {"name": "b", "listen": "127.0.0.1:0", "upstream": "http://127.0.0.1:2", "rules": []}
	  ]
	}`
	mustParse(t, doc)
}

func TestUpstreamValidation(t *testing.T) {
	ftp := strings.Replace(minimal(),
		`"upstream": "http://127.0.0.1:18081"`, `"upstream": "ftp://127.0.0.1:21"`, 1)
	hasError(t, errorsOf(t, ftp), "proxies[0].upstream", "http or https")
	query := strings.Replace(minimal(),
		`"upstream": "http://127.0.0.1:18081"`, `"upstream": "http://127.0.0.1:18081/?x=1"`, 1)
	hasError(t, errorsOf(t, query), "proxies[0].upstream", "query")
}

func TestDuplicateRuleNamesRejected(t *testing.T) {
	doc := strings.Replace(minimal(),
		`"fault": {"status": 503, "body": "injected outage"}`,
		`"fault": {"status": 503, "body": "injected outage"}},
		 {"name": "brownout", "fault": {"delay_ms": 10}`, 1)
	hasError(t, errorsOf(t, doc), "proxies[0].rules[1].name", "duplicate")
}

func TestRateOutOfRangeRejected(t *testing.T) {
	doc := strings.Replace(minimal(), `"rate": 0.5,`, `"rate": 1.5,`, 1)
	hasError(t, errorsOf(t, doc), "proxies[0].rules[0].rate", "[0, 1]")
}

func TestEmptyFaultRejected(t *testing.T) {
	doc := strings.Replace(minimal(),
		`"fault": {"status": 503, "body": "injected outage"}`, `"fault": {}`, 1)
	hasError(t, errorsOf(t, doc), "proxies[0].rules[0].fault", "at least one of")
}

func TestFaultConflictsRejected(t *testing.T) {
	statusDrop := strings.Replace(minimal(),
		`"fault": {"status": 503, "body": "injected outage"}`,
		`"fault": {"status": 503, "drop": true}`, 1)
	hasError(t, errorsOf(t, statusDrop), "proxies[0].rules[0].fault", "mutually exclusive")
	throttleStatus := strings.Replace(minimal(),
		`"fault": {"status": 503, "body": "injected outage"}`,
		`"fault": {"status": 503, "throttle_bps": 100}`, 1)
	hasError(t, errorsOf(t, throttleStatus), "proxies[0].rules[0].fault", "cannot combine")
}

func TestFaultFieldValidation(t *testing.T) {
	orphanBody := strings.Replace(minimal(),
		`"fault": {"status": 503, "body": "injected outage"}`,
		`"fault": {"delay_ms": 5, "body": "orphan"}`, 1)
	hasError(t, errorsOf(t, orphanBody), "proxies[0].rules[0].fault.body", "requires status")
	badStatus := strings.Replace(minimal(), `"status": 503`, `"status": 99`, 1)
	hasError(t, errorsOf(t, badStatus), "proxies[0].rules[0].fault.status", "100-599")
	negDelay := strings.Replace(minimal(),
		`"fault": {"status": 503, "body": "injected outage"}`,
		`"fault": {"delay_ms": -5}`, 1)
	hasError(t, errorsOf(t, negDelay), "proxies[0].rules[0].fault.delay_ms", ">= 0")
}

func TestWindowToBeforeFromRejected(t *testing.T) {
	doc := strings.Replace(minimal(), `"rate": 0.5,`,
		`"window": {"from": 20, "to": 10},`, 1)
	hasError(t, errorsOf(t, doc), "proxies[0].rules[0].window", "must be >=")
}

func TestMatchBlockValidation(t *testing.T) {
	empty := strings.Replace(minimal(), `"rate": 0.5,`, `"match": {},`, 1)
	hasError(t, errorsOf(t, empty), "proxies[0].rules[0].match", "empty")
	relative := strings.Replace(minimal(), `"rate": 0.5,`,
		`"match": {"path": "users/*"},`, 1)
	hasError(t, errorsOf(t, relative), "proxies[0].rules[0].match.path", "must start")
}

func TestAllErrorsAreCollectedInOnePass(t *testing.T) {
	doc := `{
	  "version": 3,
	  "proxies": [{
	    "name": "",
	    "listen": "nope",
	    "upstream": "ftp://x",
	    "rules": [{"name": "", "rate": 2, "fault": {}}]
	  }]
	}`
	list := errorsOf(t, doc)
	if len(list) < 6 {
		t.Fatalf("expected all findings in one pass, got %d:\n%s", len(list), list.Error())
	}
}

func TestInWindowSemantics(t *testing.T) {
	r := Rule{Window: &Window{From: 11, To: 20, Every: 2}}
	fires := []uint64{11, 13, 15, 17, 19}
	quiet := []uint64{10, 12, 20, 21, 1}
	for _, c := range fires {
		if !r.InWindow(c) {
			t.Errorf("counter %d should be inside from=11 to=20 every=2", c)
		}
	}
	for _, c := range quiet {
		if r.InWindow(c) {
			t.Errorf("counter %d should be outside from=11 to=20 every=2", c)
		}
	}
	var open Rule // no window at all
	for _, c := range []uint64{1, 2, 1000000} {
		if !open.InWindow(c) {
			t.Errorf("absent window must admit counter %d", c)
		}
	}
}

func TestProxyByName(t *testing.T) {
	sc := mustParse(t, minimal())
	if sc.ProxyByName("api") == nil {
		t.Fatal("existing proxy not found")
	}
	if sc.ProxyByName("nope") != nil {
		t.Fatal("missing proxy should return nil")
	}
}

func TestLoadReadsAFileAndReportsMissingOnes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scenario.json")
	if err := os.WriteFile(path, []byte(minimal()), 0o644); err != nil {
		t.Fatal(err)
	}
	sc, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if sc.Proxies[0].Name != "api" {
		t.Fatal("loaded scenario has wrong content")
	}
	if _, err := Load(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Fatal("missing file must be an error")
	}
}
