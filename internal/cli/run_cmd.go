// The run subcommand: bind every proxy in the scenario, serve until the
// context is cancelled (Ctrl-C), and log one line per handled request.
// There is no admin API and no control plane — the scenario file is the
// entire configuration surface.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/JaydenCJ/slowlane/internal/proxy"
	"github.com/JaydenCJ/slowlane/internal/scenario"
)

const runUsage = `Usage: slowlane run [flags] <scenario.json>

Start every proxy in the scenario and inject its faults until interrupted.
Each proxy prints one ready line ("proxy <name> listening on <addr> -> <upstream>")
and then one log line per handled request.

Flags:
  --log string   request log format: text or json (default "text")
  --quiet        suppress per-request log lines (ready lines still print)
`

func cmdRun(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("run", stderr, runUsage)
	logMode := fs.String("log", "text", "request log format: text or json")
	quiet := fs.Bool("quiet", false, "suppress per-request log lines")
	path, code, proceed := parseArgs(fs, args, stderr, true)
	if !proceed {
		return code
	}
	if *logMode != "text" && *logMode != "json" {
		fmt.Fprintf(stderr, "slowlane run: --log must be text or json (got %q)\n", *logMode)
		return ExitUsage
	}
	sc, code := loadScenario(path, stderr)
	if code != ExitOK {
		return code
	}
	return serveScenario(ctx, sc, *logMode, *quiet, stdout, stderr)
}

// bound pairs a proxy's HTTP server with its listener.
type bound struct {
	srv  *http.Server
	ln   net.Listener
	name string
}

// serveScenario binds and serves every proxy until ctx is cancelled. It is
// split from flag handling so tests can drive it with a cancelable context.
func serveScenario(ctx context.Context, sc *scenario.Scenario, logMode string, quiet bool, stdout, stderr io.Writer) int {
	logger := &eventLogger{w: stdout, mode: logMode, quiet: quiet}

	var all []bound
	closeAll := func() {
		for _, b := range all {
			b.ln.Close()
		}
	}

	for i := range sc.Proxies {
		px := &sc.Proxies[i]
		handler, err := proxy.New(sc, px)
		if err != nil {
			closeAll()
			fmt.Fprintf(stderr, "slowlane: %v\n", err)
			return ExitRuntime
		}
		handler.OnEvent = logger.log
		ln, err := net.Listen("tcp", px.Listen)
		if err != nil {
			closeAll()
			fmt.Fprintf(stderr, "slowlane: proxy %q: %v\n", px.Name, err)
			return ExitRuntime
		}
		fmt.Fprintf(stdout, "proxy %s listening on %s -> %s\n",
			px.Name, ln.Addr(), px.Upstream)
		all = append(all, bound{
			srv:  &http.Server{Handler: handler},
			ln:   ln,
			name: px.Name,
		})
	}

	errCh := make(chan error, len(all))
	var wg sync.WaitGroup
	for _, b := range all {
		wg.Add(1)
		go func(b bound) {
			defer wg.Done()
			if err := b.srv.Serve(b.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("proxy %q: %w", b.name, err)
			}
		}(b)
	}

	select {
	case <-ctx.Done():
	case err := <-errCh:
		fmt.Fprintf(stderr, "slowlane: %v\n", err)
		shutdownAll(all, stdout)
		wg.Wait()
		return ExitRuntime
	}
	shutdownAll(all, stdout)
	wg.Wait()
	return ExitOK
}

func shutdownAll(all []bound, stdout io.Writer) {
	shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, b := range all {
		b.srv.Shutdown(shutCtx)
	}
	fmt.Fprintln(stdout, "slowlane: shut down")
}

// eventLogger serializes concurrent request events into the log stream.
type eventLogger struct {
	mu    sync.Mutex
	w     io.Writer
	mode  string
	quiet bool
}

func (l *eventLogger) log(ev proxy.Event) {
	if l.quiet {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.mode == "json" {
		raw, _ := json.Marshal(ev)
		fmt.Fprintf(l.w, "%s\n", raw)
		return
	}
	switch {
	case ev.Dropped:
		fmt.Fprintf(l.w, "%s #%d %s %s [%s]\n", ev.Proxy, ev.Counter, ev.Method, ev.Path, ev.Action)
	case ev.UpstreamErr != "":
		fmt.Fprintf(l.w, "%s #%d %s %s [%s] -> 502 upstream unreachable\n",
			ev.Proxy, ev.Counter, ev.Method, ev.Path, ev.Action)
	default:
		fmt.Fprintf(l.w, "%s #%d %s %s [%s] -> %d\n",
			ev.Proxy, ev.Counter, ev.Method, ev.Path, ev.Action, ev.Status)
	}
}
