package cli

import (
	"sort"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/completion"
	"github.com/norcubeplatform/cli/internal/config"
)

const completeOrganizations = "organizations"

// registerCompletions wires the account-level completions: organization
// slugs for --org and `org use`, profile names for --profile.
func registerCompletions(root *cobra.Command) {
	orgs := completion.Flag(completeOrganizations, organizationCandidates)
	_ = root.RegisterFlagCompletionFunc("org", orgs)
	_ = root.RegisterFlagCompletionFunc("profile", profileCandidates)
	if use, _, err := root.Find([]string{"org", "use"}); err == nil && use.Name() == "use" {
		use.ValidArgsFunction = completion.Func(completeOrganizations, 1, organizationCandidates)
	}
}

func organizationCandidates(cmd *cobra.Command) ([]cobra.Completion, error) {
	flags := clictx.Get(cmd)
	cfg, err := flags.Config()
	if err != nil {
		return nil, err
	}
	client, err := flags.AuthClient(cfg, cfg.ActiveOrg.ID)
	if err != nil {
		return nil, err
	}
	list, err := client.ListOrganizations(cmd.Context())
	if err != nil {
		return nil, err
	}
	out := make([]cobra.Completion, 0, len(list))
	for _, o := range list {
		out = append(out, o.Slug+"\t"+o.Name)
	}
	return out, nil
}

// profileCandidates reads the config file only, so it needs no cache.
func profileCandidates(*cobra.Command, []string, string) ([]cobra.Completion, cobra.ShellCompDirective) {
	f, err := config.Load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names := make([]cobra.Completion, 0, len(f.Profiles))
	for name := range f.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, cobra.ShellCompDirectiveNoFileComp
}
