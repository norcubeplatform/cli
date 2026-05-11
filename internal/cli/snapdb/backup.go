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

// defaultPageLimit is sent as ?limit= when --limit isn't explicitly passed.
// The backend SQL takes LIMIT straight from the request, so omitting the
// param causes a `LIMIT 0` query that returns nothing — see the discussion
// in apps/snapdb/internal/handler/backuphandler/handler.go (listJobsDefaultLimit).
// We send our own default so the CLI works even against backend builds
// that haven't picked up the clamp yet.
const defaultPageLimit = 50

func newBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "backup",
		Aliases: []string{"backups", "b"},
		Short:   "Inspect SnapDB backup jobs",
		Long: `List backup jobs in the active organization. Server-side ordering is
created_at descending (newest first), so the most recent backups appear at
the top regardless of which data source they came from.

Note: backup detail (get) and download endpoints are not yet implemented in
the SnapDB backend — those subcommands will be added once the routes ship.`,
	}
	cmd.AddCommand(newBackupListCmd())
	return cmd
}

func newBackupListCmd() *cobra.Command {
	var datasourceIDs []string
	var limit int
	var cursor string
	var allPages bool
	var maxItems int

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List backup jobs across the active organization, newest first",
		Long: `Lists backup jobs sorted by created_at descending. By default lists
across every data source in the active organization — pass --datasource
one or more times to filter to a subset. The SnapDB backend paginates
with an opaque "next" cursor; by default this command fetches one page
and prints a hint to stderr if there are more results. Use --all-pages
to follow the cursor until exhausted, with a safety cap of --max-items.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newSnapdbContext(cmd)
			if err != nil {
				return err
			}

			cap := maxItems
			if cap <= 0 {
				cap = maxBackupItems
			}

			// Initialise non-nil so `-o json` on an empty result prints "[]"
			// rather than "null" — saves every downstream `jq` invocation
			// from having to handle both shapes.
			jobs := []snapdb.DtoBackupJob{}
			nextCursor := cursor
			truncated := false

			for {
				params := &snapdb.GetBackupsParams{}
				effectiveLimit := limit
				if effectiveLimit <= 0 {
					effectiveLimit = defaultPageLimit
				}
				params.Limit = &effectiveLimit
				if nextCursor != "" {
					params.Cursor = &nextCursor
				}
				if len(datasourceIDs) > 0 {
					ids := append([]string(nil), datasourceIDs...)
					params.DatasourceIDs = &ids
				}

				res, err := c.client.GetBackupsWithResponse(cmd.Context(), params)
				if err != nil {
					return err
				}
				if res.JSON200 == nil {
					return apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON500)
				}
				jobs = append(jobs, res.JSON200.List...)

				next := res.JSON200.Cursors.Next
				if !allPages || next == nil || *next == "" {
					if next != nil && *next != "" {
						nextCursor = *next
					} else {
						nextCursor = ""
					}
					break
				}
				if len(jobs) >= cap {
					truncated = true
					nextCursor = *next
					break
				}
				nextCursor = *next
			}

			flags := clictx.Get(cmd)
			err = output.PrintPaged(cmd.OutOrStdout(), c.output, flags.NoPager, output.Table[snapdb.DtoBackupJob]{
				Headers:   []string{"DATASOURCE", "STATUS", "TRIGGER", "STARTED", "DURATION", "SIZE", "JOB_ID"},
				MaxWidths: []int{32, 0, 0, 0, 0, 0, 0},
				Style:     output.Style{StatusColumn: 1},
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
			// clean. Skip in JSON/YAML modes and when stderr isn't a TTY.
			stderr := cmd.ErrOrStderr()
			if c.output == output.FormatTable && output.IsInteractive(stderr) {
				if truncated {
					fmt.Fprintf(stderr,
						"\nStopped at --max-items=%d. Re-run with --max-items 0 (no cap) or a higher value to fetch the rest.\n",
						cap)
				} else if !allPages && nextCursor != "" {
					fmt.Fprintf(stderr,
						"\nMore results available. Re-run with --cursor %s, or --all-pages to follow until exhausted.\n",
						nextCursor)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&datasourceIDs, "datasource", nil, "Filter to one or more data source IDs (repeatable; default: every data source in the active org)")
	cmd.Flags().IntVar(&limit, "limit", 0, "Max items per page (0 = backend default)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "Continue from a previous page's `next` cursor")
	cmd.Flags().BoolVar(&allPages, "all-pages", false, "Follow `next` cursors and return every page (may be many requests)")
	cmd.Flags().IntVar(&maxItems, "max-items", 0, "Safety cap for --all-pages (0 = use built-in default, currently 10000)")
	return cmd
}
