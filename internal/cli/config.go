package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/config"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect the local CLI config",
	}
	cmd.AddCommand(newConfigShowCmd(), newConfigPathCmd(), newConfigResetURLsCmd())
	return cmd
}

func newConfigResetURLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reset-urls",
		Short: "Refresh per-service URLs (snapdb, langsync, ...) to the current built-in defaults",
		Long: `Useful after upgrading the CLI when the built-in service URLs have
changed. Leaves the auth API URL, web-app URL, user, and active
organization alone — only the per-service base URLs are rewritten.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			cfg.ResetServiceURLs()
			if err := config.Save(cfg); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, "Per-service URLs reset to defaults:")
			fmt.Fprintf(out, "  SnapDB:       %s\n", cfg.SnapDB)
			fmt.Fprintf(out, "  Langsync:     %s\n", cfg.Langsync)
			fmt.Fprintf(out, "  Domainradar:  %s\n", cfg.Domainradar)
			fmt.Fprintf(out, "  Billing:      %s\n", cfg.Billing)
			fmt.Fprintf(out, "  Prompthub:    %s\n", cfg.Prompthub)
			return nil
		},
	}
}

func newConfigShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the resolved config (URLs, active org, login state)",
		Long: `Useful for diagnosing "why is the CLI hitting the wrong host?" — shows the
URLs as they will be used after merging --auth-url / --web-app / NORCUBE_* env
overrides on top of ~/.config/norcube/config.toml.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			flags := clictx.Get(cmd)

			path, _ := config.Path()
			out := cmd.OutOrStdout()
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)

			loggedIn := "no"
			if t, _ := auth.LoadRefreshToken(flags.ResolveAuth(cfg)); t != "" {
				loggedIn = "yes"
			}

			fmt.Fprintf(tw, "Config path:\t%s\n", path)
			fmt.Fprintf(tw, "Auth API:\t%s\n", flags.ResolveAuth(cfg))
			fmt.Fprintf(tw, "Web app:\t%s\n", flags.ResolveWebApp(cfg))
			fmt.Fprintf(tw, "SnapDB:\t%s\n", cfg.SnapDB)
			fmt.Fprintf(tw, "Langsync:\t%s\n", cfg.Langsync)
			fmt.Fprintf(tw, "Domainradar:\t%s\n", cfg.Domainradar)
			fmt.Fprintf(tw, "Billing:\t%s\n", cfg.Billing)
			fmt.Fprintf(tw, "Prompthub:\t%s\n", cfg.Prompthub)
			fmt.Fprintln(tw)
			fmt.Fprintf(tw, "Logged in:\t%s\n", loggedIn)
			if cfg.User.Email != "" {
				fmt.Fprintf(tw, "User:\t%s <%s>\n", cfg.User.Name, cfg.User.Email)
			}
			if cfg.ActiveOrg.Slug != "" {
				fmt.Fprintf(tw, "Active org:\t%s (%s)\n", cfg.ActiveOrg.Slug, cfg.ActiveOrg.ID)
			}
			return tw.Flush()
		},
	}
}

func newConfigPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the path of the config file",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := config.Path()
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), path)
			return nil
		},
	}
}
