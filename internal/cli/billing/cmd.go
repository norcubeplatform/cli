// Package billing implements the `norcube billing ...` command tree: the
// organization's account status, subscriptions, usage estimate, invoices
// and spending caps.
package billing

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/apierror"
	"github.com/norcubeplatform/cli/internal/api/billing"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/config"
	"github.com/norcubeplatform/cli/internal/service"
)

// billingContext is the Billing API client plus the profile, organization
// and output format of the invocation.
type billingContext = service.Client[*billing.ClientWithResponses]

// deps are what the commands need from outside. Production code uses
// defaultDeps; tests can pass fakes, so no command reaches for a global.
type deps struct {
	// client builds the Billing client for the invocation.
	client func(cmd *cobra.Command) (*billingContext, error)
	// streaming builds the same client on the streaming transport, for
	// invoice PDFs.
	streaming func(c *billingContext) (*billing.ClientWithResponses, error)
}

func defaultDeps() deps {
	return deps{
		client: func(cmd *cobra.Command) (*billingContext, error) {
			return service.New(cmd, config.ServiceBilling, newAPI)
		},
		streaming: func(c *billingContext) (*billing.ClientWithResponses, error) {
			return c.Streaming(newAPI)
		},
	}
}

func newAPI(baseURL string, httpc *http.Client, edit service.RequestEditor) (*billing.ClientWithResponses, error) {
	return billing.NewClientWithResponses(baseURL,
		billing.WithHTTPClient(httpc),
		billing.WithRequestEditorFn(edit))
}

// NewCmd returns the `billing` parent command with all subcommands.
func NewCmd() *cobra.Command { return newCmd(defaultDeps()) }

func newCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "billing",
		Short: "See the organization's billing: status, subscriptions, usage, invoices, spending caps",
		Long: `Read the active organization's billing: whether the account is in good
standing, its subscriptions, this month's usage estimate and invoices, and
set its monthly spending caps.

Plans and payment methods are managed in the dashboard. A CI token with
billing.read can run every read command here; changing the spending caps
needs a person signed in with billing permissions.`,
	}
	cmd.AddCommand(
		newStatusCmd(d),
		newSubscriptionsCmd(d),
		newUsageCmd(d),
		newInvoicesCmd(d),
		newBudgetCmd(d),
	)
	return cmd
}

// apiError renders a non-2xx Billing response through the shared
// formatter: its code and exit code follow the status, and a 403 for a
// scope a CI token lacks exits 5.
func apiError(resp *http.Response, body []byte, typed ...*billing.ResponseAPIError) error {
	conv := make([]*apierror.Typed, 0, len(typed))
	for _, t := range typed {
		if t != nil {
			conv = append(conv, &apierror.Typed{Msg: t.Msg, Type: string(t.Type)})
		}
	}
	return apierror.Format("billing", resp, body, conv...)
}

// money renders an amount in major units ("12.30 USD").
func money(amount float32, currency string) string {
	return fmt.Sprintf("%.2f %s", amount, strings.ToUpper(currency))
}

// cents renders an amount in US cents as dollars ("$100.00"), or
// "none" for nil.
func cents(c *int) string {
	if c == nil {
		return "none"
	}
	return fmt.Sprintf("$%d.%02d", *c/100, *c%100)
}

// parseCents reads a USD amount ("100", "99.50", "$1,000") into cents.
// "none" (or "off") is nil: no cap.
func parseCents(flag, s string) (*int, error) {
	v := strings.TrimSpace(strings.ToLower(s))
	if v == "none" || v == "off" {
		return nil, nil
	}
	v = strings.ReplaceAll(strings.TrimPrefix(v, "$"), ",", "")
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 || math.IsInf(f, 0) || math.IsNaN(f) || f > 1e9 {
		return nil, clierr.Usage(fmt.Errorf(`--%s %q: want an amount in US dollars (100, 99.50) or "none"`, flag, s))
	}
	c := int(math.Round(f * 100))
	return &c, nil
}
