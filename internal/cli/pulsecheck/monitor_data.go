package pulsecheck

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/pulsecheck"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

// ---------- results ----------

func newResultsCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "results",
		Aliases: []string{"checks"},
		Short:   "A monitor's check results",
	}
	cmd.AddCommand(newResultsListCmd(d))
	return cmd
}

func newResultsListCmd(d deps) *cobra.Command {
	var (
		pf           cmdutil.PageFlags
		since, until string
	)
	cmd := &cobra.Command{
		Use:         "list <monitor>",
		Annotations: cmdutil.Reads(),
		Short:       "List a monitor's check results, newest first",
		Long: `Lists the monitor's checks in [--since, --until), newest first; by default
the last 24 hours. Raw results are kept at least 30 days. JSON has every
detail of a result: timings, the certificate seen, the domain's
registration.`,
		Example: `  norcube pulsecheck monitors results list "Shop"
  norcube pulsecheck monitors results list "Shop" --since 7d --all-pages -o json --query "[?severity=='critical'].checkedAt"`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			params := pulsecheck.ListMonitorResultsParams{}
			if err := timeRange(since, until, d.now(), &params.From, &params.To); err != nil {
				return nil, err
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			m, err := getMonitor(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			page, err := cmdutil.Paginate(cmd.Context(), pf, func(ctx context.Context, cursor string, size int) ([]pulsecheck.DtoCheckResult, string, error) {
				p := params
				p.Limit = &size
				if cursor != "" {
					p.Cursor = &cursor
				}
				res, err := c.API.ListMonitorResultsWithResponse(ctx, m.Id, &p)
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
			return nil, page.Print(cmd, output.Table[pulsecheck.DtoCheckResult]{
				Headers: resultHeaders,
				Style:   output.Style{StatusColumn: 2},
				Rows:    resultRow(string(m.Type)),
				Items:   page.Items,
			})
		}),
	}
	addTimeFlags(cmd, &since, &until, "results")
	cmdutil.AddPageFlags(cmd, &pf, defaultPageSize, maxItems)
	return cmd
}

func addTimeFlags(cmd *cobra.Command, since, until *string, what string) {
	cmd.Flags().StringVar(since, "since", "", "Only "+what+" at or after this time: an RFC 3339 timestamp, a date or an age such as 24h or 7d")
	cmd.Flags().StringVar(until, "until", "", "Only "+what+" before this time (timestamp, date or age)")
}

// timeRange parses --since and --until into optional query parameters.
func timeRange(since, until string, now time.Time, from, to **time.Time) error {
	if since != "" {
		t, err := cmdutil.ParseTimeArg("since", since, now)
		if err != nil {
			return err
		}
		*from = &t
	}
	if until != "" {
		t, err := cmdutil.ParseTimeArg("until", until, now)
		if err != nil {
			return err
		}
		*to = &t
	}
	if *from != nil && *to != nil && !(*from).Before(**to) {
		return clierr.Usage(fmt.Errorf("--since must be before --until"))
	}
	return nil
}

// ---------- status changes ----------

func newStatusChangesCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "status-changes",
		Aliases: []string{"history"},
		Short:   "When a monitor went up, down, or expiring, and for how long",
	}
	cmd.AddCommand(newStatusChangesListCmd(d))
	return cmd
}

