package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/norcubeplatform/cli/internal/api"
	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/config"
)

func newOrgCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "org",
		Short: "Manage the active Norcube organization",
		Long:  "Switch between organizations you have access to. Most other commands run against the active org unless --org is passed.",
	}
	cmd.AddCommand(newOrgListCmd(), newOrgUseCmd(), newOrgSwitchCmd(), newOrgCurrentCmd(), newOrgCreateCmd())
	return cmd
}

func newOrgCreateCmd() *cobra.Command {
	var (
		slug      string
		setActive bool
		setActiveExplicit bool
	)
	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create a new organization",
		Long: `Creates a new Norcube organization with you as the owner. The
name is required (positional or via the interactive prompt); slug
is optional — the server derives one from the name when omitted.

The new org is set as the active organization by default. Pass
--set-active=false to keep your current active org untouched (useful
when you just need the org to exist for a project-level
.langsync.json that pins it directly).

Examples:
  norcube org create
  norcube org create "Acme Inc"
  norcube org create "Acme Inc" --slug acme
  norcube org create "Acme Inc" --set-active=false`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			flags := clictx.Get(cmd)
			apiURL := flags.ResolveAuth(cfg)
			orgID, _ := flags.ResolveOrg(cfg)

			// Resolve name: positional first, then interactive
			// prompt when stdin is a TTY, else error out so scripts
			// fail fast.
			var name string
			if len(args) == 1 {
				name = strings.TrimSpace(args[0])
			}
			if name == "" {
				if !stdinIsInteractive() {
					return fmt.Errorf("organization name is required as a positional argument (or run interactively)")
				}
				if err := huh.NewInput().
					Title("Organization name").
					Description("Human-readable name; you can pick a URL slug separately.").
					Value(&name).
					Run(); err != nil {
					return err
				}
				name = strings.TrimSpace(name)
				if name == "" {
					return fmt.Errorf("organization name must not be empty")
				}
			}

			ts := auth.NewTokenSource(apiURL, auth.AudienceAuth, orgID)
			client := api.NewAuthClient(apiURL, ts.Token)

			var slugPtr *string
			if s := strings.TrimSpace(slug); s != "" {
				slugPtr = &s
			}
			created, err := client.CreateOrganization(cmd.Context(), name, slugPtr)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Created organization %q (slug: %s, id: %s).\n",
				created.Name, created.Slug, created.ID)

			// Decide whether to flip active_org. When --set-active
			// was given explicitly, honor it. Otherwise: default
			// true in an interactive shell (user just made the org,
			// presumably wants to work in it), false in scripts (no
			// silent state changes).
			wantActive := setActive
			if !setActiveExplicit {
				wantActive = stdinIsInteractive()
			}
			if wantActive {
				cfg.ActiveOrg = config.Organization{
					ID:   created.ID,
					Slug: created.Slug,
					Name: created.Name,
				}
				if err := config.Save(cfg); err != nil {
					return fmt.Errorf("save active org: %w", err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Active organization set to %s.\n", created.Slug)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&slug, "slug", "", "URL slug (optional — the server derives one from the name when omitted)")
	cmd.Flags().BoolVar(&setActive, "set-active", true, "Set the new org as the CLI's active org")
	// Hook into PreRun to detect whether --set-active was explicitly
	// passed (we want different defaults for interactive vs scripted).
	cmd.PreRun = func(cmd *cobra.Command, _ []string) {
		setActiveExplicit = cmd.Flags().Changed("set-active")
	}
	return cmd
}

// stdinIsInteractive is duplicated from internal/cli/langsync to
// keep this package from importing the langsync subpackage just for
// a one-liner. Same semantics: true when stdin is attached to a TTY.
func stdinIsInteractive() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

func newOrgSwitchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "switch",
		Short: "Pick an organization interactively (arrow keys / j,k to navigate, enter to select)",
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
			orgs, err := client.ListOrganizations(cmd.Context())
			if err != nil {
				return err
			}
			if len(orgs) == 0 {
				return fmt.Errorf("no organizations available")
			}

			options := make([]huh.Option[string], 0, len(orgs))
			for _, o := range orgs {
				label := o.Slug
				if o.Name != "" && o.Name != o.Slug {
					label = fmt.Sprintf("%s — %s", o.Slug, o.Name)
				}
				if o.Slug == cfg.ActiveOrg.Slug {
					label += "  (current)"
				}
				options = append(options, huh.NewOption(label, o.ID))
			}

			var selectedID string
			err = huh.NewSelect[string]().
				Title("Switch active organization").
				Options(options...).
				Value(&selectedID).
				Run()
			if err != nil {
				if err == huh.ErrUserAborted {
					fmt.Fprintln(cmd.OutOrStdout(), "Cancelled.")
					return nil
				}
				return err
			}

			var match *api.Organization
			for i := range orgs {
				if orgs[i].ID == selectedID {
					match = &orgs[i]
					break
				}
			}
			if match == nil {
				return fmt.Errorf("internal: selection %q not in org list", selectedID)
			}

			cfg.ActiveOrg = config.Organization{
				ID:   match.ID,
				Slug: match.Slug,
				Name: match.Name,
			}
			if err := config.Save(cfg); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Active organization set to %s.\n", match.Slug)
			return nil
		},
	}
}

func newOrgListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List organizations you can access",
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

			orgs, err := client.ListOrganizations(cmd.Context())
			if err != nil {
				return err
			}

			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ACTIVE\tSLUG\tNAME\tID")
			for _, o := range orgs {
				active := " "
				if o.Slug == cfg.ActiveOrg.Slug {
					active = "*"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", active, o.Slug, o.Name, o.ID)
			}
			return tw.Flush()
		},
	}
}

func newOrgUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <slug-or-id>",
		Short: "Set the active organization",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := strings.TrimSpace(args[0])
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
			orgs, err := client.ListOrganizations(cmd.Context())
			if err != nil {
				return err
			}

			var match *api.Organization
			for i := range orgs {
				if orgs[i].Slug == target || orgs[i].ID == target {
					match = &orgs[i]
					break
				}
			}
			if match == nil {
				return fmt.Errorf("no organization found matching %q — run `norcube org list`", target)
			}

			cfg.ActiveOrg = config.Organization{
				ID:   match.ID,
				Slug: match.Slug,
				Name: match.Name,
			}
			if err := config.Save(cfg); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Active organization set to %s (%s).\n", match.Slug, match.ID)
			return nil
		},
	}
}

func newOrgCurrentCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "current",
		Short: "Print the currently active organization",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if cfg.ActiveOrg.Slug == "" {
				return fmt.Errorf("no active organization — run `norcube org use <slug>`")
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s (%s)\n", cfg.ActiveOrg.Slug, cfg.ActiveOrg.ID)
			return nil
		},
	}
}
