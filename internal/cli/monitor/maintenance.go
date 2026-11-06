package monitor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/monitor"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

// The states a window can be in, as the API names them.
var maintenanceStates = []string{"scheduled", "in_progress", "completed", "cancelled"}

const (
	maintenanceTimeFormats = "an RFC 3339 timestamp, now, or a span from now (+2h, +7d)"
	reasonPublishDenied    = "MAINTENANCE_PUBLISH_DENIED"
	reasonWindowState      = "MAINTENANCE_WINDOW_STATE"
)

func newMaintenanceCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "maintenance",
		Aliases: []string{"maintenance-windows"},
		Short:   "Schedule maintenance windows that pause alerts and stay out of uptime",
		Long: `A maintenance window is planned work on monitors or status page components.
While it runs, its monitors don't alert or open incidents for availability
problems, and the time stays out of their uptime. Checks keep running and
count towards your usage. A certificate or domain running out still alerts,
because that is true whatever work is going on.

A window shows by itself on every published status page displaying one of its
targets, and subscribers get mail when it is scheduled, starts and finishes
unless you turn that off. Which pages show it isn't a choice: otherwise a page
could stay green while its alerts are silent.

A CI token changes windows with the monitor.maintenance scope, and also needs
monitor.statuspages when a window shows on a published page. Cancelling or
finishing needs only monitor.maintenance, so whoever can edit a window can
always end its silence. Any Norcube Monitor scope reads them.

A window ends by itself, so a deploy that dies before "finish" can't silence
alerts for longer than you asked for. Wrap a deploy in one:

  id=$(norcube monitor maintenance create --monitor api --title "Deploy" --duration 15m -o id)
  ./deploy.sh
  norcube monitor maintenance finish "$id"`,
	}
	cmd.AddCommand(
		newMaintenanceListCmd(d),
		newMaintenanceGetCmd(d),
		newMaintenanceCreateCmd(d),
		newMaintenanceUpdateCmd(d),
		newMaintenanceCancelCmd(d),
		newMaintenanceFinishCmd(d),
	)
	return cmd
}

