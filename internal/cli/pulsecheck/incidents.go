package pulsecheck

import (
	"context"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/pulsecheck"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

var (
	incidentStatuses   = []string{"open", "resolved"}
	incidentSeverities = []string{"warning", "critical"}
	incidentAspects    = []string{"availability", "certificate", "domain"}
)

func newIncidentsCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "incidents",
		Aliases: []string{"incident"},
		Short:   "List, inspect and acknowledge incidents",
		Long: `An incident is a confirmed problem with one aspect of a monitor: its
availability, its certificate or its domain. It opens after the monitor's
confirmations (or at once for an expiry), alerts the monitor's channels,
reminds until acknowledged, and resolves when the checks are good again.`,
	}
	cmd.AddCommand(newIncidentListCmd(d), newIncidentGetCmd(d), newIncidentAckCmd(d), newIncidentSummaryCmd(d))
	return cmd
}

func newIncidentListCmd(d deps) *cobra.Command {
	var (
		pf                                cmdutil.PageFlags
		status, severity, aspect, monitor string
		search, since, until              string
		acked, unacked                    bool
	)
	cmd := &cobra.Command{
		Use:         "list",
		Annotations: cmdutil.Reads(),
		Short:       "List incidents, open ones first",
		Long: `Lists incidents: open critical ones first, then open warnings, then
resolved ones, newest first within each. Every filter given must match.
--severity reads an open incident's current severity and a resolved one's
peak, so "--severity critical" finds every incident that ever paged.
--since and --until select by when the incident started.`,
		Example: `  norcube pulsecheck incidents list --status open
  norcube pulsecheck incidents list --status open --unacknowledged --severity critical
  norcube pulsecheck incidents list --monitor "Shop" --since 30d -o json`,
		Args: cobra.NoArgs,
		RunE: cmdutil.Run(func(cmd *cobra.Command, _ []string) (any, error) {
			params := pulsecheck.ListIncidentsParams{}
			if status != "" {
				if err := oneOf("--status", status, incidentStatuses); err != nil {
					return nil, err
				}
				v := pulsecheck.ListIncidentsParamsStatus(status)
				params.Status = &v
			}
			if severity != "" {
				if err := oneOf("--severity", severity, incidentSeverities); err != nil {
					return nil, err
				}
				v := pulsecheck.ListIncidentsParamsSeverity(severity)
				params.Severity = &v
			}
			if aspect != "" {
				if err := oneOf("--aspect", aspect, incidentAspects); err != nil {
					return nil, err
				}
				v := pulsecheck.ListIncidentsParamsAspect(aspect)
				params.Aspect = &v
			}
			if acked || unacked {
				v := pulsecheck.ListIncidentsParamsAcknowledged(strconv.FormatBool(acked))
				params.Acknowledged = &v
			}
			if search != "" {
				params.Q = &search
			}
			if err := timeRange(since, until, d.now(), &params.From, &params.To); err != nil {
				return nil, err
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			if monitor != "" {
				id, err := monitorRef(cmd.Context(), c, monitor)
				if err != nil {
					return nil, err
				}
				params.MonitorId = &id
			}
			page, err := cmdutil.Paginate(cmd.Context(), pf, func(ctx context.Context, cursor string, size int) ([]pulsecheck.DtoIncident, string, error) {
				p := params
				p.Limit = &size
				if cursor != "" {
					p.Cursor = &cursor
				}
				res, err := c.API.ListIncidentsWithResponse(ctx, &p)
				if err != nil {
					return nil, "", err
				}
				if res.JSON200 == nil {
					return nil, "", apiError(res.HTTPResponse, res.Body)
				}
				return res.JSON200.List, nextCursor(res.JSON200.Cursors), nil
			})
			if err != nil {
				return nil, err
			}
			return nil, page.Print(cmd, incidentTable(page.Items))
		}),
	}
	cmd.Flags().StringVar(&status, "status", "", "Only open or resolved incidents")
	cmd.Flags().StringVar(&severity, "severity", "", "Only warning or critical incidents (current severity while open, peak once resolved)")
	cmd.Flags().StringVar(&aspect, "aspect", "", "Only incidents about availability, the certificate or the domain")
	cmd.Flags().StringVar(&monitor, "monitor", "", "Only incidents of this monitor (id or exact name)")
	cmd.Flags().StringVar(&search, "search", "", "Only incidents whose monitor's name or target contains this text")
	cmd.Flags().BoolVar(&acked, "acknowledged", false, "Only acknowledged incidents")
	cmd.Flags().BoolVar(&unacked, "unacknowledged", false, "Only incidents nobody acknowledged yet")
	addTimeFlags(cmd, &since, &until, "incidents that started")
	cmd.MarkFlagsMutuallyExclusive("acknowledged", "unacknowledged")
	cmdutil.FlagEnum(cmd, "status", incidentStatuses...)
	cmdutil.FlagEnum(cmd, "severity", incidentSeverities...)
	cmdutil.FlagEnum(cmd, "aspect", incidentAspects...)
	cmdutil.AddPageFlags(cmd, &pf, defaultPageSize, maxItems)
	return cmd
}

func newIncidentGetCmd(d deps) *cobra.Command {
	return &cobra.Command{
		Use:         "get <incident>",
		Annotations: cmdutil.Reads(),
		Short:       "Show an incident with its timeline and the alerts sent",
		Long: `Shows the incident, its timeline (opened, escalated, reminders,
acknowledged, resolved) and every alert sent for it with its delivery
status, so a failing channel shows here.`,
		Example: `  norcube pulsecheck incidents get 3f2c8a8e-6c1d-4a59-9a43-2b1f0f6f9d10
  norcube pulsecheck incidents get 3f2c8a8e-6c1d-4a59-9a43-2b1f0f6f9d10 -o json --query timeline`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			id, err := idArg("incident", args[0])
			if err != nil {
				return nil, err
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.GetIncidentWithResponse(cmd.Context(), id)
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			return incidentObject(*res.JSON200), nil
		}),
	}
}

