package langsync

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/charmbracelet/huh"
	"golang.org/x/term"

	"github.com/norcubeplatform/cli/internal/api/langsync"
)

// ErrCancelled is returned by interactive helpers when the user dismisses
// the prompt. Commands turn it into "Cancelled." rather than a stack trace.
var ErrCancelled = errors.New("cancelled by user")

// stdinIsInteractive reports whether stdin is a terminal. We only prompt
// when there's a human at the keyboard — pipes, CI, and redirected stdin
// skip the picker and get a clear "namespace required" error instead.
func stdinIsInteractive() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// pickNamespace lists every namespace in the active organization and
// returns the name (the `:namespaceName` URL segment) of the chosen one.
func pickNamespace(ctx context.Context, c *langsyncContext, title string) (string, error) {
	res, err := c.client.ListNamespacesWithResponse(ctx)
	if err != nil {
		return "", err
	}
	if res.JSON200 == nil {
		return "", apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON401, res.JSON403, res.JSON404, res.JSON500)
	}
	list := *res.JSON200
	if len(list) == 0 {
		return "", fmt.Errorf("no namespaces in the active organization — create one in the web app first")
	}

	options := make([]huh.Option[string], 0, len(list))
	for _, ns := range list {
		name := ""
		if ns.Name != nil {
			name = *ns.Name
		}
		label := name
		if ns.Context != nil && *ns.Context != "" {
			label = fmt.Sprintf("%s — %s", name, *ns.Context)
		}
		options = append(options, huh.NewOption(label, name))
	}

	var selected string
	err = huh.NewSelect[string]().
		Title(title).
		Options(options...).
		Value(&selected).
		Run()
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return "", ErrCancelled
		}
		return "", err
	}
	return selected, nil
}

// resolveNamespace returns the namespace name to operate on. Precedence:
//
//  1. the --namespace flag value
//  2. interactive picker when stdin is a terminal
//  3. error "namespace required"
//
// Suppress the picker by always passing --namespace in scripts.
func resolveNamespace(ctx context.Context, c *langsyncContext, flagValue, pickerTitle string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if !stdinIsInteractive() {
		return "", fmt.Errorf("--namespace is required (pass it explicitly or run interactively)")
	}
	return pickNamespace(ctx, c, pickerTitle)
}

// confirm prompts the user with a yes/no question. When stdin is not
// interactive or --yes was already passed, returns true without prompting
// so scripts work without surprise. Identical contract to the snapdb
// package's helper.
func confirm(prompt string, yesFlag bool) (bool, error) {
	if yesFlag {
		return true, nil
	}
	if !stdinIsInteractive() {
		return false, fmt.Errorf("%s — pass --yes to confirm non-interactively", prompt)
	}
	var ok bool
	err := huh.NewConfirm().
		Title(prompt).
		Affirmative("Yes").
		Negative("Cancel").
		Value(&ok).
		Run()
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, ErrCancelled
		}
		return false, err
	}
	return ok, nil
}

// silence unused-import lint when we only need types from the langsync
// package via the rest of the file's references.
var _ = langsync.DtoDTONamespace{}
