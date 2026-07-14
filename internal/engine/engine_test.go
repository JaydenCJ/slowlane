// Tests for the decision engine: composition laws (delays accumulate,
// first terminal wins, first throttle wins), selector gating, and the
// determinism guarantees the README stakes its name on.
package engine

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/JaydenCJ/slowlane/internal/scenario"
)

// px builds a proxy named "api" with the given rules.
func px(rules ...scenario.Rule) *scenario.Proxy {
	return &scenario.Proxy{
		Name:     "api",
		Listen:   "127.0.0.1:0",
		Upstream: "http://127.0.0.1:1",
		Rules:    rules,
	}
}

// sc wraps a proxy in a scenario with the given seed.
func sc(seed uint64, p *scenario.Proxy) *scenario.Scenario {
	return &scenario.Scenario{Version: 1, Seed: seed, Proxies: []scenario.Proxy{*p}}
}

func rate(v float64) *float64 { return &v }

var get = Request{Method: "GET", Path: "/", Header: http.Header{}}

func TestNoRulesMeansPass(t *testing.T) {
	p := px()
	d := Decide(sc(1, p), p, 1, get)
	if !d.Pass() {
		t.Fatalf("no rules must pass through, got %+v", d)
	}
}

func TestDelaysApplyAndAccumulateAcrossRules(t *testing.T) {
	p := px(
		scenario.Rule{Name: "hop-a", Fault: scenario.Fault{DelayMS: 100}},
		scenario.Rule{Name: "hop-b", Fault: scenario.Fault{DelayMS: 30}},
	)
	d := Decide(sc(1, p), p, 1, get)
	if d.Delay != 130*time.Millisecond {
		t.Fatalf("delays must accumulate: got %v, want 130ms", d.Delay)
	}
	if !reflect.DeepEqual(d.DelayRules, []string{"hop-a", "hop-b"}) {
		t.Fatalf("delay attribution wrong: %v", d.DelayRules)
	}
}

// Jitter draws are pinned by the seeded package's golden tests; here we
// assert the engine wires them in: delay = fixed + DrawInt(…, jitter).
func TestJitterAddsPinnedDeterministicExtra(t *testing.T) {
	p := px(scenario.Rule{Name: "slow", Fault: scenario.Fault{DelayMS: 100, JitterMS: 100}})
	s := sc(42, p)
	want := []time.Duration{ // 100ms + {64,79,43}ms for counters 1..3
		164 * time.Millisecond, 179 * time.Millisecond, 143 * time.Millisecond,
	}
	for i, w := range want {
		if d := Decide(s, p, uint64(i+1), get); d.Delay != w {
			t.Errorf("counter %d: delay = %v, want %v", i+1, d.Delay, w)
		}
	}
	// A jitter-only rule must still delay, bounded by the jitter range.
	jp := px(scenario.Rule{Name: "wobble", Fault: scenario.Fault{JitterMS: 50}})
	js := sc(42, jp)
	sawNonZero := false
	for c := uint64(1); c <= 20; c++ {
		d := Decide(js, jp, c, get)
		if d.Delay < 0 || d.Delay > 50*time.Millisecond {
			t.Fatalf("counter %d: jitter-only delay out of range: %v", c, d.Delay)
		}
		if d.Delay > 0 {
			sawNonZero = true
		}
	}
	if !sawNonZero {
		t.Fatal("jitter never produced a delay in 20 requests")
	}
}

func TestTerminalFaultsStopEvaluation(t *testing.T) {
	// A status terminal keeps earlier delays but hides later rules.
	p := px(
		scenario.Rule{Name: "slow", Fault: scenario.Fault{DelayMS: 200}},
		scenario.Rule{Name: "outage", Fault: scenario.Fault{Status: 503, Body: "down"}},
		scenario.Rule{Name: "late-delay", Fault: scenario.Fault{DelayMS: 500}},
		scenario.Rule{Name: "late-drop", Fault: scenario.Fault{Drop: true}},
	)
	d := Decide(sc(1, p), p, 1, get)
	if d.Status != 503 || d.StatusRule != "outage" || d.Body != "down" {
		t.Fatalf("terminal status not applied: %+v", d)
	}
	if d.Delay != 200*time.Millisecond {
		t.Fatalf("delay before the terminal must still apply: %v", d.Delay)
	}
	if d.Drop || !d.Terminal() {
		t.Fatalf("rules after a terminal must not fire: %+v", d)
	}

	// Drop is equally terminal.
	dp := px(
		scenario.Rule{Name: "cut", Fault: scenario.Fault{Drop: true}},
		scenario.Rule{Name: "after", Fault: scenario.Fault{Status: 503}},
	)
	dd := Decide(sc(1, dp), dp, 1, get)
	if !dd.Drop || dd.DropRule != "cut" || dd.Status != 0 {
		t.Fatalf("drop terminal wrong: %+v", dd)
	}
}

