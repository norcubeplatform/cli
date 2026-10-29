package cmdutil

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// EnvDeprecationWarnings turns on notes about renamed commands. Off by
// default until v1.0, so renames don't add noise to existing scripts.
const EnvDeprecationWarnings = "NORCUBE_DEPRECATION_WARNINGS"

// Renamed marks cmd as the old name of a command that moved to newPath
// (e.g. "norcube backup jobs list"). The old name keeps working but is
// hidden from help and the reference docs; with
// NORCUBE_DEPRECATION_WARNINGS=1 it prints a one-line note to stderr.
func Renamed(cmd *cobra.Command, newPath string) *cobra.Command {
	cmd.Hidden = true
	cmd.Annotations = mergeAnnotations(cmd.Annotations, map[string]string{AnnotationRenamedTo: newPath})
	prev := cmd.PreRunE
	cmd.PreRunE = func(c *cobra.Command, args []string) error {
		if os.Getenv(EnvDeprecationWarnings) == "1" {
			fmt.Fprintf(c.ErrOrStderr(), "Note: `%s` is now `%s`; the old name keeps working until v1.0.\n", c.CommandPath(), newPath)
		}
		if prev != nil {
			return prev(c, args)
		}
		return nil
	}
	return cmd
}

// AnnotationRenamedTo records where a renamed command moved.
const AnnotationRenamedTo = "norcube/renamed-to"

func mergeAnnotations(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
