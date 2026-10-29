package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/norcubeplatform/cli/internal/cli"
	"github.com/norcubeplatform/cli/internal/clierr"
)

func main() {
	os.Exit(run())
}

// run executes the command and maps its outcome to an exit code (see
// internal/clierr): 0 success, 1 error, 2 usage, 3 not found, 4 login
// required, 5 permission denied, 6 rate limited, 7 timeout, 8 conflict,
// 130 interrupted.
func run() int {
	// The first SIGINT/SIGTERM cancels the command's context so in-flight
	// requests stop and temp files are cleaned up; once that happened the
	// default handlers come back, so a second Ctrl-C kills at once.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop()
	}()

	err := cli.NewRootCmd().ExecuteContext(ctx)
	asJSON := cli.ErrorsAsJSON()
	if ctx.Err() != nil {
		return cli.PrintError(os.Stderr, context.Canceled, asJSON)
	}
	// Wait briefly for the background version check (if any) to land,
	// then surface "new release available" before the error message so
	// the prompt isn't the last thing the user reads. Both calls are
	// safe no-ops when the check was skipped or returned nothing. A JSON
	// error keeps stderr machine-readable, so it gets no nudge.
	cli.WaitForUpdateCheck()
	if !asJSON || err == nil {
		cli.MaybePrintUpdateNudge(os.Stderr)
	}
	if err == nil {
		return clierr.ExitOK
	}
	return cli.PrintError(os.Stderr, err, asJSON)
}
