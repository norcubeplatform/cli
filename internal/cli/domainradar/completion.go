package domainradar

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/domainradar"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/completion"
)

// Completion lookup keys (they also separate the cache entries).
const (
	completeBulkJobs = "domainradar-bulk-jobs"
	completeWatchers = "domainradar-watchers"
	completeNameJobs = "domainradar-name-jobs"
	completeSaved    = "domainradar-saved"
)

// registerCompletions offers bulk job ids, watcher ids, name job ids and
// saved domains to the commands that take them.
func registerCompletions(root *cobra.Command, d deps) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Flags().Lookup("watcher") != nil {
			_ = c.RegisterFlagCompletionFunc("watcher", completion.Flag(completeWatchers, watcherCandidates(d)))
		}
		parent := ""
		if c.HasParent() {
			parent = c.Parent().Name()
		}
		switch {
		case parent == "bulk" && c.Name() != "create" && c.Name() != "list":
			c.ValidArgsFunction = completion.Func(completeBulkJobs, 1, bulkCandidates(d))
		case parent == "watchers" && c.Name() != "create" && c.Name() != "list":
			c.ValidArgsFunction = completion.Func(completeWatchers, 1, watcherCandidates(d))
		case parent == "names" && c.Name() != "generate" && c.Name() != "list":
			c.ValidArgsFunction = completion.Func(completeNameJobs, 1, nameJobCandidates(d))
		case parent == "saved" && c.Name() == "remove":
			c.ValidArgsFunction = completion.Func(completeSaved, 1, savedCandidates(d))
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
}

func bulkCandidates(d deps) completion.Fetcher {
	return func(cmd *cobra.Command) ([]cobra.Completion, error) {
		c, err := d.client(cmd)
		if err != nil {
			return nil, err
		}
		limit := 50
		res, err := c.API.ListBulkChecksWithResponse(cmd.Context(), &domainradar.ListBulkChecksParams{Limit: &limit})
		if err != nil {
			return nil, err
		}
		if res.JSON200 == nil {
			return nil, fmt.Errorf("status %d", res.StatusCode())
		}
		out := make([]cobra.Completion, 0, len(res.JSON200.List))
		for _, j := range res.JSON200.List {
			out = append(out, fmt.Sprintf("%s\t%s, %d domains", cmdutil.Deref(j.Id), cmdutil.Deref(j.Status), cmdutil.Deref(j.TotalDomains)))
		}
		return out, nil
	}
}

func watcherCandidates(d deps) completion.Fetcher {
	return func(cmd *cobra.Command) ([]cobra.Completion, error) {
		c, err := d.client(cmd)
		if err != nil {
			return nil, err
		}
		res, err := c.API.ListBrandWatchersWithResponse(cmd.Context())
		if err != nil {
			return nil, err
		}
		if res.JSON200 == nil {
			return nil, fmt.Errorf("status %d", res.StatusCode())
		}
		out := make([]cobra.Completion, 0, len(*res.JSON200))
		for _, w := range *res.JSON200 {
			out = append(out, cmdutil.Deref(w.Id)+"\t"+cmdutil.Deref(w.Word))
		}
		return out, nil
	}
}

func nameJobCandidates(d deps) completion.Fetcher {
	return func(cmd *cobra.Command) ([]cobra.Completion, error) {
		c, err := d.client(cmd)
		if err != nil {
			return nil, err
		}
		limit := 50
		res, err := c.API.ListNameJobsWithResponse(cmd.Context(), &domainradar.ListNameJobsParams{Limit: &limit})
		if err != nil {
			return nil, err
		}
		if res.JSON200 == nil {
			return nil, fmt.Errorf("status %d", res.StatusCode())
		}
		out := make([]cobra.Completion, 0, len(res.JSON200.List))
		for _, j := range res.JSON200.List {
			out = append(out, cmdutil.Deref(j.Id)+"\t"+cmdutil.Deref(j.Description))
		}
		return out, nil
	}
}

func savedCandidates(d deps) completion.Fetcher {
	return func(cmd *cobra.Command) ([]cobra.Completion, error) {
		c, err := d.client(cmd)
		if err != nil {
			return nil, err
		}
		limit := 100
		res, err := c.API.ListSavedDomainsWithResponse(cmd.Context(), &domainradar.ListSavedDomainsParams{Limit: &limit})
		if err != nil {
			return nil, err
		}
		if res.JSON200 == nil {
			return nil, fmt.Errorf("status %d", res.StatusCode())
		}
		out := make([]cobra.Completion, 0, len(res.JSON200.List))
		for _, s := range res.JSON200.List {
			out = append(out, cmdutil.Deref(s.Domain))
		}
		return out, nil
	}
}
