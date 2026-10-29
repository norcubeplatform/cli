package cli

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/buildinfo"
	"github.com/norcubeplatform/cli/internal/cli/backup"
	"github.com/norcubeplatform/cli/internal/cli/domainradar"
	"github.com/norcubeplatform/cli/internal/cli/langsync"
	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/completion"
	"github.com/norcubeplatform/cli/internal/output"
	"github.com/norcubeplatform/cli/internal/registry"
	"github.com/norcubeplatform/cli/internal/term"
)

// NewRootCmd builds the command tree. Run it with ExecuteContext and a
// context that is cancelled on SIGINT/SIGTERM (see cmd/norcube).

func NewRootCmd() *cobra.Command {
	flags := &clictx.Flags{}
	var noColor bool

	cmd := &cobra.Command{
		Use:           "norcube",
		Short:         "Command-line interface for the Norcube platform",
		Long:          "Norcube CLI — manage backups, namespaces, and more across your Norcube services from the terminal.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       fmt.Sprintf("%s (commit %s, built %s)", buildinfo.Version, buildinfo.Commit, buildinfo.Date),
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			flags.CaptureChanged(cmd)
			term.SetNoColor(noColor)
			// A --query typo fails before the command does any work.
			if flags.Query != "" {
				if err := output.CompileQuery(flags.Query); err != nil {
					return clierr.Usage(err)
				}
			}
			if err := flags.ApplyPreferences(); err != nil {
				return err
			}
			selectedOutput = flags.Output
			if res, err := flags.Resolve(); err == nil {
				selectedProfile = res.Profile.Value
				// --profile or NORCUBE_PROFILE picked it; the hint then
				// names it even when it is the default.
				selectedProfileExplicit = !strings.HasPrefix(res.Profile.Source, "default")
			}
			clictx.Attach(cmd, ctx, flags)
			StartUpdateCheck(ctx, cmd.Name())
			return nil
		},
	}
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return clierr.Usage(err)
	})

	cmd.PersistentFlags().StringVar(&flags.Profile, "profile", "", "Use a named profile from the config file (env: NORCUBE_PROFILE)")
	cmd.PersistentFlags().StringVar(&flags.OrgOverride, "org", "", "Run this command against a specific organization, id or slug (env: NORCUBE_ORG)")
	cmd.PersistentFlags().StringVarP(&flags.Output, "output", "o", "table", "Output format: table | json | yaml | text (env: NORCUBE_OUTPUT)")
	cmd.PersistentFlags().StringVar(&flags.Query, "query", "", "JMESPath expression applied to the result before it is printed, e.g. \"[?status=='failed'].jobId\"")
	cmd.PersistentFlags().StringArrayVar(&flags.EndpointURLs, "endpoint-url", nil, "Override a service endpoint, service=url (repeatable; services: "+strings.Join(registry.Keys(), ", ")+")")
	cmd.PersistentFlags().StringVar(&flags.TokenFile, "token-file", "", "Read an access token from this file instead of the profile's credentials (env: NORCUBE_TOKEN holds one directly)")
	cmd.PersistentFlags().StringVar(&flags.AuthOverride, "auth-url", "", "Override the auth service base URL, same as --endpoint-url auth=URL (env: NORCUBE_AUTH_URL)")
	cmd.PersistentFlags().StringVar(&flags.WebAppOverride, "web-app", "", "Override the web app base URL used for browser login (env: NORCUBE_WEB_APP)")
	cmd.PersistentFlags().IntVar(&flags.ReadTimeoutSecs, clictx.FlagReadTimeout, 60, "Seconds to wait for each API response, 0 for no limit (env: NORCUBE_READ_TIMEOUT)")
	cmd.PersistentFlags().IntVar(&flags.ConnectTimeoutSecs, clictx.FlagConnectTimeout, 10, "Seconds to wait for a connection, 0 for no limit (env: NORCUBE_CONNECT_TIMEOUT)")
	cmd.PersistentFlags().IntVar(&flags.MaxAttempts, clictx.FlagMaxAttempts, 3, "Attempts for a request that hit throttling or a transient failure, 1 disables retries (env: NORCUBE_MAX_ATTEMPTS)")
	cmd.PersistentFlags().BoolVar(&flags.Debug, "debug", false, "Print every HTTP request and response to stderr, with credentials redacted (env: NORCUBE_DEBUG=1)")
	cmd.PersistentFlags().BoolVar(&noColor, "no-color", false, "Disable color and styling (env: NO_COLOR, CLICOLOR=0)")
	cmd.PersistentFlags().BoolVar(&flags.NoPager, "no-pager", false, "Disable automatic paging of long table output (env: $PAGER controls the pager, default: less)")

	cmd.AddCommand(
		newLoginCmd(),
		newLogoutCmd(),
		newWhoamiCmd(),
		newAuthCmd(),
		newConfigureCmd(),
		newOrgCmd(),
		newConfigCmd(),
		newUpgradeCmd(),
		newAPICmd(),
		newDoctorCmd(),
		backup.NewCmd(),
		langsync.NewCmd(),
		domainradar.NewCmd(),
	)
	markUsageErrors(cmd)
	completion.Bind(flags)
	registerCompletions(cmd)

	return cmd
}

