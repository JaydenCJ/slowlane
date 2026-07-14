// Tests for the data plane. Everything runs against in-process handlers or
// loopback httptest servers; injected waits go through a recording fake
// Sleeper, so no test ever sleeps on the wall clock.
package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JaydenCJ/slowlane/internal/scenario"
)

// fakeSleeper records requested sleep durations instead of sleeping.
type fakeSleeper struct {
	mu     sync.Mutex
	sleeps []time.Duration
}

func (f *fakeSleeper) Sleep(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sleeps = append(f.sleeps, d)
}

func (f *fakeSleeper) total() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	var t time.Duration
	for _, d := range f.sleeps {
		t += d
	}
	return t
}

func (f *fakeSleeper) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sleeps)
}

func rate(v float64) *float64 { return &v }

// newServer wires a Server for one proxy with a fake sleeper, pointing at
// upstream (a URL, possibly unreachable).
func newServer(t *testing.T, upstream string, seed uint64, rules ...scenario.Rule) (*Server, *fakeSleeper) {
	t.Helper()
	sc := &scenario.Scenario{
		Version: 1,
		Seed:    seed,
		Proxies: []scenario.Proxy{{
			Name: "api", Listen: "127.0.0.1:0", Upstream: upstream, Rules: rules,
		}},
	}
	srv, err := New(sc, &sc.Proxies[0])
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	fs := &fakeSleeper{}
	srv.Sleeper = fs
	return srv, fs
}

// upstreamServer starts a loopback origin that replies 200 "hello from
// upstream" and records the last request it saw.
func upstreamServer(t *testing.T) (*httptest.Server, *http.Request) {
	t.Helper()
	var last http.Request
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last = *r.Clone(r.Context())
		w.Header().Set("X-Origin", "yes")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "hello from upstream")
	}))
	t.Cleanup(ts.Close)
	return ts, &last
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

func TestForwardPassesThrough(t *testing.T) {
	ts, _ := upstreamServer(t)
	srv, _ := newServer(t, ts.URL, 1)
	rec := get(t, srv, "/hello")
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "hello from upstream" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if rec.Header().Get("X-Origin") != "yes" {
		t.Fatal("upstream response headers must be relayed")
	}
}

func TestForwardPreservesPathQueryAndJoinsBasePaths(t *testing.T) {
	ts, last := upstreamServer(t)
	srv, _ := newServer(t, ts.URL, 1)
	get(t, srv, "/users/7?page=2")
	if last.URL.Path != "/users/7" || last.URL.RawQuery != "page=2" {
		t.Fatalf("upstream saw %q ? %q", last.URL.Path, last.URL.RawQuery)
	}
	based, _ := newServer(t, ts.URL+"/base/", 1)
	get(t, based, "/leaf")
	if last.URL.Path != "/base/leaf" {
		t.Fatalf("joined path = %q, want /base/leaf", last.URL.Path)
	}
}

func TestRequestCounterHeaderIncrements(t *testing.T) {
	ts, _ := upstreamServer(t)
	srv, _ := newServer(t, ts.URL, 1)
	first := get(t, srv, "/")
	second := get(t, srv, "/")
	if first.Header().Get("X-Slowlane-Request") != "1" ||
		second.Header().Get("X-Slowlane-Request") != "2" {
		t.Fatalf("counter headers: %q then %q",
			first.Header().Get("X-Slowlane-Request"),
			second.Header().Get("X-Slowlane-Request"))
	}
	if srv.Counter() != 2 {
		t.Fatalf("Counter() = %d, want 2", srv.Counter())
	}
}

