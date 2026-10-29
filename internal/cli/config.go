package cli

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/config"
	"github.com/norcubeplatform/cli/internal/output"
	"github.com/norcubeplatform/cli/internal/resolve"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect the local CLI config and its profiles",
		Long: `Inspect the configuration file and the values a command would use.

Change settings with ` + "`norcube configure`" + `.`,
	}
	cmd.AddCommand(newConfigListCmd(), newConfigProfilesCmd(), newConfigPathCmd(), newConfigEditCmd(), newConfigResetURLsCmd())
	return cmd
}

// setting is one row of `config list`.
type setting struct {
	Name   string `json:"name" yaml:"name"`
	Value  string `json:"value" yaml:"value"`
	Source string `json:"source" yaml:"source"`
}

func newConfigListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"show"},
		Short:   "Show the settings a command would use, and where each comes from",
		Example: `  norcube config list
  norcube config list --profile client-acme
  norcube config list -o json --query "[?name=='endpoint.auth'].value | [0]"`,
		Long: `Shows the effective profile, environment, endpoints, organization,
credential store and output format after merging flags, NORCUBE_*
environment variables and the config file, with the source of each value.
The first stop for "why is the CLI hitting the wrong host?".`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags := clictx.Get(cmd)
			res, err := flags.Resolve()
			if err != nil {
				return err
			}
			cfg := res.Config()
			path, _ := config.Path()
			rows := []setting{
				{"config_file", path, configFileSource()},
				{"profile", res.Profile.Value, res.Profile.Source},
				{"environment", res.Environment.Value, res.Environment.Source},
			}
			for _, svc := range config.Services {
				v := res.Endpoints[svc]
				rows = append(rows, setting{"endpoint." + svc, v.Value, v.Source})
			}
			org := res.Org
			if org.Value == "" {
				org = resolve.Value{Value: "(none)", Source: "-"}
			}
			rows = append(rows,
				setting{"organization", org.Value, org.Source},
				setting{"credential_store", res.Credentials.Value, res.Credentials.Source},
				setting{"output", res.Output.Value, res.Output.Source},
			)
			signedIn := "no"
			if static, _ := flags.StaticToken(); static != "" {
				signedIn = "token"
			} else if auth.HasCredentials(cfg.Profile, cfg.Auth) {
				signedIn = "yes"
			}
			user := "-"
			if cfg.User.Email != "" {
				user = fmt.Sprintf("%s <%s>", cfg.User.Name, cfg.User.Email)
			}
			rows = append(rows,
				setting{"signed_in", signedIn, "credential store"},
				setting{"user", user, "profile " + cfg.Profile},
			)
			return cmdutil.Print(cmd, output.Table[setting]{
				Headers: []string{"NAME", "VALUE", "SOURCE"},
				Rows:    func(s setting) []string { return []string{s.Name, s.Value, s.Source} },
				Items:   rows,
			})
		},
	}
}

func configFileSource() string {
	if os.Getenv(config.EnvConfigFile) != "" {
		return "env " + config.EnvConfigFile
	}
	return "default"
}

// profileRow is one row of `config profiles`.
type profileRow struct {
	Name         string `json:"name" yaml:"name"`
	Default      bool   `json:"default" yaml:"default"`
	Selected     bool   `json:"selected" yaml:"selected"`
	Environment  string `json:"environment" yaml:"environment"`
	Organization string `json:"organization" yaml:"organization"`
	User         string `json:"user" yaml:"user"`
}

func newConfigProfilesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "profiles",
		Short: "List the profiles in the config file",
		Example: `  norcube config profiles
  norcube config profiles -o json --query "[?default].name"`,
		Long: "Lists every profile; * marks the one this command would use.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags := clictx.Get(cmd)
			f, err := flags.ConfigFile()
			if err != nil {
				return err
			}
			res, err := flags.Resolve()
			if err != nil {
				return err
			}
			defaultName := f.DefaultProfile
			if defaultName == "" {
				defaultName = config.DefaultProfileName
			}
			rows := []profileRow{}
			for _, name := range resolve.ProfileNames(f) {
				p := f.Profiles[name]
				env := p.Environment
				if env == "" {
					env = config.EnvProduction
				}
				rows = append(rows, profileRow{
					Name:         name,
					Default:      name == defaultName,
					Selected:     name == res.Profile.Value,
					Environment:  env,
					Organization: p.Organization,
					User:         p.User.Email,
				})
			}
			if len(rows) == 0 && cmdutil.Human(cmd) {
				fmt.Fprintln(cmd.ErrOrStderr(), "No profiles yet. Run `norcube login` (or `norcube configure`) to create one.")
				return nil
			}
			return cmdutil.Print(cmd, output.Table[profileRow]{
				Headers: []string{"", "PROFILE", "ENVIRONMENT", "ORGANIZATION", "USER", "DEFAULT"},
				Rows: func(r profileRow) []string {
					mark, def := "", ""
					if r.Selected {
						mark = "*"
					}
					if r.Default {
						def = "yes"
					}
					return []string{mark, r.Name, r.Environment, orDash(r.Organization), orDash(r.User), def}
				},
				Items: rows,
			})
		},
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func newConfigPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the path of the config file",
		Example: `  norcube config path
  cat "$(norcube config path)"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := config.Path()
			if err != nil {
				return err
			}
			return cmdutil.Print(cmd, output.Done{
				Message: path,
				Value:   map[string]string{"path": path},
				Fields:  []output.Field{{Value: path}},
			})
		},
	}
}

func newConfigEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "edit",
		Short: "Open the config file in $VISUAL or $EDITOR",
		Example: `  norcube config edit
  EDITOR=nano norcube config edit`,
		Long: `Opens the config file in your editor ($VISUAL, then $EDITOR, else vi, or
notepad on Windows) and checks it when the editor exits.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := config.Path()
			if err != nil {
				return err
			}
			// Create (or migrate) the file first so the editor opens the
			// current format.
			if _, err := config.Update(func(*config.File) error { return nil }); err != nil {
				return err
			}
			editor := os.Getenv("VISUAL")
			if editor == "" {
				editor = os.Getenv("EDITOR")
			}
			if editor == "" {
				editor = "vi"
				if runtime.GOOS == "windows" {
					editor = "notepad"
				}
			}
			parts := strings.Fields(editor)
			c := exec.CommandContext(cmd.Context(), parts[0], append(parts[1:], path)...) // #nosec G204 G702 -- the user's own editor on their own config file
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := c.Run(); err != nil {
				return fmt.Errorf("run editor %q: %w", editor, err)
			}
			if _, err := config.Load(); err != nil {
				return fmt.Errorf("the edited config doesn't parse: %w", err)
			}
			return nil
		},
	}
}

func newConfigResetURLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reset-urls",
		Short: "Remove the selected profile's endpoint overrides",
		Example: `  norcube config reset-urls
  norcube config reset-urls --profile client-acme`,
		Long: `Makes the selected profile use its environment's built-in endpoints again
by removing every [profiles.<name>.endpoints] override. The environment,
organization and session stay.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags := clictx.Get(cmd)
			if err := flags.UpdateProfile(func(p *config.Profile) error {
				p.Endpoints = nil
				return nil
			}); err != nil {
				return err
			}
			res, err := flags.Resolve()
			if err != nil {
				return err
			}
			var msg strings.Builder
			fmt.Fprintf(&msg, "Profile %s now uses the %s endpoints:", res.Profile.Value, res.Environment.Value)
			endpoints := map[string]string{}
			for _, svc := range config.Services {
				endpoints[svc] = res.Endpoints[svc].Value
				fmt.Fprintf(&msg, "\n  %-12s %s", svc, res.Endpoints[svc].Value)
			}
			return cmdutil.Print(cmd, output.Done{
				Message: msg.String(),
				Value: resetURLsResult{
					Profile:     res.Profile.Value,
					Environment: res.Environment.Value,
					Endpoints:   endpoints,
				},
			})
		},
	}
}

type resetURLsResult struct {
	Profile     string            `json:"profile" yaml:"profile"`
	Environment string            `json:"environment" yaml:"environment"`
	Endpoints   map[string]string `json:"endpoints" yaml:"endpoints"`
}
