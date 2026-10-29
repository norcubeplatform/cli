// Package service builds the typed API client a command needs, the same
// way for every Norcube service: it resolves the profile, the target
// organization, the service endpoint, a token source for the service's
// audience (from the selected credential store) and the shared HTTP
// transport. Product command packages call New once per invocation instead
// of each wiring these by hand.
package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api"
	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/config"
	"github.com/norcubeplatform/cli/internal/registry"
)

// RequestEditor adds the bearer token to a request; generated clients
// accept it as their RequestEditorFn.
type RequestEditor = func(ctx context.Context, req *http.Request) error

// Constructor creates a generated client for a base URL that sends its
// requests through httpc and edit.
type Constructor[T any] func(baseURL string, httpc *http.Client, edit RequestEditor) (T, error)

// Client is a typed API client plus the context it was built in.
type Client[T any] struct {
	// API is the generated client.
	API T
	// Service is the registry entry the client talks to.
	Service registry.Service
	// Config is the resolved configuration of the selected profile.
	Config *config.Config
	// Flags are the invocation's global flags.
	Flags *clictx.Flags
	// Org is the organization the client acts in.
	Org api.Organization
	// Tokens mints the access tokens the client sends.
	Tokens *auth.TokenSource
	// Output is the requested output format.
	Output string
}

// Streaming returns the same API client built on the streaming transport:
// no limit on the total response time (a server-sent event stream or a
// large export may take as long as it needs), but a body that stops
// sending data for the idle timeout fails. Use it only for those
// endpoints; regular requests keep the per-request timeout of API.
func (c *Client[T]) Streaming(ctor Constructor[T]) (T, error) {
	var zero T
	httpc, err := c.Flags.DownloadClient()
	if err != nil {
		return zero, err
	}
	client, err := ctor(c.Config.Endpoint(c.Service.Key), httpc, c.Tokens.BearerInjector())
	if err != nil {
		return zero, fmt.Errorf("%s client: %w", c.Service.Name, err)
	}
	return client, nil
}

// OrgLabel names the target organization for messages: its slug when
// known, otherwise its id.
func (c *Client[T]) OrgLabel() string {
	if c.Org.Slug != "" {
		return c.Org.Slug
	}
	return c.Org.ID
}

// ErrNoOrganization is returned when nothing names the organization to
// act in.
var ErrNoOrganization = clierr.Usage(errors.New("no active organization. Run `norcube org use <slug>` or pass --org <slug>"))

type options struct {
	orgID      string
	orgSlug    string
	orgDefault func() (api.Organization, bool)
}

// Option adjusts how New picks the organization.
type Option func(*options)

// ForOrg makes the client act in this organization, skipping --org and the
// profile (for commands that pick the organization themselves).
func ForOrg(id, slug string) Option {
	return func(o *options) { o.orgID, o.orgSlug = id, slug }
}

// WithOrgDefault supplies an organization that applies when neither --org
// nor NORCUBE_ORG is given, ahead of the profile's (a project file such
// as .langsync.json).
func WithOrgDefault(fn func() (api.Organization, bool)) Option {
	return func(o *options) { o.orgDefault = fn }
}

// New returns a client for the registry service key (config.ServiceSnapDB,
// ...), built with ctor.
func New[T any](cmd *cobra.Command, key string, ctor Constructor[T], opts ...Option) (*Client[T], error) {
	svc, ok := registry.Get(key)
	if !ok || svc.Audience == "" {
		return nil, fmt.Errorf("service %q has no API the CLI can call", key)
	}
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	flags := clictx.Get(cmd)
	cfg, err := flags.Config()
	if err != nil {
		return nil, err
	}
	ci, err := flags.CIToken(cfg)
	if err != nil {
		return nil, err
	}
	var org api.Organization
	if ci != nil {
		org, err = ciOrg(cmd.Context(), flags, ci, svc.Audience, o)
	} else {
		org, err = targetOrg(cmd.Context(), flags, cfg, o)
	}
	if err != nil {
		return nil, err
	}

	ts, err := flags.TokenSource(cfg, svc.Audience, org.ID)
	if err != nil {
		return nil, err
	}
	if org.Slug != "" {
		ts.OrgLabel = org.Slug
	}

	client, err := ctor(cfg.Endpoint(key), ts.HTTP, ts.BearerInjector())
	if err != nil {
		return nil, fmt.Errorf("%s client: %w", svc.Name, err)
	}
	return &Client[T]{
		API:     client,
		Service: svc,
		Config:  cfg,
		Flags:   flags,
		Org:     org,
		Tokens:  ts,
		Output:  flags.Output,
	}, nil
}

// targetOrg applies the organization precedence: an explicit ForOrg, then
// --org / NORCUBE_ORG, then the command's default (project file), then the
// profile.
func targetOrg(ctx context.Context, flags *clictx.Flags, cfg *config.Config, o options) (api.Organization, error) {
	if o.orgID != "" {
		if err := flags.RequireSession(cfg); err != nil {
			return api.Organization{}, err
		}
		return api.Organization{ID: o.orgID, Slug: o.orgSlug}, nil
	}
	if o.orgDefault != nil && !flags.HasOrgFlag() {
		if org, ok := o.orgDefault(); ok && org.ID != "" {
			if err := flags.RequireSession(cfg); err != nil {
				return api.Organization{}, err
			}
			return org, nil
		}
	}
	org, err := flags.Org(ctx, cfg)
	if err != nil {
		return api.Organization{}, err
	}
	if org.ID == "" {
		return api.Organization{}, ErrNoOrganization
	}
	return org, nil
}

// ciOrg is the organization a CI token belongs to, learned from its first
// exchange for the service. A CI token can't act anywhere else, so an
// organization named by --org, NORCUBE_ORG, the command or a project file
// must be that one: running a deploy step against another organization
// than the one written in the config is an error, not something to paper
// over.
func ciOrg(ctx context.Context, flags *clictx.Flags, ci *auth.CIExchanger, audience string, o options) (api.Organization, error) {
	g, err := ci.Grant(ctx, audience)
	if err != nil {
		return api.Organization{}, err
	}
	org := api.Organization{ID: g.OrgID, Slug: g.OrgSlug, Name: g.OrgName}
	mismatch := func(source, want string) error {
		return clierr.Usage(fmt.Errorf("the CI token belongs to organization %s, but %s names %s. A CI token only works in its own organization: drop %s or use a token created in %s",
			orgName(org), source, want, source, want))
	}
	switch {
	case o.orgID != "":
		if o.orgID != org.ID {
			return api.Organization{}, mismatch("the command", firstNonEmpty(o.orgSlug, o.orgID))
		}
	case flags.OrgFlag() != "":
		if want := flags.OrgFlag(); want != org.ID && !strings.EqualFold(want, org.Slug) {
			return api.Organization{}, mismatch("--org / NORCUBE_ORG", want)
		}
	case o.orgDefault != nil:
		if want, ok := o.orgDefault(); ok && want.ID != "" && want.ID != org.ID {
			return api.Organization{}, mismatch("the project file", firstNonEmpty(want.Slug, want.ID))
		}
	}
	return org, nil
}

func orgName(o api.Organization) string {
	return firstNonEmpty(o.Slug, o.ID)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
