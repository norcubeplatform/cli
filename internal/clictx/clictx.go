// Package clictx carries the global per-invocation state (flags, cancellable
// context) attached to every cobra command, so subcommand packages can read
// it without importing the root cli package and creating an import cycle.
package clictx

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/config"
)

// Flags are the values of the persistent flags declared on the root command.
type Flags struct {
	AuthOverride   string
	WebAppOverride string
	OrgOverride    string
	Output         string
	NoPager        bool
}

// ResolveAuth returns the auth-service base URL: --auth-url > config.auth.
func (g *Flags) ResolveAuth(cfg *config.Config) string {
	if g.AuthOverride != "" {
		return g.AuthOverride
	}
	return cfg.Auth
}

// ResolveWebApp returns the web-app base URL: --web-app > config.web_app.
func (g *Flags) ResolveWebApp(cfg *config.Config) string {
	if g.WebAppOverride != "" {
		return g.WebAppOverride
	}
	return cfg.WebApp
}

// ResolveOrg returns the org id+slug for this invocation. The --org flag
// (slug or id) wins over the persisted active org. When --org is set we
// can't tell whether it's a slug or id without a round trip, so both are
// returned identical and the caller resolves as needed.
func (g *Flags) ResolveOrg(cfg *config.Config) (id, slug string) {
	if g.OrgOverride != "" {
		return g.OrgOverride, g.OrgOverride
	}
	return cfg.ActiveOrg.ID, cfg.ActiveOrg.Slug
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
