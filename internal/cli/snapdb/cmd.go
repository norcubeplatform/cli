// Package snapdb implements the `norcube snapdb ...` command tree. It owns
// the wiring between the CLI's TokenSource (audience: snapdb-api) and the
// generated snapdb HTTP client.
package snapdb

import (
	"context"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/snapdb"
	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/config"
)

// NewCmd returns the `snapdb` parent command with all subcommands wired up.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapdb",
		Short: "Manage SnapDB data sources, backups, policies, and storage",
	}
	cmd.AddCommand(
		newDataSourceCmd(),
		newBackupCmd(),
		newPolicyCmd(),
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

	bearerInjector := func(ctx context.Context, req *http.Request) error {
		token, err := ts.Token(ctx)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		return nil
	}

	client, err := snapdb.NewClientWithResponses(
		cfg.SnapDB,
		snapdb.WithRequestEditorFn(bearerInjector),
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
