// Command slowlane is a fault-injection HTTP proxy driven entirely by a
// declarative, seeded scenario file. See README.md for the full story.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/JaydenCJ/slowlane/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(cli.Main(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