func TestInjectedStatusShortCircuitsUpstream(t *testing.T) {
	hits := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	t.Cleanup(ts.Close)
	srv, _ := newServer(t, ts.URL, 1,
		scenario.Rule{Name: "outage", Fault: scenario.Fault{Status: 503, Body: "down for maintenance"}})
	rec := get(t, srv, "/")
	if rec.Code != 503 {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if hits != 0 {
		t.Fatal("upstream must not be contacted for injected responses")
	}
	if rec.Header().Get("X-Slowlane-Injected") != "outage" {
		t.Fatal("injected responses must name the rule in X-Slowlane-Injected")
	}
	if rec.Body.String() != "down for maintenance\n" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	// Without a configured body, the injected response describes itself.
	bare, _ := newServer(t, ts.URL, 1,
		scenario.Rule{Name: "outage", Fault: scenario.Fault{Status: 500}})
	body := get(t, bare, "/").Body.String()
	if !strings.Contains(body, "injected 500") || !strings.Contains(body, `"outage"`) {
		t.Fatalf("default body should be self-describing, got %q", body)
	}
}

func TestDelayGoesThroughSleeper(t *testing.T) {
	ts, _ := upstreamServer(t)
	srv, fs := newServer(t, ts.URL, 1,
		scenario.Rule{Name: "slow", Fault: scenario.Fault{DelayMS: 250}})
	rec := get(t, srv, "/")
	if fs.total() != 250*time.Millisecond {
		t.Fatalf("slept %v, want 250ms", fs.total())
	}
	if rec.Header().Get("X-Slowlane-Delay") != "250ms" {
		t.Fatalf("X-Slowlane-Delay = %q", rec.Header().Get("X-Slowlane-Delay"))
	}
	if rec.Code != 200 {
		t.Fatal("delayed request must still reach the upstream")
	}
}

func TestDropClosesConnectionWithoutResponse(t *testing.T) {
	// Drop needs a hijackable connection, so this test uses a real
	// loopback server around the proxy handler.
	origin, _ := upstreamServer(t)
	srv, _ := newServer(t, origin.URL, 1,
		scenario.Rule{Name: "cut", Fault: scenario.Fault{Drop: true}})
	front := httptest.NewServer(srv)
	t.Cleanup(front.Close)

	_, err := http.Get(front.URL + "/")
	if err == nil {
		t.Fatal("dropped connection must surface as a client error")
	}
	if !strings.Contains(err.Error(), "EOF") && !strings.Contains(err.Error(), "reset") {
		t.Fatalf("unexpected error kind: %v", err)
	}
}

func TestUpstreamUnreachableYields502(t *testing.T) {
	// Port 1 on loopback is essentially never listening; the dial fails
	// fast and deterministically.
	srv, _ := newServer(t, "http://127.0.0.1:1", 1)
	rec := get(t, srv, "/")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if rec.Header().Get("X-Slowlane-Upstream-Error") != "1" {
		t.Fatal("502 must be marked as slowlane-generated")
	}
}

func TestHopByHopRequestHeadersAreStripped(t *testing.T) {
	ts, last := upstreamServer(t)
	srv, _ := newServer(t, ts.URL, 1)
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Keep-Alive", "timeout=5")
	req.Header.Set("Proxy-Connection", "keep-alive")
	req.Header.Set("X-Keep-Me", "yes")
	srv.ServeHTTP(httptest.NewRecorder(), req)
	if last.Header.Get("Keep-Alive") != "" || last.Header.Get("Proxy-Connection") != "" {
		t.Fatal("hop-by-hop request headers must not be relayed")
	}
	if last.Header.Get("X-Keep-Me") != "yes" {
		t.Fatal("end-to-end headers must be relayed")
	}
	// Headers named by a Connection header are hop-by-hop as well.
	named := httptest.NewRequest("GET", "/", nil)
	named.Header.Set("Connection", "X-Per-Hop")
	named.Header.Set("X-Per-Hop", "secret")
	srv.ServeHTTP(httptest.NewRecorder(), named)
	if last.Header.Get("X-Per-Hop") != "" {
		t.Fatal("headers named by Connection are hop-by-hop and must be stripped")
	}
}

func TestXForwardedForIsSetAndAppended(t *testing.T) {
	ts, last := upstreamServer(t)
	srv, _ := newServer(t, ts.URL, 1)

	// httptest requests carry RemoteAddr 192.0.2.1:1234 (TEST-NET-1).
	srv.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	if got := last.Header.Get("X-Forwarded-For"); got != "192.0.2.1" {
		t.Fatalf("X-Forwarded-For = %q, want 192.0.2.1", got)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Forwarded-For", "198.51.100.9")
	srv.ServeHTTP(httptest.NewRecorder(), req)
	if got := last.Header.Get("X-Forwarded-For"); got != "198.51.100.9, 192.0.2.1" {
		t.Fatalf("X-Forwarded-For append = %q", got)
	}
}

func TestThrottleMarksResponseAndPacesCopy(t *testing.T) {
	big := strings.Repeat("x", 1000)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, big)
	}))
	t.Cleanup(ts.Close)
	srv, fs := newServer(t, ts.URL, 1,
		scenario.Rule{Name: "dialup", Fault: scenario.Fault{ThrottleBPS: 1000}})
	rec := get(t, srv, "/")
	if rec.Body.Len() != 1000 {
		t.Fatalf("throttled body truncated: %d bytes", rec.Body.Len())
	}
	if rec.Header().Get("X-Slowlane-Throttled") != "dialup" {
		t.Fatal("throttled responses must name the rule")
	}
	// 1000 bytes at 1000 B/s in 100-byte ticks = 10 full chunks → 10 ticks.
	if fs.count() != 10 || fs.total() != time.Second {
		t.Fatalf("throttle paced %d sleeps totalling %v, want 10 × 100ms", fs.count(), fs.total())
	}
}

