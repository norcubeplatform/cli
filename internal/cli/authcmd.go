package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/config"
	"github.com/norcubeplatform/cli/internal/output"
	"github.com/norcubeplatform/cli/internal/resolve"
	"github.com/norcubeplatform/cli/internal/term"
)

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Check the session, manage CLI sessions and get access tokens for scripts",
	}
	cmd.AddCommand(newAuthStatusCmd(), newAuthTokenCmd(), newAuthSessionsCmd())
	return cmd
}

// authStatus is one profile's sign-in state.
type authStatus struct {
	Profile         string `json:"profile" yaml:"profile"`
	Environment     string `json:"environment" yaml:"environment"`
	AuthURL         string `json:"authUrl" yaml:"authUrl"`
	CredentialStore string `json:"credentialStore" yaml:"credentialStore"`
	// SignedIn: the profile can make requests. With the server check that
	// means Session is "active"; with --offline (or a static token) that
	// credentials are present.
	SignedIn bool `json:"signedIn" yaml:"signedIn"`
	// Session is Norcube Auth's answer: active, ended (revoked or expired),
	// none, or unknown (couldn't ask). Absent with --offline and for static
	// tokens (NORCUBE_TOKEN, --token-file), which have no session.
	Session         auth.SessionState `json:"session,omitempty" yaml:"session,omitempty"`
	User            string            `json:"user,omitempty" yaml:"user,omitempty"`
	Organization    string            `json:"organization,omitempty" yaml:"organization,omitempty"`
	AccessExpiresAt *time.Time        `json:"accessExpiresAt,omitempty" yaml:"accessExpiresAt,omitempty"`

	cfg      *config.Config
	checkErr error
}

func newAuthStatusCmd() *cobra.Command {
	var allProfiles, offline bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show whether a profile is signed in (exit code 4 when it isn't)",
		Example: `  norcube auth status
  norcube auth status --all-profiles
  norcube auth status --offline
  norcube auth status --profile ci || norcube login --profile ci`,
		Long: `Shows the selected profile's user, organization, environment, credential
store and when its cached access token expires, and asks Norcube Auth
whether the stored session still works. SIGNED IN is:

  yes              the session works
  no               nothing is stored; run ` + "`norcube login`" + `
  revoked/expired  Norcube Auth ended the session (revoked in the dashboard,
                   signed out elsewhere, or unused for 30 days)
  unknown          Norcube Auth couldn't be reached

A cached access token that hasn't expired counts as "yes" without a
request: it was minted from the session within the last few minutes.
--offline skips the check and only looks at what is stored locally.

Exits with 4 when the profile can't be used ("no" or "revoked/expired"), so
scripts can check before they start: ` + "`norcube auth status >/dev/null || norcube login`" + `.

With --all-profiles, lists every profile (checked in parallel) and always
exits 0.`,
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
			var rows []authStatus
			if allProfiles {
				for _, name := range resolve.ProfileNames(f) {
					r, err := resolve.Resolve(resolve.Input{File: f, Getenv: os.Getenv, Profile: name})
					if err != nil {
						return err
					}
					if err := clictx.UseProfileStore(r); err != nil {
						return err
					}
					rows = append(rows, statusOf(r))
				}
			} else {
				rows = append(rows, statusOf(res))
			}
			if !offline {
				checkSessions(cmd.Context(), flags, rows)
			}
			for _, s := range rows {
				if s.Session == auth.SessionUnknown {
					fmt.Fprintf(cmd.ErrOrStderr(), "Couldn't verify profile %s with Norcube Auth: %v\n", s.Profile, s.checkErr)
				}
			}
			if err := cmdutil.Print(cmd, output.Table[authStatus]{
				Headers: []string{"PROFILE", "ENVIRONMENT", "SIGNED IN", "USER", "ORGANIZATION", "STORE", "TOKEN EXPIRES"},
				Rows: func(s authStatus) []string {
					exp := "-"
					if s.AccessExpiresAt != nil {
						exp = output.When(*s.AccessExpiresAt)
					}
					return []string{s.Profile, s.Environment, signedInLabel(s), orDash(s.User), orDash(s.Organization), s.CredentialStore, exp}
				},
				Items: rows,
			}); err != nil {
				return err
			}
			if allProfiles {
				return nil
			}
			switch {
			case rows[0].Session == auth.SessionEnded:
				return fmt.Errorf("%w: Norcube Auth ended the session of profile %s (revoked or expired)", auth.ErrLoginRequired, rows[0].Profile)
			case !rows[0].SignedIn && rows[0].Session != auth.SessionUnknown:
				return auth.ErrNotLoggedIn
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&allProfiles, "all-profiles", false, "List every profile")
	cmd.Flags().BoolVar(&offline, "offline", false, "Don't ask Norcube Auth; only look at the stored credentials")
	return cmd
}

func signedInLabel(s authStatus) string {
	switch s.Session {
	case auth.SessionActive:
		return "yes"
	case auth.SessionEnded:
		return "revoked/expired"
	case auth.SessionUnknown:
		return "unknown"
	case auth.SessionNone:
		return "no"
	}
	if s.SignedIn {
		return "yes"
	}
	return "no"
}

func statusOf(r *resolve.Result) authStatus {
	cfg := r.Config()
	s := authStatus{
		Profile:         cfg.Profile,
		Environment:     cfg.Environment,
		AuthURL:         cfg.Auth,
		CredentialStore: r.Credentials.Value,
		Organization:    orgLabel(cfg.ActiveOrg),
	}
	if s.Organization == "(none)" {
		s.Organization = ""
	}
	if cfg.User.Email != "" {
		s.User = cfg.User.Email
	}
	switch r.Credentials.Value {
	case config.CredentialEnv, resolve.CredentialTokenFile:
		s.SignedIn = r.Credentials.Value == resolve.CredentialTokenFile || os.Getenv(resolve.EnvToken) != ""
	default:
		s.cfg = cfg
		s.SignedIn = auth.HasCredentials(cfg.Profile, cfg.Auth)
		s.setAccessExpiry()
	}
	return s
}

func (s *authStatus) setAccessExpiry() {
	s.AccessExpiresAt = nil
	if !s.SignedIn || s.cfg == nil {
		return
	}
	if exp := auth.AccessExpiry(s.cfg.Profile, s.cfg.Auth, auth.AudienceAuth, s.cfg.ActiveOrg.ID); !exp.IsZero() {
		s.AccessExpiresAt = &exp
	}
}

// checkSessions asks Norcube Auth about every row that has a stored session,
// in parallel. Static tokens have no session and keep their offline state.
func checkSessions(ctx context.Context, flags *clictx.Flags, rows []authStatus) {
	var wg sync.WaitGroup
	for i := range rows {
		s := &rows[i]
		if s.cfg == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Session, s.checkErr = checkSession(ctx, flags, s.cfg)
			s.SignedIn = s.Session == auth.SessionActive
			s.setAccessExpiry()
		}()
	}
	wg.Wait()
}

