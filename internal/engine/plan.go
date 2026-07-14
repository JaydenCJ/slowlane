// Plan generation: the dry-run behind `slowlane plan`. Because Decide is
// pure, the plan for requests [from, from+n) is not a simulation of the
// proxy — it is the proxy's exact future behavior for that request shape.
package engine

import (
	"fmt"
	"strings"
	"time"

	"github.com/JaydenCJ/slowlane/internal/scenario"
)

// PlanEntry pairs a request counter with its decision.
type PlanEntry struct {
	Counter  uint64
	Decision Decision
}

// Plan computes the decisions for n consecutive requests of the given
// shape, starting at counter from (from >= 1).
func Plan(sc *scenario.Scenario, px *scenario.Proxy, req Request, from, n uint64) []PlanEntry {
	entries := make([]PlanEntry, 0, n)
	for i := uint64(0); i < n; i++ {
		c := from + i
		entries = append(entries, PlanEntry{Counter: c, Decision: Decide(sc, px, c, req)})
	}
	return entries
}

// Summary renders a decision as the single human-readable phrase used by
// both `slowlane plan` and the live `slowlane run` log, e.g.
// "delay 273ms (slow-reads) + 503 (brownout)".
func (e PlanEntry) Summary() string {
	d := e.Decision
	if d.Pass() {
		return "pass"
	}
	var parts []string
	if d.Delay > 0 {
		parts = append(parts, fmt.Sprintf("delay %s (%s)",
			formatDelay(d.Delay), strings.Join(d.DelayRules, ", ")))
	}
	if d.Status != 0 {
		parts = append(parts, fmt.Sprintf("%d (%s)", d.Status, d.StatusRule))
	}
	if d.Drop {
		parts = append(parts, fmt.Sprintf("drop (%s)", d.DropRule))
	}
	if d.ThrottleBPS > 0 {
		parts = append(parts, fmt.Sprintf("throttle %dB/s (%s)", d.ThrottleBPS, d.ThrottleRule))
	}
	return strings.Join(parts, " + ")
}

// formatDelay prints whole milliseconds — every injected delay is
// millisecond-granular by construction.
func formatDelay(d time.Duration) string {
	return fmt.Sprintf("%dms", d.Milliseconds())
}

// Stats aggregates a plan for the summary footer.
type Stats struct {
	Requests  uint64
	Passed    uint64
	Delayed   uint64
	Injected  uint64 // status faults
	Dropped   uint64
	Throttled uint64
	// TotalDelay is the sum of injected latency across the plan.
	TotalDelay time.Duration
}

// Summarize folds a plan into aggregate counts.
func Summarize(entries []PlanEntry) Stats {
	var s Stats
	s.Requests = uint64(len(entries))
	for _, e := range entries {
		d := e.Decision
		if d.Pass() {
			s.Passed++
		}
		if d.Delay > 0 {
			s.Delayed++
			s.TotalDelay += d.Delay
		}
		if d.Status != 0 {
			s.Injected++
		}
		if d.Drop {
			s.Dropped++
		}
		if d.ThrottleBPS > 0 {
			s.Throttled++
		}
	}
	return s
}
