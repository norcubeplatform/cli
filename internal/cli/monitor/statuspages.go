package monitor

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/monitor"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

// The bar ranges and history window a page may have; the API refuses others.
var statusPageUptimeDays = []string{"30", "60", "90"}

const (
	statusPageHistoryDaysMin = 7
	statusPageHistoryDaysMax = 365
)

func newStatusPagesCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "status-pages",
		Aliases: []string{"status-page"},
		Short:   "Manage public status pages, their layout, posted incidents and subscribers",
		Long: `A status page is a public page that shows chosen monitors and manual rows
as components, under names you pick; a monitor's own name and target are
never shown. A page starts unpublished: set its layout, check it with a
preview link, then publish it. Changes to a published page, and incidents
posted on it, are public within a minute.

Pages are named by their id or exact slug. Any Norcube Monitor scope reads
them; a CI token changes them, posts on them or reaches their subscribers
only with the monitor.statuspages scope, which no other scope includes,
because what it changes is public and subscribers' addresses are personal
data.`,
	}
	cmd.AddCommand(
		newStatusPageListCmd(d), newStatusPageGetCmd(d), newStatusPageCreateCmd(d), newStatusPageUpdateCmd(d),
		newStatusPageDeleteCmd(d), newStatusPagePublishCmd(d), newStatusPageUnpublishCmd(d), newStatusPagePreviewLinkCmd(d),
		newStatusPageLayoutCmd(d), newStatusPageIncidentsCmd(d), newStatusPageSubscribersCmd(d),
	)
	return cmd
}