func newStatusChangesListCmd(d deps) *cobra.Command {
	var pf cmdutil.PageFlags
	cmd := &cobra.Command{
		Use:         "list <monitor>",
		Annotations: cmdutil.Reads(),
		Short:       "List every status change of a monitor, newest first",
		Long: `Lists each time a check moved the monitor to another status: from, to,
when, how long it lasted (the newest one is still going) and what the
check said. Kept a year. Pausing is not a status change. JSON adds the
events in each span (a renewed certificate).`,
		Example: `  norcube pulsecheck monitors status-changes list "Shop"
  norcube pulsecheck monitors history list "Shop" -o json`,
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
			page, err := cmdutil.Paginate(cmd.Context(), pf, func(ctx context.Context, cursor string, size int) ([]pulsecheck.DtoStatusChange, string, error) {
				p := pulsecheck.ListMonitorStatusChangesParams{Limit: &size}
				if cursor != "" {
					p.Cursor = &cursor
				}
				res, err := c.API.ListMonitorStatusChangesWithResponse(ctx, m.Id, &p)
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
			typ := string(m.Type)
			return nil, page.Print(cmd, output.Table[pulsecheck.DtoStatusChange]{
				Headers:   []string{"CHANGED", "FROM", "TO", "FOR", "CONDITION", "MESSAGE"},
				MaxWidths: []int{0, 0, 0, 0, 0, 60},
				Style:     output.Style{StatusColumn: 2},
				Rows: func(s pulsecheck.DtoStatusChange) []string {
					return []string{
						output.WhenStringSeconds(s.ChangedAt),
						statusWord(typ, string(s.From), ""),
						statusWord(typ, string(s.To), s.Condition),
						duration(s.DurationSeconds),
						s.Condition,
						strOr(s.Message),
					}
				},
				Items: page.Items,
			})
		}),
	}
	cmdutil.AddPageFlags(cmd, &pf, defaultPageSize, maxItems)
	return cmd
}

// ---------- stats ----------

var statsRanges = []string{"24h", "7d", "30d", "90d"}

func newStatsCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "A monitor's uptime and response times",
	}
	var rng string
	get := &cobra.Command{
		Use:         "get <monitor>",
		Annotations: cmdutil.Reads(),
		Short:       "Show uptime, response times and check counts over a range",
		Long: `Shows uptime ((ok + warning) / conclusive checks, HTTP monitors only),
average and maximum response time and check counts for the range ending
now. JSON adds the series: hourly buckets up to 7d, daily beyond.
Refreshed every few minutes.`,
		Example: `  norcube pulsecheck monitors stats get "Shop"
  norcube pulsecheck monitors stats get "Shop" --range 30d -o json --query uptimePercent`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			if err := oneOf("--range", rng, statsRanges); err != nil {
				return nil, err
			}
			params := pulsecheck.GetMonitorStatsParams{Range: pulsecheck.GetMonitorStatsParamsRange(rng)}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			id, err := monitorRef(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			res, err := c.API.GetMonitorStatsWithResponse(cmd.Context(), id, &params)
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			s := *res.JSON200
			return output.Object{Value: s, Fields: []output.Field{
				{Name: "From", Value: output.WhenString(s.From)},
				{Name: "To", Value: output.WhenString(s.To)},
				{Name: "Uptime", Value: percent(s.UptimePercent)},
				{Name: "Average", Value: ms(s.AvgMs)},
				{Name: "Slowest", Value: ms(s.MaxMs)},
				{Name: "Checks", Value: strconv.Itoa(s.Checks)},
				{Name: "OK", Value: strconv.Itoa(s.Ok)},
				{Name: "Warning", Value: strconv.Itoa(s.Warning)},
				{Name: "Critical", Value: strconv.Itoa(s.Critical)},
				{Name: "Inconclusive", Value: strconv.Itoa(s.Inconclusive)},
			}}, nil
		}),
	}
	get.Flags().StringVar(&rng, "range", "24h", "Range ending now: 24h, 7d, 30d or 90d")
	cmdutil.FlagEnum(get, "range", statsRanges...)
	cmd.AddCommand(get)
	return cmd
}

// ---------- availability ----------

func newAvailabilityCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "availability",
		Short: "A monitor's uptime and downtime per day",
	}
	var days int
	get := &cobra.Command{
		Use:         "get <monitor>",
		Annotations: cmdutil.Reads(),
		Short:       "Show uptime and estimated downtime per UTC day",
		Long: `One row per UTC day ending today, oldest first: check counts, uptime and
an estimate of the minutes spent down (HTTP monitors). A day without
checks has no uptime, never 100%.`,
		Example: `  norcube pulsecheck monitors availability get "Shop" --days 30`,
		Args:    cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			params := pulsecheck.GetMonitorAvailabilityParams{}
			if cmd.Flags().Changed("days") {
				if days < 1 || days > 90 {
					return nil, clierr.Usage(fmt.Errorf("--days must be between 1 and 90"))
				}
				params.Days = &days
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			id, err := monitorRef(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			res, err := c.API.GetMonitorAvailabilityWithResponse(cmd.Context(), id, &params)
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			return availabilityView(*res.JSON200), nil
		}),
	}
	get.Flags().IntVar(&days, "days", 0, "Days ending today, 1 to 90 (default 90)")
	cmd.AddCommand(get)
	return cmd
}

