// Package clictx carries the global per-invocation state (flags, cancellable
// context) attached to every cobra command, so subcommand packages can read
// it without importing the root cli package and creating an import cycle.
package clictx

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api"
	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/config"
	"github.com/norcubeplatform/cli/internal/credstore"
	"github.com/norcubeplatform/cli/internal/orgs"
	"github.com/norcubeplatform/cli/internal/resolve"
	"github.com/norcubeplatform/cli/internal/transport"
)

// Flags are the values of the persistent flags declared on the root
// command, plus the configuration resolved from them.
type Flags struct {
	Profile        string
	AuthOverride   string
	WebAppOverride string
	OrgOverride    string
	EndpointURLs   []string
	TokenFile      string
	// Output is the resolved output format once the root command's
	// PersistentPreRunE has run (-o > NORCUBE_OUTPUT > profile > defaults).
	Output  string
	NoPager bool
	// Query is the --query JMESPath expression applied to the result.
	Query string

	outputSet  bool
	noPagerSet bool

	resOnce sync.Once
	res     *resolve.Result
	file    *config.File
	resErr  error

	// HTTP behavior. Each wins over its NORCUBE_* variable only when the
	// flag was given on the command line (see CaptureChanged).
	ReadTimeoutSecs    int
	ConnectTimeoutSecs int
	MaxAttempts        int

	readTimeoutSet    bool
	connectTimeoutSet bool
	maxAttemptsSet    bool

	httpOnce   sync.Once
	httpClient *http.Client
	dlClient   *http.Client
	httpErr    error
}

// Flag names shared with the root command.
const (
	FlagReadTimeout    = "cli-read-timeout"
	FlagConnectTimeout = "cli-connect-timeout"
	FlagMaxAttempts    = "max-attempts"
)

// CaptureChanged records which HTTP flags were set explicitly, so their
// zero values ("no timeout") can be told apart from "not given". Call it
// once the flags are parsed.
func (g *Flags) CaptureChanged(cmd *cobra.Command) {
	changed := func(name string) bool {
		f := cmd.Flag(name)
		return f != nil && f.Changed
	}
	g.readTimeoutSet = changed(FlagReadTimeout)
	g.connectTimeoutSet = changed(FlagConnectTimeout)
	g.maxAttemptsSet = changed(FlagMaxAttempts)
	g.outputSet = changed("output")
	g.noPagerSet = changed("no-pager")
}

// ApplyPreferences settles the output format and paging before a command
// runs. A config file that can't be read doesn't stop commands that don't
// need it (config edit, config path, upgrade): only an explicitly invalid
// output format is an error here.
func (g *Flags) ApplyPreferences() error {
	res, err := g.Resolve()
	if err != nil {
		if g.outputSet {
			return validOutput(g.Output)
		}
		if v := os.Getenv(resolve.EnvOutput); v != "" {
			g.Output = v
			return validOutput(v)
		}
		g.Output = "table"
		return nil
	}
	g.Output = res.Output.Value
	if !g.noPagerSet && g.file != nil && g.file.Defaults.Pager != nil && !*g.file.Defaults.Pager {
		g.NoPager = true
	}
	return nil
}

func validOutput(v string) error {
	switch v {
	case "table", "json", "yaml", "text":
		return nil
	}
	return clierr.Usage(fmt.Errorf("output format %q: want table, json, yaml or text", v))
}

// Resolve loads the config file (migrating an old one) and resolves the
// profile, endpoints, organization, credentials and output of this
// invocation. The result is computed once.
func (g *Flags) Resolve() (*resolve.Result, error) {
	g.resOnce.Do(func() {
		g.file, g.resErr = config.Load()
		if g.resErr != nil {
			return
		}
		g.res, g.resErr = g.resolveWith(g.file)
	})
	return g.res, g.resErr
}

