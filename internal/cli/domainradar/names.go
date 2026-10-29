package domainradar

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/domainradar"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

// nameSizes are the --size values, smallest first.
var nameSizes = []string{
	string(domainradar.NameGenSizeSmall),
	string(domainradar.NameGenSizeMedium),
	string(domainradar.NameGenSizeLarge),
}

// nameTerminal are the statuses a name generation never leaves.
var nameTerminal = map[string]bool{"completed": true, "failed": true}

// maxNameCheck caps how many names one `names check` sends; the bulk
// check limit applies to names times TLDs.
const maxNameCheck = maxBulkDomains

func newNamesCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "names",
		Aliases: []string{"name-generator"},
		Short:   "Generate name ideas and check which are free as domains",
		Long: `The name generator turns a description of your product into thousands of
name ideas. Generating is billed per job; checking names for free
domains runs a bulk check, billed like any other.`,
	}
	cmd.AddCommand(
		newNameGenerateCmd(d),
		newNameListCmd(d),
		newNameGetCmd(d),
		newNameResultsCmd(d),
		newNameCheckCmd(d),
		newNameDeleteCmd(d),
	)
	return cmd
}

func nameJobObject(j domainradar.DtoNameGeneratorJob) output.Object {
	return output.Object{Value: j, Fields: []output.Field{
		{Name: "ID", Value: cmdutil.Deref(j.Id)},
		{Name: "Status", Value: string(cmdutil.Deref(j.Status))},
		{Name: "Description", Value: cmdutil.Deref(j.Description)},
		{Name: "Category", Value: cmdutil.Deref(j.Category)},
		{Name: "Style", Value: cmdutil.Deref(j.Style)},
		{Name: "Size", Value: string(cmdutil.Deref(j.Size))},
		{Name: "Max length", Value: maxLen(j.MaxLength)},
		{Name: "Names", Value: fmt.Sprint(cmdutil.Deref(j.TotalNames))},
		{Name: "Error", Value: cmdutil.Deref(j.Error)},
		{Name: "Created", Value: output.WhenString(cmdutil.Deref(j.CreatedAt))},
	}}
}

func maxLen(p *int) string {
	if n := cmdutil.Deref(p); n > 0 {
		return fmt.Sprint(n)
	}
	return ""
}

func newNameGenerateCmd(d deps) *cobra.Command {
	var (
		category, style, size string
		maxLength             int
		wait                  cmdutil.WaitFlags
	)
	cmd := &cobra.Command{
		Use:   "generate <description>",
		Short: "Generate name ideas from a description",
		Long: `Starts a name generation job from a description of what you are naming.
It runs on the server; with --wait the command waits for it to finish.
Read the names with "names results", and check them as domains with
"names check".

Each job is billed (see https://norcube.com/domainradar/#pricing).`,
		Example: `  norcube domainradar names generate "invoicing for freelancers" --size small --wait
  norcube domainradar names generate "a privacy-first analytics tool" --style short --max-length 8`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			description := strings.TrimSpace(args[0])
			if description == "" {
				return nil, clierr.Usage(fmt.Errorf("the description must not be empty"))
			}
			if !slices.Contains(nameSizes, size) {
				return nil, clierr.Usage(fmt.Errorf("--size must be one of %s", strings.Join(nameSizes, ", ")))
			}
			if maxLength < 0 {
				return nil, clierr.Usage(fmt.Errorf("--max-length must not be negative"))
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			sz := domainradar.ConstantsNameGeneratorSize(size)
			body := domainradar.CreateNameJobJSONRequestBody{Description: &description, Size: &sz}
			if category != "" {
				body.Category = &category
			}
			if style != "" {
				body.Style = &style
			}
			if maxLength > 0 {
				body.MaxLength = &maxLength
			}
			res, err := c.API.CreateNameJobWithResponse(cmd.Context(), body)
			if err != nil {
				return nil, err
			}
			if res.JSON202 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			job := *res.JSON202
			if wait.Wait {
				job, err = waitForNames(cmd, d, c, cmdutil.Deref(job.Id), wait.Timeout)
				if err != nil {
					return nil, err
				}
			} else if cmdutil.Human(cmd) {
				fmt.Fprintf(cmdutil.Stderr(cmd), "Generating names. Read them with: norcube domainradar names results %s\n", cmdutil.Deref(job.Id))
			}
			return nameJobObject(job), nil
		}),
	}
	cmd.Flags().StringVar(&category, "category", "", "Industry or category, to steer the ideas (e.g. fintech)")
	cmd.Flags().StringVar(&style, "style", "", "Naming style (e.g. short, playful, compound)")
	cmd.Flags().StringVar(&size, "size", string(domainradar.NameGenSizeSmall), "How many ideas: small (about 5-10k), medium (30-50k) or large (100-200k)")
	cmd.Flags().IntVar(&maxLength, "max-length", 0, "Longest name to keep (0 = no limit)")
	_ = cmd.RegisterFlagCompletionFunc("size", cobra.FixedCompletions(nameSizes, cobra.ShellCompDirectiveNoFileComp))
	cmdutil.AddWaitFlags(cmd, &wait, "name generation", 15*time.Minute)
	return cmd
}

