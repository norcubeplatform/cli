package pulsecheck

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/pulsecheck"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

// What `status --fail-on` accepts, from the strictest to none.
var statusFailOn = []string{"warning", "critical", "none"}

// statusReport is what `pulsecheck status` prints as JSON.
type statusReport struct {
	// Status is the organization's verdict from its open incidents:
	// critical, warning or ok.
	Status    string                        `json:"status" yaml:"status"`
	Incidents pulsecheck.DtoIncidentSummary `json:"incidents" yaml:"incidents"`
	Monitors  monitorCounts                 `json:"monitors" yaml:"monitors"`
	// Problems are the monitors whose overall status is critical or
	// warning, worst first.
	Problems []problemMonitor `json:"problems" yaml:"problems"`
}

// monitorCounts counts monitors by overall status.
type monitorCounts struct {
	Total    int `json:"total" yaml:"total"`
	OK       int `json:"ok" yaml:"ok"`
	Warning  int `json:"warning" yaml:"warning"`
	Critical int `json:"critical" yaml:"critical"`
	Pending  int `json:"pending" yaml:"pending"`
	Paused   int `json:"paused" yaml:"paused"`
}

// problemMonitor is a monitor that needs attention, in the API's field
// names.
type problemMonitor struct {
	ID            string `json:"id" yaml:"id"`
	Name          string `json:"name" yaml:"name"`
	Type          string `json:"type" yaml:"type"`
	Variant       string `json:"variant" yaml:"variant"`
	Target        string `json:"target" yaml:"target"`
	Status        string `json:"status" yaml:"status"`
	OverallStatus string `json:"overallStatus" yaml:"overallStatus"`
	Condition     string `json:"condition,omitempty" yaml:"condition,omitempty"`
	Message       string `json:"message,omitempty" yaml:"message,omitempty"`
}

func newStatusCmd(d deps) *cobra.Command {
	var failOn string
	cmd := &cobra.Command{
		Use:         "status",
		Annotations: cmdutil.Reads(),
		Short:       "Show the organization's monitoring status, or fail a CI job on open incidents",
		Long: `Shows the open incidents by severity, the monitors by status, and every
monitor that is critical or warning. The verdict ("status" in JSON) comes
from open incidents, which are problems the checks confirmed: critical
while a critical incident is open, warning while only warnings are, else
ok.

--fail-on turns the verdict into the exit code: with --fail-on critical
the command exits 1 (code unhealthy, the report in the error's details)
while a critical incident is open, with --fail-on warning while any
incident is open. The report is printed either way. The default, none,
always exits 0.`,
		Example: `  norcube pulsecheck status
  # Gate a deploy on PulseCheck
  norcube pulsecheck status --fail-on critical
  norcube pulsecheck status -o json --query problems[].name`,
		Args: cobra.NoArgs,
		RunE: cmdutil.Run(func(cmd *cobra.Command, _ []string) (any, error) {
			if err := oneOf("--fail-on", failOn, statusFailOn); err != nil {
				return nil, err
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			report, err := buildStatus(cmd.Context(), c)
			if err != nil {
				return nil, err
			}
			view := statusObject(report)
			fail := (failOn == "critical" && report.Status == "critical") ||
				(failOn == "warning" && report.Status != "ok")
			if !fail {
				return view, nil
			}
			if err := cmdutil.Print(cmd, view); err != nil {
				return nil, err
			}
			return nil, &clierr.Error{
				Code:    clierr.CodeUnhealthy,
				Message: fmt.Sprintf("PulseCheck is %s: %d critical and %d warning incident(s) open", report.Status, report.Incidents.Open.Critical, report.Incidents.Open.Warning),
				Hint:    "See them with `norcube pulsecheck incidents list --status open`.",
				Details: report,
			}
		}),
	}
	cmd.Flags().StringVar(&failOn, "fail-on", "none", "Exit 1 while incidents of this severity or worse are open: critical, warning or none")
	cmdutil.FlagEnum(cmd, "fail-on", statusFailOn...)
	return cmd
}

func buildStatus(ctx context.Context, c *pcContext) (statusReport, error) {
	summary, err := incidentSummary(ctx, c)
	if err != nil {
		return statusReport{}, err
	}
	r := statusReport{Status: "ok", Incidents: summary, Problems: []problemMonitor{}}
	switch {
	case summary.Open.Critical > 0:
		r.Status = "critical"
	case summary.Open.Total > 0:
		r.Status = "warning"
	}
	// Sorted by status (the API's default): critical and warning first.
	limit := maxPageSize
	p := pulsecheck.ListMonitorsParams{Limit: &limit}
	for r.Monitors.Total < maxItems {
		res, err := c.API.ListMonitorsWithResponse(ctx, &p)
		if err != nil {
			return statusReport{}, err
		}
		if res.JSON200 == nil {
			return statusReport{}, apiError(res.HTTPResponse, res.Body)
		}
		for _, m := range res.JSON200.List {
			r.Monitors.Total++
			switch m.OverallStatus {
			case "ok":
				r.Monitors.OK++
			case "warning":
				r.Monitors.Warning++
			case "critical":
				r.Monitors.Critical++
			case "pending":
				r.Monitors.Pending++
			case "paused":
				r.Monitors.Paused++
			}
			if m.OverallStatus == "critical" || m.OverallStatus == "warning" {
				pm := problemMonitor{ID: m.Id.String(), Name: m.Name, Type: string(m.Type), Variant: string(m.Variant),
					Target: m.Target, Status: string(m.Status), OverallStatus: string(m.OverallStatus)}
				if m.Condition != nil {
					pm.Condition, pm.Message = m.Condition.Code, cmdutil.Deref(m.Condition.Message)
				}
				r.Problems = append(r.Problems, pm)
			}
		}
		next := nextCursor(res.JSON200.Cursors)
		if next == "" {
			break
		}
		p.Cursor = &next
	}
	return r, nil
}

func statusObject(r statusReport) output.Object {
	f := []output.Field{{Name: "Status", Value: r.Status}}
	f = append(f, summaryFields(r.Incidents)[:4]...)
	f = append(f, output.Field{Name: "Monitors", Value: fmt.Sprintf("%d (%d ok, %d warning, %d critical, %d pending, %d paused)",
		r.Monitors.Total, r.Monitors.OK, r.Monitors.Warning, r.Monitors.Critical, r.Monitors.Pending, r.Monitors.Paused)})
	// One field for every problem monitor, a line each, so -o text keeps
	// fixed columns.
	lines := make([]string, 0, len(r.Problems))
	for _, p := range r.Problems {
		word := statusWord(p.Type, p.Status, p.Condition)
		if p.Status != p.OverallStatus {
			// The monitor itself is fine; a certificate or domain it
			// watches isn't.
			word = capitalize(p.OverallStatus)
		}
		line := word + ": " + p.Name + " (" + p.Target + ")"
		if p.Message != "" {
			line += ", " + p.Message
		}
		lines = append(lines, line)
	}
	problems := "none"
	if len(lines) > 0 {
		problems = strings.Join(lines, "\n")
	}
	f = append(f, output.Field{Name: "Problems", Value: problems})
	return output.Object{Value: r, Fields: f}
}