// availabilityView shows a table of days; JSON and YAML keep the whole
// answer (the range's totals too).
func availabilityView(a pulsecheck.DtoAvailability) output.View[pulsecheck.DtoDayAvailability] {
	return output.View[pulsecheck.DtoDayAvailability]{Value: a, Table: output.Table[pulsecheck.DtoDayAvailability]{
		Headers: []string{"DAY", "UPTIME", "DOWNTIME", "CHECKS", "OK", "WARNING", "CRITICAL"},
		Style:   output.NoStyle,
		Rows: func(day pulsecheck.DtoDayAvailability) []string {
			up := percent(day.UptimePercent)
			if day.NoData {
				up = "no data"
			}
			return []string{day.Date, up, fmt.Sprintf("%d min", day.DowntimeMinutes), strconv.Itoa(day.Checks),
				strconv.Itoa(day.Ok), strconv.Itoa(day.Warning), strconv.Itoa(day.Critical)}
		},
		Items: a.Days,
	}}
}

// ---------- certificate ----------

func newCertificateCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "certificate",
		Aliases: []string{"cert"},
		Short:   "The newest TLS certificate an SSL or HTTPS monitor saw",
	}
	cmd.AddCommand(&cobra.Command{
		Use:         "get <monitor>",
		Annotations: cmdutil.Reads(),
		Short:       "Show the monitor's newest certificate and when it expires",
		Long: `Shows the newest certificate the monitor's checks saw: subject, issuer,
names, validity, whether the chain and hostname verify, and days left. SSL
monitors add the expiry thresholds and the next one to be crossed. Exits 3
when no check has seen a certificate (plain HTTP and domain monitors).`,
		Example: `  norcube pulsecheck monitors certificate get "API cert"
  norcube pulsecheck monitors cert get "Shop" -o json --query certificate.notAfter`,
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
			res, err := c.API.GetMonitorCertificateWithResponse(cmd.Context(), id)
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			mc := *res.JSON200
			crt := mc.Certificate
			f := []output.Field{
				{Name: "Subject", Value: crt.Subject},
				{Name: "Issuer", Value: crt.Issuer},
				{Name: "Names", Value: strings.Join(cmdutil.Deref(crt.DnsNames), ", ")},
				{Name: "Valid from", Value: output.WhenString(crt.NotBefore)},
				{Name: "Expires", Value: output.WhenString(crt.NotAfter)},
				{Name: "Days left", Value: strconv.Itoa(mc.DaysRemaining)},
				{Name: "Chain valid", Value: yesNo(crt.ChainValid)},
				{Name: "Hostname valid", Value: yesNo(crt.HostnameValid)},
				{Name: "Status", Value: resultWord("ssl", string(mc.Severity), mc.Condition)},
				{Name: "Serial", Value: strOr(crt.SerialNumber)},
				{Name: "SHA-256", Value: strOr(crt.FingerprintSha256)},
				{Name: "Seen", Value: output.WhenString(mc.CheckedAt)},
			}
			alertDays := "—"
			if len(mc.ThresholdsDays) > 0 {
				alertDays = joinInts(mc.ThresholdsDays)
			}
			// Always present (SSL monitors only fill them), so -o text keeps
			// its columns.
			f = append(f,
				output.Field{Name: "Alert at days", Value: alertDays},
				output.Field{Name: "Next alert", Value: output.WhenString(cmdutil.Deref(mc.NextThresholdAt))},
				output.Field{Name: "Next alert at days", Value: intOr(mc.NextThresholdDays)},
			)
			return output.Object{Value: mc, Fields: f}, nil
		}),
	})
	return cmd
}

