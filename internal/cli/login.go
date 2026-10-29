package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api"
	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/config"
	"github.com/norcubeplatform/cli/internal/orgs"
	"github.com/norcubeplatform/cli/internal/output"
	"github.com/norcubeplatform/cli/internal/resolve"
	"github.com/norcubeplatform/cli/internal/term"
)

func newLoginCmd() *cobra.Command {
	var force, noBrowser, device bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in to Norcube via your browser",
		Long: `Opens a Norcube login page in your default browser. After you sign in there,
the CLI receives a session over a one-shot localhost callback. The session
is stored for the selected profile (--profile, NORCUBE_PROFILE or the
default profile) in your operating system's keyring (Keychain / Secret
Service / Windows Credential Manager), or in a file when no keyring is
available.

Logging in to a profile that doesn't exist yet creates it. Its
organization is --org when given, else the profile's organization, else
your default organization.

With --no-browser the CLI only prints the login URL. Open it in any
browser on this machine; the page hands the session back to the CLI over
127.0.0.1.

With --device you approve the sign-in on any other device instead, such as
your phone or laptop: the CLI shows a short code and a page to enter it on
(and a QR code when the terminal can show one). Use it over SSH, in a
container or on any machine without a browser. Only approve a code you
started yourself: Norcube never asks you for one.`,
		Example: `  norcube login
  norcube login --profile client-acme
  norcube login --org acme
  norcube login --device
  norcube login --no-browser
  norcube login --force`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if device && noBrowser {
				return clierr.Usage(fmt.Errorf("--device and --no-browser can't be combined: --device never opens a browser"))
			}
			return runLogin(cmd, force, noBrowser, device)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Re-authenticate even if a valid session already exists")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Print the login URL instead of opening a browser")
	cmd.Flags().BoolVar(&device, "device", false, "Approve the sign-in on another device (phone, laptop) with a short code; for SSH and headless machines")
	return cmd
}

// browserLogin and deviceLogin run the sign-in handshakes; tests replace
// them.
var (
	browserLogin = auth.BrowserLogin
	deviceLogin  = auth.DeviceLogin
)

func runLogin(cmd *cobra.Command, force, noBrowser, device bool) error {
	flags := clictx.Get(cmd)
	res, err := flags.Resolve()
	if err != nil {
		return err
	}
	cfg := res.Config()
	if res.Credentials.Value != config.CredentialKeyring && res.Credentials.Value != config.CredentialFile {
		return clierr.Usage(fmt.Errorf("profile %s takes its token from %s; there is nothing to log in to", cfg.Profile, res.Credentials.Source))
	}
	if err := checkLoginTarget(res); err != nil {
		return err
	}
	authURL, webApp := cfg.Auth, cfg.WebApp

	if !force && auth.HasCredentials(cfg.Profile, authURL) {
		// A stored session can be dead on the server (revoked from the
		// dashboard, ended by `logout` elsewhere, unused for 30 days).
		// Ask before claiming the profile is signed in.
		state, checkErr := checkSession(cmd.Context(), flags, cfg)
		if state != auth.SessionEnded {
			who := "this account"
			if cfg.User.Email != "" {
				who = cfg.User.Email
			}
			msg := fmt.Sprintf("Already signed in as %s (profile %s). Use `norcube login --force` to re-authenticate or `norcube logout` to sign out first.", who, cfg.Profile)
			if state == auth.SessionUnknown {
				msg += fmt.Sprintf("\nCouldn't verify the session with Norcube Auth: %v", checkErr)
			}
			return cmdutil.Print(cmd, output.Done{
				Message: msg,
				Value: loginResult{
					Profile:      cfg.Profile,
					User:         userRef{ID: cfg.User.ID, Name: cfg.User.Name, Email: cfg.User.Email},
					Organization: orgRefOf(cfg.ActiveOrg),
				},
			})
		}
		if err := auth.DeleteCredentials(cfg.Profile, authURL); err != nil {
			return fmt.Errorf("forget the ended session: %w", err)
		}
		fmt.Fprintln(cmd.ErrOrStderr(), "Your previous session ended (revoked or expired); signing in again.")
	}

	var payload *auth.CallbackPayload
	if device {
		payload, err = runDeviceLogin(cmd, flags, authURL)
	} else {
		payload, err = runBrowserLogin(cmd, webApp, noBrowser)
	}
	if err != nil {
		return err
	}

	if err := storeSession(cmd.Context(), cfg.Profile, authURL, payload); err != nil {
		return err
	}

	org := config.Organization{ID: payload.DefaultOrg.ID, Slug: payload.DefaultOrg.Slug, Name: payload.DefaultOrg.Name}
	if want := wantedLoginOrg(res); want != "" {
		found, err := resolveLoginOrg(cmd.Context(), flags, cfg, payload, want)
		if err != nil {
			return err
		}
		org = config.Organization{ID: found.ID, Slug: found.Slug, Name: found.Name}
	}

	err = flags.UpdateProfile(func(p *config.Profile) error {
		if !res.ProfileDefined {
			p.Environment, p.Endpoints = newProfileEnvironment(res)
		}
		p.User = config.User{ID: payload.User.ID, Name: payload.User.Name, Email: payload.User.Email}
		p.SetActiveOrg(org)
		return nil
	})
	if err != nil {
		return fmt.Errorf("save profile: %w", err)
	}

	msg := fmt.Sprintf("Signed in as %s (%s).\nActive organization: %s.", payload.User.Name, payload.User.Email, orgLabel(org))
	if cfg.Profile != config.DefaultProfileName {
		msg += fmt.Sprintf("\nProfile: %s.", cfg.Profile)
	}
	return cmdutil.Print(cmd, output.Done{
		Message: msg,
		Value: loginResult{
			Profile:      cfg.Profile,
			User:         userRef{ID: payload.User.ID, Name: payload.User.Name, Email: payload.User.Email},
			Organization: orgRefOf(org),
		},
	})
}

