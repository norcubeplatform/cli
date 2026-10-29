package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

func newWhoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the signed-in user and active organization",
		Example: `  norcube whoami
  norcube whoami -o json --query user.email`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags := clictx.Get(cmd)
			cfg, err := flags.Config()
			if err != nil {
				return err
			}
			org, err := flags.Org(cmd.Context(), cfg)
			if err != nil {
				return err
			}

			client, err := flags.AuthClient(cfg, org.ID)
			if err != nil {
				return err
			}

			me, err := client.GetMe(cmd.Context())
			if err != nil {
				return err
			}

			res := whoamiResult{
				User:         userRef{ID: me.ID, Name: me.Name, Email: me.Email},
				Organization: orgRefOf(cfg.ActiveOrg),
				Profile:      cfg.Profile,
				Environment:  cfg.Environment,
				API:          cfg.Auth,
			}
			active := "(none — run `norcube org use <slug>`)"
			if res.Organization != nil {
				active = fmt.Sprintf("%s (%s)", orgLabel(cfg.ActiveOrg), cfg.ActiveOrg.ID)
			}
			return cmdutil.Print(cmd, output.Object{Value: res, Fields: []output.Field{
				{Name: "User", Value: fmt.Sprintf("%s <%s>", me.Name, me.Email)},
				{Name: "User ID", Value: me.ID},
				{Name: "Active org", Value: active},
				{Name: "Profile", Value: fmt.Sprintf("%s (%s)", cfg.Profile, cfg.Environment)},
				{Name: "API", Value: cfg.Auth},
			}})
		},
	}
}

type whoamiResult struct {
	User         userRef `json:"user" yaml:"user"`
	Organization *orgRef `json:"organization" yaml:"organization"`
	Profile      string  `json:"profile" yaml:"profile"`
	Environment  string  `json:"environment" yaml:"environment"`
	API          string  `json:"api" yaml:"api"`
}
