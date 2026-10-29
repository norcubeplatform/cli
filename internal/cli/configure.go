package cli

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/config"
	"github.com/norcubeplatform/cli/internal/output"
	"github.com/norcubeplatform/cli/internal/resolve"
	"github.com/norcubeplatform/cli/internal/term"
	"github.com/norcubeplatform/cli/internal/transport"
)

func newConfigureCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "configure",
		Short: "Create or change a profile",
		Long: `Without a subcommand, asks for the selected profile's organization and
where to keep its session (creating the profile if needed). Profiles use
Norcube production; select one with --profile. Most people only ever use
the default profile; extra profiles keep several accounts or organizations
side by side.

` + "`configure set|get|unset <key>`" + ` change single settings. Profile keys
apply to the selected profile:

  environment         production (default) | custom (self-hosted or proxied endpoints)
  organization        slug or id of the organization commands target
  credential_store    keyring | file | env (env reads NORCUBE_TOKEN)
  output              table | json | yaml | text
  endpoints.<service> URL override; services: ` + strings.Join(config.Services, ", ") + `

Global keys:

  default_profile          profile used when --profile and NORCUBE_PROFILE are unset
  defaults.output          table | json | yaml | text
  defaults.pager           true | false
  defaults.max_attempts    attempts per request (1 disables retries)
  defaults.read_timeout    seconds or a duration such as 90s
  defaults.connect_timeout seconds or a duration such as 10s`,
		Example: `  norcube configure
  norcube configure --profile client-acme
  norcube configure set organization acme
  norcube configure set default_profile work
  norcube configure set endpoints.langsync http://localhost:4600 --profile local
  norcube configure get organization
  norcube configure unset endpoints.langsync --profile local`,
		Args: cobra.NoArgs,
		RunE: runConfigureWizard,
	}
	cmd.AddCommand(newConfigureSetCmd(), newConfigureGetCmd(), newConfigureUnsetCmd())
	return cmd
}

func runConfigureWizard(cmd *cobra.Command, _ []string) error {
	if !term.StdinIsTerminal() {
		return clierr.Usage(errors.New("configure needs an interactive terminal; use `norcube configure set <key> <value>` in scripts"))
	}
	flags := clictx.Get(cmd)
	res, err := flags.Resolve()
	if err != nil {
		return err
	}
	name := res.Profile.Value
	p := res.StoredProfile()

	org := p.Organization
	env := p.Environment
	if env == "" {
		env = config.EnvProduction
	}
	store := p.CredentialStore
	if store == "" {
		store = config.CredentialKeyring
	}

	// The wizard doesn't ask for an environment: profiles use production.
	// Custom endpoints (self-hosted, proxies) are set with `configure set`.
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title(fmt.Sprintf("Organization for profile %q", name)).
				Description("Slug or id. Leave empty to use your default organization after login.").
				Value(&org),
			huh.NewSelect[string]().
				Title("Where to keep the session").
				Options(
					huh.NewOption("OS keyring", config.CredentialKeyring),
					huh.NewOption("File next to the config (0600)", config.CredentialFile),
				).
				Value(&store),
		),
	)
	if err := form.RunWithContext(cmd.Context()); err != nil {
		return err
	}

	_, err = config.Update(func(f *config.File) error {
		_, existed := f.Profiles[name]
		prof := f.Profile(name)
		// Keep an environment set earlier (custom endpoints); new profiles
		// are production.
		if prof.Environment == "" {
			prof.Environment = env
		}
		prof.CredentialStore = store
		if strings.TrimSpace(org) != prof.Organization {
			prof.SetActiveOrg(config.Organization{Slug: strings.TrimSpace(org)})
		}
		if !existed && f.DefaultProfile == "" && len(f.Profiles) == 1 {
			f.DefaultProfile = name
		}
		return nil
	})
	if err != nil {
		return err
	}
	return cmdutil.Print(cmd, output.Done{
		Message: fmt.Sprintf("Saved profile %s.\nSign in with: norcube login%s", name, profileFlag(name)),
		Value: configureResult{
			Profile:         name,
			Environment:     env,
			Organization:    strings.TrimSpace(org),
			CredentialStore: store,
		},
	})
}

func setEndpoint(p *config.Profile, svc, url string) {
	url = strings.TrimSpace(url)
	if url == "" {
		delete(p.Endpoints, svc)
		return
	}
	p.Endpoints[svc] = strings.TrimRight(url, "/")
}