func waitForNames(cmd *cobra.Command, d deps, c *drContext, id string, timeout time.Duration) (domainradar.DtoNameGeneratorJob, error) {
	var last domainradar.DtoNameGeneratorJob
	err := cmdutil.Poll(cmd.Context(), cmdutil.PollOptions{Interval: 2 * time.Second, Timeout: timeout, Sleep: d.sleep}, func(ctx context.Context) (bool, error) {
		job, err := getNameJob(ctx, c, id)
		if err != nil {
			return false, err
		}
		last = job
		return nameTerminal[string(cmdutil.Deref(job.Status))], nil
	})
	if err != nil {
		return last, err
	}
	if status := string(cmdutil.Deref(last.Status)); status != "completed" {
		msg := fmt.Sprintf("name generation %s ended %s", id, status)
		if e := strings.TrimSpace(cmdutil.Deref(last.Error)); e != "" {
			msg += ": " + e
		}
		return last, cmdutil.JobFailed(msg, last)
	}
	return last, nil
}

func getNameJob(ctx context.Context, c *drContext, id string) (domainradar.DtoNameGeneratorJob, error) {
	res, err := c.API.GetNameJobWithResponse(ctx, id)
	if err != nil {
		return domainradar.DtoNameGeneratorJob{}, err
	}
	if res.JSON200 == nil {
		return domainradar.DtoNameGeneratorJob{}, apiError(res.HTTPResponse, res.Body)
	}
	return *res.JSON200, nil
}

