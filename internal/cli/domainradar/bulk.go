package domainradar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/domainradar"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
	"github.com/norcubeplatform/cli/internal/sse"
)

// maxBulkDomains is the API's per-request limit for a bulk check.
const maxBulkDomains = 100_000

// bulkTerminal are the statuses a bulk job never leaves.
var bulkTerminal = map[string]bool{"completed": true, "cancelled": true, "failed": true}

func newBulkCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bulk",
		Short: "Run and manage bulk checks of up to 100,000 domains",
		Long: `A bulk check runs on the server: start it, close the terminal, and read
the results later, here or in the dashboard.`,
	}
	cmd.AddCommand(
		newBulkCreateCmd(d),
		newBulkListCmd(d),
		newBulkGetCmd(d),
		newBulkResultsCmd(d),
		newBulkWatchCmd(d),
		newBulkExportCmd(d),
		newBulkCancelCmd(d),
		newBulkDeleteCmd(d),
	)
	return cmd
}

func newBulkCreateCmd(d deps) *cobra.Command {
	var (
		flags checkFlags
		file  string
		wait  cmdutil.WaitFlags
	)
	cmd := &cobra.Command{
		Use:   "create [domain...]",
		Short: "Start a bulk check",
		Long: `Starts a bulk check of up to 100,000 domains on the server and prints the
job. Domains come from the arguments, a file (--file) or standard input
(--file - or a single -), one per line; blank lines, "#" comments and
duplicates are skipped.

With --wait the command waits for the job to finish and prints it again,
and exits 1 (job_failed) if the job failed or was cancelled; "bulk watch"
shows the results as they arrive instead.

` + metered,
		Example: `  norcube domainradar bulk create --file names.txt --mode bloom
  norcube domainradar bulk create acme.io acme.dev acme.app --wait
  generate-names | norcube domainradar bulk create - -o json --query id`,
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			mode, maxAge, err := flags.validate()
			if err != nil {
				return nil, err
			}
			domains, err := cmdutil.ReadItems(cmdutil.ItemSource{
				Args: args, File: file, Stdin: cmd.InOrStdin(), Max: maxBulkDomains, What: "domains",
			})
			if err != nil {
				return nil, err
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.CreateBulkCheckWithResponse(cmd.Context(), domainradar.CreateBulkCheckJSONRequestBody{
				Domains: &domains, Mode: mode, MaxAge: maxAge,
			})
			if err != nil {
				return nil, err
			}
			if res.JSON202 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			job := *res.JSON202
			if !wait.Wait {
				if cmdutil.Human(cmd) {
					fmt.Fprintf(cmdutil.Stderr(cmd), "Started a bulk check of %d domains. Follow it with: norcube domainradar bulk watch %s\n",
						len(domains), cmdutil.Deref(job.Id))
				}
				return jobObject(job), nil
			}
			done, err := waitForBulk(cmd, d, c, cmdutil.Deref(job.Id), wait.Timeout)
			if err != nil {
				return nil, err
			}
			return jobObject(done), nil
		}),
	}
	addCheckFlags(cmd, &flags)
	cmd.Flags().StringVarP(&file, "file", "f", "", "Read domains from this file, one per line (- for standard input)")
	cmdutil.AddWaitFlags(cmd, &wait, "bulk check", time.Hour)
	return cmd
}

// waitForBulk polls a job until it reaches a terminal status, showing the
// progress on stderr for a person.
func waitForBulk(cmd *cobra.Command, d deps, c *drContext, id string, timeout time.Duration) (domainradar.DtoBulkCheckJob, error) {
	var last domainradar.DtoBulkCheckJob
	stderr := cmdutil.Stderr(cmd)
	human := cmdutil.Human(cmd) && output.IsInteractive(stderr)
	err := cmdutil.Poll(cmd.Context(), cmdutil.PollOptions{Timeout: timeout, Sleep: d.sleep}, func(ctx context.Context) (bool, error) {
		job, err := getBulkJob(ctx, c, id)
		if err != nil {
			return false, err
		}
		last = job
		if human {
			fmt.Fprintf(stderr, "\r\033[K%s %s", cmdutil.Deref(job.Status),
				progress(cmdutil.Deref(job.CompletedDomains), cmdutil.Deref(job.TotalDomains)))
		}
		return bulkTerminal[cmdutil.Deref(job.Status)], nil
	})
	if human {
		fmt.Fprint(stderr, "\r\033[K")
	}
	if err != nil {
		return last, err
	}
	if status := cmdutil.Deref(last.Status); status != "completed" {
		return last, cmdutil.JobFailed(fmt.Sprintf("bulk check %s ended %s", id, status), last)
	}
	return last, nil
}

