package monitor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/monitor"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

var (
	postFilters      = []string{"open", "resolved"}
	postStatuses     = []string{"investigating", "identified", "monitoring", "resolved"}
	postImpacts      = []string{"none", "minor", "major", "critical"}
	affectedStatuses = []string{"operational", "degraded_performance", "partial_outage", "major_outage"}
)

const componentFlagHelp = `An affected component and the status the page shows it in while the incident is open, as "<id or name>=<status>" (repeatable): operational, degraded_performance, partial_outage or major_outage`

func newStatusPageIncidentsCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "incidents",
		Aliases: []string{"incident"},
		Short:   "Post incidents on a status page and keep them updated",
		Long: `A posted incident is what you tell the public: a title, an impact and a
series of updates. It is separate from a monitor's own incidents, which stay
internal; a post may link to them for your navigation, but those links are
never published.

While a post is open, the status it gives an affected component wins over
what the component's monitor says. A post with status resolved moves to the
page's history. On a published page everything here is public within a
minute.`,
	}
	cmd.AddCommand(newPostListCmd(d), newPostGetCmd(d), newPostCreateCmd(d), newPostUpdateCmd(d), newPostUpdatesCmd(d))
	return cmd
}

func newPostListCmd(d deps) *cobra.Command {
	var (
		pf     cmdutil.PageFlags
		status string
	)
	cmd := &cobra.Command{
		Use:         "list <page>",
		Annotations: cmdutil.Reads(),
		Short:       "List the incidents posted on a status page, newest first",
		Example: `  norcube monitor status-pages incidents list acme
  norcube monitor status-pages incidents list acme --status open`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			params := monitor.ListStatusPageIncidentsParams{}
			if status != "" {
				if err := oneOf("--status", status, postFilters); err != nil {
					return nil, err
				}
				v := monitor.ListStatusPageIncidentsParamsStatus(status)
				params.Status = &v
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			id, err := statusPageID(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			page, err := cmdutil.Paginate(cmd.Context(), pf, func(ctx context.Context, cursor string, size int) ([]monitor.DtoStatusPageIncident, string, error) {
				p := params
				p.Limit = &size
				if cursor != "" {
					p.Cursor = &cursor
				}
				res, err := c.API.ListStatusPageIncidentsWithResponse(ctx, id, &p)
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
			return nil, page.Print(cmd, postTable(page.Items))
		}),
	}
	cmd.Flags().StringVar(&status, "status", "", "Only open or resolved incidents")
	cmdutil.FlagEnum(cmd, "status", postFilters...)
	cmdutil.AddPageFlags(cmd, &pf, defaultPageSize, maxItems)
	return cmd
}

func newPostGetCmd(d deps) *cobra.Command {
	return &cobra.Command{
		Use:         "get <page> <incident>",
		Annotations: cmdutil.Reads(),
		Short:       "Show a posted incident with its updates and public address",
		Example:     `  norcube monitor status-pages incidents get acme 8c7d6e5f-4a3b-4c2d-9e1f-0a1b2c3d4e5f`,
		Args:        cobra.ExactArgs(2),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			incidentID, err := idArg("posted incident", args[1])
			if err != nil {
				return nil, err
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			p, err := getStatusPage(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			res, err := c.API.GetStatusPageIncidentWithResponse(cmd.Context(), p.Id, incidentID)
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			return postObject(p, *res.JSON200), nil
		}),
	}
}

// messageFlags are an update's text, as an argument or from a file.
type messageFlags struct{ message, file string }

func addMessageFlags(cmd *cobra.Command, f *messageFlags, what string) {
	cmd.Flags().StringVar(&f.message, "message", "", what+"; public")
	cmd.Flags().StringVar(&f.file, "message-file", "", "Read the message from this file (- for standard input)")
	cmd.MarkFlagsMutuallyExclusive("message", "message-file")
	cmd.MarkFlagsOneRequired("message", "message-file")
}

func (f messageFlags) read(cmd *cobra.Command) (string, error) {
	text, err := cmdutil.ReadText(cmd.InOrStdin(), f.message, f.file, "the message")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return "", clierr.Usage(fmt.Errorf("the message is empty"))
	}
	return text, nil
}

func newPostCreateCmd(d deps) *cobra.Command {
	var (
		msg              messageFlags
		title            string
		status, impact   string
		startedAt        string
		components       []string
		monitorIncidents []string
	)
	cmd := &cobra.Command{
		Use:         "create <page>",
		Annotations: cmdutil.Writes(),
		Short:       "Post an incident on a status page; public within a minute on a published page",
		Long: `Posts an incident with its first update. On a published page it is public
within a minute: write it for your customers. It counts toward the page's
overall status while it is open, and --component sets what the page shows
for the components it affects.

--monitor-incident links the monitor incidents the post is about, for your
own navigation; the links are never published.`,
		Example: `  norcube monitor status-pages incidents create acme --title "Elevated API errors" --status investigating --impact minor --message "We are looking into it."
  norcube monitor status-pages incidents create acme --title "Checkout is down" --status identified --impact major --message-file update.txt --component "Checkout=major_outage" --started-at 20m`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			if err := oneOf("--status", status, postStatuses); err != nil {
				return nil, err
			}
			if err := oneOf("--impact", impact, postImpacts); err != nil {
				return nil, err
			}
			parsed, err := parseAffected(components)
			if err != nil {
				return nil, err
			}
			links, err := monitorIncidentIDs(monitorIncidents)
			if err != nil {
				return nil, err
			}
			text, err := msg.read(cmd)
			if err != nil {
				return nil, err
			}
			body := monitor.DtoStatusPageIncidentCreate{
				Title: strings.TrimSpace(title), Body: text,
				Status: monitor.DtoStatusPageIncidentCreateStatus(status),
				Impact: monitor.DtoStatusPageIncidentCreateImpact(impact),
			}
			if startedAt != "" {
				at, err := startedAtArg(startedAt, d.now())
				if err != nil {
					return nil, err
				}
				body.StartedAt = &at
			}
			if len(links) > 0 {
				body.MonitorIncidentIds = &links
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			p, err := getStatusPage(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			if len(parsed) > 0 {
				affected, err := resolveAffected(cmd.Context(), c, p.Id, parsed)
				if err != nil {
					return nil, err
				}
				body.Components = &affected
			}
			res, err := c.API.CreateStatusPageIncidentWithResponse(cmd.Context(), p.Id, body)
			if err != nil {
				return nil, err
			}
			if res.JSON201 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			return postDone(p, *res.JSON201, "Posted incident"), nil
		}),
	}
	cmd.Flags().StringVar(&title, "title", "", "The incident's public title")
	cmd.Flags().StringVar(&status, "status", "", "Where it stands: investigating, identified, monitoring or resolved")
	cmd.Flags().StringVar(&impact, "impact", "", "How bad it is: none, minor, major or critical")
	addMessageFlags(cmd, &msg, "The first update's text")
	cmd.Flags().StringVar(&startedAt, "started-at", "", "When it began, if not now: an RFC 3339 timestamp, a date or an age (20m, 2h)")
	cmd.Flags().StringArrayVar(&components, "component", nil, componentFlagHelp)
	cmd.Flags().StringSliceVar(&monitorIncidents, "monitor-incident", nil, "Id of a monitor incident this post is about (repeat or comma-separate); never published")
	cmdutil.FlagEnum(cmd, "status", postStatuses...)
	cmdutil.FlagEnum(cmd, "impact", postImpacts...)
	for _, name := range []string{"title", "status", "impact"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

func newPostUpdateCmd(d deps) *cobra.Command {
	var (
		title, impact, startedAt string
		components               []string
		noComponents             bool
		monitorIncidents         []string
		noMonitorIncidents       bool
	)
	cmd := &cobra.Command{
		Use:         "update <page> <incident>",
		Annotations: cmdutil.Writes(),
		Short:       "Correct a posted incident; public within a minute on a published page",
		Long: `Corrects a post's title, impact, start or references, and changes only what
you pass. It adds no update and doesn't move the status: for that, use
"incidents updates create". --component replaces the affected components as
a whole. Resolved posts can be corrected too. On a published page a
correction is public within a minute.`,
		Example: `  norcube monitor status-pages incidents update acme 8c7d6e5f-4a3b-4c2d-9e1f-0a1b2c3d4e5f --title "Elevated API errors in Europe"
  norcube monitor status-pages incidents update acme 8c7d6e5f-4a3b-4c2d-9e1f-0a1b2c3d4e5f --impact major --component "API=partial_outage"`,
		Args: cobra.ExactArgs(2),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			incidentID, err := idArg("posted incident", args[1])
			if err != nil {
				return nil, err
			}
			set := cmd.Flags().Changed
			var body monitor.DtoStatusPageIncidentUpdate
			changed := false
			if set("title") {
				t := strings.TrimSpace(title)
				body.Title, changed = &t, true
			}
			if set("impact") {
				if err := oneOf("--impact", impact, postImpacts); err != nil {
					return nil, err
				}
				v := monitor.DtoStatusPageIncidentUpdateImpact(impact)
				body.Impact, changed = &v, true
			}
			if set("started-at") {
				at, err := startedAtArg(startedAt, d.now())
				if err != nil {
					return nil, err
				}
				body.StartedAt, changed = &at, true
			}
			parsed, err := parseAffected(components)
			if err != nil {
				return nil, err
			}
			if noComponents {
				body.Components, changed = &[]monitor.DtoStatusPageAffected{}, true
			}
			if set("monitor-incident") {
				links, err := monitorIncidentIDs(monitorIncidents)
				if err != nil {
					return nil, err
				}
				body.MonitorIncidentIds, changed = &links, true
			}
			if noMonitorIncidents {
				body.MonitorIncidentIds, changed = &[]uuid.UUID{}, true
			}
			if !changed && len(parsed) == 0 {
				return nil, clierr.Usage(fmt.Errorf("nothing to change: pass --title, --impact, --started-at, --component or --monitor-incident (to add an update, use: incidents updates create)"))
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			p, err := getStatusPage(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			if len(parsed) > 0 {
				affected, err := resolveAffected(cmd.Context(), c, p.Id, parsed)
				if err != nil {
					return nil, err
				}
				body.Components = &affected
			}
			res, err := c.API.UpdateStatusPageIncidentWithResponse(cmd.Context(), p.Id, incidentID, body)
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			return postDone(p, *res.JSON200, "Corrected incident"), nil
		}),
	}
	cmd.Flags().StringVar(&title, "title", "", "New public title")
	cmd.Flags().StringVar(&impact, "impact", "", "New impact: none, minor, major or critical")
	cmd.Flags().StringVar(&startedAt, "started-at", "", "When it began: an RFC 3339 timestamp, a date or an age (20m, 2h)")
	cmd.Flags().StringArrayVar(&components, "component", nil, componentFlagHelp)
	cmd.Flags().BoolVar(&noComponents, "no-components", false, "Remove every affected component")
	cmd.Flags().StringSliceVar(&monitorIncidents, "monitor-incident", nil, "Replace the linked monitor incidents with these ids (repeat or comma-separate); never published")
	cmd.Flags().BoolVar(&noMonitorIncidents, "no-monitor-incidents", false, "Remove every link to a monitor incident")
	cmd.MarkFlagsMutuallyExclusive("component", "no-components")
	cmd.MarkFlagsMutuallyExclusive("monitor-incident", "no-monitor-incidents")
	cmdutil.FlagEnum(cmd, "impact", postImpacts...)
	return cmd
}

func newPostUpdatesCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "updates",
		Short: "Add updates to a posted incident",
		Long: `Updates are the timeline of a posted incident: "incidents get" shows them,
and they can't be edited or removed.`,
	}
	cmd.AddCommand(newPostUpdateCreateCmd(d))
	return cmd
}

func newPostUpdateCreateCmd(d deps) *cobra.Command {
	var (
		msg          messageFlags
		status       string
		components   []string
		noComponents bool
	)
	cmd := &cobra.Command{
		Use:         "create <page> <incident>",
		Annotations: cmdutil.Writes(),
		Short:       "Post an update on an incident; public within a minute on a published page",
		Long: `Adds an update to an open post. Its status becomes the post's; resolved ends
the post and moves it to the page's history, after which it takes no more
updates (post a new incident instead). On a published page the update is
public within a minute. --component replaces the affected components.`,
		Example: `  norcube monitor status-pages incidents updates create acme 8c7d6e5f-4a3b-4c2d-9e1f-0a1b2c3d4e5f --status monitoring --message "A fix is rolling out."
  norcube monitor status-pages incidents updates create acme 8c7d6e5f-4a3b-4c2d-9e1f-0a1b2c3d4e5f --status resolved --message "All clear."`,
		Args: cobra.ExactArgs(2),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			incidentID, err := idArg("posted incident", args[1])
			if err != nil {
				return nil, err
			}
			if err := oneOf("--status", status, postStatuses); err != nil {
				return nil, err
			}
			parsed, err := parseAffected(components)
			if err != nil {
				return nil, err
			}
			text, err := msg.read(cmd)
			if err != nil {
				return nil, err
			}
			body := monitor.DtoStatusPageIncidentPost{Status: monitor.DtoStatusPageIncidentPostStatus(status), Body: text}
			if noComponents {
				body.Components = &[]monitor.DtoStatusPageAffected{}
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			p, err := getStatusPage(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			if len(parsed) > 0 {
				affected, err := resolveAffected(cmd.Context(), c, p.Id, parsed)
				if err != nil {
					return nil, err
				}
				body.Components = &affected
			}
			res, err := c.API.CreateStatusPageIncidentUpdateWithResponse(cmd.Context(), p.Id, incidentID, body)
			if err != nil {
				return nil, err
			}
			if res.JSON201 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			return postDone(p, *res.JSON201, "Posted an update on incident"), nil
		}),
	}
	cmd.Flags().StringVar(&status, "status", "", "Where it stands now: investigating, identified, monitoring or resolved")
	addMessageFlags(cmd, &msg, "The update's text")
	cmd.Flags().StringArrayVar(&components, "component", nil, componentFlagHelp)
	cmd.Flags().BoolVar(&noComponents, "no-components", false, "Remove every affected component")
	cmd.MarkFlagsMutuallyExclusive("component", "no-components")
	cmdutil.FlagEnum(cmd, "status", postStatuses...)
	_ = cmd.MarkFlagRequired("status")
	return cmd
}

// postDone is the result of a write on a post: the API's resource, and a
// line saying who can see it now.
func postDone(p monitor.DtoStatusPage, inc monitor.DtoStatusPageIncident, verb string) output.Done {
	msg := fmt.Sprintf("%s %q on %s. ", verb, inc.Title, p.Slug)
	if p.Published {
		msg += "Public within a minute at " + postURL(p, inc.Id) + "."
	} else {
		msg += "The page isn't published, so only preview links show it."
	}
	return output.Done{Message: msg, Value: inc, Fields: []output.Field{{Value: inc.Id.String()}, {Value: string(inc.Status)}}}
}

// postURL is a post's public address.
func postURL(p monitor.DtoStatusPage, id uuid.UUID) string {
	return strings.TrimRight(p.Url, "/") + "/incidents/" + id.String()
}

func startedAtArg(v string, now time.Time) (string, error) {
	t, err := cmdutil.ParseTimeArg("started-at", v, now)
	if err != nil {
		return "", err
	}
	return t.Format(time.RFC3339), nil
}

func monitorIncidentIDs(values []string) ([]uuid.UUID, error) {
	out := make([]uuid.UUID, 0, len(values))
	for _, v := range values {
		id, err := idArg("monitor incident", v)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

// affectedArg is one --component value: a component's id or public name,
// and the status it should show.
type affectedArg struct{ ref, status string }

// parseAffected reads --component values before any request. The status
// comes after the last "=", since a component's name may contain one.
func parseAffected(values []string) ([]affectedArg, error) {
	out := make([]affectedArg, 0, len(values))
	for _, v := range values {
		i := strings.LastIndex(v, "=")
		if i <= 0 || i == len(v)-1 {
			return nil, clierr.Usage(fmt.Errorf(`--component %q: use "<id or name>=<status>"`, v))
		}
		status := strings.TrimSpace(v[i+1:])
		if err := oneOf("--component "+strings.TrimSpace(v[:i]), status, affectedStatuses); err != nil {
			return nil, err
		}
		out = append(out, affectedArg{ref: strings.TrimSpace(v[:i]), status: status})
	}
	return out, nil
}

// resolveAffected turns --component values into the API's shape, reading
// the page's layout only when one names a component by name.
func resolveAffected(ctx context.Context, c *monitorContext, pageID uuid.UUID, args []affectedArg) ([]monitor.DtoStatusPageAffected, error) {
	var layout *monitor.DtoStatusPageLayout
	out := make([]monitor.DtoStatusPageAffected, 0, len(args))
	for _, a := range args {
		id, err := uuid.Parse(a.ref)
		if err != nil {
			if layout == nil {
				res, err := c.API.GetStatusPageLayoutWithResponse(ctx, pageID)
				if err != nil {
					return nil, err
				}
				if res.JSON200 == nil {
					return nil, apiError(res.HTTPResponse, res.Body)
				}
				layout = res.JSON200
			}
			if id, err = componentByName(*layout, a.ref); err != nil {
				return nil, err
			}
		}
		out = append(out, monitor.DtoStatusPageAffected{ComponentId: id, Status: monitor.DtoStatusPageAffectedStatus(a.status)})
	}
	return out, nil
}

// componentByName finds a component by its exact public name
// (case-insensitive). A name that matches several is a usage error, never a
// guess.
func componentByName(l monitor.DtoStatusPageLayout, name string) (uuid.UUID, error) {
	var ids []uuid.UUID
	match := func(cs []monitor.DtoStatusPageComponent) {
		for _, x := range cs {
			if x.Id != nil && strings.EqualFold(x.Name, name) {
				ids = append(ids, *x.Id)
			}
		}
	}
	match(l.Components)
	for _, g := range l.Groups {
		match(g.Components)
	}
	switch len(ids) {
	case 0:
		return uuid.Nil, &clierr.Error{Code: clierr.CodeNotFound, Message: fmt.Sprintf("no component named %q on this page", name),
			Hint: "List them with `norcube monitor status-pages layout get <page>`."}
	case 1:
		return ids[0], nil
	}
	names := make([]string, len(ids))
	for i, id := range ids {
		names[i] = id.String()
	}
	return uuid.Nil, clierr.Usage(fmt.Errorf("%d components are named %q: name one by id (%s)", len(ids), name, strings.Join(names, ", ")))
}