func TestFirstThrottleWins(t *testing.T) {
	p := px(
		scenario.Rule{Name: "narrow", Fault: scenario.Fault{ThrottleBPS: 512}},
		scenario.Rule{Name: "wide", Fault: scenario.Fault{ThrottleBPS: 4096}},
	)
	d := Decide(sc(1, p), p, 1, get)
	if d.ThrottleBPS != 512 || d.ThrottleRule != "narrow" {
		t.Fatalf("first throttle must win: %+v", d)
	}
}

func TestRateExtremes(t *testing.T) {
	never := px(scenario.Rule{Name: "never", Rate: rate(0), Fault: scenario.Fault{Status: 503}})
	always := px(scenario.Rule{Name: "always", Rate: rate(1), Fault: scenario.Fault{Status: 503}})
	for c := uint64(1); c <= 200; c++ {
		if !Decide(sc(42, never), never, c, get).Pass() {
			t.Fatalf("rate 0 fired at counter %d", c)
		}
		if Decide(sc(42, always), always, c, get).Status != 503 {
			t.Fatalf("rate 1 skipped counter %d", c)
		}
	}
}

// Pinned by the seeded golden values: with seed 42, proxy "api", rule
// "brownout", counters 1..3 draw {0.77, 0.37, 0.84}, so at rate 0.5 only
// counter 2 fires.
func TestRateHalfFiresExactlyThePinnedCounters(t *testing.T) {
	p := px(scenario.Rule{Name: "brownout", Rate: rate(0.5), Fault: scenario.Fault{Status: 503}})
	s := sc(42, p)
	want := map[uint64]bool{1: false, 2: true, 3: false}
	for c, fires := range want {
		got := Decide(s, p, c, get).Status == 503
		if got != fires {
			t.Errorf("counter %d: fired=%v, want %v", c, got, fires)
		}
	}
}

func TestDecisionsAreIndependentOfOtherRules(t *testing.T) {
	// Adding an unrelated rule must not change an existing rule's rate
	// decisions — draws are keyed by rule name, not evaluation position.
	alone := px(scenario.Rule{Name: "brownout", Rate: rate(0.5), Fault: scenario.Fault{Status: 503}})
	crowded := px(
		scenario.Rule{Name: "other", Rate: rate(0), Fault: scenario.Fault{DelayMS: 5}},
		scenario.Rule{Name: "brownout", Rate: rate(0.5), Fault: scenario.Fault{Status: 503}},
	)
	for c := uint64(1); c <= 100; c++ {
		a := Decide(sc(42, alone), alone, c, get).Status
		b := Decide(sc(42, crowded), crowded, c, get).Status
		if a != b {
			t.Fatalf("counter %d: rule decision changed when a neighbor rule was added", c)
		}
	}
}

func TestWindowGatesTheRule(t *testing.T) {
	p := px(scenario.Rule{
		Name:   "phase-two",
		Window: &scenario.Window{From: 3, To: 4},
		Fault:  scenario.Fault{Status: 503},
	})
	s := sc(1, p)
	want := map[uint64]bool{1: false, 2: false, 3: true, 4: true, 5: false}
	for c, fires := range want {
		if got := Decide(s, p, c, get).Status == 503; got != fires {
			t.Errorf("counter %d: fired=%v, want %v", c, got, fires)
		}
	}
}

func TestMatchGatesTheRule(t *testing.T) {
	p := px(scenario.Rule{
		Name:  "users-only",
		Match: &scenario.Match{Methods: []string{"GET"}, Path: "/users/*"},
		Fault: scenario.Fault{Status: 500},
	})
	s := sc(1, p)
	hit := Request{Method: "GET", Path: "/users/7", Header: http.Header{}}
	missPath := Request{Method: "GET", Path: "/orders/7", Header: http.Header{}}
	missMethod := Request{Method: "POST", Path: "/users/7", Header: http.Header{}}
	if Decide(s, p, 1, hit).Status != 500 {
		t.Fatal("matching request must fire the rule")
	}
	if !Decide(s, p, 2, missPath).Pass() || !Decide(s, p, 3, missMethod).Pass() {
		t.Fatal("non-matching requests must pass")
	}

	hp := px(scenario.Rule{
		Name:  "canary-only",
		Match: &scenario.Match{Headers: map[string]string{"X-Canary": "1"}},
		Fault: scenario.Fault{Status: 503},
	})
	hs := sc(1, hp)
	h := http.Header{}
	h.Set("X-Canary", "1")
	if Decide(hs, hp, 1, Request{Method: "GET", Path: "/", Header: h}).Status != 503 {
		t.Fatal("header-matching request must fire")
	}
	if !Decide(hs, hp, 2, get).Pass() {
		t.Fatal("request without the header must pass")
	}
}

