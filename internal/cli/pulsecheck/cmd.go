// Package pulsecheck implements the `norcube pulsecheck ...` command tree:
// uptime, SSL certificate and domain expiry monitors, their check history,
// incidents and alert channels, and an organization-wide status for
// scripts and CI.
package pulsecheck

import (
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/apierror"
	"github.com/norcubeplatform/cli/internal/api/pulsecheck"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/config"
	"github.com/norcubeplatform/cli/internal/service"
)

// pcContext is the PulseCheck API client plus the profile, organization
// and output format of the invocation.
type pcContext = service.Client[*pulsecheck.ClientWithResponses]

// deps are what the commands need from outside: how to build the API
// client and what time it is. Production code uses defaultDeps; tests
// pass fakes, so no command reaches for a global.
type deps struct {
	// client builds the PulseCheck client for the invocation.
	client func(cmd *cobra.Command) (*pcContext, error)
	// now is the current time (--since ages, timeline durations).
	now func() time.Time
}

func defaultDeps() deps {
	return deps{
		client: func(cmd *cobra.Command) (*pcContext, error) {
			return service.New(cmd, config.ServicePulsecheck, newAPI)
		},
		now: time.Now,
	}
}

func newAPI(baseURL string, httpc *http.Client, edit service.RequestEditor) (*pulsecheck.ClientWithResponses, error) {
	return pulsecheck.NewClientWithResponses(baseURL,
		pulsecheck.WithHTTPClient(httpc),
		pulsecheck.WithRequestEditorFn(edit))
}

// NewCmd returns the `pulsecheck` parent command with all subcommands.
func NewCmd() *cobra.Command { return newCmd(defaultDeps()) }

func newCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pulsecheck",
		Short: "Monitor uptime, SSL certificates and domain expiry, and manage incidents and alerts",
		Long: `PulseCheck checks your sites, certificates and domains on a schedule
from Norcube's servers. A monitor is an HTTP check (optionally with a
keyword), an SSL certificate check or a domain expiry check. Problems
confirmed by consecutive checks open incidents, which alert through your
alert channels (email, webhook, Slack, Discord, SMS).

Monitors and channels are named by their id or, where a command says so,
by their exact name. Checks count towards your PulseCheck usage, on a
schedule and when you run one now.

In CI, "pulsecheck status --fail-on critical" fails the job while a
critical incident is open, and "pulsecheck monitors run <monitor> --wait"
fails it when the check finds the monitor down. CI tokens need the
pulsecheck.read, pulsecheck.check or pulsecheck.write scope; alert
channels can only be changed by a signed-in person.`,
	}
	cmd.AddCommand(
		newStatusCmd(d),
		newMonitorsCmd(d),
		newIncidentsCmd(d),
		newChannelsCmd(d),
	)
	registerCompletions(cmd, d)
	return cmd
}

// apiError renders a non-2xx PulseCheck response (an RFC 9457 problem)
// through the shared formatter: its code and exit code follow the status,
// and a 403 for a scope the token lacks exits 5.
func apiError(resp *http.Response, body []byte) error {
	return apierror.Format("pulsecheck", resp, body)
}

// nextCursor reads the next-page cursor of a PulseCheck list.
func nextCursor(c pulsecheck.ResponseAPIResponsePaginationCursors) string {
	return cmdutil.Deref(c.Next)
}

// Page sizes and caps for --all-pages. The API returns at most 100 items
// per page.
const (
	defaultPageSize = 50
	maxPageSize     = 100
	maxItems        = 10000
)
