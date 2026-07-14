// Package proxy is slowlane's data plane: an HTTP reverse proxy that asks
// the engine for a Decision on every request and acts it out — sleeping,
// short-circuiting with an injected status, dropping the connection, or
// throttling the upstream body copy.
//
// All waiting goes through the Sleeper interface so tests can observe
// intended delays without any wall-clock time passing.
package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/JaydenCJ/slowlane/internal/engine"
	"github.com/JaydenCJ/slowlane/internal/scenario"
	"github.com/JaydenCJ/slowlane/internal/version"
)

// Sleeper abstracts blocking waits. The live proxy uses RealSleeper; tests
// substitute a recorder.
type Sleeper interface {
	Sleep(d time.Duration)
}

// RealSleeper sleeps on the wall clock.
type RealSleeper struct{}

// Sleep implements Sleeper.
func (RealSleeper) Sleep(d time.Duration) { time.Sleep(d) }

// Event describes one handled request, for logging.
type Event struct {
	Proxy    string        `json:"proxy"`
	Counter  uint64        `json:"n"`
	Method   string        `json:"method"`
	Path     string        `json:"path"`
	Action   string        `json:"action"` // the PlanEntry.Summary phrase
	Delay    time.Duration `json:"-"`
	DelayMS  int64         `json:"delay_ms,omitempty"`
	Status   int           `json:"status"` // status returned to the client (0 for drop)
	Injected bool          `json:"injected"`
	Dropped  bool          `json:"dropped"`
	// UpstreamErr is set when the upstream could not be reached; the
	// client saw a 502 that slowlane itself generated.
	UpstreamErr string `json:"upstream_error,omitempty"`
}

// Server proxies one scenario proxy entry. It implements http.Handler.
type Server struct {
	sc *scenario.Scenario
	px *scenario.Proxy

	upstream *url.URL
	counter  atomic.Uint64

	// Transport performs upstream round trips; replaceable in tests.
	Transport http.RoundTripper
	// Sleeper performs injected waits; replaceable in tests.
	Sleeper Sleeper
	// OnEvent, when set, receives one Event per handled request.
	OnEvent func(Event)
}

// New builds a Server for one proxy of a validated scenario.
func New(sc *scenario.Scenario, px *scenario.Proxy) (*Server, error) {
	u, err := url.Parse(px.Upstream)
	if err != nil {
		return nil, fmt.Errorf("proxy %q: upstream: %w", px.Name, err)
	}
	return &Server{
		sc:        sc,
		px:        px,
		upstream:  u,
		Transport: http.DefaultTransport,
		Sleeper:   RealSleeper{},
	}, nil
}

// Counter returns the number of requests handled so far.
func (s *Server) Counter() uint64 { return s.counter.Load() }

// ServeHTTP implements the full fault pipeline for one request.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := s.counter.Add(1)
	dec := engine.Decide(s.sc, s.px, n, engine.Request{
		Method: r.Method,
		Path:   r.URL.Path,
		Header: r.Header,
	})
	ev := Event{
		Proxy:   s.px.Name,
		Counter: n,
		Method:  r.Method,
		Path:    r.URL.Path,
		Action:  engine.PlanEntry{Counter: n, Decision: dec}.Summary(),
		Delay:   dec.Delay,
		DelayMS: dec.Delay.Milliseconds(),
	}

	if dec.Delay > 0 {
		s.Sleeper.Sleep(dec.Delay)
	}

	switch {
	case dec.Drop:
		ev.Dropped = true
		s.drop(w)
	case dec.Status != 0:
		ev.Status = dec.Status
		ev.Injected = true
		s.inject(w, n, dec)
	default:
		ev.Status, ev.UpstreamErr = s.forward(w, r, n, dec)
	}

	if s.OnEvent != nil {
		s.OnEvent(ev)
	}
}

// drop severs the client connection without writing a response, which is
// what a partitioned or crashed upstream looks like from the client side.
func (s *Server) drop(w http.ResponseWriter) {
	if hj, ok := w.(http.Hijacker); ok {
		conn, _, err := hj.Hijack()
		if err == nil {
			conn.Close()
			return
		}
	}
	// No hijack available (e.g. HTTP/2): abort the handler so the server
	// resets the stream instead of sending a well-formed response.
	panic(http.ErrAbortHandler)
}

// inject writes a fault response without contacting the upstream. The
// X-Slowlane-* headers make injected responses self-describing, so test
// assertions never have to guess whether a 503 came from slowlane or from
// a genuinely broken upstream.
func (s *Server) inject(w http.ResponseWriter, n uint64, dec engine.Decision) {
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("X-Slowlane-Injected", dec.StatusRule)
	h.Set("X-Slowlane-Request", strconv.FormatUint(n, 10))
	setDelayHeader(h, dec)
	body := dec.Body
	if body == "" {
		body = fmt.Sprintf("slowlane %s: injected %d by rule %q\n",
			version.Version, dec.Status, dec.StatusRule)
	} else if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(dec.Status)
	io.WriteString(w, body)
}

