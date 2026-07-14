// The check subcommand: validate a scenario file and summarize it. Exit 0
// on a clean file, 1 with every finding listed otherwise — made for CI
// gates before a `run`.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
)

const checkUsage = `Usage: slowlane check [flags] <scenario.json>

Validate a scenario file: strict JSON (unknown fields rejected), then
semantic checks. Prints every problem with its location, or a summary of
the proxies and rules when the file is clean.

Flags:
  --format string   output format: text or json (default "text")
`

func cmdCheck(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("check", stderr, checkUsage)
	format := fs.String("format", "text", "output format: text or json")
	path, code, proceed := parseArgs(fs, args, stderr, true)
	if !proceed {
		return code
	}
	if *format != "text" && *format != "json" {
		fmt.Fprintf(stderr, "slowlane check: --format must be text or json (got %q)\n", *format)
		return ExitUsage
	}

	sc, code := loadScenario(path, stderr)
	if code != ExitOK {
		return code
	}

	if *format == "json" {
		type proxySummary struct {
			Name     string `json:"name"`
			Listen   string `json:"listen"`
			Upstream string `json:"upstream"`
			Rules    int    `json:"rules"`
		}
		summary := struct {
			File    string         `json:"file"`
			OK      bool           `json:"ok"`
			Version int            `json:"version"`
			Seed    uint64         `json:"seed"`
			Proxies []proxySummary `json:"proxies"`
		}{File: path, OK: true, Version: sc.Version, Seed: sc.Seed}
		for i := range sc.Proxies {
			p := &sc.Proxies[i]
			summary.Proxies = append(summary.Proxies, proxySummary{
				Name: p.Name, Listen: p.Listen, Upstream: p.Upstream, Rules: len(p.Rules),
			})
		}
		out, _ := json.MarshalIndent(summary, "", "  ")
		fmt.Fprintf(stdout, "%s\n", out)
		return ExitOK
	}

	fmt.Fprintf(stdout, "%s: OK (version %d, seed %d, %d prox%s, %d rule%s)\n",
		path, sc.Version, sc.Seed,
		len(sc.Proxies), plural(len(sc.Proxies), "y", "ies"),
		sc.RuleCount(), plural(sc.RuleCount(), "", "s"))
	for i := range sc.Proxies {
		p := &sc.Proxies[i]
		fmt.Fprintf(stdout, "  proxy %-12s %s -> %s  (%d rule%s)\n",
			p.Name, p.Listen, p.Upstream, len(p.Rules), plural(len(p.Rules), "", "s"))
	}
	return ExitOK
}

// plural picks the singular or plural suffix for a count.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
