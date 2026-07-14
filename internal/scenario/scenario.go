// Package scenario defines the slowlane scenario file format: parsing,
// strict validation, and the defaulting rules. The full format reference
// lives in docs/scenario-format.md; this package is its executable form.
//
// Design constraints: unknown fields are rejected (a typoed "jitters_ms"
// must fail `slowlane check`, not silently inject nothing), every
// validation error carries a JSON-path-like location, and all errors are
// collected in one pass so CI users fix a file in one round trip.
package scenario

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"sort"
	"strings"
)

// Scenario is the root of a scenario file.
type Scenario struct {
	// Version is the file format version. The only accepted value is 1.
	Version int `json:"version"`
	// Seed keys every probabilistic decision. Two runs of the same
	// scenario against the same request sequence behave identically;
	// change the seed to explore a different deterministic fault pattern.
	Seed uint64 `json:"seed"`
	// Proxies lists the listeners slowlane opens. At least one.
	Proxies []Proxy `json:"proxies"`
}

// Proxy is one listener forwarding to one upstream.
type Proxy struct {
	// Name identifies the proxy in logs, plan output, and hash
	// coordinates. Unique within the file.
	Name string `json:"name"`
	// Listen is the host:port to bind, e.g. "127.0.0.1:18080". Port 0
	// asks the OS for a free port (printed at startup).
	Listen string `json:"listen"`
	// Upstream is the http:// or https:// origin requests are forwarded to.
	Upstream string `json:"upstream"`
	// Rules are evaluated top to bottom for every request; see Rule.
	Rules []Rule `json:"rules"`
}

// Rule binds selectors (match, window, rate) to a fault. For each request,
// every rule is tried in order: delays from matching rules accumulate, the
// first matching terminal fault (status or drop) wins and stops evaluation,
// and the first matching throttle wins.
type Rule struct {
	// Name identifies the rule in logs, injected headers, and hash
	// coordinates. Unique within its proxy.
	Name string `json:"name"`
	// Match restricts the rule to some requests. Absent means all.
	Match *Match `json:"match,omitempty"`
	// Window restricts the rule to a request-counter range. Absent means
	// always. Counters are per proxy and start at 1.
	Window *Window `json:"window,omitempty"`
	// Rate is the probability the rule fires when match and window pass,
	// decided deterministically from the seed. Default 1 (always).
	Rate *float64 `json:"rate,omitempty"`
	// Fault is what the rule does. Required.
	Fault Fault `json:"fault"`
}

// Match mirrors match.Criteria in file form.
type Match struct {
	// Methods is a list of HTTP methods, matched case-insensitively.
	Methods []string `json:"methods,omitempty"`
	// Path is a segment glob: "*" spans within a segment, "**" spans
	// segments. E.g. "/api/**", "/users/*".
	Path string `json:"path,omitempty"`
	// Headers maps header names (case-insensitive) to required exact values.
	Headers map[string]string `json:"headers,omitempty"`
}

// Window selects requests by per-proxy counter, the deterministic
// alternative to wall-clock phases: "requests 11-20" replays identically in
// every CI run, "the second 10 seconds" does not.
type Window struct {
	// From is the first counter the rule applies to. Default 1.
	From uint64 `json:"from,omitempty"`
	// To is the last counter the rule applies to. 0 (default) means open.
	To uint64 `json:"to,omitempty"`
	// Every fires the rule on every N-th request counted from From
	// (From, From+Every, …). Default 1.
	Every uint64 `json:"every,omitempty"`
}

// Fault is the injected behavior. delay/jitter may combine with any other
// field (the delay happens first); status and drop are mutually exclusive
// terminals; throttle shapes the upstream response body and is therefore
// incompatible with both terminals.
type Fault struct {
	// DelayMS pauses the request for a fixed number of milliseconds.
	DelayMS int64 `json:"delay_ms,omitempty"`
	// JitterMS adds a deterministic extra delay in [0, jitter_ms] ms,
	// drawn from the seed per request.
	JitterMS int64 `json:"jitter_ms,omitempty"`
	// Status short-circuits the request with this HTTP status; the
	// upstream is never contacted. Injected responses carry an
	// X-Slowlane-Injected header naming the rule.
	Status int `json:"status,omitempty"`
	// Body is the plain-text body for an injected Status response.
	Body string `json:"body,omitempty"`
	// Drop closes the client connection abruptly, before any response
	// bytes — what a crashed or partitioned upstream looks like.
	Drop bool `json:"drop,omitempty"`
	// ThrottleBPS caps the upstream response body copy rate, in bytes
	// per second.
	ThrottleBPS int64 `json:"throttle_bps,omitempty"`
}

