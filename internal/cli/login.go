package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
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
	var scopes []string
	cmd := &cobra.Command{
		Use:         "login",
		Annotations: cmdutil.Writes(),
		Short:       "Sign in to Norcube via your browser",
		Long: `Opens the Norcube sign-in page in your default browser. After you approve
the sign-in there, the browser hands the CLI a one-time code over a localhost
callback (127.0.0.1), which the CLI exchanges for its session (OAuth 2.0 with
PKCE). The session is stored for the selected profile (--profile,
NORCUBE_PROFILE or the default profile) in your operating system's keyring
(Keychain / Secret Service / Windows Credential Manager), or in a file when
no keyring is available.

Logging in to a profile that doesn't exist yet creates it. Its
organization is --org when given, else the profile's organization, else
your default organization.

By default the login can do everything you can do in the dashboard. With
--scopes it is limited to the scopes you list, the same scopes CI tokens
use (for example --scopes backup.read,langsync.sync); commands outside them
fail with exit code 5. Your own permissions in each organization apply
either way. Every login shows up under Account › CLI sessions and Connected
apps in the dashboard, where you can revoke it.

With --no-browser the CLI only prints the sign-in URL. Open it in any
browser on this machine; the page hands the code back to the CLI over
127.0.0.1.

With --device you approve the sign-in on any other device instead, such as
your phone or laptop: the CLI shows a short code and a page to enter it on
(and a QR code when the terminal can show one). Use it over SSH, in a
container or on any machine without a browser. Only approve a code you
started yourself: Norcube never asks you for one.`,
		Example: `  norcube login
  norcube login --profile client-acme
  norcube login --org acme
  norcube login --scopes backup.read,langsync.sync
  norcube login --device
  norcube login --no-browser
  norcube login --force`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if device && noBrowser {
				return clierr.Usage(fmt.Errorf("--device and --no-browser can't be combined: --device never opens a browser"))
			}
			wanted, err := auth.ParseLoginScopes(scopes)
			if err != nil {
				return clierr.Usage(fmt.Errorf("--scopes: %w", err))
			}
			return runLogin(cmd, loginOptions{
				force: force, noBrowser: noBrowser, device: device,
				scopes: wanted, scopesSet: cmd.Flags().Changed("scopes"),
			})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Re-authenticate even if a valid session already exists")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Print the login URL instead of opening a browser")
	cmd.Flags().BoolVar(&device, "device", false, "Approve the sign-in on another device (phone, laptop) with a short code; for SSH and headless machines")
	cmd.Flags().StringSliceVar(&scopes, "scopes", nil, "Limit the login to these scopes, comma-separated (default full: everything you can do in the dashboard); one of "+strings.Join(auth.LoginScopeNames(), ", "))
	_ = cmd.RegisterFlagCompletionFunc("scopes", completeLoginScopes)
	return cmd
}

// completeLoginScopes completes the last of the comma-separated scopes.
func completeLoginScopes(_ *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	prefix := ""
	if i := strings.LastIndex(toComplete, ","); i >= 0 {
		prefix = toComplete[:i+1]
	}
	used := strings.Split(prefix, ",")
	var out []cobra.Completion
	for _, s := range auth.LoginScopes {
		if slices.Contains(used, s.Name) {
			continue
		}
		out = append(out, prefix+s.Name+"\t"+s.Description)
	}
	return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
}

type loginOptions struct {
	force, noBrowser, device bool
	// scopes to ask for (ParseLoginScopes); scopesSet when --scopes was
	// given.
	scopes    []string
	scopesSet bool
}

// The sign-in handshakes and the metadata lookup; tests replace them.
var (
	browserLogin = auth.BrowserLogin
	oauthLogin   = auth.OAuthLogin
	deviceLogin  = auth.DeviceLogin
	discoverAuth = auth.Discover
	// openBrowser opens the sign-in page; nil means the default browser.
	openBrowser func(string) error
)

