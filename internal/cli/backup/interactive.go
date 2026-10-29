package backup

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/norcubeplatform/cli/internal/api/snapdb"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/term"
)

// pickDataSource opens a huh.Select listing every data source in the
// active organization and returns the chosen one. Used by commands that
// accept an optional positional argument — when the caller passes an id
// we run directly; when they don't and stdin is interactive, we pick.
func pickDataSource(ctx context.Context, c *snapdbContext, title string) (*snapdb.DtoDataSource, error) {
	res, err := c.client.ListWithResponse(ctx, &snapdb.ListParams{})
	if err != nil {
		return nil, err
	}
	if res.JSON200 == nil {
		return nil, apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON500)
	}
	list := res.JSON200.List
	if len(list) == 0 {
		return nil, fmt.Errorf("no data sources in the active organization")
	}

	options := make([]huh.Option[string], 0, len(list))
	for _, ds := range list {
		label := ds.Name
		if ds.Environment != "" {
			label = fmt.Sprintf("%s [%s]", ds.Name, ds.Environment)
		}
		if !ds.IsActive {
			label += "  (paused)"
		}
		options = append(options, huh.NewOption(label, ds.Id))
	}

	var selectedID string
	err = huh.NewSelect[string]().
		Title(title).
		Options(options...).
		Value(&selectedID).
		Run()
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil, ErrCancelled
		}
		return nil, err
	}
	for i := range list {
		if list[i].Id == selectedID {
			return &list[i], nil
		}
	}
	return nil, fmt.Errorf("internal: picker returned %q which is not in the list", selectedID)
}

// resolveDataSourceID returns the data-source ID to operate on. Precedence:
//
//  1. argv (positional id)
//  2. interactive picker, when stdin is a terminal
//  3. error "id required"
//
// The returned name is set when we know it (from the picker); empty
// otherwise. Commands use it to print friendlier success messages
// ("paused 'JDS SnapDB'") without an extra round-trip.
func resolveDataSourceID(ctx context.Context, c *snapdbContext, args []string, pickerTitle string) (id, name string, err error) {
	if len(args) > 0 {
		return strings.TrimSpace(args[0]), "", nil
	}
	if !term.StdinIsTerminal() {
		return "", "", clierr.Usage(errors.New("data source id required (pass it as a positional argument or run interactively)"))
	}
	ds, err := pickDataSource(ctx, c, pickerTitle)
	if err != nil {
		return "", "", err
	}
	return ds.Id, ds.Name, nil
}

// confirm prompts the user with a yes/no question. --yes answers it
// without prompting; without a TTY it fails and asks for --yes, so a
// script never deletes anything by accident.
func confirm(prompt string, yesFlag bool) (bool, error) {
	if yesFlag {
		return true, nil
	}
	if !term.StdinIsTerminal() {
		return false, clierr.Usage(fmt.Errorf("%s: pass --yes to confirm non-interactively", prompt))
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

// ErrCancelled is returned by interactive helpers when the user dismisses
// the prompt. Commands turn it into "Cancelled." rather than a stack
// trace.
var ErrCancelled = errors.New("cancelled by user")
