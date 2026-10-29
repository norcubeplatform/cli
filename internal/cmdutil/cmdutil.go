// Package cmdutil is the one path from a command's result to stdout. A
// command returns its value; cmdutil applies --query and renders it in the
// format -o selected. stdout carries only that value: progress, hints and
// confirmations go to stderr.
package cmdutil

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/output"
)

// RunFunc is a command body that returns its result instead of printing.
// A nil result prints nothing (and is not "null").
type RunFunc func(cmd *cobra.Command, args []string) (any, error)

// Run adapts fn to cobra: the result goes through Print.
func Run(fn RunFunc) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if err := CheckQuery(cmd); err != nil {
			return err
		}
		v, err := fn(cmd, args)
		if err != nil {
			return err
		}
		if v == nil {
			return nil
		}
		return Print(cmd, v)
	}
}

// CheckQuery fails with a usage error when --query doesn't parse, before
// the command does any work.
func CheckQuery(cmd *cobra.Command) error {
	if q := query(cmd); q != "" {
		if err := output.CompileQuery(q); err != nil {
			return clierr.Usage(err)
		}
	}
	return nil
}

// Print renders value to the command's stdout. Tables page through $PAGER
// on a terminal unless --no-pager.
func Print(cmd *cobra.Command, value any) error {
	format, noPager := output.FormatTable, true
	if f := clictx.Get(cmd); f != nil {
		format, noPager = f.Output, f.NoPager
	}
	if format == "" {
		format = output.FormatTable
	}
	if q := query(cmd); q != "" {
		g, err := output.Query(value, q)
		if err != nil {
			return clierr.Usage(err)
		}
		value = g
	}
	return output.PrintPaged(cmd.OutOrStdout(), format, noPager, value)
}

// Format is the output format of the running command.
func Format(cmd *cobra.Command) string {
	if f := clictx.Get(cmd); f != nil && f.Output != "" {
		return f.Output
	}
	return output.FormatTable
}

// Human reports whether the output is for a person (table), so commands
// may add notes to stderr that a script wouldn't want.
func Human(cmd *cobra.Command) bool { return Format(cmd) == output.FormatTable }

// Stderr is where progress, hints and confirmations go.
func Stderr(cmd *cobra.Command) io.Writer { return cmd.ErrOrStderr() }

func query(cmd *cobra.Command) string {
	if f := clictx.Get(cmd); f != nil {
		return f.Query
	}
	return ""
}
