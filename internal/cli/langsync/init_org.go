package langsync

import (
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
	if len(orgs) == 0 {
		return api.Organization{}, fmt.Errorf("you don't have access to any organization — visit the web app to get added to one")
	}

	// Case 1: --org flag — match by id OR slug.
	if flags.HasOrgFlag() {
		want := strings.ToLower(strings.TrimSpace(flags.OrgOverride))
		for _, o := range orgs {
			if o.ID == flags.OrgOverride || strings.ToLower(o.Slug) == want {
				return o, nil
			}
		}
		return api.Organization{}, fmt.Errorf("--org %q didn't match any organization slug or id you have access to", flags.OrgOverride)
	}

	// Case 3a: single org → silent.
	if len(orgs) == 1 {
		return orgs[0], nil
	}

	// Case 3b: multiple orgs → interactive picker, preselect active.
	if !stdinIsInteractive() {
		return api.Organization{}, fmt.Errorf(
			"you have access to %d organizations — pass --org <slug> to pick one (non-interactive shell can't show the picker)",
			len(orgs))
	}
	options := make([]huh.Option[string], 0, len(orgs))
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
	for _, o := range orgs {
		if o.ID == selectedID {
			return o, nil
		}
	}
	return api.Organization{}, fmt.Errorf("internal: selection %q not found in org list", selectedID)
}