func (g *Flags) resolveWith(f *config.File) (*resolve.Result, error) {
	endpoints, err := resolve.ParseEndpoints(g.EndpointURLs)
	if err != nil {
		return nil, err
	}
	// --auth-url and --web-app predate --endpoint-url; same precedence.
	if g.AuthOverride != "" {
		endpoints[config.ServiceAuth] = g.AuthOverride
	}
	if g.WebAppOverride != "" {
		endpoints[config.ServiceWebApp] = g.WebAppOverride
	}
	res, err := resolve.Resolve(resolve.Input{
		File:      f,
		Getenv:    os.Getenv,
		Profile:   g.Profile,
		Org:       g.OrgOverride,
		Output:    g.Output,
		OutputSet: g.outputSet,
		Endpoints: endpoints,
		TokenFile: g.TokenFile,
	})
	if err != nil {
		return nil, err
	}
	if err := UseProfileStore(res); err != nil {
		return nil, err
	}
	return res, nil
}

// UseProfileStore points the profile's credentials at the store it asked
// for: the file store for credential_store = "file", and also for
// "keyring" when no keyring is available (with a one-time notice).
func UseProfileStore(res *resolve.Result) error {
	kind := res.Credentials.Value
	if kind != config.CredentialFile && kind != config.CredentialKeyring {
		return nil
	}
	dir, err := CredentialsDir()
	if err != nil {
		return err
	}
	store, _, err := credstore.Open(kind, dir, os.Stderr)
	if err != nil {
		return err
	}
	if _, isFile := store.(credstore.File); isFile {
		auth.UseStore(res.Profile.Value, store)
	} else {
		auth.UseStore(res.Profile.Value, nil)
	}
	return nil
}

// CredentialsDir is where the file credential store keeps one file per
// profile: credentials/ next to the config file.
func CredentialsDir() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "credentials"), nil
}

// ConfigFile returns the loaded config file.
func (g *Flags) ConfigFile() (*config.File, error) {
	if _, err := g.Resolve(); err != nil {
		return nil, err
	}
	return g.file, nil
}

// Config returns the configuration this command runs with.
func (g *Flags) Config() (*config.Config, error) {
	res, err := g.Resolve()
	if err != nil {
		return nil, err
	}
	return res.Config(), nil
}

// UpdateProfile changes the selected profile in the config file (creating
// the profile when it doesn't exist yet) and re-resolves.
func (g *Flags) UpdateProfile(fn func(p *config.Profile) error) error {
	res, err := g.Resolve()
	if err != nil {
		return err
	}
	name := res.Profile.Value
	f, err := config.Update(func(f *config.File) error {
		return fn(f.Profile(name))
	})
	if err != nil {
		return err
	}
	g.file = f
	g.res, err = g.resolveWith(f)
	return err
}

// SetActiveOrg makes o the selected profile's organization.
func (g *Flags) SetActiveOrg(o config.Organization) error {
	return g.UpdateProfile(func(p *config.Profile) error {
		p.SetActiveOrg(o)
		return nil
	})
}

// Transport returns the HTTP settings: flag > NORCUBE_* env > [defaults]
// in the config file > built-in default.
func (g *Flags) Transport() (transport.Config, error) {
	base := transport.Defaults()
	if g.file != nil {
		d := g.file.Defaults
		if d.MaxAttempts > 0 {
			base.MaxAttempts = d.MaxAttempts
		}
		if d.ReadTimeout != "" {
			v, err := transport.ParseTimeout(d.ReadTimeout)
			if err != nil {
				return base, fmt.Errorf("config [defaults] read_timeout: %w", err)
			}
			base.ReadTimeout = v
		}
		if d.ConnectTimeout != "" {
			v, err := transport.ParseTimeout(d.ConnectTimeout)
			if err != nil {
				return base, fmt.Errorf("config [defaults] connect_timeout: %w", err)
			}
			base.ConnectTimeout = v
		}
	}
	cfg, err := transport.FromEnvOver(base)
	if err != nil {
		return cfg, err
	}
	if g.readTimeoutSet {
		cfg.ReadTimeout = time.Duration(max(g.ReadTimeoutSecs, 0)) * time.Second
	}
	if g.connectTimeoutSet {
		cfg.ConnectTimeout = time.Duration(max(g.ConnectTimeoutSecs, 0)) * time.Second
	}
	if g.maxAttemptsSet {
		cfg.MaxAttempts = max(g.MaxAttempts, 1)
	}
	return cfg, nil
}