// profileFlag is " --profile <name>" for a non-default profile.
func profileFlag(name string) string {
	if name == "" || name == config.DefaultProfileName {
		return ""
	}
	return " --profile " + name
}

// configKeys lists every key configure set/get/unset accept.
func configKeys() []string {
	keys := []string{
		"environment", "organization", "credential_store", "output",
		"default_profile",
		"defaults.output", "defaults.pager", "defaults.max_attempts",
		"defaults.read_timeout", "defaults.connect_timeout",
	}
	for _, s := range config.Services {
		keys = append(keys, "endpoints."+s)
	}
	sort.Strings(keys)
	return keys
}

func unknownKey(key string) error {
	return clierr.Usage(fmt.Errorf("unknown setting %q; one of: %s", key, strings.Join(configKeys(), ", ")))
}

func newConfigureSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set one setting (run `norcube configure --help` for the keys)",
		Example: `  norcube configure set organization acme
  norcube configure set defaults.output json`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value := args[0], strings.TrimSpace(args[1])
			if key == "organization" {
				return setOrganization(cmd, value)
			}
			flags := clictx.Get(cmd)
			res, err := flags.Resolve()
			if err != nil {
				return err
			}
			name := res.Profile.Value
			if _, err := config.Update(func(f *config.File) error {
				return applySetting(f, name, key, value)
			}); err != nil {
				return err
			}
			return cmdutil.Print(cmd, settingDone(fmt.Sprintf("Set %s = %s%s.", key, value, scopeNote(key, name)), key, value, name))
		},
	}
}

func scopeNote(key, profile string) string {
	if key == "default_profile" || strings.HasPrefix(key, "defaults.") {
		return ""
	}
	return " (profile " + profile + ")"
}

// applySetting validates and stores one key. An empty value removes it.
func applySetting(f *config.File, profile, key, value string) error {
	usage := func(format string, a ...any) error { return clierr.Usage(fmt.Errorf(format, a...)) }
	switch key {
	case "default_profile":
		if value != "" {
			if err := resolve.ValidateProfileName(value); err != nil {
				return clierr.Usage(err)
			}
		}
		f.DefaultProfile = value
		return nil
	case "defaults.output":
		if value != "" && !validFormat(value) {
			return usage("defaults.output: want table, json, yaml or text")
		}
		f.Defaults.Output = value
		return nil
	case "defaults.pager":
		if value == "" {
			f.Defaults.Pager = nil
			return nil
		}
		b, err := strconv.ParseBool(value)
		if err != nil {
			return usage("defaults.pager: want true or false")
		}
		f.Defaults.Pager = &b
		return nil
	case "defaults.max_attempts":
		if value == "" {
			f.Defaults.MaxAttempts = 0
			return nil
		}
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 {
			return usage("defaults.max_attempts: want a whole number of at least 1")
		}
		f.Defaults.MaxAttempts = n
		return nil
	case "defaults.read_timeout", "defaults.connect_timeout":
		if value != "" {
			if _, err := transport.ParseTimeout(value); err != nil {
				return usage("%s: %v", key, err)
			}
		}
		if key == "defaults.read_timeout" {
			f.Defaults.ReadTimeout = value
		} else {
			f.Defaults.ConnectTimeout = value
		}
		return nil
	}

	p := f.Profile(profile)
	switch {
	case key == "environment":
		if value != "" && !config.IsEnvironment(value) {
			return usage("environment: want production or custom")
		}
		p.Environment = value
	case key == "credential_store":
		if value != "" && !config.IsCredentialStore(value) {
			return usage("credential_store: want keyring, file or env")
		}
		p.CredentialStore = value
	case key == "output":
		if value != "" && !validFormat(value) {
			return usage("output: want table, json, yaml or text")
		}
		p.Output = value
	case strings.HasPrefix(key, "endpoints."):
		svc := strings.TrimPrefix(key, "endpoints.")
		if !config.IsService(svc) {
			return unknownKey(key)
		}
		if p.Endpoints == nil {
			p.Endpoints = map[string]string{}
		}
		setEndpoint(p, svc, value)
	default:
		return unknownKey(key)
	}
	return nil
}

func validFormat(v string) bool {
	return v == "table" || v == "json" || v == "yaml" || v == "text"
}

