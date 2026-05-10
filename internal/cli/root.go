package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/buildinfo"
	"github.com/norcubeplatform/cli/internal/cli/snapdb"
	"github.com/norcubeplatform/cli/internal/clictx"
)

func NewRootCmd() *cobra.Command {
	flags := &clictx.Flags{}

	cmd := &cobra.Command{
		Use:           "norcube",
		Short:         "Command-line interface for the Norcube platform",
		Long:          "Norcube CLI — manage backups, namespaces, and more across your Norcube services from the terminal.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       fmt.Sprintf("%s (commit %s, built %s)", buildinfo.Version, buildinfo.Commit, buildinfo.Date),
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			clictx.Attach(cmd, ctx, flags)
			cobra.OnFinalize(stop)
			return nil
		},
	}

	cmd.PersistentFlags().StringVar(&flags.AuthOverride, "auth-url", "", "Override the auth service base URL (env: NORCUBE_AUTH_URL)")
	cmd.PersistentFlags().StringVar(&flags.WebAppOverride, "web-app", "", "Override the web app base URL used for browser login (env: NORCUBE_WEB_APP)")
	cmd.PersistentFlags().StringVar(&flags.OrgOverride, "org", "", "Run this command against a specific organization (id or slug)")
	cmd.PersistentFlags().StringVarP(&flags.Output, "output", "o", "table", "Output format: table | json | yaml")
	cmd.PersistentFlags().BoolVar(&flags.NoPager, "no-pager", false, "Disable automatic paging of long table output (env: $PAGER controls the pager, default: less)")

	cmd.AddCommand(
		newLoginCmd(),
		newLogoutCmd(),
		newWhoamiCmd(),
		newOrgCmd(),
		newConfigCmd(),
		newUpgradeCmd(),
		snapdb.NewCmd(),
	)

	return cmd
}