func (g *Flags) buildHTTP() {
	g.httpOnce.Do(func() {
		cfg, err := g.Transport()
		if err != nil {
			g.httpErr = err
			return
		}
		if g.httpClient, err = transport.New(cfg); err != nil {
			g.httpErr = err
			return
		}
		g.dlClient, g.httpErr = transport.NewDownload(cfg)
	})
}

// HTTPClient returns the shared client for API requests of this invocation.
func (g *Flags) HTTPClient() (*http.Client, error) {
	g.buildHTTP()
	return g.httpClient, g.httpErr
}

// DownloadClient returns the shared client for streaming downloads.
func (g *Flags) DownloadClient() (*http.Client, error) {
	g.buildHTTP()
	return g.dlClient, g.httpErr
}

// AuthClient returns a Norcube Auth client authenticated for orgID, using
// the shared HTTP client.
func (g *Flags) AuthClient(cfg *config.Config, orgID string) (*api.AuthClient, error) {
	ts, err := g.TokenSource(cfg, auth.AudienceAuth, orgID)
	if err != nil {
		return nil, err
	}
	client := api.NewAuthClient(g.ResolveAuth(cfg), ts.Token)
	client.HTTP = ts.HTTP
	return client, nil
}

// TokenSource returns a token source for (audience, org) of the selected
// profile that uses the shared HTTP client.
func (g *Flags) TokenSource(cfg *config.Config, audience, orgID string) (*auth.TokenSource, error) {
	httpc, err := g.HTTPClient()
	if err != nil {
		return nil, err
	}
	ts := auth.NewTokenSource(g.ResolveAuth(cfg), audience, orgID)
	ts.Profile = cfg.Profile
	ts.HTTP = httpc
	if static, err := g.StaticToken(); err != nil {
		return nil, err
	} else if static != "" {
		ts.Static = static
		return ts, nil
	}
	// The active org is the one the user is known to belong to: when a
	// token for another org is refused, it tells a dead session apart
	// from a missing membership.
	ts.ProbeOrgID = cfg.ActiveOrg.ID
	switch {
	case orgID == cfg.ActiveOrg.ID && cfg.ActiveOrg.Slug != "":
		ts.OrgLabel = cfg.ActiveOrg.Slug
	case g.orgOverride() != "" && !orgs.IsID(g.orgOverride()):
		ts.OrgLabel = g.orgOverride()
	}
	return ts, nil
}

// StaticToken returns the access token given by --token-file or
// NORCUBE_TOKEN ("" when the profile's credential store is used).
func (g *Flags) StaticToken() (string, error) {
	res, err := g.Resolve()
	if err != nil {
		return "", err
	}
	switch res.Credentials.Value {
	case resolve.CredentialTokenFile:
		b, err := os.ReadFile(res.TokenFile) // #nosec G304 -- a path the user passed
		if err != nil {
			return "", clierr.Usage(fmt.Errorf("--token-file: %w", err))
		}
		tok := strings.TrimSpace(string(b))
		if tok == "" {
			return "", clierr.Usage(fmt.Errorf("--token-file %s is empty", res.TokenFile))
		}
		return tok, nil
	case config.CredentialEnv:
		tok := strings.TrimSpace(os.Getenv(resolve.EnvToken))
		if tok == "" {
			return "", fmt.Errorf("profile %s uses credential_store = \"env\", but %s is not set", res.Profile.Value, resolve.EnvToken)
		}
		return tok, nil
	}
	return "", nil
}

// ResolveAuth returns the auth-service base URL of this invocation.
func (g *Flags) ResolveAuth(cfg *config.Config) string { return cfg.Auth }