func newIncidentAckCmd(d deps) *cobra.Command {
	return &cobra.Command{
		Use:         "acknowledge <incident>",
		Aliases:     []string{"ack"},
		Annotations: cmdutil.Writes(),
		Short:       "Acknowledge an incident: someone is on it, stop the reminders",
		Long:        "Marks an open incident as being handled; its reminders stop. Idempotent: a resolved or already acknowledged incident is returned unchanged.",
		Example:     `  norcube pulsecheck incidents acknowledge 3f2c8a8e-6c1d-4a59-9a43-2b1f0f6f9d10`,
		Args:        cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			id, err := idArg("incident", args[0])
			if err != nil {
				return nil, err
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.AcknowledgeIncidentWithResponse(cmd.Context(), id)
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			i := *res.JSON200
			return output.Done{
				Message: fmt.Sprintf("Acknowledged incident %s (%s).", i.Id, i.Title),
				Value:   i,
				Fields:  []output.Field{{Value: i.Id.String()}, {Value: string(i.Status)}},
			}, nil
		}),
	}
}

func newIncidentSummaryCmd(d deps) *cobra.Command {
	return &cobra.Command{
		Use:         "summary",
		Annotations: cmdutil.Reads(),
		Short:       "Count open incidents by severity, and resolved ones with the time to resolve",
		Long: `The organization's incidents at a glance: open ones by current severity
and those nobody acknowledged, incidents resolved in the last 7 and 30
days, and the mean time from opened to resolved over the last 30 days.
For a pass or fail answer in CI use "pulsecheck status --fail-on".`,
		Example: `  norcube pulsecheck incidents summary
  norcube pulsecheck incidents summary -o json --query open.critical`,
		Args: cobra.NoArgs,
		RunE: cmdutil.Run(func(cmd *cobra.Command, _ []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			s, err := incidentSummary(cmd.Context(), c)
			if err != nil {
				return nil, err
			}
			return summaryObject(s), nil
		}),
	}
}

func incidentSummary(ctx context.Context, c *pcContext) (pulsecheck.DtoIncidentSummary, error) {
	res, err := c.API.GetIncidentSummaryWithResponse(ctx)
	if err != nil {
		return pulsecheck.DtoIncidentSummary{}, err
	}
	if res.JSON200 == nil {
		return pulsecheck.DtoIncidentSummary{}, apiError(res.HTTPResponse, res.Body)
	}
	return *res.JSON200, nil
}

func summaryFields(s pulsecheck.DtoIncidentSummary) []output.Field {
	mttr := "—"
	if s.MttrLast30dSeconds != nil {
		mttr = duration(*s.MttrLast30dSeconds)
	}
	return []output.Field{
		{Name: "Open", Value: strconv.Itoa(s.Open.Total)},
		{Name: "Critical", Value: strconv.Itoa(s.Open.Critical)},
		{Name: "Warning", Value: strconv.Itoa(s.Open.Warning)},
		{Name: "Unacknowledged", Value: strconv.Itoa(s.Open.Unacknowledged)},
		{Name: "Resolved 7d", Value: strconv.Itoa(s.ResolvedLast7d)},
		{Name: "Resolved 30d", Value: strconv.Itoa(s.ResolvedLast30d)},
		{Name: "Mean time to resolve 30d", Value: mttr},
	}
}

func summaryObject(s pulsecheck.DtoIncidentSummary) output.Object {
	return output.Object{Value: s, Fields: summaryFields(s)}
}