func newStatusPageListCmd(d deps) *cobra.Command {
	var (
		pf           cmdutil.PageFlags
		monitorIDArg string
	)
	cmd := &cobra.Command{
		Use:         "list",
		Annotations: cmdutil.Reads(),
		Short:       "List status pages, newest first",
		Long: `Lists the organization's status pages, published or not. --monitor lists
the pages that show one monitor instead (at most 50, on one page).`,
		Example: `  norcube monitor status-pages list
  norcube monitor status-pages list --monitor "Shop"
  norcube monitor status-pages list -o json --query "[?published].url"`,
		Args: cobra.NoArgs,
		RunE: cmdutil.Run(func(cmd *cobra.Command, _ []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			params := monitor.ListStatusPagesParams{}
			if monitorIDArg != "" {
				id, err := monitorRef(cmd.Context(), c, monitorIDArg)
				if err != nil {
					return nil, err
				}
				params.MonitorId = &id
			}
			page, err := cmdutil.Paginate(cmd.Context(), pf, func(ctx context.Context, cursor string, size int) ([]monitor.DtoStatusPage, string, error) {
				p := params
				p.Limit = &size
				if cursor != "" {
					p.Cursor = &cursor
				}
				res, err := c.API.ListStatusPagesWithResponse(ctx, &p)
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
			return nil, page.Print(cmd, statusPageTable(page.Items))
		}),
	}
	cmd.Flags().StringVar(&monitorIDArg, "monitor", "", "Only pages that show this monitor (id or exact name)")
	cmdutil.AddPageFlags(cmd, &pf, defaultPageSize, maxItems)
	return cmd
}

func newStatusPageGetCmd(d deps) *cobra.Command {
	return &cobra.Command{
		Use:         "get <page>",
		Annotations: cmdutil.Reads(),
		Short:       "Show a status page and its settings",
		Example: `  norcube monitor status-pages get acme
  norcube monitor status-pages get acme -o json --query url`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			p, err := getStatusPage(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			return statusPageObject(p), nil
		}),
	}
}

// statusPageFlags are the settings create and update share.
type statusPageFlags struct {
	slug, description, timeZone string
	uptimeDays, historyDays     int
	bars, percent, response     bool
	indexing, autoPost          bool
}

func addStatusPageFlags(cmd *cobra.Command, f *statusPageFlags) {
	fl := cmd.Flags()
	fl.StringVar(&f.slug, "slug", "", "The page's public address: 3 to 40 lowercase letters, digits or hyphens")
	fl.StringVar(&f.description, "description", "", "Public text shown under the page's name")
	fl.StringVar(&f.timeZone, "time-zone", "", "IANA time zone the page shows times in, e.g. Europe/Prague")
	fl.IntVar(&f.uptimeDays, "uptime-days", 0, "Days the uptime bars cover: 30, 60 or 90")
	fl.IntVar(&f.historyDays, "history-days", 0, "Days resolved incidents stay on the page, 7 to 365")
	fl.BoolVar(&f.bars, "show-uptime-bars", false, "Show daily uptime bars (--show-uptime-bars=false hides them)")
	fl.BoolVar(&f.percent, "show-uptime-percent", false, "Show each component's uptime percentage (--show-uptime-percent=false hides it)")
	fl.BoolVar(&f.response, "show-response-time", false, "Show each component's average response time (--show-response-time=false hides it)")
	fl.BoolVar(&f.indexing, "search-indexing", false, "Let search engines index the page (--search-indexing=false asks them to leave it out)")
	fl.BoolVar(&f.autoPost, "auto-post", false, "Post confirmed critical monitor incidents on the page by itself and email subscribers (--auto-post=false turns it off)")
	cmdutil.FlagEnum(cmd, "uptime-days", statusPageUptimeDays...)
}

// checkStatusPageFlags refuses values the API would, before any request.
func checkStatusPageFlags(cmd *cobra.Command, f statusPageFlags) error {
	if cmd.Flags().Changed("uptime-days") {
		if err := oneOf("--uptime-days", strconv.Itoa(f.uptimeDays), statusPageUptimeDays); err != nil {
			return err
		}
	}
	if cmd.Flags().Changed("history-days") && (f.historyDays < statusPageHistoryDaysMin || f.historyDays > statusPageHistoryDaysMax) {
		return clierr.Usage(fmt.Errorf("--history-days %d: from %d to %d", f.historyDays, statusPageHistoryDaysMin, statusPageHistoryDaysMax))
	}
	return nil
}

func newStatusPageCreateCmd(d deps) *cobra.Command {
	var f statusPageFlags
	cmd := &cobra.Command{
		Use:         "create <name>",
		Annotations: cmdutil.Writes(),
		Short:       "Create a status page, unpublished",
		Long: `Creates a page that nobody can see yet: set what it shows with "layout
set", check it with "preview-link", then "publish" it. Every setting is
required: the page's public face is your choice, so nothing is filled in
for you. A boolean takes =false to turn it off. The one exception is
--auto-post, which is off unless you ask for it.

--auto-post lets the page post by itself: a monitor incident that stays
critical for 3 minutes is posted in fixed wording and emailed to subscribers,
and the post resolves when the incident does. A warning is never posted.

Your organization's limit sets how many pages it can have, published or not.`,
		Example: `  norcube monitor status-pages create "Acme status" --slug acme --time-zone Europe/Prague --uptime-days 90 --history-days 90 --show-uptime-bars --show-uptime-percent --show-response-time=false --search-indexing`,
		Args:    cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			if err := checkStatusPageFlags(cmd, f); err != nil {
				return nil, err
			}
			body := monitor.DtoStatusPageCreate{
				Name: strings.TrimSpace(args[0]),
				Slug: f.slug,
				Settings: monitor.DtoStatusPageSettings{
					TimeZone:          f.timeZone,
					UptimeDays:        monitor.DtoStatusPageSettingsUptimeDays(f.uptimeDays),
					HistoryDays:       f.historyDays,
					ShowUptimeBars:    f.bars,
					ShowUptimePercent: f.percent,
					ShowResponseTime:  f.response,
					SearchIndexing:    f.indexing,
				},
			}
			if cmd.Flags().Changed("auto-post") {
				body.Settings.AutoPost = &f.autoPost
			}
			if f.description != "" {
				body.Description = &f.description
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.CreateStatusPageWithResponse(cmd.Context(), body)
			if err != nil {
				return nil, err
			}
			if res.JSON201 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			p := *res.JSON201
			return output.Done{
				Message: fmt.Sprintf("Created status page %s (%s), unpublished. Next: norcube monitor status-pages layout set %s --file layout.json", p.Slug, p.Id, p.Slug),
				Value:   p,
				Fields:  []output.Field{{Value: p.Id.String()}, {Value: p.Slug}},
			}, nil
		}),
	}
	addStatusPageFlags(cmd, &f)
	for _, name := range []string{"slug", "time-zone", "uptime-days", "history-days", "show-uptime-bars", "show-uptime-percent", "show-response-time", "search-indexing"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

func newStatusPageUpdateCmd(d deps) *cobra.Command {
	var (
		f    statusPageFlags
		name string
	)
	cmd := &cobra.Command{
		Use:         "update <page>",
		Annotations: cmdutil.Writes(),
		Short:       "Change a status page's address, texts or settings",
		Long: `Changes only what you pass. On a published page the change is public within
a minute.

--slug moves the page to a new address. The old address of a published
page redirects to the new one for 30 days, and no other organization can
take it meanwhile; unpublishing or deleting the page gives it up.

--auto-post=false stops new automatic posts; one already open still ends by
itself when its monitors recover.`,
		Example: `  norcube monitor status-pages update acme --name "Acme Inc. status"
  norcube monitor status-pages update acme --slug acme-inc
  norcube monitor status-pages update acme --uptime-days 30 --show-response-time
  norcube monitor status-pages update acme --auto-post`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			if err := checkStatusPageFlags(cmd, f); err != nil {
				return nil, err
			}
			body, changed := statusPageUpdateBody(cmd, f, name)
			if !changed {
				return nil, clierr.Usage(fmt.Errorf("nothing to change: pass --name, --slug, --description or a setting flag (see --help)"))
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			id, err := statusPageID(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			res, err := c.API.UpdateStatusPageWithResponse(cmd.Context(), id, body)
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			p := *res.JSON200
			msg := fmt.Sprintf("Updated status page %s (%s).", p.Slug, p.Id)
			if p.Published {
				msg += " Public within a minute at " + p.Url + "."
			}
			return output.Done{Message: msg, Value: p, Fields: []output.Field{{Value: p.Id.String()}, {Value: p.Slug}}}, nil
		}),
	}
	cmd.Flags().StringVar(&name, "name", "", "New public name")
	addStatusPageFlags(cmd, &f)
	return cmd
}

// statusPageUpdateBody builds the patch from the flags given, and says
// whether any was.
func statusPageUpdateBody(cmd *cobra.Command, f statusPageFlags, name string) (monitor.DtoStatusPageUpdate, bool) {
	set := cmd.Flags().Changed
	var body monitor.DtoStatusPageUpdate
	changed := false
	if set("name") {
		n := strings.TrimSpace(name)
		body.Name, changed = &n, true
	}
	if set("slug") {
		body.Slug, changed = &f.slug, true
	}
	if set("description") {
		body.Description, changed = &f.description, true
	}
	var s monitor.DtoStatusPageSettingsUpdate
	settings := false
	if set("time-zone") {
		s.TimeZone, settings = &f.timeZone, true
	}
	if set("uptime-days") {
		v := monitor.DtoStatusPageSettingsUpdateUptimeDays(f.uptimeDays)
		s.UptimeDays, settings = &v, true
	}
	if set("history-days") {
		s.HistoryDays, settings = &f.historyDays, true
	}
	if set("show-uptime-bars") {
		s.ShowUptimeBars, settings = &f.bars, true
	}
	if set("show-uptime-percent") {
		s.ShowUptimePercent, settings = &f.percent, true
	}
	if set("show-response-time") {
		s.ShowResponseTime, settings = &f.response, true
	}
	if set("search-indexing") {
		s.SearchIndexing, settings = &f.indexing, true
	}
	if set("auto-post") {
		s.AutoPost, settings = &f.autoPost, true
	}
	if settings {
		body.Settings, changed = &s, true
	}
	return body, changed
}

func newStatusPageDeleteCmd(d deps) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:         "delete <page>",
		Annotations: cmdutil.Writes(),
		Short:       "Delete a status page with its layout and posted incidents",
		Long: `Deletes the page and everything on it, takes it offline and gives up its
old addresses. The monitors it shows are not touched.`,
		Example: `  norcube monitor status-pages delete acme
  norcube monitor status-pages delete 6e0f4b1a-2c3d-4e5f-8a9b-0c1d2e3f4a5b --yes`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			id, err := statusPageID(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			if ok, err := cmdutil.Confirm(fmt.Sprintf("Delete status page %s with its layout and posted incidents? It goes offline", args[0]), yes); err != nil || !ok {
				return nil, cmdutil.Cancelled(cmd, err)
			}
			res, err := c.API.DeleteStatusPageWithResponse(cmd.Context(), id)
			if err != nil {
				return nil, err
			}
			if res.StatusCode() != http.StatusNoContent {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			return output.Done{
				Message: fmt.Sprintf("Deleted status page %s.", args[0]),
				Value:   deleted{ID: id.String(), Deleted: true},
				Fields:  []output.Field{{Value: id.String()}},
			}, nil
		}),
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Don't ask for confirmation")
	return cmd
}

func newStatusPagePublishCmd(d deps) *cobra.Command {
	return &cobra.Command{
		Use:         "publish <page>",
		Annotations: cmdutil.Writes(),
		Short:       "Make a status page public within a minute",
		Long: `Publishes the page at its address: it is public within a minute, along
with its page.json and its Atom feed. It needs at least one component.
Publishing a published page changes nothing.`,
		Example: `  norcube monitor status-pages publish acme`,
		Args:    cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			id, err := statusPageID(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			res, err := c.API.PublishStatusPageWithResponse(cmd.Context(), id)
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			p := *res.JSON200
			return output.Done{
				Message: fmt.Sprintf("Published status page %s: public within a minute at %s.", p.Slug, p.Url),
				Value:   p,
				Fields:  []output.Field{{Value: p.Id.String()}, {Value: p.Url}},
			}, nil
		}),
	}
}