// setOrganization records the profile's organization. With a session it
// is looked up right away (so a typo fails now, not on the next command);
// without one it is resolved at the next login.
func setOrganization(cmd *cobra.Command, value string) error {
	flags := clictx.Get(cmd)
	cfg, err := flags.Config()
	if err != nil {
		return err
	}
	if value == "" || !auth.HasCredentials(cfg.Profile, cfg.Auth) || cfg.ActiveOrg.ID == "" {
		if err := flags.SetActiveOrg(config.Organization{Slug: value}); err != nil {
			return err
		}
		msg := fmt.Sprintf("Set organization = %s (profile %s); it is looked up at the next login.", value, cfg.Profile)
		if value == "" {
			msg = fmt.Sprintf("Cleared organization (profile %s).", cfg.Profile)
		}
		return cmdutil.Print(cmd, settingDone(msg, "organization", value, cfg.Profile))
	}
	flags.OrgOverride = value
	org, err := flags.Org(cmd.Context(), cfg)
	flags.OrgOverride = ""
	if err != nil {
		return err
	}
	if err := flags.SetActiveOrg(config.Organization{ID: org.ID, Slug: org.Slug, Name: org.Name}); err != nil {
		return err
	}
	label := orgLabel(config.Organization{ID: org.ID, Slug: org.Slug})
	return cmdutil.Print(cmd, settingDone(fmt.Sprintf("Set organization = %s (profile %s).", label, cfg.Profile), "organization", label, cfg.Profile))
}

func newConfigureGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <key>",
		Short: "Print one setting as stored in the config file",
		Example: `  norcube configure get organization
  norcube configure get endpoints.langsync --profile local`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := clictx.Get(cmd)
			f, err := flags.ConfigFile()
			if err != nil {
				return err
			}
			res, err := flags.Resolve()
			if err != nil {
				return err
			}
			v, err := getSetting(f, res.Profile.Value, args[0])
			if err != nil {
				return err
			}
			return cmdutil.Print(cmd, settingDone(v, args[0], v, res.Profile.Value))
		},
	}
}

func getSetting(f *config.File, profile, key string) (string, error) {
	p := f.Profiles[profile]
	if p == nil {
		p = &config.Profile{}
	}
	switch {
	case key == "default_profile":
		return f.DefaultProfile, nil
	case key == "defaults.output":
		return f.Defaults.Output, nil
	case key == "defaults.pager":
		if f.Defaults.Pager == nil {
			return "", nil
		}
		return strconv.FormatBool(*f.Defaults.Pager), nil
	case key == "defaults.max_attempts":
		if f.Defaults.MaxAttempts == 0 {
			return "", nil
		}
		return strconv.Itoa(f.Defaults.MaxAttempts), nil
	case key == "defaults.read_timeout":
		return f.Defaults.ReadTimeout, nil
	case key == "defaults.connect_timeout":
		return f.Defaults.ConnectTimeout, nil
	case key == "environment":
		return p.Environment, nil
	case key == "organization":
		return p.Organization, nil
	case key == "credential_store":
		return p.CredentialStore, nil
	case key == "output":
		return p.Output, nil
	case strings.HasPrefix(key, "endpoints.") && config.IsService(strings.TrimPrefix(key, "endpoints.")):
		return p.Endpoints[strings.TrimPrefix(key, "endpoints.")], nil
	}
	return "", unknownKey(key)
}

func newConfigureUnsetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unset <key>",
		Short: "Remove one setting from the config file",
		Example: `  norcube configure unset endpoints.langsync --profile local
  norcube configure unset defaults.output`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			if key == "organization" {
				return setOrganization(cmd, "")
			}
			flags := clictx.Get(cmd)
			res, err := flags.Resolve()
			if err != nil {
				return err
			}
			name := res.Profile.Value
			if _, err := config.Update(func(f *config.File) error {
				return applySetting(f, name, key, "")
			}); err != nil {
				return err
			}
			return cmdutil.Print(cmd, settingDone(fmt.Sprintf("Unset %s%s.", key, scopeNote(key, name)), key, "", name))
		},
	}
}

// settingResult is what configure set/get/unset print with -o json.
type settingResult struct {
	Key     string `json:"key" yaml:"key"`
	Value   string `json:"value" yaml:"value"`
	Profile string `json:"profile" yaml:"profile"`
}

func settingDone(msg, key, value, profile string) output.Done {
	return output.Done{
		Message: msg,
		Value:   settingResult{Key: key, Value: value, Profile: profile},
		Fields:  []output.Field{{Value: value}},
	}
}

type configureResult struct {
	Profile         string `json:"profile" yaml:"profile"`
	Environment     string `json:"environment" yaml:"environment"`
	Organization    string `json:"organization" yaml:"organization"`
	CredentialStore string `json:"credentialStore" yaml:"credentialStore"`
}
