// Package langsync implements the `norcube langsync ...` command tree.
// Mirrors the structure of internal/cli/snapdb — owns the wiring between
// the CLI's TokenSource (audience: langsync-api) and the generated
// langsync HTTP client.
package langsync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/langsync"
	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/cli/langsync/projectcfg"
	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/config"
)

// NewCmd returns the `langsync` parent command with all subcommands wired up.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "langsync",
		Short: "Manage Langsync namespaces, marks (terms), and translations",
	}
	cmd.AddCommand(newMarkCmd(), newNamespaceCmd(), newLangCmd(), newInitCmd(), newSyncCmd(), newPullCmd())
	return cmd
}

// langsyncContext bundles per-invocation state used across langsync
// subcommands — resolved config, the typed client, and the requested
// output format. Built once per RunE so child commands don't repeat
// the wiring.
type langsyncContext struct {
	cfg    *config.Config
	flags  *clictx.Flags
	client *langsync.ClientWithResponses
	output string
}

func newLangsyncContext(cmd *cobra.Command) (*langsyncContext, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	flags := clictx.Get(cmd)
	authURL := flags.ResolveAuth(cfg)

	// Org precedence:
	//   1. --org flag (explicit user override)
	//   2. .langsync.json organization (project-pinned, if a config
	//      exists in cwd or any ancestor)
	//   3. cfg.ActiveOrg (CLI's persisted active org)
	//
	// The project-config override is what makes a multi-org dev
	// safe: if I'm inside `~/work/acme-project`, running any
	// langsync command in that tree hits acme's org regardless of
	// what `nrc org use` last set. --org still wins so an explicit
	// override (e.g. for debugging) is always available.
	orgID, _ := flags.ResolveOrg(cfg)
	flagSet := flags.HasOrgFlag()
	projectOrgNote := ""
	if !flagSet {
		if projOrgID, projSlug, ok := tryReadProjectOrg(); ok {
			if projOrgID != cfg.ActiveOrg.ID {
				// Only note when we're actually overriding —
				// silent when the project's org matches the
				// active one (most common case).
				projectOrgNote = fmt.Sprintf("Operating on org %q (from .langsync.json); active org is %q.", projSlug, cfg.ActiveOrg.Slug)
			}
			orgID = projOrgID
		}
	}
	if orgID == "" {
		return nil, fmt.Errorf("no active organization — run `norcube org use <slug>` or pass --org <slug>")
	}
	if projectOrgNote != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), projectOrgNote)
	}

	ts := auth.NewTokenSource(authURL, auth.AudienceLangsync, orgID)

	bearerInjector := func(ctx context.Context, req *http.Request) error {
		token, err := ts.Token(ctx)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		return nil
	}

	client, err := langsync.NewClientWithResponses(
		cfg.Langsync,
		langsync.WithRequestEditorFn(bearerInjector),
	)
	if err != nil {
		return nil, fmt.Errorf("langsync client: %w", err)
	}

	return &langsyncContext{
		cfg:    cfg,
		flags:  flags,
		client: client,
		output: flags.Output,
	}, nil
}

// newLangsyncContextForOrg is the explicit-org constructor used by
// init, which decides the target org BEFORE the client exists (so
// the picker/auth dance happens against the right org from the
// outset). All other callers should use newLangsyncContext, which
// resolves the org via the flag/project-config/active-org chain.
func newLangsyncContextForOrg(cmd *cobra.Command, orgID string) (*langsyncContext, error) {
	if orgID == "" {
		return nil, fmt.Errorf("orgID must not be empty")
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	flags := clictx.Get(cmd)
	authURL := flags.ResolveAuth(cfg)

	ts := auth.NewTokenSource(authURL, auth.AudienceLangsync, orgID)

	bearerInjector := func(ctx context.Context, req *http.Request) error {
		token, err := ts.Token(ctx)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		return nil
	}

	client, err := langsync.NewClientWithResponses(
		cfg.Langsync,
		langsync.WithRequestEditorFn(bearerInjector),
	)
	if err != nil {
		return nil, fmt.Errorf("langsync client: %w", err)
	}

	return &langsyncContext{
		cfg:    cfg,
		flags:  flags,
		client: client,
		output: flags.Output,
	}, nil
}

// tryReadProjectOrg walks up from cwd looking for a .langsync.json
// and returns its org id+slug if both are set. Returns ok=false on
// any failure (missing file, parse error, no organization block) —
// the caller treats that as "no project context, use CLI active org."
//
// We deliberately swallow errors here: the project-config override
// is a convenience, not a hard requirement. If the file is broken,
// other langsync commands (sync, pull) will surface a clearer error
// when they explicitly load the config; we don't want every
// `langsync namespace list` to fail just because a colleague's
// committed file is malformed.
func tryReadProjectOrg() (orgID, slug string, ok bool) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", "", false
	}
	path, err := projectcfg.Find(cwd)
	if err != nil {
		return "", "", false
	}
	cfg, err := projectcfg.Load(path)
	if err != nil {
		return "", "", false
	}
	if cfg.Organization == nil || cfg.Organization.ID == "" {
		return "", "", false
	}
	return cfg.Organization.ID, cfg.Organization.Slug, true
}

// Silence unused-import for projectcfg in commands that never reach
// tryReadProjectOrg (keeps the import line alive across refactors
// where the helper might temporarily go unreferenced).
var _ = errors.New