func TestThrottledWriterChunkMath(t *testing.T) {
	var out strings.Builder
	fs := &fakeSleeper{}
	w := &throttledWriter{w: &out, bps: 40, sleeper: fs} // 4-byte chunks
	n, err := w.Write([]byte("abcdefghij"))              // 10 bytes → 2 full chunks + 2 rest
	if err != nil || n != 10 {
		t.Fatalf("write = %d, %v", n, err)
	}
	if out.String() != "abcdefghij" {
		t.Fatalf("bytes mangled: %q", out.String())
	}
	if fs.count() != 2 {
		t.Fatalf("sleeps = %d, want 2 (only after full chunks)", fs.count())
	}
	// Very low budgets floor at 1-byte chunks instead of stalling.
	var tiny strings.Builder
	tfs := &fakeSleeper{}
	tw := &throttledWriter{w: &tiny, bps: 3, sleeper: tfs} // bps/10 < 1 → 1-byte chunks
	tw.Write([]byte("abc"))
	if tiny.String() != "abc" || tfs.count() != 3 {
		t.Fatalf("got %q with %d sleeps, want abc with 3", tiny.String(), tfs.count())
	}
}

func TestEventsCarryTheDecision(t *testing.T) {
	ts, _ := upstreamServer(t)
	srv, _ := newServer(t, ts.URL, 42,
		scenario.Rule{Name: "outage", Window: &scenario.Window{From: 2}, Fault: scenario.Fault{Status: 503}})
	var events []Event
	srv.OnEvent = func(ev Event) { events = append(events, ev) }
	get(t, srv, "/a")
	get(t, srv, "/b")
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	if events[0].Action != "pass" || events[0].Status != 200 || events[0].Counter != 1 {
		t.Fatalf("first event wrong: %+v", events[0])
	}
	if events[1].Action != "503 (outage)" || !events[1].Injected || events[1].Path != "/b" {
		t.Fatalf("second event wrong: %+v", events[1])
	}
}

func TestSingleJoin(t *testing.T) {
	cases := []struct{ base, path, want string }{
		{"", "/x", "/x"},
		{"/", "/x", "/x"},
		{"/base", "/x", "/base/x"},
		{"/base/", "/x", "/base/x"},
		{"/base", "x", "/base/x"},
	}
	for _, c := range cases {
		if got := singleJoin(c.base, c.path); got != c.want {
			t.Errorf("singleJoin(%q, %q) = %q, want %q", c.base, c.path, got, c.want)
		}
	}
}

func TestEchoHandlerDescribesTheRequest(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/things?limit=5", strings.NewReader("payload"))
	req.Header.Set("X-Tenant", "acme")
	EchoHandler().ServeHTTP(rec, req)

	if rec.Code != 200 || rec.Header().Get("X-Slowlane-Echo") != "1" {
		t.Fatalf("echo status/header wrong: %d", rec.Code)
	}
	var reply struct {
		Method   string            `json:"method"`
		Path     string            `json:"path"`
		Query    string            `json:"query"`
		Headers  map[string]string `json:"headers"`
		BodySize int64             `json:"body_size"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatalf("echo body is not JSON: %v", err)
	}
	if reply.Method != "POST" || reply.Path != "/things" || reply.Query != "limit=5" {
		t.Fatalf("echo reply wrong: %+v", reply)
	}
	if reply.Headers["X-Tenant"] != "acme" || reply.BodySize != 7 {
		t.Fatalf("echo headers/body wrong: %+v", reply)
	}
}

func TestEchoBodyIsByteStableForSameRequest(t *testing.T) {
	shoot := func() string {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/stable", nil)
		req.Header.Set("B-Header", "2")
		req.Header.Set("A-Header", "1")
		EchoHandler().ServeHTTP(rec, req)
		return rec.Body.String()
	}
	if shoot() != shoot() {
		t.Fatal("echo output must be byte-stable for identical requests")
	}
}

func TestSeededFaultsBehaveIdenticallyAcrossRestarts(t *testing.T) {
	// The flagship promise: rebuild the server (fresh counters), replay
	// the same request sequence, observe the same injected statuses.
	ts, _ := upstreamServer(t)
	replay := func() []int {
		srv, _ := newServer(t, ts.URL, 42,
			scenario.Rule{Name: "brownout", Rate: rate(0.5), Fault: scenario.Fault{Status: 503}})
		var statuses []int
		for i := 0; i < 30; i++ {
			statuses = append(statuses, get(t, srv, "/").Code)
		}
		return statuses
	}
	first, second := replay(), replay()
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("request %d differed across restarts: %d vs %d", i+1, first[i], second[i])
		}
	}
	saw503 := false
	for _, s := range first {
		if s == 503 {
			saw503 = true
		}
	}
	if !saw503 {
		t.Fatal("a 0.5-rate rule should inject at least once in 30 requests")
	}
}
