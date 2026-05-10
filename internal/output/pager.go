package output

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/term"
)

// PrintPaged behaves like Print, but pipes the output through a pager
// program when (a) the format is table, (b) w is an interactive
// terminal, and (c) the user hasn't disabled paging via the flag.
//
// The pager is taken from $NORCUBE_PAGER, then $PAGER, then "less".
// We set LESS=FRX so less:
//   - F: prints content inline and exits if it fits in one screen
//   - R: passes our ANSI color escapes through unmolested
//   - X: doesn't clear the screen on exit (output stays scrolled-back)
//
// When the pager program can't be started (e.g. less isn't installed) we
// fall back to direct Print rather than failing the command.
func PrintPaged(w io.Writer, format string, disabled bool, value any) error {
	if disabled || format != FormatTable || !isInteractive(w) {
		return Print(w, format, value)
	}
	return pageThrough(w, format, value)
}

func pageThrough(w io.Writer, format string, value any) error {
	pagerCmd := resolvePager()
	parts := strings.Fields(pagerCmd)
	if len(parts) == 0 {
		return Print(w, format, value)
	}

	cmd := exec.Command(parts[0], parts[1:]...)
	cmd.Stdout = w
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "LESS=FRX")

	pipe, err := cmd.StdinPipe()
	if err != nil {
		return Print(w, format, value)
	}
	if err := cmd.Start(); err != nil {
		// less not installed, or otherwise unusable — fall back.
		return Print(w, format, value)
	}

	// Wrap the pipe so styleFor() treats it as interactive. The pager is
	// launched with LESS=FRX (R = pass raw ANSI through), so colors do
	// reach the user's terminal even though `pipe` itself is not a TTY.
	werr := Print(&forcedTTY{Writer: pipe}, format, value)
	_ = pipe.Close()
	waitErr := cmd.Wait()

	if werr != nil && !isBrokenPipe(werr) {
		return werr
	}
	// less exiting non-zero is normal — the user pressed q before we
	// finished writing, which closes stdin from less's side. Treat as
	// success.
	_ = waitErr
	return nil
}

func resolvePager() string {
	if p := os.Getenv("NORCUBE_PAGER"); p != "" {
		return p
	}
	if p := os.Getenv("PAGER"); p != "" {
		return p
	}
	return "less"
}

// isBrokenPipe matches io.ErrClosedPipe and OS-level EPIPE, which happen
// when the user quits the pager before we've written everything.
func isBrokenPipe(err error) bool {
	if errors.Is(err, io.ErrClosedPipe) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "broken pipe") || strings.Contains(msg, "file already closed")
}

// IsInteractive reports whether w is a real terminal. Exported so command
// packages can decide whether to emit chatty stderr hints. Non-*os.File
// writers (test buffers, captured stderr) are treated as non-interactive.
func IsInteractive(w io.Writer) bool {
	return isInteractive(w)
}

func isInteractive(w io.Writer) bool {
	// forcedTTY tags a writer (typically the pager's stdin pipe) as
	// effectively interactive because we know the eventual reader is a
	// terminal. styleFor() therefore keeps colors on for the table.
	if _, ok := w.(*forcedTTY); ok {
		return true
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// forcedTTY wraps a writer whose downstream consumer is known to be a
// color-capable terminal (e.g. less -R), even though the immediate writer
// itself is a non-terminal pipe.
type forcedTTY struct {
	io.Writer
}
