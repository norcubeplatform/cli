package pulsecheck

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/pulsecheck"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

func newMonitorsCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "monitors",
		Aliases: []string{"monitor"},
		Short:   "Create, inspect and manage monitors and their check history",
		Long: `A monitor checks one thing on a schedule: an HTTP endpoint (optionally
for a keyword), an SSL certificate, or a domain's registration. Commands
that take a <monitor> accept its id or its exact name.`,
	}
	cmd.AddCommand(
		newMonitorListCmd(d),
		newMonitorGetCmd(d),
		newMonitorCreateCmd(d),
		newMonitorUpdateCmd(d),
		newMonitorDeleteCmd(d),
		newMonitorPauseCmd(d),
		newMonitorResumeCmd(d),
		newMonitorRunCmd(d),
		newMonitorBulkCmd(d),
		newResultsCmd(d),
		newStatusChangesCmd(d),
		newStatsCmd(d),
		newAvailabilityCmd(d),
		newCertificateCmd(d),
		newAlertsCmd(d),
	)
	return cmd
}

// Values of the list filters, as the API accepts them.
var (
	monitorTypes    = []string{"http", "ssl", "domain"}
	monitorStatuses = []string{"pending", "ok", "warning", "critical", "paused"}
	monitorSorts    = []string{"status", "name", "lastChecked", "uptime", "created"}
)

