// Package langsync implements the `norcube langsync ...` command tree.
package langsync

import (
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api"
	"github.com/norcubeplatform/cli/internal/api/langsync"
	"github.com/norcubeplatform/cli/internal/cli/langsync/projectcfg"
	"github.com/norcubeplatform/cli/internal/config"
	"github.com/norcubeplatform/cli/internal/service"
)

// NewCmd returns the `langsync` parent command with all subcommands wired up.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "langsync",
		Short: "Manage LangSync namespaces, terms, languages and translations",
	}
	cmd.AddCommand(newMarkCmd(), newTranslationsCmd(), newNamespaceCmd(), newLangCmd(), newPromptsCmd(),
		newGlossaryCmd(), newAPIKeysCmd(), newAuditCmd(), newInitCmd(), newSyncCmd(), newPullCmd())
	registerCompletions(cmd)
	return cmd
}

// langsyncContext is the LangSync API client plus the profile,
// organization and output format of the invocation.
type langsyncContext = service.Client[*langsync.ClientWithResponses]

// newLangsyncContext targets --org / NORCUBE_ORG, else the organization a
// .langsync.json in this directory or above pins, else the profile's. The
// project file wins over the profile so a developer working in several
// organizations' repositories always hits the right one; an explicit
// --org still overrides it.
func newLangsyncContext(cmd *cobra.Command) (*langsyncContext, error) {
	return service.New(cmd, config.ServiceLangsync, newLangsyncAPI,
		service.WithOrgDefault(func() (api.Organization, bool) {
			id, slug, ok := tryReadProjectOrg()
			return api.Organization{ID: id, Slug: slug}, ok
		}))
}

// newLangsyncContextForOrg is used by init, which picks the organization
// before any client exists.
func newLangsyncContextForOrg(cmd *cobra.Command, orgID string) (*langsyncContext, error) {
	if orgID == "" {
		return nil, fmt.Errorf("orgID must not be empty")
	}
	return service.New(cmd, config.ServiceLangsync, newLangsyncAPI, service.ForOrg(orgID, ""))
}

func newLangsyncAPI(baseURL string, httpc *http.Client, edit service.RequestEditor) (*langsync.ClientWithResponses, error) {
	return langsync.NewClientWithResponses(baseURL,
		langsync.WithHTTPClient(httpc),
		langsync.WithRequestEditorFn(edit))
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
// `langsync namespaces list` to fail just because a colleague's
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
