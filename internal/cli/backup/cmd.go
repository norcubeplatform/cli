// Package backup implements the `norcube backup ...` command tree (snapdb is
// the backend service behind the Norcube Backup product). It owns the wiring
// between the CLI's TokenSource (audience: snapdb-api) and the generated
// snapdb HTTP client.
package backup

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/snapdb"
	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/config"
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
		newBackupListCmd(),
		newBackupDownloadCmd(),
		newDataSourceCmd(),
		newPolicyCmd(),
		newHealthCmd(),
		newRestoreTestCmd(),
	)
	return cmd
}

// snapdbContext bundles per-invocation state used across snapdb subcommands —
// resolved config, the typed client, and the requested output format. Built
// once per RunE so child commands don't repeat the wiring.
type snapdbContext struct {
	cfg    *config.Config
	flags  *clictx.Flags
	client *snapdb.ClientWithResponses
	output string
}

func newSnapdbContext(cmd *cobra.Command) (*snapdbContext, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	flags := clictx.Get(cmd)
	apiURL := flags.ResolveAuth(cfg)
	orgID, _ := flags.ResolveOrg(cfg)
	if orgID == "" {
		return nil, fmt.Errorf("no active organization — run `norcube org use <slug>` or pass --org <slug>")
	}

	ts := auth.NewTokenSource(apiURL, auth.AudienceSnapDB, orgID)

	client, err := snapdb.NewClientWithResponses(
		cfg.SnapDB,
		snapdb.WithRequestEditorFn(ts.BearerInjector()),
	)
	if err != nil {
		return nil, fmt.Errorf("snapdb client: %w", err)
	}

	return &snapdbContext{
		cfg:    cfg,
		flags:  flags,
		client: client,
		output: flags.Output,
	}, nil
}