func runLogin(cmd *cobra.Command, o loginOptions) error {
	force, noBrowser, device := o.force, o.noBrowser, o.device
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

	httpc, err := flags.HTTPClient()
	if err != nil {
		return err
	}
	// The OAuth sign-in when Norcube Auth offers it, else the older one
	// (an older server). The answer is cached, so the commands after the
	// login don't ask again.
	auth.ForgetMetadata(authURL)
	meta, err := discoverAuth(cmd.Context(), httpc, authURL)
	switch {
	case errors.Is(err, auth.ErrOAuthUnavailable):
		meta = nil
		if o.scopesSet {
			return clierr.Usage(fmt.Errorf("--scopes needs the OAuth sign-in, which %s doesn't offer yet; sign in without --scopes", authURL))
		}
	case err != nil:
		return err
	}

	var payload *auth.CallbackPayload
	switch {
	case device && meta != nil && meta.SupportsDevice():
		payload, err = runDeviceLogin(cmd, httpc, authURL, meta, o.scopes)
	case device:
		if o.scopesSet {
			return clierr.Usage(fmt.Errorf("--scopes with --device needs the OAuth device sign-in, which %s doesn't offer yet", authURL))
		}
		payload, err = runDeviceLogin(cmd, httpc, authURL, nil, nil)
	case meta != nil:
		payload, err = runOAuthBrowserLogin(cmd, httpc, meta, noBrowser, o.scopes)
	default:
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
	if payload.OAuth && org.ID != "" {
		// The OAuth sign-in doesn't say who signed in: ask.
		client, err := flags.AuthClient(cfg, org.ID)
		if err != nil {
			return err
		}
		me, err := client.GetMe(cmd.Context())
		if err != nil {
			return fmt.Errorf("signed in, but reading your account failed: %w", err)
		}
		payload.User = auth.User{ID: me.ID, Name: me.Name, Email: me.Email}
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
	if payload.User.Email == "" {
		msg = "Signed in. Your account has no organization yet; create one in the dashboard."
	}
	if cfg.Profile != config.DefaultProfileName {
		msg += fmt.Sprintf("\nProfile: %s.", cfg.Profile)
	}
	result := loginResult{
		Profile:      cfg.Profile,
		User:         userRef{ID: payload.User.ID, Name: payload.User.Name, Email: payload.User.Email},
		Organization: orgRefOf(org),
	}
	if payload.OAuth {
		result.Scopes = payload.Scopes
		if auth.ScopesFull(payload.Scopes) {
			msg += "\nAccess: full (everything you can do in the dashboard)."
		} else {
			msg += fmt.Sprintf("\nAccess: limited to %s. Run `%s` without --scopes for full access.", auth.DescribeScopes(payload.Scopes), LoginCommand())
		}
	}
	return cmdutil.Print(cmd, output.Done{Message: msg, Value: result})
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
	var accessExp time.Time
	if payload.AccessExpiresIn > 0 {
		accessExp = time.Now().Add(time.Duration(payload.AccessExpiresIn) * time.Second)
	}
	if payload.OAuth {
		if err := auth.SaveOAuthSession(profile, authURL, payload.RefreshToken, payload.AccessToken, accessExp, payload.Scopes); err != nil {
			return fmt.Errorf("store the session: %w", err)
		}
		return nil
	}
	if err := auth.SaveRefreshToken(profile, authURL, payload.RefreshToken); err != nil {
		return fmt.Errorf("store refresh token: %w", err)
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
// the session the login just stored.
func resolveLoginOrg(ctx context.Context, flags *clictx.Flags, cfg *config.Config, payload *auth.CallbackPayload, want string) (api.Organization, error) {
	defaultOrg := api.Organization{ID: payload.DefaultOrg.ID, Slug: payload.DefaultOrg.Slug, Name: payload.DefaultOrg.Name}
	if o, ok := orgs.Find([]api.Organization{defaultOrg}, want); ok {
		return o, nil
	}
	// The older sign-in mints Norcube Auth tokens without an
	// organization; the OAuth sign-in needs one it may use.
	tokenOrg := ""
	if payload.OAuth {
		if defaultOrg.ID == "" {
			return api.Organization{}, clierr.Usage(fmt.Errorf("signed in, but your account has no organization yet, so %q can't be selected", want))
		}
		tokenOrg = defaultOrg.ID
	}
	client, err := flags.AuthClient(cfg, tokenOrg)
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
	// Scopes are the scopes Norcube Auth granted (OAuth sign-in only).
	Scopes []string `json:"scopes,omitempty" yaml:"scopes,omitempty"`
}

// runOAuthBrowserLogin signs in through Norcube Auth's authorization
// page in a browser on this machine (the default).
func runOAuthBrowserLogin(cmd *cobra.Command, httpc *http.Client, meta *auth.Metadata, noBrowser bool, scopes []string) (*auth.CallbackPayload, error) {
	out := cmd.ErrOrStderr()
	if !noBrowser {
		fmt.Fprintln(out, "Opening the Norcube sign-in page in your browser…")
	}
	return oauthLogin(cmd.Context(), auth.OAuthLoginOptions{
		Metadata:       meta,
		HTTP:           httpc,
		Scopes:         scopes,
		Timeout:        5 * time.Minute,
		NoBrowser:      noBrowser,
		BrowserOpenURL: openBrowser,
		OnURLReady:     loginURLPrinter(out, noBrowser),
	})
}

func loginURLPrinter(out io.Writer, noBrowser bool) func(string) {
	return func(url string) {
		if noBrowser {
			fmt.Fprintf(out, "Open this URL in a browser on this machine to sign in:\n  %s\n", url)
		} else {
			fmt.Fprintf(out, "If your browser didn't open, visit:\n  %s\n", url)
		}
		fmt.Fprintln(out, "Waiting for you to finish signing in (Ctrl-C to cancel)…")
	}
}

// runBrowserLogin signs in through the web app's older sign-in page,
// for a Norcube Auth without the OAuth sign-in.
func runBrowserLogin(cmd *cobra.Command, webApp string, noBrowser bool) (*auth.CallbackPayload, error) {
	out := cmd.ErrOrStderr()
	if !noBrowser {
		fmt.Fprintf(out, "Opening %s in your browser…\n", webApp+"/cli-login")
	}
	return browserLogin(cmd.Context(), auth.BrowserLoginOptions{
		WebApp:     webApp,
		Timeout:    5 * time.Minute,
		NoBrowser:  noBrowser,
		OnURLReady: loginURLPrinter(out, noBrowser),
	})
}

// runDeviceLogin signs in with a code approved on another device
// (RFC 8628), through the OAuth server when meta is set. The code, the
// page and, when the terminal can show one, a QR code go to stderr; a
// countdown runs while it waits.
func runDeviceLogin(cmd *cobra.Command, httpc *http.Client, authURL string, meta *auth.Metadata, scopes []string) (*auth.CallbackPayload, error) {
	out := cmd.ErrOrStderr()
	tty := term.IsTerminal(out)
	waiting := false
	payload, err := deviceLogin(cmd.Context(), auth.DeviceLoginOptions{
		AuthURL:  authURL,
		HTTP:     httpc,
		Metadata: meta,
		Scopes:   scopes,
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
