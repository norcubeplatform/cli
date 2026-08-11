package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/cli"
)

// Exit codes follow the convention popularized by gh: 0 success, 1 error,
// 4 authentication required. Scripts can branch on them.
const (
	exitError = 1
	exitAuth  = 4
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
		if errors.Is(err, auth.ErrLoginRequired) {
			fmt.Fprintln(os.Stderr, "\nRun `norcube login` to sign in again.")
			os.Exit(exitAuth)
		}
		os.Exit(exitError)
	}
}