func newMonitorListCmd(d deps) *cobra.Command {
	var (
		pf                         cmdutil.PageFlags
		typ, status, search, order string
		history                    bool
	)
	cmd := &cobra.Command{
		Use:         "list",
		Annotations: cmdutil.Reads(),
		Short:       "List monitors with their current status",
		Long: `Lists the organization's monitors, by default the ones that need
attention first (critical, warning, pending, ok, paused, then by name).
--status filters by the overall status: the worse of the monitor's own
status and the certificate and domain it watches.`,
		Example: `  norcube pulsecheck monitors list
  norcube pulsecheck monitors list --status critical --type http
  norcube pulsecheck monitors list --search shop --sort uptime --all-pages -o json`,
		Args: cobra.NoArgs,
		RunE: cmdutil.Run(func(cmd *cobra.Command, _ []string) (any, error) {
			params := pulsecheck.ListMonitorsParams{}
			if typ != "" {
				if err := oneOf("--type", typ, monitorTypes); err != nil {
					return nil, err
				}
				t := pulsecheck.ListMonitorsParamsType(typ)
				params.Type = &t
			}
			if status != "" {
				if err := oneOf("--status", status, monitorStatuses); err != nil {
					return nil, err
				}
				s := pulsecheck.ListMonitorsParamsStatus(status)
				params.Status = &s
			}
			if order != "" {
				if err := oneOf("--sort", order, monitorSorts); err != nil {
					return nil, err
				}
				s := pulsecheck.ListMonitorsParamsSort(order)
				params.Sort = &s
			}
			if search != "" {
				params.Q = &search
			}
			if history {
				h := pulsecheck.ListMonitorsParamsInclude("history")
				params.Include = &h
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			page, err := cmdutil.Paginate(cmd.Context(), pf, func(ctx context.Context, cursor string, size int) ([]pulsecheck.DtoMonitor, string, error) {
				p := params
				p.Limit = &size
				if cursor != "" {
					p.Cursor = &cursor
				}
				res, err := c.API.ListMonitorsWithResponse(ctx, &p)
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
			return nil, page.Print(cmd, monitorTable(page.Items))
		}),
	}
	cmd.Flags().StringVar(&typ, "type", "", "Only monitors of this type: http, ssl or domain")
	cmd.Flags().StringVar(&status, "status", "", "Only monitors with this overall status: pending, ok, warning, critical or paused")
	cmd.Flags().StringVar(&search, "search", "", "Only monitors whose name or target contains this text")
	cmd.Flags().StringVar(&order, "sort", "", "Order: status (default), name, lastChecked, uptime (lowest first) or created (newest first)")
	cmd.Flags().BoolVar(&history, "history", false, "Include each monitor's newest 60 results (JSON and YAML only)")
	cmdutil.FlagEnum(cmd, "type", monitorTypes...)
	cmdutil.FlagEnum(cmd, "status", monitorStatuses...)
	cmdutil.FlagEnum(cmd, "sort", monitorSorts...)
	cmdutil.AddPageFlags(cmd, &pf, defaultPageSize, maxItems)
	return cmd
}

func newMonitorGetCmd(d deps) *cobra.Command {
	return &cobra.Command{
		Use:         "get <monitor>",
		Annotations: cmdutil.Reads(),
		Short:       "Show a monitor's settings and current status",
		Long:        "Shows the monitor, its check settings (secret header values are never returned), its status with the reason, and the certificate and domain it watches.",
		Example: `  norcube pulsecheck monitors get "Shop API"
  norcube pulsecheck monitors get 7d1c2f7e-6a1f-4f5e-9b8a-1f7c2b3d4e5f -o json`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			m, err := getMonitor(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			return monitorObject(m), nil
		}),
	}
}

// getMonitor resolves ref and fetches the monitor.
func getMonitor(ctx context.Context, c *pcContext, ref string) (pulsecheck.DtoMonitor, error) {
	id, err := monitorRef(ctx, c, ref)
	if err != nil {
		return pulsecheck.DtoMonitor{}, err
	}
	res, err := c.API.GetMonitorWithResponse(ctx, id)
	if err != nil {
		return pulsecheck.DtoMonitor{}, err
	}
	if res.JSON200 == nil {
		return pulsecheck.DtoMonitor{}, apiError(res.HTTPResponse, res.Body)
	}
	return *res.JSON200, nil
}

func newMonitorDeleteCmd(d deps) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:         "delete <monitor>",
		Annotations: cmdutil.Writes(),
		Short:       "Delete a monitor and its whole check history",
		Long: `Deletes the monitor with its results, status history and incidents.
To stop checks but keep the history, use "monitors pause". A monitor that
belongs to another Norcube product (a DomainRadar saved domain's expiry
alerts) can't be deleted here (exit 8): remove it in that product.`,
		Example: `  norcube pulsecheck monitors delete "Old landing page"
  norcube pulsecheck monitors delete 7d1c2f7e-6a1f-4f5e-9b8a-1f7c2b3d4e5f --yes`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			id, err := monitorRef(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			if ok, err := cmdutil.Confirm(fmt.Sprintf("Delete monitor %s and its whole check history? This can't be undone", args[0]), yes); err != nil || !ok {
				return nil, cmdutil.Cancelled(cmd, err)
			}
			res, err := c.API.DeleteMonitorWithResponse(cmd.Context(), id)
			if err != nil {
				return nil, err
			}
			if res.StatusCode() != http.StatusNoContent {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			return output.Done{
				Message: fmt.Sprintf("Deleted monitor %s.", args[0]),
				Value:   deleted{ID: id.String(), Deleted: true},
				Fields:  []output.Field{{Value: id.String()}},
			}, nil
		}),
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Don't ask for confirmation")
	return cmd
}

// deleted is what delete commands print as JSON.
type deleted struct {
	ID      string `json:"id" yaml:"id"`
	Deleted bool   `json:"deleted" yaml:"deleted"`
}

// monitorAction is pause or resume of one monitor.
type monitorAction struct {
	use, short, long, example, done string
	call                            func(ctx context.Context, c *pcContext, id uuid.UUID) (*pulsecheck.DtoMonitor, *http.Response, []byte, error)
}

func newMonitorPauseCmd(d deps) *cobra.Command {
	return newMonitorActionCmd(d, monitorAction{
		use:   "pause <monitor>",
		short: "Stop a monitor's scheduled checks",
		long: `Stops scheduled checks; the history stays. Pausing closes the monitor's
open incident without alerting. Idempotent.`,
		example: `  norcube pulsecheck monitors pause "Shop API"`,
		done:    "Paused",
		call: func(ctx context.Context, c *pcContext, id uuid.UUID) (*pulsecheck.DtoMonitor, *http.Response, []byte, error) {
			res, err := c.API.PauseMonitorWithResponse(ctx, id)
			if err != nil {
				return nil, nil, nil, err
			}
			return res.JSON200, res.HTTPResponse, res.Body, nil
		},
	})
}

func newMonitorResumeCmd(d deps) *cobra.Command {
	return newMonitorActionCmd(d, monitorAction{
		use:   "resume <monitor>",
		short: "Restart a paused monitor's scheduled checks",
		long: `Restarts scheduled checks; the next one runs right away unless the last
one is less than an interval old. Idempotent.`,
		example: `  norcube pulsecheck monitors resume "Shop API"`,
		done:    "Resumed",
		call: func(ctx context.Context, c *pcContext, id uuid.UUID) (*pulsecheck.DtoMonitor, *http.Response, []byte, error) {
			res, err := c.API.ResumeMonitorWithResponse(ctx, id)
			if err != nil {
				return nil, nil, nil, err
			}
			return res.JSON200, res.HTTPResponse, res.Body, nil
		},
	})
}

func newMonitorActionCmd(d deps, a monitorAction) *cobra.Command {
	return &cobra.Command{
		Use:         a.use,
		Annotations: cmdutil.Writes(),
		Short:       a.short,
		Long:        a.long,
		Example:     a.example,
		Args:        cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			id, err := monitorRef(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			m, resp, body, err := a.call(cmd.Context(), c, id)
			if err != nil {
				return nil, err
			}
			if m == nil {
				return nil, apiError(resp, body)
			}
			return output.Done{
				Message: fmt.Sprintf("%s monitor %s (%s).", a.done, m.Name, m.Id),
				Value:   *m,
				Fields:  []output.Field{{Value: m.Id.String()}, {Value: string(m.Status)}},
			}, nil
		}),
	}
}

