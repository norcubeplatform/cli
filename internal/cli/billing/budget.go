package billing

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/billing"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

func newBudgetCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "budget",
		Short: "Show and set the organization's monthly spending caps",
		Long: `The budget caps what the organization spends in a billing month, in US
dollars. When the month's charges reach the hard cap, the account is
blocked and paid usage is refused (` + "`norcube billing status`" + ` shows it); a
soft cap only warns. Alert thresholds are percentages of the hard cap (of
the soft cap when there is no hard cap) at which the organization's
invoicing email address gets a warning.`,
	}
	cmd.AddCommand(newBudgetGetCmd(d), newBudgetSetCmd(d))
	return cmd
}

func budgetObject(b billing.DtoSpendingCap) output.Object {
	alerts := "—"
	if b.AlertThresholds != nil && len(*b.AlertThresholds) > 0 {
		parts := make([]string, len(*b.AlertThresholds))
		for i, t := range *b.AlertThresholds {
			parts[i] = fmt.Sprintf("%d%%", t)
		}
		alerts = strings.Join(parts, ", ")
	}
	return output.Object{Value: b, Fields: []output.Field{
		{Name: "Hard cap", Value: cents(b.HardCapCents)},
		{Name: "Soft cap", Value: cents(b.SoftCapCents)},
		{Name: "Alerts at", Value: alerts},
	}}
}

func getBudget(cmd *cobra.Command, c *billingContext) (billing.DtoSpendingCap, error) {
	res, err := c.API.GetSpendingCapWithResponse(cmd.Context())
	if err != nil {
		return billing.DtoSpendingCap{}, err
	}
	if res.JSON200 == nil {
		return billing.DtoSpendingCap{}, apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON500)
	}
	return *res.JSON200, nil
}

func newBudgetGetCmd(d deps) *cobra.Command {
	return &cobra.Command{
		Use:   "get",
		Short: "Show the monthly spending caps and alert thresholds",
		Example: `  norcube billing budget get
  norcube billing budget get -o json --query hardCapCents`,
		Args: cobra.NoArgs,
		RunE: cmdutil.Run(func(cmd *cobra.Command, _ []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			b, err := getBudget(cmd, c)
			if err != nil {
				return nil, err
			}
			return budgetObject(b), nil
		}),
	}
}

// parseAlerts reads --alerts: comma-separated percentages from 1 to 100.
func parseAlerts(s string) ([]int, error) {
	var out []int
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSuffix(strings.TrimSpace(p), "%")
		if p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 100 {
			return nil, clierr.Usage(fmt.Errorf("--alerts %q: want percentages from 1 to 100, comma-separated (50,80,100)", s))
		}
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, clierr.Usage(errors.New("--alerts needs at least one percentage"))
	}
	slices.Sort(out)
	return out, nil
}

func newBudgetSetCmd(d deps) *cobra.Command {
	var (
		hard, soft, alerts string
		yes                bool
	)
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Set the monthly spending caps or alert thresholds",
		Long: `Changes only what you pass; the other settings stay as they are.

  --hard-cap 500     stop paid usage at $500.00 a month ("none" removes it)
  --soft-cap 300     warn at $300.00 a month ("none" removes it)
  --alerts 50,80,100 email at these percentages of the cap

Reaching a hard cap blocks paid usage, so the command asks first; --yes
answers for scripts, and without a terminal --yes is required.`,
		Example: `  norcube billing budget set --hard-cap 500 --soft-cap 300
  norcube billing budget set --alerts 50,90 --yes
  norcube billing budget set --hard-cap none`,
		Args: cobra.NoArgs,
		RunE: cmdutil.Run(func(cmd *cobra.Command, _ []string) (any, error) {
			changed := cmd.Flags().Changed
			if !changed("hard-cap") && !changed("soft-cap") && !changed("alerts") {
				return nil, clierr.Usage(errors.New("nothing to set: pass --hard-cap, --soft-cap or --alerts"))
			}
			var (
				hardCents, softCents *int
				thresholds           []int
				err                  error
			)
			if changed("hard-cap") {
				if hardCents, err = parseCents("hard-cap", hard); err != nil {
					return nil, err
				}
			}
			if changed("soft-cap") {
				if softCents, err = parseCents("soft-cap", soft); err != nil {
					return nil, err
				}
			}
			if changed("alerts") {
				if thresholds, err = parseAlerts(alerts); err != nil {
					return nil, err
				}
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			// The API replaces the whole budget: start from what is set.
			cur, err := getBudget(cmd, c)
			if err != nil {
				return nil, err
			}
			next := cur
			if changed("hard-cap") {
				next.HardCapCents = hardCents
			}
			if changed("soft-cap") {
				next.SoftCapCents = softCents
			}
			if changed("alerts") {
				next.AlertThresholds = &thresholds
			}
			if h, s := next.HardCapCents, next.SoftCapCents; h != nil && s != nil && *s > *h {
				return nil, clierr.Usage(fmt.Errorf("the soft cap (%s) is above the hard cap (%s)", cents(s), cents(h)))
			}
			ok, err := cmdutil.Confirm(budgetQuestion(cur, next), yes)
			if err != nil || !ok {
				return nil, cmdutil.Cancelled(cmd, err)
			}
			res, err := c.API.UpdateSpendingCapWithResponse(cmd.Context(), billing.UpdateSpendingCapJSONRequestBody(next))
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON500)
			}
			return budgetObject(*res.JSON200), nil
		}),
	}
	cmd.Flags().StringVar(&hard, "hard-cap", "", `Monthly hard cap in US dollars ("none" removes it)`)
	cmd.Flags().StringVar(&soft, "soft-cap", "", `Monthly soft (warning) cap in US dollars ("none" removes it)`)
	cmd.Flags().StringVar(&alerts, "alerts", "", "Alert thresholds, percentages of the cap (50,80,100)")
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip the confirmation prompt (required when stdin isn't a terminal)")
	return cmd
}

// budgetQuestion describes the change for the confirmation prompt.
func budgetQuestion(cur, next billing.DtoSpendingCap) string {
	var changes []string
	if !samePtr(cur.HardCapCents, next.HardCapCents) {
		changes = append(changes, fmt.Sprintf("hard cap %s → %s", cents(cur.HardCapCents), cents(next.HardCapCents)))
	}
	if !samePtr(cur.SoftCapCents, next.SoftCapCents) {
		changes = append(changes, fmt.Sprintf("soft cap %s → %s", cents(cur.SoftCapCents), cents(next.SoftCapCents)))
	}
	if len(changes) == 0 {
		return "Update the alert thresholds of the organization's budget?"
	}
	return "Change the organization's budget: " + strings.Join(changes, ", ") + "?"
}

func samePtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