func newNameListCmd(d deps) *cobra.Command {
	var pages cmdutil.PageFlags
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List name generation jobs, newest first",
		Example: `  norcube domainradar names list`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := d.client(cmd)
			if err != nil {
				return err
			}
			page, err := cmdutil.Paginate(cmd.Context(), pages, func(ctx context.Context, cursor string, size int) ([]domainradar.DtoNameGeneratorJob, string, error) {
				params := &domainradar.ListNameJobsParams{Limit: &size}
				if cursor != "" {
					params.Cursor = &cursor
				}
				res, err := c.API.ListNameJobsWithResponse(ctx, params)
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
			return page.Print(cmd, output.Table[domainradar.DtoNameGeneratorJob]{
				Headers:   []string{"ID", "STATUS", "NAMES", "DESCRIPTION", "CREATED"},
				MaxWidths: []int{0, 0, 0, 48, 0},
				Style:     output.Style{StatusColumn: 1},
				Rows: func(j domainradar.DtoNameGeneratorJob) []string {
					return []string{
						cmdutil.Deref(j.Id),
						string(cmdutil.Deref(j.Status)),
						fmt.Sprint(cmdutil.Deref(j.TotalNames)),
						cmdutil.Deref(j.Description),
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

func newNameGetCmd(d deps) *cobra.Command {
	return &cobra.Command{
		Use:     "get <job-id>",
		Short:   "Show a name generation job",
		Example: `  norcube domainradar names get 2c9d…`,
		Args:    cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			job, err := getNameJob(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			return nameJobObject(job), nil
		}),
	}
}

// nameSorts are the --sort values of `names results`.
var nameSorts = []string{string(domainradar.Name), string(domainradar.Length)}

func newNameResultsCmd(d deps) *cobra.Command {
	var (
		search          string
		minLen, maxLen  int
		sortBy, sortDir string
		pages           cmdutil.PageFlags
	)
	cmd := &cobra.Command{
		Use:   "results <job-id>",
		Short: "List the names a job generated",
		Example: `  norcube domainradar names results 2c9d… --max-length 6 --sort length
  norcube domainradar names results 2c9d… --all-pages -o text --query "[].name" > names.txt`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if sortBy != "" && !slices.Contains(nameSorts, sortBy) {
				return clierr.Usage(fmt.Errorf("--sort must be name or length"))
			}
			if sortDir != "asc" && sortDir != "desc" {
				return clierr.Usage(fmt.Errorf("--order must be asc or desc"))
			}
			if minLen < 0 || maxLen < 0 || (maxLen > 0 && minLen > maxLen) {
				return clierr.Usage(fmt.Errorf("--min-length and --max-length must not be negative, min not above max"))
			}
			c, err := d.client(cmd)
			if err != nil {
				return err
			}
			page, err := cmdutil.Paginate(cmd.Context(), pages, func(ctx context.Context, cursor string, size int) ([]domainradar.DtoGeneratedName, string, error) {
				params := &domainradar.ListNameJobNamesParams{Limit: &size}
				if search != "" {
					params.Search = &search
				}
				if minLen > 0 {
					params.MinLength = &minLen
				}
				if maxLen > 0 {
					params.MaxLength = &maxLen
				}
				if sortBy != "" {
					s := domainradar.ListNameJobNamesParamsSortBy(sortBy)
					o := domainradar.ListNameJobNamesParamsSortDir(sortDir)
					params.SortBy, params.SortDir = &s, &o
				}
				if cursor != "" {
					params.Cursor = &cursor
				}
				res, err := c.API.ListNameJobNamesWithResponse(ctx, args[0], params)
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
			return page.Print(cmd, output.Table[domainradar.DtoGeneratedName]{
				Headers: []string{"NAME", "LENGTH"},
				Rows: func(n domainradar.DtoGeneratedName) []string {
					return []string{cmdutil.Deref(n.Name), fmt.Sprint(cmdutil.Deref(n.Length))}
				},
				Items: page.Items,
			})
		},
	}
	cmd.Flags().StringVar(&search, "search", "", "Only names containing this text")
	cmd.Flags().IntVar(&minLen, "min-length", 0, "Only names at least this long")
	cmd.Flags().IntVar(&maxLen, "max-length", 0, "Only names at most this long")
	cmd.Flags().StringVar(&sortBy, "sort", "", "Sort by name or length")
	cmd.Flags().StringVar(&sortDir, "order", "asc", "Sort order with --sort: asc or desc")
	_ = cmd.RegisterFlagCompletionFunc("sort", cobra.FixedCompletions(nameSorts, cobra.ShellCompDirectiveNoFileComp))
	cmdutil.AddPageFlags(cmd, &pages, 200, 200_000)
	return cmd
}

func newNameCheckCmd(d deps) *cobra.Command {
	var (
		flags checkFlags
		tlds  []string
		file  string
		wait  cmdutil.WaitFlags
	)
	cmd := &cobra.Command{
		Use:   "check <job-id> [name...]",
		Short: "Check generated names as domains in the TLDs you choose",
		Long: `Starts a bulk check of names from a name generation job, each in every
TLD given with --tld (example: 20 names and --tld com,io check 40 domains).
Pick the names as arguments or with --file (- for standard input), for
example from "names results ... -o text".

The result is an ordinary bulk check: follow it with "bulk watch" or
read it with "bulk results".

` + metered,
		Example: `  norcube domainradar names check 2c9d… nexo zentra --tld com,io
  norcube domainradar names results 2c9d… --max-length 6 --all-pages -o text --query "[].name" |
    norcube domainradar names check 2c9d… --file - --tld com --wait`,
		Args: cobra.MinimumNArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			mode, maxAge, err := flags.validate()
			if err != nil {
				return nil, err
			}
			if len(tlds) == 0 {
				return nil, clierr.Usage(fmt.Errorf("pass at least one --tld"))
			}
			names, err := cmdutil.ReadItems(cmdutil.ItemSource{
				Args: args[1:], File: file, Stdin: cmd.InOrStdin(), Max: maxNameCheck, What: "names",
			})
			if err != nil {
				return nil, err
			}
			if len(names)*len(tlds) > maxBulkDomains {
				return nil, clierr.Usage(fmt.Errorf("%d names in %d TLDs make %d domains, at most %d per check: pick fewer", len(names), len(tlds), len(names)*len(tlds), maxBulkDomains))
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			tl := resultFilters{tlds: tlds}.tldParam()
			res, err := c.API.CheckNameJobNamesWithResponse(cmd.Context(), args[0], domainradar.CheckNameJobNamesJSONRequestBody{
				Names: &names, Tlds: tl, Mode: mode, MaxAge: maxAge,
			})
			if err != nil {
				return nil, err
			}
			if res.JSON202 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			job := *res.JSON202
			if wait.Wait {
				job, err = waitForBulk(cmd, d, c, cmdutil.Deref(job.Id), wait.Timeout)
				if err != nil {
					return nil, err
				}
			} else if cmdutil.Human(cmd) {
				fmt.Fprintf(cmdutil.Stderr(cmd), "Started a bulk check of %d domains. Follow it with: norcube domainradar bulk watch %s\n",
					len(names)*len(*tl), cmdutil.Deref(job.Id))
			}
			return jobObject(job), nil
		}),
	}
	addCheckFlags(cmd, &flags)
	cmd.Flags().StringSliceVar(&tlds, "tld", nil, "TLDs to check every name in (repeat or comma-separate: --tld com,io)")
	cmd.Flags().StringVarP(&file, "file", "f", "", "Read names from this file, one per line (- for standard input)")
	cmdutil.AddWaitFlags(cmd, &wait, "bulk check", time.Hour)
	return cmd
}

func newNameDeleteCmd(d deps) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <job-id>",
		Short:   "Delete a name generation job and its names",
		Example: `  norcube domainradar names delete 2c9d… --yes`,
		Args:    cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			if ok, err := cmdutil.Confirm(fmt.Sprintf("Delete name generation job %s and its names? This can't be undone", args[0]), yes); err != nil || !ok {
				return nil, cmdutil.Cancelled(cmd, err)
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.DeleteNameJobWithResponse(cmd.Context(), args[0])
			if err != nil {
				return nil, err
			}
			if res.StatusCode() != http.StatusNoContent {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			return output.Done{
				Message: fmt.Sprintf("Deleted name generation job %s.", args[0]),
				Value:   map[string]any{"id": args[0], "deleted": true},
				Fields:  []output.Field{{Value: args[0]}},
			}, nil
		}),
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Don't ask for confirmation")
	return cmd
}
