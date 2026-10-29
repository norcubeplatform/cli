package cmdutil

import (
	"errors"
	"fmt"

	"github.com/charmbracelet/huh"

	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/term"
)

// ErrCancelled is returned by interactive helpers when the user dismisses
// a prompt. Commands report it as "Cancelled." instead of an error.
var ErrCancelled = errors.New("cancelled by user")

// Confirm asks a yes/no question. yes answers it without prompting;
// without a terminal it fails with a usage error that asks for --yes, so a
// script never deletes anything by accident.
func Confirm(prompt string, yes bool) (bool, error) {
	if yes {
		return true, nil
	}
	if !term.StdinIsTerminal() {
		return false, clierr.Usage(fmt.Errorf("%s: pass --yes to confirm non-interactively", prompt))
	}
	var ok bool
	err := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title(prompt).
			Affirmative("Yes").
			Negative("Cancel").
			Value(&ok),
	)).WithTheme(huh.ThemeCharm()).Run()
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, ErrCancelled
		}
		return false, err
	}
	return ok, nil
}

// Deref returns *p, or the zero value for nil (generated API models use
// pointers for optional fields).
func Deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// DerefOr returns *p, or fallback for nil.
func DerefOr[T any](p *T, fallback T) T {
	if p == nil {
		return fallback
	}
	return *p
}