// sessionCheckTimeout bounds one profile's server check in login and
// auth status.
const sessionCheckTimeout = 10 * time.Second

// checkSession asks Norcube Auth whether cfg's stored session still works
// (see auth.TokenSource.CheckSession). It reads the profile's own credential
// store and never a static token, which has no session to check.
func checkSession(ctx context.Context, flags *clictx.Flags, cfg *config.Config) (auth.SessionState, error) {
	httpc, err := flags.HTTPClient()
	if err != nil {
		return auth.SessionUnknown, err
	}
	ts := auth.NewTokenSource(cfg.Auth, auth.AudienceAuth, cfg.ActiveOrg.ID)
	ts.Profile = cfg.Profile
	ts.HTTP = httpc
	ts.ProbeOrgID = cfg.ActiveOrg.ID
	ctx, cancel := context.WithTimeout(ctx, sessionCheckTimeout)
	defer cancel()
	return ts.CheckSession(ctx)
}

// audiences maps the names `auth token --audience` accepts to Norcube
// audiences.
var audiences = map[string]string{
	"auth":        auth.AudienceAuth,
	"billing":     auth.AudienceBilling,
	"backup":      auth.AudienceSnapDB,
	"snapdb":      auth.AudienceSnapDB,
	"langsync":    auth.AudienceLangsync,
	"domainradar": auth.AudienceDomainradar,
	"prompthub":   auth.AudiencePrompthub,
}

func audienceNames() string {
	names := make([]string, 0, len(audiences))
	for n := range audiences {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func newAuthTokenCmd() *cobra.Command {
	var audience string
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Print a short-lived access token for a service",
		Long: `Prints an access token for the service (--audience) and organization
(--org, else the profile's) to stdout, for curl or other tools:

  curl -H "Authorization: Bearer $(norcube auth token --audience langsync)" …

The token is valid for a few minutes. Treat it like a password: it acts as
you in that organization.`,
		Example: `  norcube auth token --audience langsync
  norcube auth token --audience snapdb --org acme -o json --query expiresAt`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			aud, ok := audiences[audience]
			if !ok {
				return clierr.Usage(fmt.Errorf("--audience %q: one of %s", audience, audienceNames()))
			}
			flags := clictx.Get(cmd)
			cfg, err := flags.Config()
			if err != nil {
				return err
			}
			org, err := flags.Org(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			if org.ID == "" && aud != auth.AudienceAuth {
				return clierr.Usage(fmt.Errorf("no organization. Run `norcube org use <slug>` or pass --org <slug>"))
			}
			ts, err := flags.TokenSource(cfg, aud, org.ID)
			if err != nil {
				return err
			}
			tok, err := ts.Token(cmd.Context())
			if err != nil {
				return err
			}
			if term.IsTerminal(cmd.OutOrStdout()) {
				fmt.Fprintln(cmd.ErrOrStderr(), "This token acts as you; don't paste it anywhere you wouldn't paste a password.")
			}
			res := tokenResult{AccessToken: tok, Audience: audience, Organization: org.ID}
			if exp := auth.AccessExpiry(cfg.Profile, cfg.Auth, aud, org.ID); !exp.IsZero() {
				res.ExpiresAt = &exp
			}
			return cmdutil.Print(cmd, output.Done{Message: tok, Value: res, Fields: []output.Field{{Value: tok}}})
		},
	}
	cmd.Flags().StringVar(&audience, "audience", "auth", "Service the token is for: "+audienceNames())
	return cmd
}

// tokenResult is what `auth token -o json` prints.
type tokenResult struct {
	AccessToken  string     `json:"accessToken" yaml:"accessToken"`
	Audience     string     `json:"audience" yaml:"audience"`
	Organization string     `json:"organization,omitempty" yaml:"organization,omitempty"`
	ExpiresAt    *time.Time `json:"expiresAt,omitempty" yaml:"expiresAt,omitempty"`
}