func newMaintenanceListCmd(d deps) *cobra.Command {
	var (
		pf                 cmdutil.PageFlags
		state, mon, spPage string
	)
	cmd := &cobra.Command{
		Use:         "list",
		Annotations: cmdutil.Reads(),
		Short:       "List maintenance windows, latest start first",
		Long:        `Lists the organization's windows, latest start first; with --state scheduled, soonest first.`,
		Example: `  norcube monitor maintenance list
  norcube monitor maintenance list --state in_progress
  norcube monitor maintenance list --monitor "Shop API" --all-pages
  norcube monitor maintenance list --status-page acme --state scheduled -o json`,
		Args: cobra.NoArgs,
		RunE: cmdutil.Run(func(cmd *cobra.Command, _ []string) (any, error) {
			params := monitor.ListMaintenanceWindowsParams{}
			if state != "" {
				if err := oneOf("--state", state, maintenanceStates); err != nil {
					return nil, err
				}
				v := monitor.ListMaintenanceWindowsParamsStatus(state)
				params.Status = &v
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			if mon != "" {
				id, err := monitorRef(cmd.Context(), c, mon)
				if err != nil {
					return nil, err
				}
				params.MonitorId = &id
			}
			if spPage != "" {
				id, err := statusPageID(cmd.Context(), c, spPage)
				if err != nil {
					return nil, err
				}
				params.StatusPageId = &id
			}
			page, err := cmdutil.Paginate(cmd.Context(), pf, func(ctx context.Context, cursor string, size int) ([]monitor.DtoMaintenanceWindow, string, error) {
				p := params
				p.Limit = &size
				if cursor != "" {
					p.Cursor = &cursor
				}
				res, err := c.API.ListMaintenanceWindowsWithResponse(ctx, &p)
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
			return nil, page.Print(cmd, windowTable(page.Items))
		}),
	}
	cmd.Flags().StringVar(&state, "state", "", "Only windows in this state: scheduled, in_progress, completed or cancelled")
	cmd.Flags().StringVar(&mon, "monitor", "", "Only windows holding this monitor (id or exact name)")
	cmd.Flags().StringVar(&spPage, "status-page", "", "Only windows showing on this status page (id or exact slug)")
	cmdutil.FlagEnum(cmd, "state", maintenanceStates...)
	cmdutil.AddPageFlags(cmd, &pf, defaultPageSize, maxItems)
	return cmd
}

func newMaintenanceGetCmd(d deps) *cobra.Command {
	return &cobra.Command{
		Use:         "get <window>",
		Annotations: cmdutil.Reads(),
		Short:       "Show a maintenance window, its targets and the pages it shows on",
		Example:     `  norcube monitor maintenance get 4d3c2b1a-0f9e-4d8c-b7a6-5f4e3d2c1b0a`,
		Args:        cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			id, err := idArg("maintenance window", args[0])
			if err != nil {
				return nil, err
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			w, err := getWindow(cmd.Context(), c, id)
			if err != nil {
				return nil, err
			}
			return windowObject(w), nil
		}),
	}
}

func getWindow(ctx context.Context, c *monitorContext, id uuid.UUID) (monitor.DtoMaintenanceWindow, error) {
	res, err := c.API.GetMaintenanceWindowWithResponse(ctx, id)
	if err != nil {
		return monitor.DtoMaintenanceWindow{}, err
	}
	if res.JSON200 == nil {
		return monitor.DtoMaintenanceWindow{}, apiError(res.HTTPResponse, res.Body)
	}
	return *res.JSON200, nil
}

// windowFlags are what create and update share.
type windowFlags struct {
	title, description        string
	start, end, length        string
	monitors, components      []string
	pages                     []string
	notifyScheduled           bool
	notifyStarted             bool
	notifyFinished, notifyOff bool
}

// add registers the flags. A switch's default is shown only where it applies:
// update changes what you pass and nothing else.
func (f *windowFlags) add(cmd *cobra.Command, notifyDefault bool) {
	cmd.Flags().StringVar(&f.title, "title", "", "The window's title; public on a published status page")
	cmd.Flags().StringVar(&f.description, "description", "", "What is happening; public on a published status page and in mail")
	cmd.Flags().StringArrayVar(&f.monitors, "monitor", nil, "A monitor to hold, by id or exact name (repeatable); domain monitors can't be held")
	cmd.Flags().StringArrayVar(&f.components, "component", nil, "A manual status page component to mark, by id or exact name (repeatable)")
	cmd.Flags().StringArrayVar(&f.pages, "status-page", nil, "Everything a status page shows right now, by id or exact slug (repeatable); something added to the page later isn't included")
	cmd.Flags().StringVar(&f.end, "end", "", "When it ends: "+maintenanceTimeFormats)
	cmd.Flags().StringVar(&f.length, "duration", "", "How long it lasts, counted from its start: 15m, 2h, 7d (at most 30 days)")
	cmd.MarkFlagsMutuallyExclusive("end", "duration")
	cmd.Flags().BoolVar(&f.notifyScheduled, "notify-scheduled", notifyDefault, "Email subscribers when it is scheduled")
	cmd.Flags().BoolVar(&f.notifyStarted, "notify-started", notifyDefault, "Email subscribers when it starts")
	cmd.Flags().BoolVar(&f.notifyFinished, "notify-finished", notifyDefault, "Email subscribers when it finishes")
	cmd.Flags().BoolVar(&f.notifyOff, "no-notify", false, "Turn off all three mails; a cancellation still tells whoever was told")
	for _, name := range []string{"notify-scheduled", "notify-started", "notify-finished"} {
		cmd.MarkFlagsMutuallyExclusive("no-notify", name)
	}
}

func (f *windowFlags) hasTargets() bool {
	return len(f.monitors)+len(f.components)+len(f.pages) > 0
}

// targets resolves the named monitors, components and pages into ids. A list
// nobody named stays out of the request.
func (f *windowFlags) targets(ctx context.Context, c *monitorContext) (monitor.DtoMaintenanceWindowTargets, error) {
	var out monitor.DtoMaintenanceWindowTargets
	if len(f.monitors) > 0 {
		ids := make([]uuid.UUID, 0, len(f.monitors))
		for _, ref := range f.monitors {
			id, err := monitorRef(ctx, c, ref)
			if err != nil {
				return out, err
			}
			ids = append(ids, id)
		}
		out.MonitorIds = &ids
	}
	if len(f.components) > 0 {
		ids, err := componentRefs(ctx, c, f.components)
		if err != nil {
			return out, err
		}
		out.ComponentIds = &ids
	}
	if len(f.pages) > 0 {
		ids := make([]uuid.UUID, 0, len(f.pages))
		for _, ref := range f.pages {
			id, err := statusPageID(ctx, c, ref)
			if err != nil {
				return out, err
			}
			ids = append(ids, id)
		}
		out.StatusPageIds = &ids
	}
	return out, nil
}

// componentRefs resolves manual components by id or exact name, reading what a
// window can target only when one is named by name. A name is unique on a page
// at most, so one matching several is a usage error, never a guess.
func componentRefs(ctx context.Context, c *monitorContext, refs []string) ([]uuid.UUID, error) {
	var known *monitor.DtoMaintenanceTargets
	out := make([]uuid.UUID, 0, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if id, err := uuid.Parse(ref); err == nil {
			out = append(out, id)
			continue
		}
		if known == nil {
			res, err := c.API.GetMaintenanceTargetsWithResponse(ctx)
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			known = res.JSON200
		}
		var ids []string
		for _, x := range known.Components {
			if strings.EqualFold(x.Name, ref) {
				ids = append(ids, x.Id.String())
			}
		}
		switch len(ids) {
		case 0:
			return nil, &clierr.Error{Code: clierr.CodeNotFound, Message: fmt.Sprintf("no manual component named %q", ref),
				Hint: "Only a page's manual components are targets; to hold a monitor's, use --monitor. See a page's with `norcube monitor status-pages layout get <page>`."}
		case 1:
			out = append(out, uuid.MustParse(ids[0]))
		default:
			return nil, clierr.Usage(fmt.Errorf("%d components are named %q: name one by id (%s)", len(ids), ref, strings.Join(ids, ", ")))
		}
	}
	return out, nil
}

func newMaintenanceCreateCmd(d deps) *cobra.Command {
	var f windowFlags
	cmd := &cobra.Command{
		Use:         "create",
		Annotations: cmdutil.Writes(),
		Short:       "Schedule a maintenance window, or start one now",
		Long: `Schedules a window. Without --start it begins right away. It needs a title,
at least one target, and --end or --duration; it can last at most 30 days and
start at most a year ahead.

It shows by itself on every published status page displaying a target, and
subscribers get all three mails unless you turn them off with --notify-scheduled,
--notify-started, --notify-finished or --no-notify. A window that starts within
15 minutes sends no scheduled mail, only the started one.

A CI token needs the monitor.maintenance scope, and monitor.statuspages too when
the window shows on a published page.`,
		Example: `  # Silence a monitor around a deploy
  id=$(norcube monitor maintenance create --monitor api --title "Deploy" --duration 15m -o id)
  ./deploy.sh
  norcube monitor maintenance finish "$id"

  # Announce work for tomorrow on everything a status page shows
  norcube monitor maintenance create --status-page acme --title "Database upgrade" --description "Short interruptions are expected." --start 2026-11-07T14:00:00Z --end 2026-11-07T16:00:00Z

  # A quiet window nobody hears about
  norcube monitor maintenance create --monitor "Shop API" --monitor "Shop cert" --title "Load test" --start +1h --duration 2h --no-notify`,
		Args: cobra.NoArgs,
		RunE: cmdutil.Run(func(cmd *cobra.Command, _ []string) (any, error) {
			title := strings.TrimSpace(f.title)
			if title == "" {
				return nil, clierr.Usage(fmt.Errorf("the title is empty"))
			}
			if !f.hasTargets() {
				return nil, clierr.Usage(fmt.Errorf("name at least one target: --monitor, --component or --status-page"))
			}
			now := d.now()
			start := now
			if f.start != "" {
				t, err := cmdutil.ParseFutureTimeArg("start", f.start, now)
				if err != nil {
					return nil, err
				}
				start = t
			}
			end, err := windowEnd(f, start, now)
			if err != nil {
				return nil, err
			}
			notify := !f.notifyOff
			body := monitor.DtoMaintenanceWindowCreate{
				Title:           title,
				StartsAt:        start.UTC().Format(time.RFC3339),
				EndsAt:          end.UTC().Format(time.RFC3339),
				NotifyScheduled: notify && f.notifyScheduled,
				NotifyStarted:   notify && f.notifyStarted,
				NotifyFinished:  notify && f.notifyFinished,
			}
			if cmd.Flags().Changed("description") {
				body.Description = &f.description
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			if body.Targets, err = f.targets(cmd.Context(), c); err != nil {
				return nil, err
			}
			res, err := c.API.CreateMaintenanceWindowWithResponse(cmd.Context(), body)
			if err != nil {
				return nil, err
			}
			if res.JSON201 == nil {
				return nil, maintenanceError(res.HTTPResponse, res.Body, "create", "")
			}
			return windowSaved("Scheduled", *res.JSON201), nil
		}),
	}
	f.add(cmd, true)
	cmd.Flags().StringVar(&f.start, "start", "", "When it starts, if not now: "+maintenanceTimeFormats)
	_ = cmd.MarkFlagRequired("title")
	cmd.MarkFlagsOneRequired("end", "duration")
	return cmd
}

// windowEnd reads --end or --duration, the latter counted from start.
func windowEnd(f windowFlags, start, now time.Time) (time.Time, error) {
	if f.length != "" {
		span, err := cmdutil.ParseDurationArg("duration", f.length)
		if err != nil {
			return time.Time{}, err
		}
		return start.Add(span), nil
	}
	return cmdutil.ParseFutureTimeArg("end", f.end, now)
}

func newMaintenanceUpdateCmd(d deps) *cobra.Command {
	var f windowFlags
	cmd := &cobra.Command{
		Use:         "update <window>",
		Annotations: cmdutil.Writes(),
		Short:       "Change a maintenance window, as far as where it stands allows",
		Long: `Changes only what you pass.

Before the window starts, everything can change; moving a time by 5 minutes or
more tells subscribers who were already told. Once it has started, its targets
and start are frozen: you can still change the text and the mail switches, and
extend or shorten it, which sends no mail. Once it is over, only the text can
change. A cancelled window can't change. The API refuses anything else with
exit 8.

--monitor, --component and --status-page replace the targets as a whole, so name
everything the window should keep. --duration counts from the new --start, or
else from the window's current start.

A CI token needs the monitor.maintenance scope, and monitor.statuspages too when
the window shows on a published page before or after the change. To end a window
now, use "finish"; to call off one that hasn't started, use "cancel".`,
		Example: `  # The deploy needs longer
  norcube monitor maintenance update 4d3c2b1a-0f9e-4d8c-b7a6-5f4e3d2c1b0a --end +1h
  norcube monitor maintenance update 4d3c2b1a-0f9e-4d8c-b7a6-5f4e3d2c1b0a --start 2026-11-08T14:00:00Z --duration 2h
  norcube monitor maintenance update 4d3c2b1a-0f9e-4d8c-b7a6-5f4e3d2c1b0a --title "Database upgrade" --no-notify`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			id, err := idArg("maintenance window", args[0])
			if err != nil {
				return nil, err
			}
			set := cmd.Flags().Changed
			now := d.now()
			var body monitor.DtoMaintenanceWindowUpdate
			changed := f.hasTargets()
			if set("title") {
				t := strings.TrimSpace(f.title)
				if t == "" {
					return nil, clierr.Usage(fmt.Errorf("the title is empty"))
				}
				body.Title, changed = &t, true
			}
			if set("description") {
				body.Description, changed = &f.description, true
			}
			var start *time.Time
			if set("start") {
				t, err := cmdutil.ParseFutureTimeArg("start", f.start, now)
				if err != nil {
					return nil, err
				}
				start = &t
				v := t.Format(time.RFC3339)
				body.StartsAt, changed = &v, true
			}
			if set("end") {
				t, err := cmdutil.ParseFutureTimeArg("end", f.end, now)
				if err != nil {
					return nil, err
				}
				v := t.Format(time.RFC3339)
				body.EndsAt, changed = &v, true
			}
			var span time.Duration
			if set("duration") {
				if span, err = cmdutil.ParseDurationArg("duration", f.length); err != nil {
					return nil, err
				}
				changed = true
			}
			off := false
			if f.notifyOff {
				body.NotifyScheduled, body.NotifyStarted, body.NotifyFinished, changed = &off, &off, &off, true
			}
			if set("notify-scheduled") {
				body.NotifyScheduled, changed = &f.notifyScheduled, true
			}
			if set("notify-started") {
				body.NotifyStarted, changed = &f.notifyStarted, true
			}
			if set("notify-finished") {
				body.NotifyFinished, changed = &f.notifyFinished, true
			}
			if !changed {
				return nil, clierr.Usage(fmt.Errorf("nothing to change: pass what should change, such as --title, --end or --monitor (to end it now, use: maintenance finish)"))
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			if f.hasTargets() {
				t, err := f.targets(cmd.Context(), c)
				if err != nil {
					return nil, err
				}
				body.Targets = &t
			}
			if set("duration") {
				if start == nil {
					w, err := getWindow(cmd.Context(), c, id)
					if err != nil {
						return nil, err
					}
					t, err := time.Parse(time.RFC3339Nano, w.StartsAt)
					if err != nil {
						return nil, fmt.Errorf("the window's start %q: %w", w.StartsAt, err)
					}
					start = &t
				}
				v := start.Add(span).UTC().Format(time.RFC3339)
				body.EndsAt = &v
			}
			res, err := c.API.UpdateMaintenanceWindowWithResponse(cmd.Context(), id, body)
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, maintenanceError(res.HTTPResponse, res.Body, "update", id.String())
			}
			return windowSaved("Updated", *res.JSON200), nil
		}),
	}
	f.add(cmd, false)
	cmd.Flags().StringVar(&f.start, "start", "", "Move its start, before it has started: an RFC 3339 timestamp or a span from now (+2h, +7d)")
	return cmd
}

func newMaintenanceCancelCmd(d deps) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:         "cancel <window>",
		Annotations: cmdutil.Writes(),
		Short:       "Call off a maintenance window that hasn't started",
		Long: `Calls off a scheduled window. It won't run, and subscribers who were told hear
it was cancelled. One that has started exits 8: use "finish" instead. It needs
only the monitor.maintenance scope, even on a published page.`,
		Example: `  norcube monitor maintenance cancel 4d3c2b1a-0f9e-4d8c-b7a6-5f4e3d2c1b0a
  norcube monitor maintenance cancel 4d3c2b1a-0f9e-4d8c-b7a6-5f4e3d2c1b0a --yes`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			id, err := idArg("maintenance window", args[0])
			if err != nil {
				return nil, err
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			if ok, err := cmdutil.Confirm(fmt.Sprintf("Cancel maintenance window %s? Subscribers who were told hear of it.", id), yes); err != nil || !ok {
				return nil, cmdutil.Cancelled(cmd, err)
			}
			res, err := c.API.CancelMaintenanceWindowWithResponse(cmd.Context(), id)
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, maintenanceError(res.HTTPResponse, res.Body, "cancel", id.String())
			}
			w := *res.JSON200
			return windowDone(fmt.Sprintf("Cancelled maintenance %q (%s). Subscribers who were told hear of it.", w.Title, w.Id), w), nil
		}),
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Don't ask for confirmation")
	return cmd
}

