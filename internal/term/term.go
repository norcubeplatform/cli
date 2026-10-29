// Package term decides what the CLI may do with the user's terminal: is a
// stream a TTY, may it carry color, may we prompt. Every package asks here
// instead of calling isatty itself, so NO_COLOR, CLICOLOR=0 and --no-color
// are honored everywhere at once.
package term

import (
	"io"
	"os"
	"sync/atomic"

	xterm "golang.org/x/term"
)

var noColorFlag atomic.Bool

// SetNoColor records --no-color.
func SetNoColor(v bool) { noColorFlag.Store(v) }

// NoColor reports whether color is switched off: --no-color, a non-empty
// NO_COLOR (https://no-color.org) or CLICOLOR=0.
func NoColor() bool {
	return noColorFlag.Load() || os.Getenv("NO_COLOR") != "" || os.Getenv("CLICOLOR") == "0"
}

// Forced marks a writer whose reader is known to be a terminal even though
// the writer itself isn't one, such as the stdin pipe of `less -R`.
type Forced struct{ io.Writer }

// IsTerminal reports whether w is a terminal. Writers that aren't files
// (test buffers, captured output) are not.
func IsTerminal(w io.Writer) bool {
	if _, ok := w.(Forced); ok {
		return true
	}
	if _, ok := w.(*Forced); ok {
		return true
	}
	f, ok := w.(*os.File)
	return ok && xterm.IsTerminal(int(f.Fd()))
}

// Color reports whether w may carry ANSI color and styling.
func Color(w io.Writer) bool { return !NoColor() && IsTerminal(w) }

// StdinIsTerminal reports whether the CLI may prompt: stdin is a terminal.
func StdinIsTerminal() bool { return xterm.IsTerminal(int(os.Stdin.Fd())) }

// StderrIsTerminal reports whether progress, spinners and hints meant for a
// person may be written to stderr.
func StderrIsTerminal() bool { return xterm.IsTerminal(int(os.Stderr.Fd())) }

// Width is w's width in columns, or 0 when w isn't a terminal.
func Width(w io.Writer) int {
	f, ok := w.(*os.File)
	if !ok {
		return 0
	}
	cols, _, err := xterm.GetSize(int(f.Fd()))
	if err != nil {
		return 0
	}
	return cols
}
