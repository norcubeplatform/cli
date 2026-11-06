package monitor

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/monitor"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

// bulkMax is how many monitors one bulk request takes; longer lists are
// sent in batches.
const bulkMax = 100

func newMonitorBulkCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bulk",
		Short: "Pause, resume or delete many monitors at once",
		Long: `Applies one action to many monitors: the ones you name (ids or exact
names, as arguments or one per line with --file), or every monitor the
list filters match (--type, --status, --search). Each monitor is handled as
its own request would be, so one refusal doesn't stop the others; the
result lists every monitor with ok or its problem code. The command exits
1 when any monitor failed.

For planned work, "monitor maintenance create" is usually better than
pausing: checks keep running, the time stays out of uptime, status pages say
so, and it ends by itself.`,
	}
	for _, a := range []struct{ action, short, example string }{
		{"pause", "Pause many monitors", `  # Pause everything behind api.example.com; for planned work, see "monitor maintenance"
  norcube monitor monitors bulk pause --search api.example.com --yes`},
		{"resume", "Resume many monitors", `  norcube monitor monitors bulk resume --status paused --search api.example.com --yes`},
		{"delete", "Delete many monitors and their history", `  norcube monitor monitors bulk delete 7d1c2f7e-6a1f-4f5e-9b8a-1f7c2b3d4e5f "Old landing page"
  norcube monitor monitors bulk delete --file old-monitors.txt --yes`},
	} {
		cmd.AddCommand(newBulkActionCmd(d, a.action, a.short, a.example))
	}
	return cmd
}

func newBulkActionCmd(d deps, action, short, example string) *cobra.Command {
	var (
		yes                 bool
		file                string
		typ, status, search string
	)
	cmd := &cobra.Command{
		Use:         action + " [<monitor>...]",
		Annotations: cmdutil.Writes(),
		Short:       short,
		Example:     example,
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			byFilter := typ != "" || status != "" || search != ""
			if byFilter && (len(args) > 0 || file != "") {
				return nil, clierr.Usage(fmt.Errorf("name monitors or select them with --type, --status and --search, not both"))
			}
			if typ != "" {
				if err := oneOf("--type", typ, monitorTypes); err != nil {
					return nil, err
				}
			}
			if status != "" {
				if err := oneOf("--status", status, monitorStatuses); err != nil {
					return nil, err
				}
			}
			var refs []string
			if !byFilter {
				var err error
				refs, err = cmdutil.ReadItems(cmdutil.ItemSource{Args: args, File: file, Stdin: cmd.InOrStdin(), What: "monitors"})
				if err != nil {
					return nil, err
				}
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			ids, err := bulkTargets(cmd.Context(), c, refs, typ, status, search)
			if err != nil {
				return nil, err
			}
			if len(ids) == 0 {
				return nil, &clierr.Error{Code: clierr.CodeNotFound, Message: "no monitor matches the filters"}
			}
			if ok, err := cmdutil.Confirm(fmt.Sprintf("%s %d monitor(s)?", capitalize(action), len(ids)), yes); err != nil || !ok {
				return nil, cmdutil.Cancelled(cmd, err)
			}
			result := monitor.DtoMonitorBulkResult{Action: monitor.DtoMonitorBulkResultAction(action), Items: []monitor.DtoMonitorBulkItem{}}
			for start := 0; start < len(ids); start += bulkMax {
				batch := ids[start:min(start+bulkMax, len(ids))]
				res, err := c.API.ApplyMonitorBulkActionWithResponse(cmd.Context(), monitor.DtoMonitorBulkRequest{
					Action: monitor.DtoMonitorBulkRequestAction(action), MonitorIds: batch,
				})
				if err != nil {
					return nil, err
				}
				if res.JSON200 == nil {
					return nil, apiError(res.HTTPResponse, res.Body)
				}
				result.Items = append(result.Items, res.JSON200.Items...)
				result.Succeeded += res.JSON200.Succeeded
				result.Failed += res.JSON200.Failed
			}
			table := output.Table[monitor.DtoMonitorBulkItem]{
				Headers: []string{"MONITOR", "RESULT", "DETAIL"},
				Style:   output.Style{StatusColumn: 1},
				Rows: func(it monitor.DtoMonitorBulkItem) []string {
					r := "ok"
					if !it.Ok {
						r = cmdutil.DerefOr(it.Code, "failed")
					}
					return []string{it.MonitorId.String(), r, strOr(it.Detail)}
				},
				Items: result.Items,
			}
			if result.Failed > 0 {
				if err := cmdutil.Print(cmd, table); err != nil {
					return nil, err
				}
				return nil, &clierr.Error{Code: clierr.CodeError, Details: result,
					Message: fmt.Sprintf("%s: %d of %d monitors failed", action, result.Failed, len(ids))}
			}
			if cmdutil.Human(cmd) {
				fmt.Fprintf(cmdutil.Stderr(cmd), "%sd %d monitor(s).\n", capitalize(strings.TrimSuffix(action, "e")), result.Succeeded)
			}
			return table, nil
		}),
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Don't ask for confirmation")
	cmd.Flags().StringVarP(&file, "file", "f", "", "Read monitors (ids or names) from this file, one per line (- for standard input)")
	cmd.Flags().StringVar(&typ, "type", "", "Select every monitor of this type: http, ssl or domain")
	cmd.Flags().StringVar(&status, "status", "", "Select every monitor with this overall status")
	cmd.Flags().StringVar(&search, "search", "", "Select every monitor whose name or target contains this text")
	cmdutil.FlagEnum(cmd, "type", monitorTypes...)
	cmdutil.FlagEnum(cmd, "status", monitorStatuses...)
	return cmd
}

// bulkTargets resolves the named monitors, or lists the ones the filters
// match (every page), into ids without duplicates.
func bulkTargets(ctx context.Context, c *monitorContext, refs []string, typ, status, search string) ([]string, error) {
	seen := map[string]bool{}
	var ids []string
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if refs != nil {
		for _, r := range refs {
			id, err := monitorRef(ctx, c, r)
			if err != nil {
				return nil, err
			}
			add(id.String())
		}
		return ids, nil
	}
	limit := maxPageSize
	p := monitor.ListMonitorsParams{Limit: &limit}
	if typ != "" {
		t := monitor.ListMonitorsParamsType(typ)
		p.Type = &t
	}
	if status != "" {
		s := monitor.ListMonitorsParamsStatus(status)
		p.Status = &s
	}
	if search != "" {
		p.Q = &search
	}
	for len(ids) < maxItems {
		res, err := c.API.ListMonitorsWithResponse(ctx, &p)
		if err != nil {
			return nil, err
		}
		if res.JSON200 == nil {
			return nil, apiError(res.HTTPResponse, res.Body)
		}
		for _, m := range res.JSON200.List {
			add(m.Id.String())
		}
		next := nextCursor(res.JSON200.Cursors)
		if next == "" {
			break
		}
		p.Cursor = &next
	}
	return ids, nil
}
