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
	org, err := targetOrg(cmd.Context(), flags, cfg, o)
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
