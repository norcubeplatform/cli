package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api"
	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

// The CLI sessions of your account: every `norcube login` on any machine.
// The same list as Account › CLI sessions in the dashboard.
func newAuthSessionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "List and revoke the CLI sessions of your account",
		Long: `Every ` + "`norcube login`" + ` creates a CLI session for your account. These
commands list them and end the ones you don't use or don't recognize, the
same as Account › CLI sessions in the dashboard. Revoking a session stops
its refresh token at once; the CLI on that machine has to sign in again.`,
	}
	cmd.AddCommand(newAuthSessionsListCmd(), newAuthSessionsRevokeCmd())
	return cmd
}

func newAuthSessionsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List your CLI sessions; * marks this one",
		Example: `  norcube auth sessions list
  norcube auth sessions list -o json --query "[?!current].id"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := sessionsClient(cmd)
			if err != nil {
				return err
			}
			list, err := client.ListSessions(cmd.Context())
			if err != nil {
				return err
			}
			return cmdutil.Print(cmd, output.Table[api.Session]{
				Headers: []string{"CURRENT", "DEVICE", "CLIENT", "LAST USED", "IP", "ID"},
				Rows: func(s api.Session) []string {
					current := " "
					if s.Current {
						current = "*"
					}
					return []string{
						current,
						orDash(deref(s.Hostname)),
						sessionClientLabel(s.Client),
						output.When(s.LastUsedAt),
						orDash(deref(s.IPAddress)),
						s.ID,
					}
				},
				Items: list,
				Style: output.NoStyle,
			})
		},
	}
}

func newAuthSessionsRevokeCmd() *cobra.Command {
	var others bool
	cmd := &cobra.Command{
		Use:   "revoke <session-id> | --others",
		Short: "End a CLI session, or every session but this one",
		Example: `  norcube auth sessions revoke 5b0e4d7a-3c1f-4e2b-9a8d-7f6e5d4c3b2a
  norcube auth sessions revoke --others`,
		Args: func(cmd *cobra.Command, args []string) error {
			switch {
			case others && len(args) > 0:
				return clierr.Usage(fmt.Errorf("pass a session id or --others, not both"))
			case !others && len(args) != 1:
				return clierr.Usage(fmt.Errorf("pass one session id (see `norcube auth sessions list`) or --others"))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := sessionsClient(cmd)
			if err != nil {
				return err
			}
			if others {
				n, err := client.RevokeOtherSessions(cmd.Context())
				if err != nil {
					return err
				}
				return cmdutil.Print(cmd, output.Done{
					Message: fmt.Sprintf("Revoked %d other session(s). This one stays signed in.", n),
					Value:   revokeResult{Revoked: n},
					Fields:  []output.Field{{Value: fmt.Sprint(n)}},
				})
			}
			id := args[0]
			if err := client.RevokeSession(cmd.Context(), id); err != nil {
				return err
			}
			return cmdutil.Print(cmd, output.Done{
				Message: fmt.Sprintf("Revoked session %s.", id),
				Value:   revokeResult{Revoked: 1, ID: id},
				Fields:  []output.Field{{Value: id}},
			})
		},
	}
	cmd.Flags().BoolVar(&others, "others", false, "Revoke every session except the one this CLI uses")
	return cmd
}

// revokeResult is what `auth sessions revoke -o json` prints.
type revokeResult struct {
	Revoked int64  `json:"revoked" yaml:"revoked"`
	ID      string `json:"id,omitempty" yaml:"id,omitempty"`
}

// sessionsClient is a Norcube Auth client for the profile's session. The
// sessions belong to the account, but tokens are minted per organization,
// so any of the user's organizations will do.
func sessionsClient(cmd *cobra.Command) (*api.AuthClient, error) {
	flags := clictx.Get(cmd)
	cfg, err := flags.Config()
	if err != nil {
		return nil, err
	}
	org, err := flags.Org(cmd.Context(), cfg)
	if err != nil {
		return nil, err
	}
	return flags.AuthClient(cfg, org.ID)
}

// sessionClientLabel reads like "norcube 0.17.0 (darwin/arm64)".
func sessionClientLabel(c api.SessionClient) string {
	parts := []string{}
	if c.Name != nil {
		parts = append(parts, *c.Name)
	}
	if c.Version != nil {
		parts = append(parts, *c.Version)
	}
	if c.OS != nil && c.Arch != nil {
		parts = append(parts, "("+*c.OS+"/"+*c.Arch+")")
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ")
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