// checkLoginTarget refuses to sign an existing profile in to another
// environment than its own: a profile holds one session, and replacing a
// session with one from other endpoints (because NORCUBE_AUTH_URL or
// --auth-url pointed elsewhere) would quietly sign the profile out.
func checkLoginTarget(res *resolve.Result) error {
	if !res.ProfileDefined {
		return nil
	}
	a := res.Endpoints[config.ServiceAuth]
	if a.Source == "flag" || strings.HasPrefix(a.Source, "env ") {
		own := profileAuthURL(res)
		if own != a.Value {
			return clierr.Usage(fmt.Errorf(
				"profile %s signs in to %s, but %s points at %s. Use a separate profile for those endpoints, e.g. `norcube configure set endpoints.auth %s --profile custom` then `norcube login --profile custom`",
				res.Profile.Value, own, a.Source, a.Value, a.Value))
		}
	}
	return nil
}

// profileAuthURL is the auth URL the profile uses on its own, without
// flags or environment variables.
func profileAuthURL(res *resolve.Result) string {
	p := res.StoredProfile()
	if u := p.Endpoints[config.ServiceAuth]; u != "" {
		return u
	}
	env := p.Environment
	if env == "" || env == config.EnvCustom {
		env = config.EnvProduction
	}
	e, _ := config.EnvironmentEndpoints(env)
	return e[config.ServiceAuth]
}

// newProfileEnvironment is what a login records for a profile it creates:
// the endpoints given explicitly for this command (flags, NORCUBE_*_URL)
// as an environment plus overrides, else production.
func newProfileEnvironment(res *resolve.Result) (string, map[string]string) {
	explicit := map[string]string{}
	for svc, v := range res.Endpoints {
		if v.Source == "flag" || strings.HasPrefix(v.Source, "env ") {
			explicit[svc] = v.Value
		}
	}
	if len(explicit) == 0 {
		return config.EnvProduction, nil
	}
	return config.EnvironmentFor(explicit)
}

// storeSession saves the login's refresh token (and its first access
// token) under the profile's credential lock.
func storeSession(ctx context.Context, profile, authURL string, payload *auth.CallbackPayload) error {
	unlock, err := auth.LockProfile(ctx, "", profile, authURL, 0)
	if err != nil {
		return err
	}
	defer unlock()
	if err := auth.SaveRefreshToken(profile, authURL, payload.RefreshToken); err != nil {
		return fmt.Errorf("store refresh token: %w", err)
	}
	var accessExp time.Time
	if payload.AccessExpiresIn > 0 {
		accessExp = time.Now().Add(time.Duration(payload.AccessExpiresIn) * time.Second)
	}
	// Cache failure is non-fatal: the next command mints a new token.
	_ = auth.SaveAccessTokenUntil(profile, authURL, auth.AudienceAuth, "", payload.AccessToken, accessExp)
	return nil
}

