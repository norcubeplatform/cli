// Package backup implements the `norcube backup ...` command tree (snapdb is
// the backend service behind the Norcube Backup product).
package backup

import (
	"net/http"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/snapdb"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/config"
	"github.com/norcubeplatform/cli/internal/service"
)

// NewCmd returns the `backup` parent command with all subcommands wired up.
// The command is named after the product (Norcube Backup); "snapdb" is the
// internal service name and survives only as a compatibility alias.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "backup",
		Aliases: []string{"snapdb"},
		Short:   "Manage Norcube Backup: data sources, backup jobs, policies, restore tests",
	}
	cmd.AddCommand(
		newJobsCmd(),
		newDataSourceCmd(),
		newPolicyCmd(),
		newHealthCmd(),
		newRestoreTestCmd(),
		// Names from before the `<service> <resource> <verb>` convention.
		cmdutil.Renamed(newBackupListCmd(), "norcube backup jobs list"),
		cmdutil.Renamed(newBackupDownloadCmd(), "norcube backup jobs download"),
	)
	registerCompletions(cmd)
	return cmd
}

func newJobsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "jobs",
		Aliases: []string{"job"},
		Short:   "List backup jobs and download their backups",
	}
	cmd.AddCommand(newBackupListCmd(), newBackupDownloadCmd())
	return cmd
}

// snapdbContext is the Backup API client plus the profile, organization
// and output format of the invocation.
type snapdbContext = service.Client[*snapdb.ClientWithResponses]

func newSnapdbContext(cmd *cobra.Command) (*snapdbContext, error) {
	return service.New(cmd, config.ServiceSnapDB, newSnapdbAPI)
}

func newSnapdbAPI(baseURL string, httpc *http.Client, edit service.RequestEditor) (*snapdb.ClientWithResponses, error) {
	return snapdb.NewClientWithResponses(baseURL,
		snapdb.WithHTTPClient(httpc),
		snapdb.WithRequestEditorFn(edit))
}
