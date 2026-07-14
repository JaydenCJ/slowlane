// Package engine turns (scenario, request counter, request shape) into a
// fault Decision. It is a pure function of its inputs — no clocks, no RNG
// state — which is what lets `slowlane plan` print the exact behavior the
// live proxy will exhibit.
package engine

import (
	"net/http"
	"time"

	"github.com/JaydenCJ/slowlane/internal/match"
	"github.com/JaydenCJ/slowlane/internal/scenario"
	"github.com/JaydenCJ/slowlane/internal/seeded"
)

// Request is the shape of one request as the engine sees it.
type Request struct {
	Method string
	Path   string
	Header http.Header
}

// Decision is everything the proxy must do for one request. The zero value
// means "pass through untouched".
type Decision struct {
	// Delay is the total injected latency (fixed + jitter, summed across
	// all matching delay rules), applied before anything else.
	Delay time.Duration
	// DelayRules names the rules that contributed to Delay, in order.
	DelayRules []string
	// Status, when non-zero, short-circuits the request with an injected
	// response; Body is its plain-text body and StatusRule the source rule.
	Status     int
	Body       string
	StatusRule string
	// Drop, when true, closes the client connection without a response;
	// DropRule is the source rule.
	Drop     bool
	DropRule string
	// ThrottleBPS, when non-zero, caps the upstream response body copy
	// rate; ThrottleRule is the source rule.
	ThrottleBPS  int64
	ThrottleRule string
}

// Pass reports whether the decision changes nothing.
func (d Decision) Pass() bool {
	return d.Delay == 0 && d.Status == 0 && !d.Drop && d.ThrottleBPS == 0
}

// Terminal reports whether the decision ends the request without
// contacting the upstream.
func (d Decision) Terminal() bool { return d.Status != 0 || d.Drop }

// Decide evaluates a proxy's rules for the counter-th request (counters
// start at 1).
//
// Evaluation order is the file order, with three composition laws:
//
//  1. delays accumulate — every matching delay/jitter rule adds latency;
//  2. the first matching terminal fault (status or drop) wins and stops
//     evaluation, so later rules cannot fire behind it;
//  3. the first matching throttle wins; later throttles are ignored.
func Decide(sc *scenario.Scenario, px *scenario.Proxy, counter uint64, req Request) Decision {
	var d Decision
	for i := range px.Rules {
		r := &px.Rules[i]
		if !ruleFires(sc.Seed, px.Name, r, counter, req) {
			continue
		}
		f := r.Fault
		if f.DelayMS > 0 || f.JitterMS > 0 {
			delay := f.DelayMS
			if f.JitterMS > 0 {
				delay += seeded.DrawInt(sc.Seed, px.Name, r.Name, "jitter", counter, f.JitterMS)
			}
			if delay > 0 {
				d.Delay += time.Duration(delay) * time.Millisecond
				d.DelayRules = append(d.DelayRules, r.Name)
			}
		}
		if f.ThrottleBPS > 0 && d.ThrottleBPS == 0 {
			d.ThrottleBPS = f.ThrottleBPS
			d.ThrottleRule = r.Name
		}
		if f.Status != 0 {
			d.Status = f.Status
			d.Body = f.Body
			d.StatusRule = r.Name
			break
		}
		if f.Drop {
			d.Drop = true
			d.DropRule = r.Name
			break
		}
	}
	return d
}

// ruleFires applies the three selectors: match criteria, counter window,
// and the seeded rate draw. The draw is keyed by (seed, proxy, rule,
// counter), so adding or removing one rule never changes another rule's
// decisions.
func ruleFires(seed uint64, proxyName string, r *scenario.Rule, counter uint64, req Request) bool {
	if !r.InWindow(counter) {
		return false
	}
	if m := r.Match; m != nil {
		crit := match.Criteria{Methods: m.Methods, Path: m.Path, Headers: m.Headers}
		if !crit.Matches(req.Method, req.Path, req.Header) {
			return false
		}
	}
	rate := r.EffectiveRate()
	if rate >= 1 {
		return true
	}
	if rate <= 0 {
		return false
	}
	return seeded.Draw(seed, proxyName, r.Name, "rate", counter) < rate
}