func newStatusPageUnpublishCmd(d deps) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:         "unpublish <page>",
		Annotations: cmdutil.Writes(),
		Short:       "Take a status page offline, keeping everything on it",
		Long: `The page's address stops answering and its old addresses are given up, so
another organization can take them. Its layout and posted incidents stay,
and preview links keep working. Unpublishing an unpublished page changes
nothing.`,
		Example: `  norcube monitor status-pages unpublish acme
  norcube monitor status-pages unpublish acme --yes`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			id, err := statusPageID(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			if ok, err := cmdutil.Confirm(fmt.Sprintf("Unpublish status page %s? Its address stops answering and its old addresses are given up", args[0]), yes); err != nil || !ok {
				return nil, cmdutil.Cancelled(cmd, err)
			}
			res, err := c.API.UnpublishStatusPageWithResponse(cmd.Context(), id)
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			p := *res.JSON200
			return output.Done{
				Message: fmt.Sprintf("Unpublished status page %s (%s).", p.Slug, p.Id),
				Value:   p,
				Fields:  []output.Field{{Value: p.Id.String()}, {Value: p.Slug}},
			}, nil
		}),
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Don't ask for confirmation")
	return cmd
}

func newStatusPagePreviewLinkCmd(d deps) *cobra.Command {
	return &cobra.Command{
		Use:         "preview-link <page>",
		Annotations: cmdutil.Reads(),
		Short:       "Print a link to the rendered page that works for 15 minutes",
		Long: `Prints a signed link to the page as the public would see it, published or
not. Anyone with the link can open it until it expires, 15 minutes later,
so treat it like a draft you are sharing. It changes nothing, but a CI
token needs the monitor.statuspages scope to get one.`,
		Example: `  norcube monitor status-pages preview-link acme
  norcube monitor status-pages preview-link acme -o text | cut -f1`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			id, err := statusPageID(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			res, err := c.API.CreateStatusPagePreviewLinkWithResponse(cmd.Context(), id)
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			l := *res.JSON200
			return output.Object{Value: l, Fields: []output.Field{
				{Name: "URL", Value: l.Url},
				{Name: "Expires", Value: output.WhenString(l.ExpiresAt)},
			}}, nil
		}),
	}
}

