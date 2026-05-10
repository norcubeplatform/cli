package snapdb

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/snapdb"
	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/output"
)

// maxBackupItems caps how many jobs the CLI will accumulate in one
// invocation when --all-pages is set, so a user with millions of historical
// jobs doesn't accidentally OOM their terminal. Adjust with --max-items.
const maxBackupItems = 10000

func newBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "backup",
		Aliases: []string{"backups", "b"},
		Short:   "Inspect SnapDB backup jobs",
		Long: `List backup jobs across one or all data sources.

Note: backup detail (get) and download endpoints are not yet implemented in
the SnapDB backend — those subcommands will be added once the routes ship.`,
	}
	cmd.AddCommand(newBackupListCmd())
	return cmd
}

func newBackupListCmd() *cobra.Command {
	var datasourceID string
	var all bool
	var limit int
	var cursor string
	var allPages bool
	var maxItems int

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List backup jobs (per data source, or --all to fan out across every data source)",
		Long: `Lists backup jobs. The SnapDB backend paginates with an opaque "next"
cursor; by default this command fetches one page and prints a hint to
stderr if there are more results. Use --all-pages to follow the cursor
until exhausted, with a safety cap of --max-items.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newSnapdbContext(cmd)
			if err != nil {
				return err
			}
			if datasourceID == "" && !all {
				return fmt.Errorf("specify --datasource <id> or --all")
			}
			if cursor != "" && all {
				return fmt.Errorf("--cursor is meaningless with --all (each data source has its own cursor); use --all-pages instead")
			}

			// Resolve which data sources to query.
			var ids []string
			if all {
				lr, err := c.client.ListWithResponse(cmd.Context(), &snapdb.ListParams{})
				if err != nil {
					return err
				}
				if lr.JSON200 == nil {
					return apiError(lr.HTTPResponse, lr.Body, lr.JSON400, lr.JSON500)
				}
				for _, ds := range lr.JSON200.List {
					ids = append(ids, ds.Id)
				}
				if len(ids) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "No data sources in this organization.")
					return nil
				}
			} else {
				ids = []string{datasourceID}
			}

			// Per-datasource: fetch page(s).
			var jobs []snapdb.DtoBackupJob
			// pendingHints lets us print a "more available" hint at the end
			// for each data source that has a non-empty next cursor.
			type pendingHint struct {
				datasourceID string
				nextCursor   string
			}
			var hints []pendingHint
			cap := maxItems
			if cap <= 0 {
				cap = maxBackupItems
			}
			truncated := false

		PERSOURCE:
			for _, id := range ids {
				var nextCursor *string
				if cursor != "" {
					nextCursor = &cursor
				}
				for {
					params := &snapdb.ListBackupJobsParams{}
					if limit > 0 {
						params.Limit = &limit
					}
					if nextCursor != nil {
						params.Cursor = nextCursor
					}
					res, err := c.client.ListBackupJobsWithResponse(cmd.Context(), id, params)
					if err != nil {
						return fmt.Errorf("list backups for %s: %w", id, err)
					}
					if res.JSON200 == nil {
						return apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON500)
					}
					jobs = append(jobs, res.JSON200.List...)

					next := res.JSON200.Cursors.Next
					hasNext := next != nil && *next != ""

					if !allPages {
						if hasNext {
							hints = append(hints, pendingHint{id, *next})
						}
						break
					}
					if !hasNext {
						break
					}
					if len(jobs) >= cap {
						truncated = true
						hints = append(hints, pendingHint{id, *next})
						break PERSOURCE
					}
					nextCursor = next
				}
			}

			flags := clictx.Get(cmd)
			err = output.PrintPaged(cmd.OutOrStdout(), c.output, flags.NoPager, output.Table[snapdb.DtoBackupJob]{
				Headers:   []string{"DATASOURCE", "STATUS", "TRIGGER", "STARTED", "DURATION", "SIZE", "JOB_ID"},
				MaxWidths: []int{32, 0, 0, 0, 0, 0, 0},
				Style:     output.Style{StatusColumn: 1}, // STATUS
				Rows: func(j snapdb.DtoBackupJob) []string {
					return []string{
						j.DatasourceName,
						string(j.JobStatus),
						string(j.JobTrigger),
						formatTimestamp(j.JobStartedAt),
						formatDurationMs(j.JobDurationMs),
						formatBytes(j.JobBytesWritten),
						j.JobId,
					}
				},
				Items: jobs,
			})
			if err != nil {
				return err
			}

			// Hints go to stderr so piping the table to jq / grep stays
			// clean. Skip entirely when not in table mode (scripts using
			// JSON/YAML don't want chatty stderr) or when stderr isn't a
			// terminal (the user is capturing it for automation).
			stderr := cmd.ErrOrStderr()
			if c.output == output.FormatTable && output.IsInteractive(stderr) {
				if truncated {
					fmt.Fprintf(stderr,
						"\nStopped at --max-items=%d. Re-run with --max-items 0 (no cap) or a higher value to fetch the rest.\n",
						cap)
				}
				switch len(hints) {
				case 0:
				case 1:
					fmt.Fprintf(stderr,
						"\nMore results available. Re-run with --cursor %s --datasource %s, or --all-pages to follow.\n",
						hints[0].nextCursor, hints[0].datasourceID)
				default:
					fmt.Fprintf(stderr,
						"\n%d data sources have more results. Re-run with --all-pages to follow every cursor.\n",
						len(hints))
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&datasourceID, "datasource", "", "Data source ID to list backups for")
	cmd.Flags().BoolVar(&all, "all", false, "Fan out across every data source in the active organization")
	cmd.Flags().IntVar(&limit, "limit", 0, "Max items per page (0 = backend default)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "Continue from a previous page's `next` cursor")
	cmd.Flags().BoolVar(&allPages, "all-pages", false, "Follow `next` cursors and return every page (may be many requests)")
	cmd.Flags().IntVar(&maxItems, "max-items", 0, "Safety cap for --all-pages (0 = use built-in default, currently 10000)")
	cmd.MarkFlagsMutuallyExclusive("datasource", "all")
	return cmd
}