// Failure thresholds of `monitors run --wait`.
var failOnValues = []string{"critical", "warning"}

const runWaitTimeout = 2 * time.Minute

func newMonitorRunCmd(d deps) *cobra.Command {
	var (
		wf     cmdutil.WaitFlags
		failOn string
	)
	cmd := &cobra.Command{
		Use:         "run <monitor>",
		Annotations: cmdutil.Writes(),
		Short:       "Run a monitor's check now and show the result",
		Long: `Runs the monitor's check right away (paused monitors too) and prints the
stored result. The check is billed like a scheduled one and rate limited
(200 per user and 600 per organization an hour).

The check runs while the command waits, so the result is always final.
--wait makes the result the command's outcome, for deploy pipelines: it
exits 1 with code job_failed (the result in "details") when the check
found the monitor critical or couldn't reach a verdict (inconclusive), or
with --fail-on warning also when it found a warning, and prints nothing
on stdout then; it exits 7 when no result arrives within --wait-timeout. Without --wait the command exits 0
whatever the check found.`,
		Example: `  norcube pulsecheck monitors run "Shop API"
  # Fail a deploy when the site is down after it
  norcube pulsecheck monitors run "Shop API" --wait
  norcube pulsecheck monitors run 7d1c2f7e-6a1f-4f5e-9b8a-1f7c2b3d4e5f --wait --fail-on warning -o json`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			if failOn != "" {
				if !wf.Wait {
					return nil, clierr.Usage(fmt.Errorf("--fail-on needs --wait"))
				}
				if err := oneOf("--fail-on", failOn, failOnValues); err != nil {
					return nil, err
				}
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			m, err := getMonitor(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			ctx := cmd.Context()
			if wf.Wait && wf.Timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, wf.Timeout)
				defer cancel()
			}
			res, err := c.API.RunMonitorCheckWithResponse(ctx, m.Id)
			if err != nil {
				if wf.Wait && errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil {
					return nil, cmdutil.ErrWaitTimeout
				}
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			r := *res.JSON200
			if wf.Wait && checkFailed(string(r.Severity), failOn) {
				// Like every failed --wait: nothing on stdout, the result
				// in the error's details.
				return nil, cmdutil.JobFailed(fmt.Sprintf("the check of %s found it %s (%s)", m.Name,
					resultWord(string(m.Type), string(r.Severity), r.Condition), r.Condition), r)
			}
			return resultObject(string(m.Type), r), nil
		}),
	}
	cmdutil.AddWaitFlags(cmd, &wf, "check", runWaitTimeout)
	cmd.Flags().StringVar(&failOn, "fail-on", "", "With --wait, the severity that fails the command: critical (default) or warning (inconclusive always fails)")
	cmdutil.FlagEnum(cmd, "fail-on", failOnValues...)
	return cmd
}

// checkFailed reports whether a check result's severity fails a --wait:
// critical and inconclusive always, warning with --fail-on warning.
func checkFailed(severity, failOn string) bool {
	switch severity {
	case "critical", "inconclusive":
		return true
	case "warning":
		return failOn == "warning"
	}
	return false
}

// oneOf validates an enum flag before any request.
func oneOf(flag, v string, allowed []string) error {
	for _, a := range allowed {
		if v == a {
			return nil
		}
	}
	return clierr.Usage(fmt.Errorf("%s %q: one of %v", flag, v, allowed))
}