func TestPlanEqualsLiveDecisions(t *testing.T) {
	p := px(
		scenario.Rule{Name: "slow", Rate: rate(0.4), Fault: scenario.Fault{DelayMS: 100, JitterMS: 50}},
		scenario.Rule{Name: "outage", Rate: rate(0.2), Fault: scenario.Fault{Status: 503}},
	)
	s := sc(7, p)
	entries := Plan(s, p, get, 1, 50)
	if len(entries) != 50 {
		t.Fatalf("plan length = %d, want 50", len(entries))
	}
	for _, e := range entries {
		live := Decide(s, p, e.Counter, get)
		if !reflect.DeepEqual(e.Decision, live) {
			t.Fatalf("counter %d: plan %+v differs from live %+v", e.Counter, e.Decision, live)
		}
	}
}

func TestPlanHonorsFromOffset(t *testing.T) {
	p := px(scenario.Rule{
		Name:   "late",
		Window: &scenario.Window{From: 100},
		Fault:  scenario.Fault{Status: 503},
	})
	s := sc(1, p)
	entries := Plan(s, p, get, 99, 3)
	if entries[0].Decision.Status != 0 || entries[1].Decision.Status != 503 || entries[2].Decision.Status != 503 {
		t.Fatalf("plan --from offset wrong: %+v", entries)
	}
}

func TestSummaryPhrases(t *testing.T) {
	cases := []struct {
		d    Decision
		want string
	}{
		{Decision{}, "pass"},
		{Decision{Delay: 273 * time.Millisecond, DelayRules: []string{"slow"}}, "delay 273ms (slow)"},
		{Decision{Status: 503, StatusRule: "outage"}, "503 (outage)"},
		{Decision{Drop: true, DropRule: "cut"}, "drop (cut)"},
		{Decision{ThrottleBPS: 1024, ThrottleRule: "dialup"}, "throttle 1024B/s (dialup)"},
		{
			Decision{Delay: 100 * time.Millisecond, DelayRules: []string{"a", "b"}, Status: 500, StatusRule: "c"},
			"delay 100ms (a, b) + 500 (c)",
		},
	}
	for _, c := range cases {
		if got := (PlanEntry{Counter: 1, Decision: c.d}).Summary(); got != c.want {
			t.Errorf("Summary() = %q, want %q", got, c.want)
		}
	}
}

func TestSummarizeCounts(t *testing.T) {
	entries := []PlanEntry{
		{Counter: 1, Decision: Decision{}},
		{Counter: 2, Decision: Decision{Delay: 100 * time.Millisecond, DelayRules: []string{"a"}}},
		{Counter: 3, Decision: Decision{Status: 503}},
		{Counter: 4, Decision: Decision{Drop: true}},
		{Counter: 5, Decision: Decision{ThrottleBPS: 100}},
		{Counter: 6, Decision: Decision{Delay: 50 * time.Millisecond, Status: 500}},
	}
	s := Summarize(entries)
	if s.Requests != 6 || s.Passed != 1 || s.Delayed != 2 || s.Injected != 2 ||
		s.Dropped != 1 || s.Throttled != 1 || s.TotalDelay != 150*time.Millisecond {
		t.Fatalf("summarize wrong: %+v", s)
	}
}

func TestChangingSeedChangesTheSchedule(t *testing.T) {
	p := px(scenario.Rule{Name: "flaky", Rate: rate(0.5), Fault: scenario.Fault{Status: 503}})
	pattern := func(seed uint64) []bool {
		s := sc(seed, p)
		var out []bool
		for c := uint64(1); c <= 64; c++ {
			out = append(out, Decide(s, p, c, get).Status != 0)
		}
		return out
	}
	if reflect.DeepEqual(pattern(1), pattern(2)) {
		t.Fatal("different seeds should produce different fault schedules")
	}
}