func getBulkJob(ctx context.Context, c *drContext, id string) (domainradar.DtoBulkCheckJob, error) {
	res, err := c.API.GetBulkCheckWithResponse(ctx, id)
	if err != nil {
		return domainradar.DtoBulkCheckJob{}, err
	}
	if res.JSON200 == nil {
		return domainradar.DtoBulkCheckJob{}, apiError(res.HTTPResponse, res.Body)
	}
	return *res.JSON200, nil
}

func jobObject(j domainradar.DtoBulkCheckJob) output.Object {
	return output.Object{Value: j, Fields: []output.Field{
		{Name: "ID", Value: cmdutil.Deref(j.Id)},
		{Name: "Status", Value: cmdutil.Deref(j.Status)},
		{Name: "Mode", Value: string(cmdutil.Deref(j.Mode))},
		{Name: "Progress", Value: progress(cmdutil.Deref(j.CompletedDomains), cmdutil.Deref(j.TotalDomains))},
		{Name: "TLDs", Value: joinList(j.Tlds)},
		{Name: "Created", Value: output.WhenString(cmdutil.Deref(j.CreatedAt))},
	}}
}

func newBulkListCmd(d deps) *cobra.Command {
	var pages cmdutil.PageFlags
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List bulk checks, newest first",
		Long:  "Lists the organization's bulk checks, including ones started in the dashboard.",
		Example: `  norcube domainradar bulk list
  norcube domainradar bulk list -o json --query "[?status=='running'].id"`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := d.client(cmd)
			if err != nil {
				return err
			}
			page, err := cmdutil.Paginate(cmd.Context(), pages, func(ctx context.Context, cursor string, size int) ([]domainradar.DtoBulkCheckJob, string, error) {
				params := &domainradar.ListBulkChecksParams{Limit: &size}
				if cursor != "" {
					params.Cursor = &cursor
				}
				res, err := c.API.ListBulkChecksWithResponse(ctx, params)
				if err != nil {
					return nil, "", err
				}
				if res.JSON200 == nil {
					return nil, "", apiError(res.HTTPResponse, res.Body)
				}
				return res.JSON200.List, nextCursor(res.JSON200.Cursors), nil
			})
			if err != nil {
				return err
			}
			return page.Print(cmd, output.Table[domainradar.DtoBulkCheckJob]{
				Headers: []string{"ID", "STATUS", "MODE", "PROGRESS", "CREATED"},
				Style:   output.Style{StatusColumn: 1},
				Rows: func(j domainradar.DtoBulkCheckJob) []string {
					return []string{
						cmdutil.Deref(j.Id),
						cmdutil.Deref(j.Status),
						string(cmdutil.Deref(j.Mode)),
						progress(cmdutil.Deref(j.CompletedDomains), cmdutil.Deref(j.TotalDomains)),
						output.WhenString(cmdutil.Deref(j.CreatedAt)),
					}
				},
				Items: page.Items,
			})
		},
	}
	cmdutil.AddPageFlags(cmd, &pages, defaultPageSize, maxItems)
	return cmd
}

func newBulkGetCmd(d deps) *cobra.Command {
	return &cobra.Command{
		Use:   "get <job-id>",
		Short: "Show a bulk check and its progress",
		Example: `  norcube domainradar bulk get 4f1c…
  norcube domainradar bulk get 4f1c… -o json --query status`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			job, err := getBulkJob(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			return jobObject(job), nil
		}),
	}
}

// resultFilters are the filters results and export share.
type resultFilters struct {
	available  bool
	registered bool
	search     string
	tlds       []string
}

