// Package cli implements the slowlane command line: argument parsing,
// subcommand dispatch, and the exit-code contract. All I/O flows through
// injected writers so tests drive the real CLI in-process.
//
// Exit codes: 0 success, 1 scenario invalid or check failed, 2 usage
// error, 3 runtime error (bind failure, I/O error).
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/JaydenCJ/slowlane/internal/scenario"
	"github.com/JaydenCJ/slowlane/internal/version"
)

// Exit codes returned by Main.
const (
	ExitOK      = 0
	ExitInvalid = 1
	ExitUsage   = 2
	ExitRuntime = 3
)

const rootUsage = `slowlane — fault-injection proxy driven by a scenario file

Usage:
  slowlane run <scenario.json>     start the proxies and inject faults
  slowlane check <scenario.json>   validate a scenario file
  slowlane plan <scenario.json>    print the deterministic fault schedule
  slowlane echo                    run the built-in echo upstream
  slowlane version                 print the version

Run "slowlane <command> -h" for command flags.
`

// Main runs the CLI and returns the process exit code. ctx cancellation
// stops the long-running commands (run, echo) gracefully.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, rootUsage)
		return ExitUsage
	}
	switch args[0] {
	case "run":
		return cmdRun(ctx, args[1:], stdout, stderr)
	case "check":
		return cmdCheck(args[1:], stdout, stderr)
	case "plan":
		return cmdPlan(args[1:], stdout, stderr)
	case "echo":
		return cmdEcho(ctx, args[1:], stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "slowlane %s\n", version.Version)
		return ExitOK
	case "help", "--help", "-h":
		fmt.Fprint(stdout, rootUsage)
		return ExitOK
	default:
		fmt.Fprintf(stderr, "slowlane: unknown command %q\n\n%s", args[0], rootUsage)
		return ExitUsage
	}
}

// newFlagSet builds a silent FlagSet whose errors we render ourselves.
func newFlagSet(name string, stderr io.Writer, usage string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	return fs
}

// parseArgs parses flags and enforces exactly one positional scenario path
// when wantPath is true. When proceed is false the command must return
// code immediately (help requested, usage error).
func parseArgs(fs *flag.FlagSet, args []string, stderr io.Writer, wantPath bool) (path string, code int, proceed bool) {
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return "", ExitOK, false
		}
		return "", ExitUsage, false
	}
	if !wantPath {
		if fs.NArg() != 0 {
			fmt.Fprintf(stderr, "slowlane %s: unexpected argument %q\n", fs.Name(), fs.Arg(0))
			return "", ExitUsage, false
		}
		return "", ExitOK, true
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(stderr, "slowlane %s: expected exactly one scenario file argument\n", fs.Name())
		return "", ExitUsage, false
	}
	return fs.Arg(0), ExitOK, true
}

// loadScenario loads and validates a scenario, rendering errors the same
// way for every subcommand. The int is an exit code, ExitOK on success.
func loadScenario(path string, stderr io.Writer) (*scenario.Scenario, int) {
	sc, err := scenario.Load(path)
	if err == nil {
		return sc, ExitOK
	}
	var list scenario.ErrorList
	if ok := asErrorList(err, &list); ok {
		fmt.Fprintf(stderr, "%s: %d problem(s):\n", path, len(list))
		for _, e := range list {
			fmt.Fprintf(stderr, "  %s\n", e.Error())
		}
		return nil, ExitInvalid
	}
	fmt.Fprintf(stderr, "%s: %v\n", path, err)
	return nil, ExitInvalid
}

// asErrorList unwraps err into an ErrorList without importing errors.As
// generics ceremony at every call site.
func asErrorList(err error, out *scenario.ErrorList) bool {
	if l, ok := err.(scenario.ErrorList); ok {
		*out = l
		return true
	}
	return false
}

// pickProxy resolves the --proxy flag: required when the scenario has more
// than one proxy, defaulted when it has exactly one.
func pickProxy(sc *scenario.Scenario, name string, stderr io.Writer) (*scenario.Proxy, int) {
	if name == "" {
		if len(sc.Proxies) == 1 {
			return &sc.Proxies[0], ExitOK
		}
		names := make([]string, len(sc.Proxies))
		for i := range sc.Proxies {
			names[i] = sc.Proxies[i].Name
		}
		fmt.Fprintf(stderr, "slowlane: scenario has %d proxies (%s); pick one with --proxy\n",
			len(sc.Proxies), strings.Join(names, ", "))
		return nil, ExitUsage
	}
	px := sc.ProxyByName(name)
	if px == nil {
		fmt.Fprintf(stderr, "slowlane: no proxy named %q in the scenario\n", name)
		return nil, ExitUsage
	}
	return px, ExitOK
}

// headerFlag collects repeated --header "Name: value" flags.
type headerFlag struct {
	pairs [][2]string
}

func (h *headerFlag) String() string { return fmt.Sprintf("%d header(s)", len(h.pairs)) }

func (h *headerFlag) Set(v string) error {
	name, value, ok := strings.Cut(v, ":")
	if !ok || strings.TrimSpace(name) == "" {
		return fmt.Errorf("expected \"Name: value\", got %q", v)
	}
	h.pairs = append(h.pairs, [2]string{strings.TrimSpace(name), strings.TrimSpace(value)})
	return nil
}