// selectedProfile is the profile of the running command, for LoginCommand;
// selectedProfileExplicit is true when --profile or NORCUBE_PROFILE chose
// it rather than default_profile.
var (
	selectedProfile         string
	selectedProfileExplicit bool
)

// LoginCommand is the command that signs the running command's profile
// in, for error hints. It names the profile unless the profile is the one
// a plain `norcube login` signs in anyway.
func LoginCommand() string {
	if selectedProfile == "" || !selectedProfileExplicit {
		return "norcube login"
	}
	return "norcube login --profile " + selectedProfile
}

// LoginHint is the message for an error that a login fixes: one line
// when the profile has no session, the server's reason plus the fix when
// the session was refused.
func LoginHint(err error) string {
	where := ""
	if selectedProfileExplicit && selectedProfile != "" {
		where = " (profile " + selectedProfile + ")"
	}
	if errors.Is(err, auth.ErrNotLoggedIn) {
		return fmt.Sprintf("Not signed in%s. Run `%s`.", where, LoginCommand())
	}
	return fmt.Sprintf("%s%s.\nRun `%s` to sign in again.", strings.TrimRight(err.Error(), ". "), where, LoginCommand())
}

// markUsageErrors walks the tree so that every invocation mistake exits
// with clierr.ExitUsage: argument validators are wrapped, and command
// groups (which cobra would answer with help and exit 0 for a typo'd
// subcommand) reject unknown subcommands.
func markUsageErrors(c *cobra.Command) {
	if args := c.Args; args != nil {
		c.Args = func(cmd *cobra.Command, a []string) error {
			return clierr.Usage(args(cmd, a))
		}
	}
	if !c.Runnable() && c.HasSubCommands() {
		c.RunE = unknownSubcommand
	}
	for _, sub := range c.Commands() {
		markUsageErrors(sub)
	}
}

// IsGroup reports whether c only groups subcommands: running it alone
// shows help. Documentation generators use it to skip a usage line.
func IsGroup(c *cobra.Command) bool {
	return c.RunE != nil && reflect.ValueOf(c.RunE).Pointer() == reflect.ValueOf(unknownSubcommand).Pointer()
}

func unknownSubcommand(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return cmd.Help()
	}
	msg := fmt.Sprintf("unknown command %q for %q", args[0], cmd.CommandPath())
	if s := cmd.SuggestionsFor(args[0]); len(s) > 0 {
		msg += "\n\nDid you mean this?\n\t" + strings.Join(s, "\n\t")
	}
	return clierr.Usage(fmt.Errorf("%s\n\nRun '%s --help' for usage", msg, cmd.CommandPath()))
}