func newMaintenanceFinishCmd(d deps) *cobra.Command {
	return &cobra.Command{
		Use:         "finish <window>",
		Annotations: cmdutil.Writes(),
		Short:       "End a maintenance window in progress now",
		Long: `Ends a window in progress. The next check counts and alerts as usual, monitors
with an open incident it held are checked again within a minute, and subscribers
hear it finished early. One that hasn't started exits 8: use "cancel" instead.

It asks for no confirmation, because ending a silence is the safe direction and
it has to run unattended at the end of a deploy. It needs only the
monitor.maintenance scope, even on a published page.`,
		Example: `  norcube monitor maintenance finish 4d3c2b1a-0f9e-4d8c-b7a6-5f4e3d2c1b0a
  norcube monitor maintenance finish "$id"`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			id, err := idArg("maintenance window", args[0])
			if err != nil {
				return nil, err
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.FinishMaintenanceWindowWithResponse(cmd.Context(), id)
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, maintenanceError(res.HTTPResponse, res.Body, "finish", id.String())
			}
			w := *res.JSON200
			return windowDone(fmt.Sprintf("Finished maintenance %q (%s). Alerts are back on from the next check.", w.Title, w.Id), w), nil
		}),
	}
}

// maintenanceError renders a refused write. The API's sentences already say
// what is wrong; the hint says what to do next.
func maintenanceError(resp *http.Response, body []byte, verb, id string) error {
	err := apiError(resp, body)
	var e *clierr.Error
	if !errors.As(err, &e) {
		return err
	}
	details, _ := e.Details.(map[string]any)
	reason, _ := details["reason"].(string)
	switch {
	case reason == reasonPublishDenied:
		e.Hint = "A window shows on every published page displaying its targets. Get permission to publish there, give a CI token the monitor.statuspages scope, or target only monitors no published page shows."
	case reason == reasonWindowState:
		data, _ := details["data"].(map[string]string)
		switch {
		case verb == "cancel" && data["state"] == "in_progress":
			e.Hint = "End it now with `norcube monitor maintenance finish " + id + "`."
		case verb == "finish" && data["state"] == "scheduled":
			e.Hint = "Call it off with `norcube monitor maintenance cancel " + id + "`."
		}
	case e.Code == clierr.CodeInsufficientScope:
		e.Hint = "Changing a maintenance window takes the monitor.maintenance scope, which no other scope includes. `norcube auth ci-token scopes` lists them."
	}
	return err
}

