package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api"
	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/config"
)

func newWhoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the signed-in user and active organization",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			flags := clictx.Get(cmd)
			apiURL := flags.ResolveAuth(cfg)
			orgID, _ := flags.ResolveOrg(cfg)

			ts := auth.NewTokenSource(apiURL, auth.AudienceAuth, orgID)
			client := api.NewAuthClient(apiURL, func(ctx context.Context) (string, error) {
				return ts.Token(ctx)
			})

			me, err := client.GetMe(cmd.Context())
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "User:         %s <%s>\n", me.Name, me.Email)
			fmt.Fprintf(out, "User ID:      %s\n", me.ID)
			if cfg.ActiveOrg.Slug != "" {
				fmt.Fprintf(out, "Active org:   %s (%s)\n", cfg.ActiveOrg.Slug, cfg.ActiveOrg.ID)
			} else {
				fmt.Fprintln(out, "Active org:   (none — run `norcube org use <slug>`)")
			}
			fmt.Fprintf(out, "API:          %s\n", apiURL)
			return nil
		},
	}
}
