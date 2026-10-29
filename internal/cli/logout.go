package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/config"
	"github.com/norcubeplatform/cli/internal/credstore"
	"github.com/norcubeplatform/cli/internal/orgs"
	"github.com/norcubeplatform/cli/internal/output"
	"github.com/norcubeplatform/cli/internal/resolve"
)

// endServerSession ends the CLI session on Norcube Auth when logging out.
// A variable so tests can observe or replace the call.
var endServerSession = func(ctx context.Context, authURL, refreshToken string) error {
	return auth.EndServerSession(ctx, nil, authURL, refreshToken)
}

// serverSignOutWarning is printed when the session couldn't be ended on the
// server. Logging out still succeeds: the local credentials are gone.
const serverSignOutWarning = "Warning: couldn't end the session on the server (%v). It expires after 30 days unused, or revoke it under Account › CLI sessions.\n"

func newLogoutCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Sign out: end the session on Norcube and forget it locally",
		Long: `Ends the selected profile's CLI session on Norcube Auth, so its refresh
token stops working everywhere, then removes the session (refresh and access
tokens) from the credential store and forgets its signed-in user. If the
server can't be reached, the local sign-out still happens and a warning says
so; the session then expires after 30 days unused, or you revoke it under
Account › CLI sessions in the dashboard. The profile itself, with its
environment and organization, stays; run ` + "`norcube login`" + ` to sign back in.

With --all, every profile is signed out.`,
		Example: `  norcube logout
  norcube logout --profile client-acme
  norcube logout --all`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags := clictx.Get(cmd)
			if all {
				return logoutAll(cmd, flags)
			}
			cfg, err := flags.Config()
			if err != nil {
				return err
			}
			ended, err := logoutProfile(cmd.Context(), cmd.ErrOrStderr(), cfg.Profile, cfg.Auth, true, auth.DeleteCredentials)
			if err != nil {
				return err
			}
			_ = orgs.DefaultCache().ClearProfile(cfg.Profile)
			if res, _ := flags.Resolve(); res != nil && res.ProfileDefined {
				if err := flags.UpdateProfile(func(p *config.Profile) error {
					p.User = config.User{}
					return nil
				}); err != nil {
					return err
				}
			}
			return cmdutil.Print(cmd, output.Done{
				Message: fmt.Sprintf("Signed out (profile %s).", cfg.Profile),
				Value:   logoutResult{Profiles: []string{cfg.Profile}, ServerSessionEnded: ended},
				Fields:  []output.Field{{Value: cfg.Profile}},
			})
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Sign out every profile")
	return cmd
}

// logoutProfile signs one profile out under its refresh lock, so no refresh
// can rotate the token while it is being ended. With endServer it first
// ends the session on Norcube Auth; ended reports the outcome, nil when the
// profile had no stored session to end (never signed in, or a profile that
// takes its token from NORCUBE_TOKEN / --token-file). The local credentials
// are deleted whatever the server said.
func logoutProfile(ctx context.Context, stderr io.Writer, profile, authURL string, endServer bool, del func(profile, api string) error) (ended *bool, err error) {
	unlock, err := auth.LockProfile(ctx, "", profile, authURL, 0)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if endServer {
		if tok, err := auth.LoadRefreshToken(profile, authURL); err == nil && tok != "" {
			ok := true
			if err := endServerSession(ctx, authURL, tok); err != nil {
				ok = false
				fmt.Fprintf(stderr, serverSignOutWarning, err)
			}
			ended = &ok
		}
	}
	if err := del(profile, authURL); err != nil {
		return ended, fmt.Errorf("clear credentials: %w", err)
	}
	return ended, nil
}

// logoutAll signs out every profile in the config file (and the implicit
// default profile), from the keyring and the file store alike.
func logoutAll(cmd *cobra.Command, flags *clictx.Flags) error {
	f, err := flags.ConfigFile()
	if err != nil {
		return err
	}
	names := resolve.ProfileNames(f)
	if _, ok := f.Profiles[config.DefaultProfileName]; !ok {
		names = append(names, config.DefaultProfileName)
	}
	dir, err := clictx.CredentialsDir()
	if err != nil {
		return err
	}
	stores := []credstore.Store{credstore.File{Dir: dir}}
	if credstore.Available() {
		stores = append(stores, credstore.Keyring{})
	}
	var allEnded *bool
	for _, name := range names {
		res, err := resolve.Resolve(resolve.Input{File: f, Getenv: os.Getenv, Profile: name})
		if err != nil {
			return err
		}
		authURL := res.Endpoints[config.ServiceAuth].Value
		for i, s := range stores {
			del := func(profile, api string) error {
				return auth.DeleteCredentialsFrom(s, profile, api)
			}
			// The server session is ended once per profile, before its
			// first store is cleared.
			ended, err := logoutProfile(cmd.Context(), cmd.ErrOrStderr(), name, authURL, i == 0, del)
			if err != nil {
				return fmt.Errorf("profile %s: %w", name, err)
			}
			if ended != nil {
				v := *ended && (allEnded == nil || *allEnded)
				allEnded = &v
			}
		}
	}
	_ = orgs.DefaultCache().Clear()
	if _, err := config.Update(func(f *config.File) error {
		for _, p := range f.Profiles {
			if p != nil {
				p.User = config.User{}
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return cmdutil.Print(cmd, output.Done{
		Message: fmt.Sprintf("Signed out of %d profile(s).", len(names)),
		Value:   logoutResult{Profiles: names, ServerSessionEnded: allEnded},
	})
}

// logoutResult lists the profiles that were signed out. ServerSessionEnded
// says whether every stored session was also ended on Norcube Auth; it is
// absent when there was no stored session to end.
type logoutResult struct {
	Profiles           []string `json:"profiles" yaml:"profiles"`
	ServerSessionEnded *bool    `json:"serverSessionEnded,omitempty" yaml:"serverSessionEnded,omitempty"`
}