// ---------- layout ----------

func newStatusPageLayoutCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "layout",
		Short: "Show or replace what a status page shows",
		Long: `A layout lists a page's components: first those outside any group, then the
groups, each in the order given. A component follows a monitor (kind
monitor, with the aspects that count: availability, certificate, domain)
or has a status you set (kind manual, with manualStatus operational,
degraded_performance, partial_outage or major_outage). The name is what
the public sees; the monitor's name and target never are.`,
	}
	cmd.AddCommand(newStatusPageLayoutGetCmd(d), newStatusPageLayoutSetCmd(d))
	return cmd
}

func newStatusPageLayoutGetCmd(d deps) *cobra.Command {
	return &cobra.Command{
		Use:         "get <page>",
		Annotations: cmdutil.Reads(),
		Short:       "Show a status page's layout",
		Long:        `-o json prints the document "layout set" takes, so you can edit and send it back.`,
		Example: `  norcube monitor status-pages layout get acme
  norcube monitor status-pages layout get acme -o json > layout.json`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			id, err := statusPageID(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			res, err := c.API.GetStatusPageLayoutWithResponse(cmd.Context(), id)
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			return statusPageLayoutObject(*res.JSON200), nil
		}),
	}
}

func newStatusPageLayoutSetCmd(d deps) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:         "set <page>",
		Annotations: cmdutil.Writes(),
		Short:       "Replace a status page's layout from a JSON file; public within a minute on a published page",
		Long: `Replaces the layout as a whole and prints the saved one. On a published page
the result is public within a minute, and adding a monitor publishes its
status, so you must be able to read every monitor you name.

The file has "components" and "groups" (use [] for none), in the shape
"layout get -o json" prints. Keep a row's id to keep the row, leave it out
for a new one. A row left out of the file is removed, and with it its place
on any posted incident.`,
		Example: `  norcube monitor status-pages layout set acme --file layout.json
  norcube monitor status-pages layout get acme -o json | jq '.groups[0].collapsed = true' | norcube monitor status-pages layout set acme --file -`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			var body monitor.DtoStatusPageLayout
			if err := readJSON(cmd, file, "the layout", &body); err != nil {
				return nil, err
			}
			if err := checkLayout(body); err != nil {
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
			res, err := c.API.SetStatusPageLayoutWithResponse(cmd.Context(), p.Id, body)
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			if p.Published {
				fmt.Fprintf(cmdutil.Stderr(cmd), "Saved the layout of %s: public within a minute at %s.\n", p.Slug, p.Url)
			} else {
				fmt.Fprintf(cmdutil.Stderr(cmd), "Saved the layout of %s. Check it with: norcube monitor status-pages preview-link %s\n", p.Slug, p.Slug)
			}
			return statusPageLayoutObject(*res.JSON200), nil
		}),
	}
	cmd.Flags().StringVar(&file, "file", "", "Read the layout as JSON from this file (- for standard input)")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

// checkLayout refuses a document that lacks a list. The layout is replaced
// as a whole, so a list left out by mistake must not read as "none".
func checkLayout(l monitor.DtoStatusPageLayout) error {
	if l.Components == nil || l.Groups == nil {
		return clierr.Usage(fmt.Errorf(`the layout needs both "components" and "groups" (use [] for none)`))
	}
	for i, g := range l.Groups {
		if g.Components == nil {
			return clierr.Usage(fmt.Errorf(`group %d (%s) needs "components" (use [] for none)`, i, g.Name))
		}
	}
	return nil
}
