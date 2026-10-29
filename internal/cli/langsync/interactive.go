package langsync

import (
	"context"
	"errors"
	"fmt"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/langsync"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/term"
)

// pickNamespace lists every namespace in the active organization and
// returns the name (the `:namespaceName` URL segment) of the chosen one.
func pickNamespace(ctx context.Context, c *langsyncContext, title string) (string, error) {
	res, err := c.API.ListNamespacesWithResponse(ctx)
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
	// No Height() — see init_org.go for the huh viewport bug
	// workaround note. Namespace lists are short enough to render
	// in full without the scrolling glitch.
	err = newWizard(huh.NewGroup(
		huh.NewSelect[string]().
			Title(title).
			Options(options...).
			Filtering(len(options) > 8).
			Value(&selected),
	)).Run()
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return "", cmdutil.ErrCancelled
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
	if !term.StdinIsTerminal() {
		return "", clierr.Usage(errors.New("--namespace is required (pass it explicitly or run interactively)"))
	}
	return pickNamespace(ctx, c, pickerTitle)
}

// silence unused-import lint when we only need types from the langsync
// package via the rest of the file's references.
var _ = langsync.DtoDTONamespace{}

// cancelledOK turns a dismissed prompt into "Cancelled." on stderr and a
// clean exit; any other error passes through.
func cancelledOK(cmd *cobra.Command, err error) error {
	if errors.Is(err, cmdutil.ErrCancelled) {
		fmt.Fprintln(cmd.ErrOrStderr(), "Cancelled.")
		return nil
	}
	return err
}

// cancelledIfDeclined turns a declined confirmation (no error) into
// ErrCancelled, so cancelledOK reports both the same way.
func cancelledIfDeclined(err error) error {
	if err == nil {
		return cmdutil.ErrCancelled
	}
	return err
}
