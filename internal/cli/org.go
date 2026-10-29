package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api"
	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/config"
	"github.com/norcubeplatform/cli/internal/orgs"
	"github.com/norcubeplatform/cli/internal/output"
	"github.com/norcubeplatform/cli/internal/term"
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
		slug              string
		setActive         bool
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

`,
		Example: `  norcube org create
  norcube org create "Acme Inc"
  norcube org create "Acme Inc" --slug acme
  norcube org create "Acme Inc" --set-active=false -o json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := clictx.Get(cmd).Config()
			if err != nil {
				return err
			}
			flags := clictx.Get(cmd)
			org, err := flags.Org(cmd.Context(), cfg)
			if err != nil {
				return err
			}

			// Resolve name: positional first, then interactive
			// prompt when stdin is a TTY, else error out so scripts
			// fail fast.
			var name string
			if len(args) == 1 {
				name = strings.TrimSpace(args[0])
			}
			if name == "" {
				if !term.StdinIsTerminal() {
					return clierr.Usage(errors.New("organization name is required as a positional argument (or run interactively)"))
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

			client, err := flags.AuthClient(cfg, org.ID)
			if err != nil {
				return err
			}

			var slugPtr *string
			if s := strings.TrimSpace(slug); s != "" {
				slugPtr = &s
			}
			created, err := client.CreateOrganization(cmd.Context(), name, slugPtr)
			if err != nil {
				return err
			}
			msg := fmt.Sprintf("Created organization %q (slug: %s, id: %s).", created.Name, created.Slug, created.ID)

			// Decide whether to flip active_org. When --set-active
			// was given explicitly, honor it. Otherwise: default
			// true in an interactive shell (user just made the org,
			// presumably wants to work in it), false in scripts (no
			// silent state changes).
			wantActive := setActive
			if !setActiveExplicit {
				wantActive = term.StdinIsTerminal()
			}
			if wantActive {
				if err := clictx.Get(cmd).SetActiveOrg(config.Organization{
					ID:   created.ID,
					Slug: created.Slug,
					Name: created.Name,
				}); err != nil {
					return fmt.Errorf("save active org: %w", err)
				}
				msg += fmt.Sprintf("\nActive organization set to %s.", created.Slug)
			}
			ref := orgRef{ID: created.ID, Slug: created.Slug, Name: created.Name}
			return cmdutil.Print(cmd, output.Done{
				Message: msg,
				Value:   orgCreateResult{Organization: ref, Active: wantActive},
				Fields:  []output.Field{{Value: ref.ID}, {Value: ref.Slug}, {Value: ref.Name}},
			})
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

func newOrgSwitchCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "switch",
		Short:   "Pick an organization interactively (arrow keys / j,k to navigate, enter to select)",
		Example: `  norcube org switch`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !term.StdinIsTerminal() {
				return clierr.Usage(errors.New("org switch needs an interactive terminal; use `norcube org use <slug>` in scripts"))
			}
			cfg, err := clictx.Get(cmd).Config()
			if err != nil {
				return err
			}
			flags := clictx.Get(cmd)
			org, err := flags.Org(cmd.Context(), cfg)
			if err != nil {
				return err
			}

			client, err := flags.AuthClient(cfg, org.ID)
			if err != nil {
				return err
			}
			list, err := client.ListOrganizations(cmd.Context())
			if err != nil {
				return err
			}
			if len(list) == 0 {
				return fmt.Errorf("no organizations available")
			}

			options := make([]huh.Option[string], 0, len(list))
			for _, o := range list {
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
					fmt.Fprintln(cmd.ErrOrStderr(), "Cancelled.")
					return nil
				}
				return err
			}

			var match *api.Organization
			for i := range list {
				if list[i].ID == selectedID {
					match = &list[i]
					break
				}
			}
			if match == nil {
				return fmt.Errorf("internal: selection %q not in org list", selectedID)
			}

			if err := clictx.Get(cmd).SetActiveOrg(config.Organization{
				ID:   match.ID,
				Slug: match.Slug,
				Name: match.Name,
			}); err != nil {
				return err
			}
			return cmdutil.Print(cmd, orgSet(*match, fmt.Sprintf("Active organization set to %s.", match.Slug)))
		},
	}
}

func newOrgListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List organizations you can access",
		Example: `  norcube org list
  norcube org list -o json --query "[?active].slug"`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := clictx.Get(cmd).Config()
			if err != nil {
				return err
			}
			flags := clictx.Get(cmd)
			org, err := flags.Org(cmd.Context(), cfg)
			if err != nil {
				return err
			}

			client, err := flags.AuthClient(cfg, org.ID)
			if err != nil {
				return err
			}

			list, err := client.ListOrganizations(cmd.Context())
			if err != nil {
				return err
			}

			items := make([]orgListItem, len(list))
			for i, o := range list {
				items[i] = orgListItem{ID: o.ID, Slug: o.Slug, Name: o.Name, Active: o.ID == cfg.ActiveOrg.ID}
			}
			return cmdutil.Print(cmd, output.Table[orgListItem]{
				Headers: []string{"ACTIVE", "SLUG", "NAME", "ID"},
				Rows: func(o orgListItem) []string {
					active := " "
					if o.Active {
						active = "*"
					}
					return []string{active, o.Slug, o.Name, o.ID}
				},
				Items: items,
				Style: output.NoStyle,
			})
		},
	}
}

func newOrgUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <slug-or-id>",
		Short: "Set the active organization",
		Example: `  norcube org use acme
  norcube org use 0b9d2b8e-1c1f-4c3e-9a51-6d1c2f9a7e01`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := strings.TrimSpace(args[0])
			cfg, err := clictx.Get(cmd).Config()
			if err != nil {
				return err
			}
			flags := clictx.Get(cmd)
			org, err := flags.Org(cmd.Context(), cfg)
			if err != nil {
				return err
			}

			client, err := flags.AuthClient(cfg, org.ID)
			if err != nil {
				return err
			}
			list, err := client.ListOrganizations(cmd.Context())
			if err != nil {
				return err
			}

			found, ok := orgs.Find(list, target)
			if !ok {
				return clierr.Usage(&orgs.NotFoundError{Value: target})
			}
			match := &found

			if err := clictx.Get(cmd).SetActiveOrg(config.Organization{
				ID:   match.ID,
				Slug: match.Slug,
				Name: match.Name,
			}); err != nil {
				return err
			}
			return cmdutil.Print(cmd, orgSet(*match, fmt.Sprintf("Active organization set to %s (%s).", match.Slug, match.ID)))
		},
	}
}

func newOrgCurrentCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "current",
		Short: "Print the currently active organization",
		Example: `  norcube org current
  norcube org current -o text`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := clictx.Get(cmd).Config()
			if err != nil {
				return err
			}
			if cfg.ActiveOrg.Slug == "" && cfg.ActiveOrg.ID == "" {
				return clierr.Usage(errors.New("no active organization. Run `norcube org use <slug>`"))
			}
			ref := orgRefOf(cfg.ActiveOrg)
			return cmdutil.Print(cmd, output.Done{
				Message: fmt.Sprintf("%s (%s)", cfg.ActiveOrg.Slug, cfg.ActiveOrg.ID),
				Value:   ref,
				Fields:  []output.Field{{Value: ref.ID}, {Value: ref.Slug}, {Value: ref.Name}},
			})
		},
	}
}

// orgListItem is one row of `org list`.
type orgListItem struct {
	ID     string `json:"id" yaml:"id"`
	Slug   string `json:"slug" yaml:"slug"`
	Name   string `json:"name" yaml:"name"`
	Active bool   `json:"active" yaml:"active"`
}

type orgCreateResult struct {
	Organization orgRef `json:"organization" yaml:"organization"`
	Active       bool   `json:"active" yaml:"active"`
}

// orgSet is the result of making o the active organization.
func orgSet(o api.Organization, msg string) output.Done {
	ref := orgRef{ID: o.ID, Slug: o.Slug, Name: o.Name}
	return output.Done{
		Message: msg,
		Value:   ref,
		Fields:  []output.Field{{Value: ref.ID}, {Value: ref.Slug}, {Value: ref.Name}},
	}
}
