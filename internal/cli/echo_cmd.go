// The echo subcommand: a built-in deterministic upstream, so a scenario
// can be tried end to end with nothing but the slowlane binary.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/JaydenCJ/slowlane/internal/proxy"
)

const echoUsage = `Usage: slowlane echo [flags]

Run the built-in echo upstream: every request is answered with 200 and a
JSON description of what arrived (method, path, query, headers). Useful as
the upstream while trying out a scenario.

Flags:
  --listen string   host:port to bind (default "127.0.0.1:0")
`

func cmdEcho(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("echo", stderr, echoUsage)
	listen := fs.String("listen", "127.0.0.1:0", "host:port to bind")
	_, code, proceed := parseArgs(fs, args, stderr, false)
	if !proceed {
		return code
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintf(stderr, "slowlane echo: %v\n", err)
		return ExitRuntime
	}
	fmt.Fprintf(stdout, "echo listening on %s\n", ln.Addr())

	srv := &http.Server{Handler: proxy.EchoHandler()}
	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
	case err, ok := <-errCh:
		if ok && err != nil {
			fmt.Fprintf(stderr, "slowlane echo: %v\n", err)
			return ExitRuntime
		}
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	srv.Shutdown(shutCtx)
	fmt.Fprintln(stdout, "slowlane: shut down")
	return ExitOK
}