func addResultFilters(cmd *cobra.Command, f *resultFilters) {
	cmd.Flags().BoolVar(&f.available, "available", false, "Only domains that are available")
	cmd.Flags().BoolVar(&f.registered, "registered", false, "Only domains that are registered")
	cmd.Flags().StringVar(&f.search, "search", "", "Only domains containing this text")
	cmd.Flags().StringSliceVar(&f.tlds, "tld", nil, "Only these TLDs (repeat or comma-separate: --tld com,io)")
	cmd.MarkFlagsMutuallyExclusive("available", "registered")
}

// availability returns the API's available filter for the flags.
func (f resultFilters) availability() *[]bool {
	switch {
	case f.available:
		return &[]bool{true}
	case f.registered:
		return &[]bool{false}
	}
	return nil
}

func (f resultFilters) searchParam() *string {
	if f.search == "" {
		return nil
	}
	return &f.search
}

func (f resultFilters) tldParam() *[]string {
	if len(f.tlds) == 0 {
		return nil
	}
	tlds := make([]string, len(f.tlds))
	for i, t := range f.tlds {
		tlds[i] = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(t)), ".")
	}
	return &tlds
}

// fetchResults returns one page of a bulk job's results.
func fetchResults(ctx context.Context, c *drContext, id string, f resultFilters, cursor string, size int) ([]domainradar.DtoDomainCheck, string, error) {
	params := &domainradar.ListBulkCheckResultsParams{
		Limit: &size, Available: f.availability(), Search: f.searchParam(), Tld: f.tldParam(),
	}
	if cursor != "" {
		params.Cursor = &cursor
	}
	res, err := c.API.ListBulkCheckResultsWithResponse(ctx, id, params)
	if err != nil {
		return nil, "", err
	}
	if res.JSON200 == nil {
		return nil, "", apiError(res.HTTPResponse, res.Body)
	}
	return res.JSON200.List, nextCursor(res.JSON200.Cursors), nil
}