// windowSaved is the result of create and update: the API's window, and a line
// saying what it does now.
func windowSaved(verb string, w monitor.DtoMaintenanceWindow) output.Done {
	var msg strings.Builder
	fmt.Fprintf(&msg, "%s maintenance %q (%s), %s to %s. ", verb, w.Title, w.Id, stamp(w.StartsAt), stamp(w.EndsAt))
	if n := len(w.Targets.MonitorIds); n > 0 {
		fmt.Fprintf(&msg, "Alerts for %s pause while it runs. ", plural(n, "monitor"))
	}
	public := publicPages(w)
	if len(public) == 0 {
		msg.WriteString("It shows on no published status page.")
		return windowDone(msg.String(), w)
	}
	fmt.Fprintf(&msg, "It shows publicly on %s within a minute. ", strings.Join(public, ", "))
	if mails := windowMails(w); len(mails) > 0 {
		fmt.Fprintf(&msg, "Subscribers get mail: %s.", strings.Join(mails, ", "))
	} else {
		msg.WriteString("Subscribers get no mail.")
	}
	return windowDone(msg.String(), w)
}

func windowDone(msg string, w monitor.DtoMaintenanceWindow) output.Done {
	return output.Done{Message: msg, Value: w, Fields: []output.Field{{Value: w.Id.String()}, {Value: string(w.State)}}}
}

