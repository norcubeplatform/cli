package billing

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/billing"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

func newStatusCmd(d deps) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether the organization's account is in good standing",
		Long: `Shows the account's status: active, grace (a payment failed; the grace
period runs until the date shown) or blocked (paid usage and new
resources are refused: an unpaid balance, or the month's charges reached
the hard spending cap).`,
		Example: `  norcube billing status
  norcube billing status -o json --query status`,
		Args: cobra.NoArgs,
		RunE: cmdutil.Run(func(cmd *cobra.Command, _ []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.GetBillingAccountStatusWithResponse(cmd.Context())
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON500)
			}
			s := *res.JSON200
			fields := []output.Field{{Name: "Status", Value: string(s.Status)}}
			if s.GraceStartedAt != nil {
				fields = append(fields,
					output.Field{Name: "Grace since", Value: output.WhenString(*s.GraceStartedAt)},
					output.Field{Name: "Grace until", Value: output.WhenString(cmdutil.Deref(s.GraceExpiresAt))})
			}
			if s.BlockedAt != nil {
				fields = append(fields, output.Field{Name: "Blocked since", Value: output.WhenString(*s.BlockedAt)})
			}
			return output.Object{Value: s, Fields: fields}, nil
		}),
	}
}

func newSubscriptionsCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "subscriptions",
		Aliases: []string{"subscription"},
		Short:   "Show the organization's subscriptions",
	}
	cmd.AddCommand(newSubscriptionListCmd(d))
	return cmd
}

func newSubscriptionListCmd(d deps) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List each service's billing and the plan the organization is on",
		Long: `Lists every billable service: how it is billed (metered: pay as you go;
plans: always on a plan; choice: pay as you go or a plan) and the
organization's current plan there. Change plans in the dashboard.`,
		Example: `  norcube billing subscriptions list
  norcube billing subscriptions list -o json --query "[?subscription].{service: code, plan: subscription.planCode}"`,
		Args: cobra.NoArgs,
		RunE: cmdutil.Run(func(cmd *cobra.Command, _ []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.GetBillingServicesWithResponse(cmd.Context())
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON500)
			}
			return output.Table[billing.DtoBillingService]{
				Headers: []string{"SERVICE", "BILLING", "PLAN", "STATUS", "PERIOD_END"},
				Style:   output.Style{StatusColumn: 3},
				Rows: func(s billing.DtoBillingService) []string {
					plan, status, end := "pay as you go", "", ""
					if sub := s.Subscription; sub != nil {
						plan = cmdutil.DerefOr(sub.PlanName, cmdutil.Deref(sub.PlanCode))
						status = string(cmdutil.Deref(sub.Status))
						end = dateOnly(cmdutil.Deref(sub.CurrentPeriodEnd))
					}
					return []string{cmdutil.DerefOr(s.Name, cmdutil.Deref(s.Code)), string(cmdutil.Deref(s.BillingMode)), plan, status, end}
				},
				Items: *res.JSON200,
			}, nil
		}),
	}
}

func newUsageCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "See this month's usage and what it will cost",
	}
	cmd.AddCommand(newUsageEstimateCmd(d), newUsageMetricsCmd(d))
	return cmd
}

func newUsageEstimateCmd(d deps) *cobra.Command {
	return &cobra.Command{
		Use:   "estimate",
		Short: "Show this month's charges so far and the estimate for the whole month",
		Long: `Shows what the organization's usage costs so far this billing month and
the estimated total, in US dollars. The estimate weighs the month's pace
against previous months; "linear" is the pace alone.`,
		Example: `  norcube billing usage estimate
  norcube billing usage estimate -o json --query estimatedTotal`,
		Args: cobra.NoArgs,
		RunE: cmdutil.Run(func(cmd *cobra.Command, _ []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.GetBillingEstimateWithResponse(cmd.Context())
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON500)
			}
			e := *res.JSON200
			return output.Object{Value: e, Fields: []output.Field{
				{Name: "Period", Value: e.PeriodStart + " – " + e.PeriodEnd},
				{Name: "Day", Value: fmt.Sprintf("%d of %d", e.DaysElapsed, e.DaysInMonth)},
				{Name: "So far", Value: money(e.CurrentUsage, "USD")},
				{Name: "Estimated total", Value: money(e.EstimatedTotal, "USD")},
				{Name: "Linear estimate", Value: money(e.LinearEstimate, "USD")},
				{Name: "Monthly average", Value: money(e.HistoricalAverage, "USD")},
			}}, nil
		}),
	}
}

func newUsageMetricsCmd(d deps) *cobra.Command {
	return &cobra.Command{
		Use:   "metrics",
		Short: "Show usage per metric against its free tier",
		Long: `Lists every billable metric with its free tier, the usage so far this
month and the projection for the month's end. OVER marks metrics on
track to exceed the free tier, which is where charges start.`,
		Example: `  norcube billing usage metrics
  norcube billing usage metrics -o json --query "[?overflowProjected].metricCode"`,
		Args: cobra.NoArgs,
		RunE: cmdutil.Run(func(cmd *cobra.Command, _ []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.GetFreeTierStatusWithResponse(cmd.Context())
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON500)
			}
			items := []billing.DtoFreeTierMetric{}
			if res.JSON200.Metrics != nil {
				items = *res.JSON200.Metrics
			}
			return output.Table[billing.DtoFreeTierMetric]{
				Headers:   []string{"SERVICE", "METRIC", "FREE", "SO_FAR", "PROJECTED", "LAST_MONTH", "UNIT", "OVER"},
				MaxWidths: []int{0, 36, 0, 0, 0, 0, 0, 0},
				Rows: func(m billing.DtoFreeTierMetric) []string {
					free := fmt.Sprint(cmdutil.Deref(m.FreeTierUnits))
					if cmdutil.Deref(m.FreeTierUnlimited) {
						free = "unlimited"
					}
					over := ""
					if cmdutil.Deref(m.OverflowProjected) {
						over = "yes"
					}
					return []string{
						cmdutil.Deref(m.ServiceName), cmdutil.DerefOr(m.MetricName, cmdutil.Deref(m.MetricCode)), free,
						units(m.CurrentUnits), units(m.ProjectedEndOfMonthUnits), units(m.PreviousMonthUnits),
						cmdutil.Deref(m.Unit), over,
					}
				},
				Items: items,
			}, nil
		}),
	}
}

// units renders a usage figure without trailing zeros.
func units(v *float32) string {
	if v == nil {
		return "—"
	}
	s := fmt.Sprintf("%.2f", *v)
	return strings.TrimSuffix(strings.TrimRight(s, "0"), ".")
}

// dateOnly trims a timestamp to its date.
func dateOnly(s string) string {
	if len(s) >= 10 && s[4] == '-' && s[7] == '-' {
		return s[:10]
	}
	return s
}
