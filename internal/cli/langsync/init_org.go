package langsync

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api"
	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/cli/langsync/projectcfg"
	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/config"
)

// resolveInitOrg picks the org to bake into the project config.
// Precedence:
//
//  1. --org flag (explicit; either slug or id; resolved via /organizations
//     so we end up with both id and slug for the config file).
//  2. existing project config's organization block (re-runs of init
//     keep the previously-pinned org instead of silently switching).
//  3. active_org from the CLI config: silently used when the user
//     has exactly one accessible org (typical case), or pre-selected
//     in the picker when there are several.
//
// Returns the org as a domain Organization with both ID and Slug
// populated — both fields are needed for the file format.
func resolveInitOrg(cmd *cobra.Command, existing *projectcfg.File) (api.Organization, error) {
	cfg, err := config.Load()
	if err != nil {
		return api.Organization{}, err
	}
	flags := clictx.Get(cmd)

	// Case 2: re-run with existing org block. Use it as-is — never
	// silently change the project's org because that would be
	// confusing on a follow-up init (e.g. dev adds a namespace and
	// wakes up to find the whole config retargeted at a different
	// org).
	if existing != nil && existing.Organization != nil && existing.Organization.ID != "" && !flags.HasOrgFlag() {
		return api.Organization{
			ID:   existing.Organization.ID,
			Name: existing.Organization.Name,
			Slug: existing.Organization.Slug,
		}, nil
	}

	// Case 1 & 3 both need the org list (case 1 to resolve a slug
	// to an id; case 3 to render the picker). One API call serves
	// both.
	authURL := flags.ResolveAuth(cfg)
	ts := auth.NewTokenSource(authURL, auth.AudienceAuth, cfg.ActiveOrg.ID)
	client := api.NewAuthClient(authURL, ts.Token)
	orgs, err := client.ListOrganizations(cmd.Context())
	if err != nil {
		return api.Organization{}, fmt.Errorf("list organizations: %w", err)
	}

	// Case 1: --org flag — match by id OR slug.
	if flags.HasOrgFlag() {
		if len(orgs) == 0 {
			return api.Organization{}, fmt.Errorf("--org %q given but you have access to no organizations yet — run `nrc org create` first", flags.OrgOverride)
		}
		want := strings.ToLower(strings.TrimSpace(flags.OrgOverride))
		for _, o := range orgs {
			if o.ID == flags.OrgOverride || strings.ToLower(o.Slug) == want {
				return o, nil
			}
		}
		return api.Organization{}, fmt.Errorf("--org %q didn't match any organization slug or id you have access to", flags.OrgOverride)
	}

	// Case zero-orgs: offer to create one inline. Non-interactive
	// shells fail with the same instruction as before.
	if len(orgs) == 0 {
		if !stdinIsInteractive() {
			return api.Organization{}, fmt.Errorf("you have no organizations yet — run `nrc org create <name>` first (non-interactive shell can't show the prompt)")
		}
		fmt.Fprintln(cmd.OutOrStdout(), "You don't have any organization yet — creating one to pin this project to.")
		return createOrgInteractive(cmd.Context(), client)
	}

	// Case 3a: single org → silent.
	if len(orgs) == 1 {
		return orgs[0], nil
	}

	// Case 3b: multiple orgs → interactive picker, preselect active,
	// with a "+ Create new" sentinel at the bottom for users who
	// want to spin up a fresh org without leaving the wizard.
	if !stdinIsInteractive() {
		return api.Organization{}, fmt.Errorf(
			"you have access to %d organizations — pass --org <slug> to pick one (non-interactive shell can't show the picker)",
			len(orgs))
	}
	const createSentinel = "__norcube_create_org__"
	options := make([]huh.Option[string], 0, len(orgs)+1)
	for _, o := range orgs {
		label := o.Slug
		if o.Name != "" && o.Name != o.Slug {
			label = fmt.Sprintf("%s — %s", o.Slug, o.Name)
		}
		if o.Slug == cfg.ActiveOrg.Slug {
			label += "  (active)"
		}
		options = append(options, huh.NewOption(label, o.ID))
	}
	options = append(options, huh.NewOption("+ Create new organization…", createSentinel))

	var selectedID string
	if cfg.ActiveOrg.ID != "" {
		selectedID = cfg.ActiveOrg.ID
	}
	err = huh.NewSelect[string]().
		Title("Which organization does this project belong to?").
		Description("The choice is baked into .langsync.json so sync always targets the right org regardless of `nrc org use`.").
		Options(options...).
		Value(&selectedID).
		Run()
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return api.Organization{}, ErrCancelled
		}
		return api.Organization{}, err
	}
	if selectedID == createSentinel {
		return createOrgInteractive(cmd.Context(), client)
	}
	for _, o := range orgs {
		if o.ID == selectedID {
			return o, nil
		}
	}
	return api.Organization{}, fmt.Errorf("internal: selection %q not found in org list", selectedID)
}

// createOrgInteractive runs an inline "name + optional slug" prompt
// pair, POSTs to /organizations, and returns the new org. Used both
// by the zero-orgs path (no choice but to create) and the
// "+ Create new" picker option (user picked it explicitly).
//
// The created org is NOT set as active_org — init pins the project
// to it via .langsync.json, and we don't want a side effect on the
// user's global active context.
func createOrgInteractive(ctx context.Context, client *api.AuthClient) (api.Organization, error) {
	var name, slug string
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("New organization name").
				Description("Human-readable name; you can pick a URL slug below.").
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return fmt.Errorf("name must not be empty")
					}
					return nil
				}).
				Value(&name),
			huh.NewInput().
				Title("Slug (optional)").
				Description("Lowercase URL identifier. Leave blank to let the server derive one from the name.").
				Value(&slug),
		),
	)
	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return api.Organization{}, ErrCancelled
		}
		return api.Organization{}, err
	}
	name = strings.TrimSpace(name)
	slug = strings.TrimSpace(slug)
	var slugPtr *string
	if slug != "" {
		slugPtr = &slug
	}
	created, err := client.CreateOrganization(ctx, name, slugPtr)
	if err != nil {
		return api.Organization{}, fmt.Errorf("create organization: %w", err)
	}
	return *created, nil
}