// publicPages names the published pages a window shows on.
func publicPages(w monitor.DtoMaintenanceWindow) []string {
	var names []string
	for _, p := range w.StatusPages {
		if p.Published {
			names = append(names, p.Name)
		}
	}
	return names
}

// windowMails names the mails a window sends.
func windowMails(w monitor.DtoMaintenanceWindow) []string {
	var mails []string
	if w.NotifyScheduled {
		mails = append(mails, "scheduled")
	}
	if w.NotifyStarted {
		mails = append(mails, "started")
	}
	if w.NotifyFinished {
		mails = append(mails, "finished")
	}
	return mails
}

func mailWords(w monitor.DtoMaintenanceWindow) string {
	if mails := windowMails(w); len(mails) > 0 {
		return strings.Join(mails, ", ")
	}
	return "none"
}

func windowRow(w monitor.DtoMaintenanceWindow) []string {
	public := "—"
	if names := publicPages(w); len(names) > 0 {
		public = strings.Join(names, ", ")
	}
	return []string{w.Id.String(), w.Title, string(w.State), output.WhenString(w.StartsAt), output.WhenString(w.EndsAt),
		strconv.Itoa(len(w.Targets.MonitorIds)), strconv.Itoa(len(w.Targets.ComponentIds)), public}
}

