// Package langsync implements the `norcube langsync ...` command tree.
// Mirrors the structure of internal/cli/snapdb — owns the wiring between
// the CLI's TokenSource (audience: langsync-api) and the generated
// langsync HTTP client.
package langsync

import (
	"context"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/langsync"
	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/config"
)

// NewCmd returns the `langsync` parent command with all subcommands wired up.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "langsync",
		Short: "Manage Langsync namespaces, marks (terms), and translations",
	}
	cmd.AddCommand(newMarkCmd())
	return cmd
}

// langsyncContext bundles per-invocation state used across langsync
// subcommands — resolved config, the typed client, and the requested
// output format. Built once per RunE so child commands don't repeat
// the wiring.
type langsyncContext struct {
	cfg    *config.Config
	flags  *clictx.Flags
	client *langsync.ClientWithResponses
	output string
}

func newLangsyncContext(cmd *cobra.Command) (*langsyncContext, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	flags := clictx.Get(cmd)
	authURL := flags.ResolveAuth(cfg)
	orgID, _ := flags.ResolveOrg(cfg)
	if orgID == "" {
		return nil, fmt.Errorf("no active organization — run `norcube org use <slug>` or pass --org <slug>")
	}

	ts := auth.NewTokenSource(authURL, auth.AudienceLangsync, orgID)

	bearerInjector := func(ctx context.Context, req *http.Request) error {
		token, err := ts.Token(ctx)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		return nil
	}

	client, err := langsync.NewClientWithResponses(
		cfg.Langsync,
		langsync.WithRequestEditorFn(bearerInjector),
	)
	if err != nil {
		return nil, fmt.Errorf("langsync client: %w", err)
	}

	return &langsyncContext{
		cfg:    cfg,
		flags:  flags,
		client: client,
		output: flags.Output,
	}, nil
}