// ---------- alert settings ----------

var alertEvents = []string{"down", "warning", "expiring", "expired", "recovered"}

func newAlertsCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "alerts",
		Short: "Where and when a monitor alerts",
		Long: `A monitor either follows the organization's defaults (its default
channels, or the members by email when none is default) or has its own
routes: which events go to which channels, how many failed checks open an
incident, how many good ones resolve it, and how often to remind.`,
	}
	cmd.AddCommand(newAlertsGetCmd(d), newAlertsSetCmd(d))
	return cmd
}

func alertsObject(a pulsecheck.DtoMonitorAlerts) output.Object {
	routes := "organization defaults"
	if !a.UseDefaults {
		parts := make([]string, 0, len(a.Routes))
		for _, r := range a.Routes {
			ev := make([]string, len(r.Events))
			for i, e := range r.Events {
				ev[i] = string(e)
			}
			parts = append(parts, r.ChannelId.String()+": "+strings.Join(ev, ","))
		}
		routes = strings.Join(parts, "\n")
		if len(parts) == 0 {
			routes = "none (muted)"
		}
	}
	reminder := "off"
	if a.ReminderMinutes > 0 {
		reminder = interval(a.ReminderMinutes * 60)
	}
	ev := make([]string, len(a.ApplicableEvents))
	for i, e := range a.ApplicableEvents {
		ev[i] = string(e)
	}
	return output.Object{Value: a, Fields: []output.Field{
		{Name: "Routes", Value: routes},
		{Name: "Open after", Value: plural(a.ConfirmFailures, "failed check")},
		{Name: "Resolve after", Value: plural(a.ConfirmSuccesses, "good check")},
		{Name: "Remind every", Value: reminder},
		{Name: "Events", Value: strings.Join(ev, ", ")},
	}}
}

func newAlertsGetCmd(d deps) *cobra.Command {
	return &cobra.Command{
		Use:         "get <monitor>",
		Annotations: cmdutil.Reads(),
		Short:       "Show how a monitor alerts",
		Example:     `  norcube pulsecheck monitors alerts get "Shop"`,
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
			res, err := c.API.GetMonitorAlertsWithResponse(cmd.Context(), id)
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			return alertsObject(*res.JSON200), nil
		}),
	}
}