// Empty reports whether the fault specifies no behavior at all.
func (f Fault) Empty() bool {
	return f.DelayMS == 0 && f.JitterMS == 0 && f.Status == 0 &&
		f.Body == "" && !f.Drop && f.ThrottleBPS == 0
}

// EffectiveRate returns the rule's rate with the default applied.
func (r Rule) EffectiveRate() float64 {
	if r.Rate == nil {
		return 1
	}
	return *r.Rate
}

// NormalizedWindow returns the window with defaults applied
// (from=1, every=1, to=0 meaning open-ended).
func (r Rule) NormalizedWindow() Window {
	w := Window{From: 1, Every: 1}
	if r.Window != nil {
		w = *r.Window
		if w.From == 0 {
			w.From = 1
		}
		if w.Every == 0 {
			w.Every = 1
		}
	}
	return w
}

// InWindow reports whether a per-proxy request counter falls inside the
// rule's window.
func (r Rule) InWindow(counter uint64) bool {
	w := r.NormalizedWindow()
	if counter < w.From {
		return false
	}
	if w.To != 0 && counter > w.To {
		return false
	}
	return (counter-w.From)%w.Every == 0
}

// ProxyByName returns the named proxy, or nil when absent.
func (s *Scenario) ProxyByName(name string) *Proxy {
	for i := range s.Proxies {
		if s.Proxies[i].Name == name {
			return &s.Proxies[i]
		}
	}
	return nil
}

// RuleCount returns the total number of rules across all proxies.
func (s *Scenario) RuleCount() int {
	n := 0
	for i := range s.Proxies {
		n += len(s.Proxies[i].Rules)
	}
	return n
}

// Error is one validation finding, locatable inside the file.
type Error struct {
	// Path is a JSON-path-like locator, e.g. `proxies[0].rules[2].rate`.
	Path string
	// Msg says what is wrong and, where possible, what would be right.
	Msg string
}

func (e Error) Error() string { return e.Path + ": " + e.Msg }

// ErrorList is the full set of findings for a file; it satisfies error.
type ErrorList []Error

func (l ErrorList) Error() string {
	msgs := make([]string, len(l))
	for i, e := range l {
		msgs[i] = e.Error()
	}
	return strings.Join(msgs, "\n")
}

// Load reads and parses a scenario file, then validates it. On validation
// failure the returned error is an ErrorList.
func Load(path string) (*Scenario, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

// Parse parses scenario JSON with unknown fields rejected, applies no
// silent repairs, and validates. On validation failure the returned error
// is an ErrorList.
func Parse(raw []byte) (*Scenario, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var s Scenario
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("not a valid scenario: %w", err)
	}
	// Trailing garbage after the closing brace is almost always a paste
	// accident; refuse it rather than ignore half the file.
	if dec.More() {
		return nil, fmt.Errorf("not a valid scenario: trailing data after the top-level object")
	}
	if errs := s.Validate(); len(errs) > 0 {
		return nil, errs
	}
	s.normalize()
	return &s, nil
}

// normalize applies canonical forms after validation: uppercase methods.
func (s *Scenario) normalize() {
	for pi := range s.Proxies {
		for ri := range s.Proxies[pi].Rules {
			m := s.Proxies[pi].Rules[ri].Match
			if m == nil {
				continue
			}
			for i, method := range m.Methods {
				m.Methods[i] = strings.ToUpper(method)
			}
		}
	}
}

