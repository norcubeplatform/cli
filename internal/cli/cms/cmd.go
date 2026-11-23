// Package cms implements the `norcube cms ...` command tree. Norcube CMS
// edits content that lives in the site's own git repository, so these
// commands work on a checkout of the site, offline: no sign-in, no
// organization, no network. The checks are the ones the editor runs,
// shared through the public cms-schema module, so a site that passes here
// opens cleanly in the CMS.
package cms

import (
	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cms-schema/gitlog"
)

// NewCmd returns the `cms` parent command with all subcommands.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cms",
		Short: "Check a Norcube CMS site's schema and content from its repository",
		Long: `Norcube CMS keeps a site's schema in .norcube/cms/ and its content in the
site's own repository. These commands read a checkout of that repository
and need no sign-in: run them on your machine before you push, or in the
site's CI to block a change that breaks the content model.`,
	}
	cmd.AddCommand(newValidateCmd(validateDeps{lastChanged: gitlog.LastChanged}))
	return cmd
}