func newBulkResultsCmd(d deps) *cobra.Command {
	var (
		filters resultFilters
		pages   cmdutil.PageFlags
	)
	cmd := &cobra.Command{
		Use:   "results <job-id>",
		Short: "List the results of a bulk check",
		Long: `Lists a bulk check's results, also while it runs. Filter by availability,
text or TLD; follow --cursor or pass --all-pages for more.`,
		Example: `  norcube domainradar bulk results 4f1c… --available
  norcube domainradar bulk results 4f1c… --tld com,io --all-pages -o json --query "[].domain"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := d.client(cmd)
			if err != nil {
				return err
			}
			page, err := cmdutil.Paginate(cmd.Context(), pages, func(ctx context.Context, cursor string, size int) ([]domainradar.DtoDomainCheck, string, error) {
				return fetchResults(ctx, c, args[0], filters, cursor, size)
			})
			if err != nil {
				return err
			}
			return page.Print(cmd, checkTable(page.Items))
		},
	}
	addResultFilters(cmd, &filters)
	cmdutil.AddPageFlags(cmd, &pages, 200, 200_000)
	return cmd
}

func newBulkWatchCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "watch <job-id>",
		Short: "Show the results of a running bulk check as they arrive",
		Long: `Streams a bulk check's results as the server produces them, then ends
when the job finishes. A dropped connection reconnects on its own;
results already shown aren't shown again. Ctrl-C stops watching, not the
job.

With -o json or -o yaml the output is the final list of results; with
-o text or a table, every result is printed as it arrives.`,
		Example: `  norcube domainradar bulk watch 4f1c…
  norcube domainradar bulk watch 4f1c… -o text | grep available`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := d.client(cmd)
			if err != nil {
				return err
			}
			api, err := d.streaming(c)
			if err != nil {
				return err
			}
			return watchBulk(cmd, d, api, args[0])
		},
	}
	return cmd
}

// watchBulk follows a job's event stream. Each data event is one domain
// result; the "done" event ends it. A reconnect makes the server replay
// what it already sent, so results are de-duplicated by domain.
func watchBulk(cmd *cobra.Command, d deps, api *domainradar.ClientWithResponses, id string) error {
	stream := cmdutil.Format(cmd) == output.FormatTable || cmdutil.Format(cmd) == output.FormatText
	seen := map[string]bool{}
	var all []domainradar.DtoDomainCheck
	printed := false
	err := sse.Follow(cmd.Context(), sse.FollowOptions{Sleep: d.sleep},
		func(ctx context.Context) (*http.Response, error) { return api.StreamBulkCheck(ctx, id) },
		func(ev sse.Event) (bool, error) {
			if ev.Name == "done" {
				return true, nil
			}
			var r domainradar.DtoDomainCheck
			if err := json.Unmarshal([]byte(ev.Data), &r); err != nil {
				return false, sse.HandlerError(fmt.Errorf("unexpected event from the stream: %w", err))
			}
			key := cmdutil.Deref(r.Domain)
			if seen[key] {
				return false, nil
			}
			seen[key] = true
			all = append(all, r)
			if !stream {
				return false, nil
			}
			// Without --query, each result goes out as it arrives (a
			// one-row table without its header after the first).
			if err := printStreamed(cmd, r, printed); err != nil {
				return false, sse.HandlerError(err)
			}
			printed = true
			return false, nil
		})
	if err != nil {
		var se *sse.StatusError
		if errors.As(err, &se) {
			body, _ := io.ReadAll(io.LimitReader(se.Response.Body, 64*1024))
			_ = se.Response.Body.Close()
			return apiError(se.Response, body)
		}
		return err
	}
	if !stream {
		if all == nil {
			all = []domainradar.DtoDomainCheck{}
		}
		return cmdutil.Print(cmd, checkTable(all))
	}
	if !printed && cmdutil.Human(cmd) {
		fmt.Fprintln(cmdutil.Stderr(cmd), "The bulk check has no results.")
	}
	return nil
}

// streamWidths are the column widths of a streamed results table
// (DOMAIN, AVAILABILITY, METHOD, EXPIRES, REGISTRAR, CACHE).
var streamWidths = []int{40, 12, 8, 10, 28, 6}

// printStreamed prints one streamed result: in a table the header goes
// out once, then every row; text output is one line per result.
func printStreamed(cmd *cobra.Command, r domainradar.DtoDomainCheck, headerDone bool) error {
	t := checkTable([]domainradar.DtoDomainCheck{r})
	// Fixed widths so the rows, printed one by one, line up; longer
	// values are cut with "…".
	t.MinWidths = streamWidths
	t.MaxWidths = streamWidths
	if headerDone && cmdutil.Format(cmd) == output.FormatTable {
		t.Headers = nil
	}
	return cmdutil.Print(cmd, t)
}

func newBulkExportCmd(d deps) *cobra.Command {
	var (
		filters resultFilters
		to      string
		format  string
	)
	cmd := &cobra.Command{
		Use:   "export <job-id>",
		Short: "Export the results of a bulk check to a CSV or JSON file",
		Long: `Writes every result of a bulk check (or those matching the filters) to a
file, or to standard output with --to -. CSV comes straight from the
server in one stream; JSON pages through the results.

The file must not exist yet. Without --to, it is named
bulk-check-<job-id>.<format> in the current directory.`,
		Example: `  norcube domainradar bulk export 4f1c…
  norcube domainradar bulk export 4f1c… --available --to free.csv
  norcube domainradar bulk export 4f1c… --format json --to - | jq length`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			if format != "csv" && format != "json" {
				return nil, clierr.Usage(fmt.Errorf("--format must be csv or json (got %q)", format))
			}
			id := args[0]
			dest := to
			if dest == "" {
				dest = fmt.Sprintf("bulk-check-%s.%s", id, format)
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			var src io.Reader
			if format == "csv" {
				body, err := exportCSV(cmd.Context(), d, c, id, filters)
				if err != nil {
					return nil, err
				}
				defer body.Close()
				src = body
			} else {
				all, err := exportJSON(cmd.Context(), c, id, filters)
				if err != nil {
					return nil, err
				}
				src = strings.NewReader(string(all) + "\n")
			}
			saved, err := cmdutil.SaveStream(cmd, src, dest, 0o644)
			if err != nil || saved == nil {
				return nil, err
			}
			return output.Done{
				Message: fmt.Sprintf("Exported the results of %s to %s.", id, saved.Path),
				Value:   saved,
				Fields:  []output.Field{{Value: saved.Path}, {Value: fmt.Sprint(saved.Bytes)}, {Value: saved.SHA256}},
			}, nil
		}),
	}
	addResultFilters(cmd, &filters)
	cmd.Flags().StringVar(&to, "to", "", "File to write (- for standard output; default bulk-check-<job-id>.<format>)")
	cmd.Flags().StringVar(&format, "format", "csv", "File format: csv or json")
	_ = cmd.RegisterFlagCompletionFunc("format", cobra.FixedCompletions([]string{"csv", "json"}, cobra.ShellCompDirectiveNoFileComp))
	return cmd
}

// exportCSV opens the server's CSV export as a stream.
func exportCSV(ctx context.Context, d deps, c *drContext, id string, f resultFilters) (io.ReadCloser, error) {
	api, err := d.streaming(c)
	if err != nil {
		return nil, err
	}
	resp, err := api.ExportBulkCheckResults(ctx, id, &domainradar.ExportBulkCheckResultsParams{
		Available: f.availability(), Search: f.searchParam(), Tld: f.tldParam(),
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		_ = resp.Body.Close()
		return nil, apiError(resp, body)
	}
	return resp.Body, nil
}

// exportJSON collects every result as a JSON array.
func exportJSON(ctx context.Context, c *drContext, id string, f resultFilters) ([]byte, error) {
	page, err := cmdutil.Paginate(ctx, cmdutil.PageFlags{AllPages: true, PageSize: 1000, MaxItems: maxBulkDomains},
		func(ctx context.Context, cursor string, size int) ([]domainradar.DtoDomainCheck, string, error) {
			return fetchResults(ctx, c, id, f, cursor, size)
		})
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(page.Items, "", "  ")
}

func newBulkCancelCmd(d deps) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "cancel <job-id>",
		Short: "Stop a running bulk check",
		Long: `Stops a bulk check. Results so far are kept (and billed); domains not
checked yet are skipped. A cancelled check can't be resumed.`,
		Example: `  norcube domainradar bulk cancel 4f1c…
  norcube domainradar bulk cancel 4f1c… --yes   # scripts`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			if ok, err := cmdutil.Confirm(fmt.Sprintf("Cancel bulk check %s? It can't be resumed", args[0]), yes); err != nil || !ok {
				return nil, cmdutil.Cancelled(cmd, err)
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.CancelBulkCheckWithResponse(cmd.Context(), args[0])
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			job := *res.JSON200
			return output.Done{
				Message: fmt.Sprintf("Cancelled bulk check %s at %s.", args[0], progress(cmdutil.Deref(job.CompletedDomains), cmdutil.Deref(job.TotalDomains))),
				Value:   job,
				Fields:  []output.Field{{Value: cmdutil.Deref(job.Id)}, {Value: cmdutil.Deref(job.Status)}},
			}, nil
		}),
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Don't ask for confirmation")
	return cmd
}

func newBulkDeleteCmd(d deps) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <job-id>",
		Short: "Delete a bulk check and its results",
		Long:  "Deletes a finished bulk check and all its results. This can't be undone.",
		Example: `  norcube domainradar bulk delete 4f1c…
  norcube domainradar bulk delete 4f1c… --yes`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			if ok, err := cmdutil.Confirm(fmt.Sprintf("Delete bulk check %s and its results? This can't be undone", args[0]), yes); err != nil || !ok {
				return nil, cmdutil.Cancelled(cmd, err)
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.DeleteBulkCheckWithResponse(cmd.Context(), args[0])
			if err != nil {
				return nil, err
			}
			if res.StatusCode() != http.StatusNoContent {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			return output.Done{
				Message: fmt.Sprintf("Deleted bulk check %s.", args[0]),
				Value:   map[string]any{"id": args[0], "deleted": true},
				Fields:  []output.Field{{Value: args[0]}},
			}, nil
		}),
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Don't ask for confirmation")
	return cmd
}

// errNotSet is a usage error for an update that changes nothing.
func errNotSet(what string) error {
	return clierr.Usage(fmt.Errorf("nothing to update: pass at least one of %s", what))
}
