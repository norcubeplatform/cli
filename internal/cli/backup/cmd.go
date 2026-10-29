// Package backup implements the `norcube backup ...` command tree (snapdb is
// the backend service behind the Norcube Backup product).
package backup

import (
	"context"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/snapdb"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/config"
	"github.com/norcubeplatform/cli/internal/service"
)

// deps are what the commands need from outside: how to build the API
// client, and how to wait between polls. Production code uses
// defaultDeps; tests can pass fakes, so no command reaches for a global.
type deps struct {
	// client builds the Backup client for the invocation.
	client func(cmd *cobra.Command) (*snapdbContext, error)
	// sleep waits between polls; it returns early when ctx ends.
	sleep func(ctx context.Context, d time.Duration) error
}

func defaultDeps() deps {
	return deps{
		client: func(cmd *cobra.Command) (*snapdbContext, error) {
			return service.New(cmd, config.ServiceSnapDB, newSnapdbAPI)
		},
		sleep: cmdutil.SleepContext,
	}
}

// NewCmd returns the `backup` parent command with all subcommands wired up.
// The command is named after the product (Norcube Backup); "snapdb" is the
// internal service name and survives only as a compatibility alias.
func NewCmd() *cobra.Command { return newCmd(defaultDeps()) }

func newCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "backup",
		Aliases: []string{"snapdb"},
		Short:   "Manage Norcube Backup: data sources, backup jobs, restores, policies, restore tests",
	}
	cmd.AddCommand(
		newJobsCmd(d),
		newDataSourceCmd(d),
		newPolicyCmd(d),
		newDestinationsCmd(d),
		newRestoresCmd(d),
		newHealthCmd(d),
		newRestoreTestCmd(d),
		// Names from before the `<service> <resource> <verb>` convention.
		cmdutil.Renamed(newBackupListCmd(d), "norcube backup jobs list"),
		cmdutil.Renamed(newBackupDownloadCmd(d), "norcube backup jobs download"),
	)
	registerCompletions(cmd, d)
	return cmd
}

func newJobsCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "jobs",
		Aliases: []string{"job"},
		Short:   "Run backups, list backup jobs and download their backups",
	}
	cmd.AddCommand(newBackupListCmd(d), newJobGetCmd(d), newJobRunCmd(d), newBackupDownloadCmd(d))
	return cmd
}

// snapdbContext is the Backup API client plus the profile, organization
// and output format of the invocation.
type snapdbContext = service.Client[*snapdb.ClientWithResponses]

func newSnapdbAPI(baseURL string, httpc *http.Client, edit service.RequestEditor) (*snapdb.ClientWithResponses, error) {
	return snapdb.NewClientWithResponses(baseURL,
		snapdb.WithHTTPClient(httpc),
		snapdb.WithRequestEditorFn(edit))
}

// Page sizes and caps for --all-pages of the per-datasource lists.
const (
	defaultPageSize = 50
	maxItems        = 10000
)