// ResolveWebApp returns the web-app base URL of this invocation.
func (g *Flags) ResolveWebApp(cfg *config.Config) string { return cfg.WebApp }

// orgOverride is the organization given by --org or NORCUBE_ORG.
func (g *Flags) orgOverride() string {
	if g.res != nil && g.res.OrgOverride {
		return g.res.Org.Value
	}
	return strings.TrimSpace(g.OrgOverride)
}

// Org returns the organization this invocation targets: --org or
// NORCUBE_ORG (an id or a slug, resolved against the organizations the
// user belongs to) wins over the profile's organization. The returned ID
// is empty when none is set. An unknown slug is a usage error.
func (g *Flags) Org(ctx context.Context, cfg *config.Config) (api.Organization, error) {
	// Every command that targets an organization needs a session, and
	// "sign in" is the more useful answer than "no organization" for a
	// profile that has neither.
	if err := g.RequireSession(cfg); err != nil {
		return api.Organization{}, err
	}
	active := api.Organization{ID: cfg.ActiveOrg.ID, Slug: cfg.ActiveOrg.Slug, Name: cfg.ActiveOrg.Name}
	value := g.orgOverride()
	if value == "" {
		if active.ID != "" || active.Slug == "" {
			return active, nil
		}
		// The profile names an organization the CLI hasn't resolved yet
		// (set with `configure set organization`).
		value = active.Slug
	}
	r := &orgs.Resolver{
		Profile: cfg.Profile,
		AuthURL: g.ResolveAuth(cfg),
		UserID:  cfg.User.ID,
		Active:  active,
		Cache:   orgs.DefaultCache(),
		// The list is fetched with a token for the active org: membership
		// of any org the user belongs to authorizes /organizations.
		List: func(ctx context.Context) ([]api.Organization, error) {
			client, err := g.AuthClient(cfg, cfg.ActiveOrg.ID)
			if err != nil {
				return nil, err
			}
			return client.ListOrganizations(ctx)
		},
	}
	org, err := r.Resolve(ctx, value)
	if err != nil {
		if active.ID == "" && g.orgOverride() == "" {
			return api.Organization{}, fmt.Errorf("profile %s: resolve organization %q: %w", cfg.Profile, value, err)
		}
		return api.Organization{}, err
	}
	if g.orgOverride() == "" && active.ID == "" {
		// Remember what the profile's slug resolved to.
		_ = g.SetActiveOrg(config.Organization{ID: org.ID, Slug: org.Slug, Name: org.Name})
	}
	return org, nil
}

// RequireSession returns auth.ErrNotLoggedIn when this invocation has no
// credentials at all: no --token-file or NORCUBE_TOKEN and no session in
// the profile's credential store. A session that exists but was revoked
// is only discovered when a token is minted.
func (g *Flags) RequireSession(cfg *config.Config) error {
	static, err := g.StaticToken()
	if err != nil {
		return err
	}
	if static != "" || auth.HasCredentials(cfg.Profile, g.ResolveAuth(cfg)) {
		return nil
	}
	return auth.ErrNotLoggedIn
}

// HasOrgFlag reports whether the organization was given for this command
// (--org or NORCUBE_ORG), as opposed to coming from the profile. A
// project's .langsync.json only applies when it wasn't.
func (g *Flags) HasOrgFlag() bool {
	_, _ = g.Resolve()
	return g.orgOverride() != ""
}

type ctxKey struct{}

// Attach installs flags on cmd's context. Call from the root command's
// PersistentPreRunE so children inherit it.
func Attach(cmd *cobra.Command, ctx context.Context, flags *Flags) {
	cmd.SetContext(context.WithValue(ctx, ctxKey{}, flags))
}

// Get returns the Flags attached by Attach. Returns a zero Flags struct if
// nothing is attached, so callers don't need to nil-check.
func Get(cmd *cobra.Command) *Flags {
	if v, ok := cmd.Context().Value(ctxKey{}).(*Flags); ok {
		return v
	}
	return &Flags{}
}
