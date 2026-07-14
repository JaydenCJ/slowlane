// The plan subcommand: print the deterministic fault schedule a scenario
// will apply to a request sequence — before starting any proxy. Because
// decisions are pure functions of (seed, counter, request shape), this is
// not an estimate; it is the exact runtime behavior.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/JaydenCJ/slowlane/internal/engine"
)

const planUsage = `Usage: slowlane plan [flags] <scenario.json>

Print the exact per-request fault decisions for a request shape, without
starting a proxy. The live proxy computes the identical decisions for the
same counters.

Flags:
  --proxy string     proxy to plan for (default: the only proxy)
  --requests uint    number of requests to plan (default 20)
  --from uint        first request counter, >= 1 (default 1)
  --method string    request method to plan with (default "GET")
  --path string      request path to plan with (default "/")
  --header value     request header "Name: value" (repeatable)
  --format string    output format: text or json (default "text")
`

func cmdPlan(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("plan", stderr, planUsage)
	proxyName := fs.String("proxy", "", "proxy to plan for")
	requests := fs.Uint64("requests", 20, "number of requests to plan")
	from := fs.Uint64("from", 1, "first request counter")
	method := fs.String("method", "GET", "request method")
	reqPath := fs.String("path", "/", "request path")
	format := fs.String("format", "text", "output format: text or json")
	var headers headerFlag
	fs.Var(&headers, "header", "request header \"Name: value\" (repeatable)")
	path, code, proceed := parseArgs(fs, args, stderr, true)
	if !proceed {
		return code
	}
	if *format != "text" && *format != "json" {
		fmt.Fprintf(stderr, "slowlane plan: --format must be text or json (got %q)\n", *format)
		return ExitUsage
	}
	if *from < 1 {
		fmt.Fprintf(stderr, "slowlane plan: --from must be >= 1\n")
		return ExitUsage
	}
	if *requests < 1 {
		fmt.Fprintf(stderr, "slowlane plan: --requests must be >= 1\n")
		return ExitUsage
	}

	sc, code := loadScenario(path, stderr)
	if code != ExitOK {
		return code
	}
	px, code := pickProxy(sc, *proxyName, stderr)
	if code != ExitOK {
		return code
	}

	req := engine.Request{Method: *method, Path: *reqPath, Header: http.Header{}}
	for _, pair := range headers.pairs {
		req.Header.Add(pair[0], pair[1])
	}
	entries := engine.Plan(sc, px, req, *from, *requests)
	stats := engine.Summarize(entries)

	if *format == "json" {
		return planJSON(stdout, sc.Seed, px.Name, req, entries, stats)
	}

	last := *from + *requests - 1
	fmt.Fprintf(stdout, "plan: proxy %q seed %d — %s %s, requests %d-%d\n\n",
		px.Name, sc.Seed, req.Method, req.Path, *from, last)
	fmt.Fprintf(stdout, "  req  action\n")
	for _, e := range entries {
		fmt.Fprintf(stdout, "  %3d  %s\n", e.Counter, e.Summary())
	}
	fmt.Fprintf(stdout, "\n%d requests: %d pass, %d delayed (total %dms), %d injected, %d dropped, %d throttled\n",
		stats.Requests, stats.Passed, stats.Delayed, stats.TotalDelay.Milliseconds(),
		stats.Injected, stats.Dropped, stats.Throttled)
	return ExitOK
}

// planJSON renders the machine-readable plan. Every field a test harness
// could gate on is structured; the human phrase rides along as "action".
func planJSON(stdout io.Writer, seed uint64, proxy string, req engine.Request,
	entries []engine.PlanEntry, stats engine.Stats) int {
	type entryJSON struct {
		N           uint64   `json:"n"`
		Action      string   `json:"action"`
		DelayMS     int64    `json:"delay_ms,omitempty"`
		DelayRules  []string `json:"delay_rules,omitempty"`
		Status      int      `json:"status,omitempty"`
		StatusRule  string   `json:"status_rule,omitempty"`
		Drop        bool     `json:"drop,omitempty"`
		DropRule    string   `json:"drop_rule,omitempty"`
		ThrottleBPS int64    `json:"throttle_bps,omitempty"`
	}
	out := struct {
		Proxy    string      `json:"proxy"`
		Seed     uint64      `json:"seed"`
		Method   string      `json:"method"`
		Path     string      `json:"path"`
		Requests []entryJSON `json:"requests"`
		Summary  struct {
			Pass         uint64 `json:"pass"`
			Delayed      uint64 `json:"delayed"`
			Injected     uint64 `json:"injected"`
			Dropped      uint64 `json:"dropped"`
			Throttled    uint64 `json:"throttled"`
			TotalDelayMS int64  `json:"total_delay_ms"`
		} `json:"summary"`
	}{Proxy: proxy, Seed: seed, Method: req.Method, Path: req.Path}
	for _, e := range entries {
		d := e.Decision
		out.Requests = append(out.Requests, entryJSON{
			N: e.Counter, Action: e.Summary(),
			DelayMS: d.Delay.Milliseconds(), DelayRules: d.DelayRules,
			Status: d.Status, StatusRule: d.StatusRule,
			Drop: d.Drop, DropRule: d.DropRule,
			ThrottleBPS: d.ThrottleBPS,
		})
	}
	out.Summary.Pass = stats.Passed
	out.Summary.Delayed = stats.Delayed
	out.Summary.Injected = stats.Injected
	out.Summary.Dropped = stats.Dropped
	out.Summary.Throttled = stats.Throttled
	out.Summary.TotalDelayMS = stats.TotalDelay.Milliseconds()
	raw, _ := json.MarshalIndent(out, "", "  ")
	fmt.Fprintf(stdout, "%s\n", raw)
	return ExitOK
}