// Validate checks the whole scenario and returns every finding.
func (s *Scenario) Validate() ErrorList {
	var errs ErrorList
	add := func(path, msg string, args ...any) {
		errs = append(errs, Error{Path: path, Msg: fmt.Sprintf(msg, args...)})
	}

	if s.Version != 1 {
		add("version", "must be 1 (got %d); the field is required", s.Version)
	}
	if len(s.Proxies) == 0 {
		add("proxies", "at least one proxy is required")
	}

	proxyNames := map[string]bool{}
	listens := map[string]string{}
	for pi := range s.Proxies {
		p := &s.Proxies[pi]
		pp := fmt.Sprintf("proxies[%d]", pi)

		if p.Name == "" {
			add(pp+".name", "is required")
		} else if !validName(p.Name) {
			add(pp+".name", "%q must be lowercase letters, digits, and hyphens", p.Name)
		} else if proxyNames[p.Name] {
			add(pp+".name", "duplicate proxy name %q", p.Name)
		}
		proxyNames[p.Name] = true

		if p.Listen == "" {
			add(pp+".listen", "is required, e.g. \"127.0.0.1:18080\"")
		} else if host, _, err := net.SplitHostPort(p.Listen); err != nil {
			add(pp+".listen", "%q is not host:port: %v", p.Listen, err)
		} else if host == "" {
			add(pp+".listen", "%q needs an explicit host; use 127.0.0.1 to stay loopback-only", p.Listen)
		} else if prev, dup := listens[p.Listen]; dup && !strings.HasSuffix(p.Listen, ":0") {
			add(pp+".listen", "%q is already used by proxy %q", p.Listen, prev)
		} else {
			listens[p.Listen] = p.Name
		}

		validateUpstream(p.Upstream, pp+".upstream", add)

		ruleNames := map[string]bool{}
		for ri := range p.Rules {
			r := &p.Rules[ri]
			rp := fmt.Sprintf("%s.rules[%d]", pp, ri)

			if r.Name == "" {
				add(rp+".name", "is required")
			} else if !validName(r.Name) {
				add(rp+".name", "%q must be lowercase letters, digits, and hyphens", r.Name)
			} else if ruleNames[r.Name] {
				add(rp+".name", "duplicate rule name %q in proxy %q", r.Name, p.Name)
			}
			ruleNames[r.Name] = true

			validateMatch(r.Match, rp+".match", add)
			validateWindow(r.Window, rp+".window", add)
			if r.Rate != nil && (*r.Rate < 0 || *r.Rate > 1) {
				add(rp+".rate", "must be within [0, 1] (got %v)", *r.Rate)
			}
			validateFault(r.Fault, rp+".fault", add)
		}
	}
	return errs
}

func validateUpstream(upstream, path string, add func(string, string, ...any)) {
	if upstream == "" {
		add(path, "is required, e.g. \"http://127.0.0.1:8080\"")
		return
	}
	u, err := url.Parse(upstream)
	if err != nil {
		add(path, "%q is not a valid URL: %v", upstream, err)
		return
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		add(path, "scheme must be http or https (got %q)", u.Scheme)
	}
	if u.Host == "" {
		add(path, "%q has no host", upstream)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		add(path, "must not carry a query or fragment (got %q)", upstream)
	}
}

func validateMatch(m *Match, path string, add func(string, string, ...any)) {
	if m == nil {
		return
	}
	if len(m.Methods) == 0 && m.Path == "" && len(m.Headers) == 0 {
		add(path, "is present but empty; drop it to match every request")
	}
	for i, method := range m.Methods {
		if method == "" || strings.ContainsAny(method, " /") {
			add(fmt.Sprintf("%s.methods[%d]", path, i), "%q is not an HTTP method", method)
		}
	}
	if m.Path != "" && !strings.HasPrefix(m.Path, "/") {
		add(path+".path", "%q must start with \"/\"", m.Path)
	}
	// Deterministic error order for map-typed headers.
	names := make([]string, 0, len(m.Headers))
	for name := range m.Headers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if name == "" {
			add(path+".headers", "header names must be non-empty")
		}
	}
}

func validateWindow(w *Window, path string, add func(string, string, ...any)) {
	if w == nil {
		return
	}
	if w.From == 0 && w.To == 0 && w.Every == 0 {
		add(path, "is present but empty; drop it to match every request")
	}
	if w.To != 0 && w.From != 0 && w.To < w.From {
		add(path, "to (%d) must be >= from (%d)", w.To, w.From)
	}
}

func validateFault(f Fault, path string, add func(string, string, ...any)) {
	if f.Empty() {
		add(path, "must specify at least one of delay_ms, jitter_ms, status, drop, throttle_bps")
		return
	}
	if f.DelayMS < 0 {
		add(path+".delay_ms", "must be >= 0 (got %d)", f.DelayMS)
	}
	if f.JitterMS < 0 {
		add(path+".jitter_ms", "must be >= 0 (got %d)", f.JitterMS)
	}
	if f.Status != 0 && (f.Status < 100 || f.Status > 599) {
		add(path+".status", "must be a valid HTTP status in 100-599 (got %d)", f.Status)
	}
	if f.Body != "" && f.Status == 0 {
		add(path+".body", "requires status to be set")
	}
	if f.Status != 0 && f.Drop {
		add(path, "status and drop are mutually exclusive in one rule")
	}
	if f.ThrottleBPS < 0 {
		add(path+".throttle_bps", "must be >= 1 (got %d)", f.ThrottleBPS)
	}
	if f.ThrottleBPS > 0 && (f.Status != 0 || f.Drop) {
		add(path, "throttle_bps shapes the upstream response and cannot combine with status or drop")
	}
}

// validName enforces the identifier alphabet shared by proxy and rule
// names: lowercase ASCII letters, digits, and interior hyphens.
func validName(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") || strings.HasSuffix(s, "-") {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c == '-' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z')
		if !ok {
			return false
		}
	}
	return true
}
