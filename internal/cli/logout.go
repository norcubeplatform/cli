package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/config"
)

func newLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Forget the locally stored Norcube session",
		Long:  "Removes the refresh token from the OS keyring and clears the user/active-org from the config file. Re-run `norcube login` to sign back in.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			flags := clictx.Get(cmd)
			apiURL := flags.ResolveAuth(cfg)

			if err := auth.DeleteAllTokens(apiURL); err != nil {
				return fmt.Errorf("clear keyring: %w", err)
			}
			cfg.User = config.User{}
			cfg.ActiveOrg = config.Organization{}
			if err := config.Save(cfg); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Signed out.")
			return nil
		},
	}
}