func newAlertsSetCmd(d deps) *cobra.Command {
	var (
		defaults, mute      bool
		routes              []string
		failures, successes int
		reminder            time.Duration
		fromFile            string
	)
	cmd := &cobra.Command{
		Use:         "set <monitor>",
		Annotations: cmdutil.Writes(),
		Short:       "Set where and when a monitor alerts",
		Long: `Replaces the monitor's alerting. --defaults returns it to the
organization's defaults. Otherwise give its routes, each a channel (id or
exact name) and the events it gets (down, warning, expiring, expired,
recovered), or --mute for no alerts; settings you leave out keep their
current values. Channels themselves are managed with "pulsecheck channels".`,
		Example: `  norcube pulsecheck monitors alerts set "Shop" --route "On-call webhook=down,recovered" --route ops-email=down
  norcube pulsecheck monitors alerts set "Shop" --confirm-failures 3 --reminder 30m
  norcube pulsecheck monitors alerts set "Shop" --mute
  norcube pulsecheck monitors alerts set "Shop" --defaults`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			set := cmd.Flags().Changed
			custom := set("route") || mute || set("confirm-failures") || set("confirm-successes") || set("reminder")
			switch {
			case fromFile != "" && (defaults || custom):
				return nil, clierr.Usage(fmt.Errorf("--from-file can't be combined with other flags"))
			case defaults && custom:
				return nil, clierr.Usage(fmt.Errorf("--defaults can't be combined with routes or settings"))
			case mute && set("route"):
				return nil, clierr.Usage(fmt.Errorf("--mute and --route can't be combined"))
			case fromFile == "" && !defaults && !custom:
				return nil, clierr.Usage(fmt.Errorf("nothing to set: pass --route, --mute, --defaults, a setting or --from-file"))
			}
			if set("reminder") && (reminder < 0 || reminder%time.Minute != 0) {
				return nil, clierr.Usage(fmt.Errorf("--reminder takes whole minutes, e.g. 30m or 2h (0 for none)"))
			}
			type route struct {
				channel string
				events  []pulsecheck.DtoMonitorAlertRouteEvents
			}
			var parsed []route
			for _, r := range routes {
				ch, evs, ok := strings.Cut(r, "=")
				if !ok || strings.TrimSpace(ch) == "" || strings.TrimSpace(evs) == "" {
					return nil, clierr.Usage(fmt.Errorf(`--route %q: write it as <channel>=<event>[,<event>...]`, r))
				}
				var events []pulsecheck.DtoMonitorAlertRouteEvents
				for _, e := range strings.Split(evs, ",") {
					e = strings.TrimSpace(e)
					if err := oneOf("--route event", e, alertEvents); err != nil {
						return nil, err
					}
					events = append(events, pulsecheck.DtoMonitorAlertRouteEvents(e))
				}
				parsed = append(parsed, route{strings.TrimSpace(ch), events})
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			id, err := monitorRef(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			var body pulsecheck.DtoMonitorAlertsUpdate
			switch {
			case fromFile != "":
				if err := readJSON(cmd, fromFile, "the alert settings", &body); err != nil {
					return nil, err
				}
			case defaults:
				t := true
				body.UseDefaults = &t
			default:
				// Every field is required when not following the defaults:
				// start from the current settings.
				cur, err := c.API.GetMonitorAlertsWithResponse(cmd.Context(), id)
				if err != nil {
					return nil, err
				}
				if cur.JSON200 == nil {
					return nil, apiError(cur.HTTPResponse, cur.Body)
				}
				a := *cur.JSON200
				f, s, rm := a.ConfirmFailures, a.ConfirmSuccesses, a.ReminderMinutes
				if set("confirm-failures") {
					f = failures
				}
				if set("confirm-successes") {
					s = successes
				}
				if set("reminder") {
					rm = int(reminder / time.Minute)
				}
				rs := a.Routes
				if a.UseDefaults {
					rs = []pulsecheck.DtoMonitorAlertRoute{}
				}
				if set("route") {
					rs = make([]pulsecheck.DtoMonitorAlertRoute, 0, len(parsed))
					for _, r := range parsed {
						chID, err := channelRef(cmd.Context(), c, r.channel)
						if err != nil {
							return nil, err
						}
						rs = append(rs, pulsecheck.DtoMonitorAlertRoute{ChannelId: chID, Events: r.events})
					}
				}
				if mute {
					rs = []pulsecheck.DtoMonitorAlertRoute{}
				}
				fls := false
				body = pulsecheck.DtoMonitorAlertsUpdate{UseDefaults: &fls, ConfirmFailures: &f, ConfirmSuccesses: &s, ReminderMinutes: &rm, Routes: &rs}
			}
			res, err := c.API.SetMonitorAlertsWithResponse(cmd.Context(), id, body)
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			return alertsObject(*res.JSON200), nil
		}),
	}
	cmd.Flags().BoolVar(&defaults, "defaults", false, "Follow the organization's default channels again")
	cmd.Flags().StringArrayVar(&routes, "route", nil, `A route "<channel>=<event>,<event>" (channel id or exact name; events down, warning, expiring, expired, recovered); repeatable, replaces the routes`)
	cmd.Flags().BoolVar(&mute, "mute", false, "Send no alerts for this monitor")
	cmd.Flags().IntVar(&failures, "confirm-failures", 0, "Failed checks in a row before an incident opens, 1 to 10")
	cmd.Flags().IntVar(&successes, "confirm-successes", 0, "Good checks in a row before it resolves, 1 to 10")
	cmd.Flags().DurationVar(&reminder, "reminder", 0, "Remind about an unacknowledged critical incident this often, e.g. 1h (0 for no reminders)")
	cmd.Flags().StringVar(&fromFile, "from-file", "", "Read the API's alert settings body as JSON from this file (- for standard input)")
	return cmd
}