// wantedLoginOrg is the organization the login should select: --org or
// NORCUBE_ORG, else the profile's organization; "" means the account's
// default.
func wantedLoginOrg(res *resolve.Result) string {
	return res.Org.Value
}

// resolveLoginOrg finds the wanted organization among the user's, using
// the access token the login just stored.
func resolveLoginOrg(ctx context.Context, flags *clictx.Flags, cfg *config.Config, payload *auth.CallbackPayload, want string) (api.Organization, error) {
	defaultOrg := api.Organization{ID: payload.DefaultOrg.ID, Slug: payload.DefaultOrg.Slug, Name: payload.DefaultOrg.Name}
	if o, ok := orgs.Find([]api.Organization{defaultOrg}, want); ok {
		return o, nil
	}
	client, err := flags.AuthClient(cfg, "")
	if err != nil {
		return api.Organization{}, err
	}
	list, err := client.ListOrganizations(ctx)
	if err != nil {
		return api.Organization{}, fmt.Errorf("look up organization %q: %w", want, err)
	}
	if o, ok := orgs.Find(list, want); ok {
		return o, nil
	}
	return api.Organization{}, clierr.Usage(fmt.Errorf("signed in, but %w", &orgs.NotFoundError{Value: want}))
}

func orgLabel(o config.Organization) string {
	switch {
	case o.Slug != "":
		return o.Slug
	case o.ID != "":
		return o.ID
	}
	return "(none)"
}

// loginResult is what `login -o json` prints.
type loginResult struct {
	Profile      string  `json:"profile" yaml:"profile"`
	User         userRef `json:"user" yaml:"user"`
	Organization *orgRef `json:"organization" yaml:"organization"`
}

// runBrowserLogin signs in through the web app in a browser on this
// machine (the default).
func runBrowserLogin(cmd *cobra.Command, webApp string, noBrowser bool) (*auth.CallbackPayload, error) {
	out := cmd.ErrOrStderr()
	if !noBrowser {
		fmt.Fprintf(out, "Opening %s in your browser…\n", webApp+"/cli-login")
	}
	return browserLogin(cmd.Context(), auth.BrowserLoginOptions{
		WebApp:    webApp,
		Timeout:   5 * time.Minute,
		NoBrowser: noBrowser,
		OnURLReady: func(url string) {
			if noBrowser {
				fmt.Fprintf(out, "Open this URL in a browser on this machine to sign in:\n  %s\n", url)
			} else {
				fmt.Fprintf(out, "If your browser didn't open, visit:\n  %s\n", url)
			}
			fmt.Fprintln(out, "Waiting for you to finish signing in (Ctrl-C to cancel)…")
		},
	})
}

// runDeviceLogin signs in with a code approved on another device
// (RFC 8628). The code, the page and, when the terminal can show one, a QR
// code go to stderr; a countdown runs while it waits.
func runDeviceLogin(cmd *cobra.Command, flags *clictx.Flags, authURL string) (*auth.CallbackPayload, error) {
	httpc, err := flags.HTTPClient()
	if err != nil {
		return nil, err
	}
	out := cmd.ErrOrStderr()
	tty := term.IsTerminal(out)
	waiting := false
	payload, err := deviceLogin(cmd.Context(), auth.DeviceLoginOptions{
		AuthURL: authURL,
		HTTP:    httpc,
		OnCode: func(c auth.DeviceCode) {
			fmt.Fprintf(out, "To sign in, open this page on any device (your phone works) and enter the code:\n\n  %s\n\n  %s\n\n", c.VerificationURI, c.UserCode)
			if c.VerificationURIComplete != "" && term.Color(out) {
				if qr := term.QR(c.VerificationURIComplete, term.Width(out)); qr != "" {
					fmt.Fprintf(out, "Or scan this to open the page with the code filled in:\n\n%s\n", qr)
				}
			}
			fmt.Fprintln(out, "Only approve if you started this sign-in. Norcube never asks you for this code.")
		},
		OnWait: func(left time.Duration) {
			switch {
			case tty:
				fmt.Fprintf(out, "\rWaiting for approval… %s left (Ctrl-C to cancel) ", countdown(left))
			case !waiting:
				fmt.Fprintf(out, "Waiting for approval (the code expires in %s)…\n", countdown(left))
			}
			waiting = true
		},
	})
	if tty && waiting {
		fmt.Fprintln(out)
	}
	return payload, err
}

// countdown reads like 9:05.
func countdown(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
