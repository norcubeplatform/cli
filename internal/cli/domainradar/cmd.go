// Package domainradar implements the `norcube domainradar ...` command
// tree: domain checks, bulk checks, saved domains, brand monitoring and
// the name generator.
package domainradar

import (
	"context"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/apierror"
	"github.com/norcubeplatform/cli/internal/api/domainradar"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/config"
	"github.com/norcubeplatform/cli/internal/service"
)

// drContext is the DomainRadar API client plus the profile, organization
// and output format of the invocation.
type drContext = service.Client[*domainradar.ClientWithResponses]

// deps are what the commands need from outside: how to build the API
// client, and how to wait. Production code uses defaultDeps; tests pass
// fakes, so no command reaches for a global.
type deps struct {
	// client builds the DomainRadar client for the invocation.
	client func(cmd *cobra.Command) (*drContext, error)
	// streaming builds the same client on the streaming transport, for the
	// event stream and exports.
	streaming func(c *drContext) (*domainradar.ClientWithResponses, error)
	// sleep waits between polls and reconnects; it returns early when ctx
	// ends.
	sleep func(ctx context.Context, d time.Duration) error
}

func defaultDeps() deps {
	return deps{
		client: func(cmd *cobra.Command) (*drContext, error) {
			return service.New(cmd, config.ServiceDomainradar, newAPI)
		},
		streaming: func(c *drContext) (*domainradar.ClientWithResponses, error) {
			return c.Streaming(newAPI)
		},
		sleep: cmdutil.SleepContext,
	}
}

func newAPI(baseURL string, httpc *http.Client, edit service.RequestEditor) (*domainradar.ClientWithResponses, error) {
	return domainradar.NewClientWithResponses(baseURL,
		domainradar.WithHTTPClient(httpc),
		domainradar.WithRequestEditorFn(edit))
}

// NewCmd returns the `domainradar` parent command with all subcommands.
func NewCmd() *cobra.Command { return newCmd(defaultDeps()) }

func newCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "domainradar",
		Short: "Check domain availability, run bulk checks, monitor brands and generate names",
		Long: `DomainRadar answers whether domains are free to register, from a quick
bloom-filter answer up to full WHOIS data, one at a time or in bulk. It also
watches newly issued certificates for lookalikes of your brand and generates
name ideas.

Checks count towards your DomainRadar usage (each one is billed by the
method that answered it; cached answers are free). See
https://norcube.com/domainradar/#pricing.`,
	}
	cmd.AddCommand(
		newCheckCmd(d),
		newBulkCmd(d),
		newSavedCmd(d),
		newWatchersCmd(d),
		newHitsCmd(d),
		newNamesCmd(d),
	)
	registerCompletions(cmd, d)
	return cmd
}

// apiError renders a non-2xx DomainRadar response through the shared
// formatter: its code and exit code follow the status, and a 403 for a
// scope a CI token lacks exits 5.
func apiError(resp *http.Response, body []byte) error {
	return apierror.Format("domainradar", resp, body)
}

// nextCursor reads the next-page cursor of a DomainRadar list.
func nextCursor(c domainradar.ResponseAPIResponsePaginationCursors) string {
	return cmdutil.Deref(c.Next)
}

// Page sizes and caps for --all-pages.
const (
	defaultPageSize = 50
	maxItems        = 10000
)

// checkModes are the check depths the API accepts, in the order of cost.
var checkModes = []string{
	string(domainradar.CheckModeBloom),
	string(domainradar.CheckModeVerified),
	string(domainradar.CheckModeInfo),
	string(domainradar.CheckModeFull),
}

// modeFlagHelp is the --mode help shared by every command that runs checks.
const modeFlagHelp = "Check depth: bloom (fastest, a bloom-filter hit answers \"registered\"), verified (DNS, then RDAP or WHOIS), info (adds registrar and DNS records) or full (also keeps the raw registry data)"

// metered is the help note for commands that run billable checks.
const metered = `Checks are billed by the method that answered them; cached answers
(see --max-age) are free.`