func windowTable(items []monitor.DtoMaintenanceWindow) output.Table[monitor.DtoMaintenanceWindow] {
	return output.Table[monitor.DtoMaintenanceWindow]{
		Headers:   []string{"ID", "TITLE", "STATE", "STARTS", "ENDS", "MONITORS", "COMPONENTS", "PUBLIC ON"},
		MaxWidths: []int{0, 40, 0, 0, 0, 0, 0, 32},
		Rows:      windowRow,
		Items:     items,
	}
}

func windowObject(w monitor.DtoMaintenanceWindow) output.Object {
	ids := func(in []uuid.UUID) []string {
		out := make([]string, len(in))
		for i, id := range in {
			out[i] = id.String()
		}
		return out
	}
	pages := make([]string, 0, len(w.StatusPages))
	for _, p := range w.StatusPages {
		state := "draft"
		if p.Published {
			state = "published"
		}
		pages = append(pages, fmt.Sprintf("%s (%s)", p.Name, state))
	}
	ends := output.WhenString(w.EndsAt)
	endsName := "Ends"
	if w.FinishedEarly {
		endsName = "Finished early"
	}
	f := []output.Field{
		{Name: "ID", Value: w.Id.String()},
		{Name: "Title", Value: w.Title},
		{Name: "State", Value: string(w.State)},
		{Name: "Starts", Value: output.WhenString(w.StartsAt)},
		{Name: endsName, Value: ends},
	}
	if w.Description != "" {
		f = append(f, output.Field{Name: "Description", Value: w.Description})
	}
	f = append(f,
		output.Field{Name: "Monitors", Value: orNone(ids(w.Targets.MonitorIds))},
		output.Field{Name: "Components", Value: orNone(ids(w.Targets.ComponentIds))},
		output.Field{Name: "Status pages", Value: orNone(pages)},
		output.Field{Name: "Mail", Value: mailWords(w)},
		output.Field{Name: "Created", Value: output.WhenString(w.CreatedAt)},
	)
	if w.CreatedByName != nil {
		f = append(f, output.Field{Name: "Created by", Value: *w.CreatedByName})
	}
	if w.FinishedByName != nil {
		f = append(f, output.Field{Name: "Finished by", Value: *w.FinishedByName})
	}
	if w.CancelledAt != nil {
		f = append(f, output.Field{Name: "Cancelled", Value: output.WhenString(*w.CancelledAt)})
	}
	if w.CancelledByName != nil {
		f = append(f, output.Field{Name: "Cancelled by", Value: *w.CancelledByName})
	}
	// -o text prints the list's columns: the fields above depend on the window.
	return output.Object{Value: w, Fields: f, Text: windowRow(w)}
}