// forward relays the request upstream and the response back, applying the
// throttle if the decision carries one. It returns the status sent to the
// client and any upstream error string.
func (s *Server) forward(w http.ResponseWriter, r *http.Request, n uint64, dec engine.Decision) (int, string) {
	out := r.Clone(r.Context())
	out.URL.Scheme = s.upstream.Scheme
	out.URL.Host = s.upstream.Host
	out.URL.Path = singleJoin(s.upstream.Path, r.URL.Path)
	out.Host = s.upstream.Host
	out.RequestURI = "" // client requests must not set this
	stripHopByHop(out.Header)
	appendForwardedFor(out, r)

	resp, err := s.Transport.RoundTrip(out)
	if err != nil {
		h := w.Header()
		h.Set("Content-Type", "text/plain; charset=utf-8")
		h.Set("X-Slowlane-Upstream-Error", "1")
		h.Set("X-Slowlane-Request", strconv.FormatUint(n, 10))
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, "slowlane: upstream %s unreachable: %v\n", s.px.Upstream, err)
		return http.StatusBadGateway, err.Error()
	}
	defer resp.Body.Close()

	h := w.Header()
	copyHeader(h, resp.Header)
	stripHopByHop(h)
	h.Set("X-Slowlane-Request", strconv.FormatUint(n, 10))
	setDelayHeader(h, dec)
	if dec.ThrottleBPS > 0 {
		h.Set("X-Slowlane-Throttled", dec.ThrottleRule)
	}
	w.WriteHeader(resp.StatusCode)

	var dst io.Writer = w
	if dec.ThrottleBPS > 0 {
		dst = &throttledWriter{w: w, bps: dec.ThrottleBPS, sleeper: s.Sleeper}
	}
	io.Copy(dst, resp.Body) // best effort; the client may hang up mid-body
	return resp.StatusCode, ""
}

// setDelayHeader annotates delayed responses so clients can tell injected
// latency from genuine upstream latency.
func setDelayHeader(h http.Header, dec engine.Decision) {
	if dec.Delay > 0 {
		h.Set("X-Slowlane-Delay", fmt.Sprintf("%dms", dec.Delay.Milliseconds()))
	}
}

// hopByHop lists headers that are connection-scoped per RFC 9110 §7.6.1
// and must not be relayed by a proxy.
var hopByHop = []string{
	"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

func stripHopByHop(h http.Header) {
	// Headers named by a Connection header are also hop-by-hop.
	for _, v := range h.Values("Connection") {
		for _, name := range strings.Split(v, ",") {
			if name = strings.TrimSpace(name); name != "" {
				h.Del(name)
			}
		}
	}
	for _, name := range hopByHop {
		h.Del(name)
	}
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

// appendForwardedFor records the client address the way every reverse
// proxy does, so upstream logs stay useful behind slowlane.
func appendForwardedFor(out *http.Request, in *http.Request) {
	addr := in.RemoteAddr
	if i := strings.LastIndex(addr, ":"); i > 0 {
		addr = addr[:i]
	}
	if addr == "" {
		return
	}
	if prior := in.Header.Get("X-Forwarded-For"); prior != "" {
		addr = prior + ", " + addr
	}
	out.Header.Set("X-Forwarded-For", addr)
}

// singleJoin joins an upstream base path and a request path with exactly
// one slash between them.
func singleJoin(base, path string) string {
	switch {
	case base == "" || base == "/":
		return path
	case strings.HasSuffix(base, "/") && strings.HasPrefix(path, "/"):
		return base + path[1:]
	case !strings.HasSuffix(base, "/") && !strings.HasPrefix(path, "/"):
		return base + "/" + path
	default:
		return base + path
	}
}

// throttledWriter caps the copy rate at bps by writing slices and sleeping
// between them. It writes in tick-sized chunks (1/10 s of budget, min 1
// byte) and sleeps one tick after every full chunk, so a body of B bytes
// takes ~B/bps seconds regardless of how the source delivers it.
type throttledWriter struct {
	w       io.Writer
	bps     int64
	sleeper Sleeper
}

// tick is the throttle granularity.
const tick = 100 * time.Millisecond

func (t *throttledWriter) Write(p []byte) (int, error) {
	chunk := t.bps / 10
	if chunk < 1 {
		chunk = 1
	}
	written := 0
	for len(p) > 0 {
		n := int64(len(p))
		if n > chunk {
			n = chunk
		}
		w, err := t.w.Write(p[:n])
		written += w
		if err != nil {
			return written, err
		}
		if f, ok := t.w.(http.Flusher); ok {
			f.Flush()
		}
		p = p[n:]
		if int64(w) == chunk { // sleep only after a full chunk of budget
			t.sleeper.Sleep(tick)
		}
	}
	return written, nil
}
