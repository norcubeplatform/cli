package main

import (
	"fmt"
	"os"

	"github.com/norcubeplatform/cli/internal/cli"
)

func main() {
	err := cli.NewRootCmd().Execute()
	// Wait briefly for the background version check (if any) to land,
	// then surface "new release available" before the error message so
	// the prompt isn't the last thing the user reads. Both calls are
	// safe no-ops when the check was skipped or returned nothing.
	cli.WaitForUpdateCheck()
	cli.MaybePrintUpdateNudge(os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
