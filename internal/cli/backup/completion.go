package backup

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/snapdb"
	"github.com/norcubeplatform/cli/internal/completion"
)

// Completion lookup keys (they also separate the cache entries).
const (
	completeDatasources = "backup-datasources"
	completeJobs        = "backup-jobs"
)

// registerCompletions offers data source ids for every --datasource flag
// and `datasources get|update|pause|resume`, and backup job ids for
// `jobs download|get` and `restore-tests run`.
func registerCompletions(c *cobra.Command, d deps) {
	if c.Flags().Lookup("datasource") != nil {
		_ = c.RegisterFlagCompletionFunc("datasource", completion.Flag(completeDatasources, d.datasourceCandidates))
	}
	if c.Flags().Lookup("backup") != nil {
		_ = c.RegisterFlagCompletionFunc("backup", completion.Flag(completeJobs, d.jobCandidates))
	}
	parent := ""
	if c.HasParent() {
		parent = c.Parent().Name()
	}
	switch {
	case parent == "datasources" && (c.Name() == "get" || c.Name() == "update" || c.Name() == "pause" || c.Name() == "resume"):
		c.ValidArgsFunction = completion.Func(completeDatasources, 1, d.datasourceCandidates)
	case c.Name() == "download" || (parent == "jobs" && c.Name() == "get") || (parent == "restore-tests" && c.Name() == "run"):
		c.ValidArgsFunction = completion.Func(completeJobs, 1, d.jobCandidates)
	}
	for _, sub := range c.Commands() {
		registerCompletions(sub, d)
	}
}

func (d deps) datasourceCandidates(cmd *cobra.Command) ([]cobra.Completion, error) {
	c, err := d.client(cmd)
	if err != nil {
		return nil, err
	}
	res, err := c.API.ListWithResponse(cmd.Context(), &snapdb.ListParams{})
	if err != nil {
		return nil, err
	}
	if res.JSON200 == nil {
		return nil, fmt.Errorf("status %d", res.StatusCode())
	}
	out := make([]cobra.Completion, 0, len(res.JSON200.List))
	for _, d := range res.JSON200.List {
		out = append(out, d.Id+"\t"+d.Name+" ("+d.Engine+")")
	}
	return out, nil
}

// jobCandidates offers the 50 newest backup jobs.
func (d deps) jobCandidates(cmd *cobra.Command) ([]cobra.Completion, error) {
	c, err := d.client(cmd)
	if err != nil {
		return nil, err
	}
	limit := 50
	res, err := c.API.GetBackupsWithResponse(cmd.Context(), &snapdb.GetBackupsParams{Limit: &limit})
	if err != nil {
		return nil, err
	}
	if res.JSON200 == nil {
		return nil, fmt.Errorf("status %d", res.StatusCode())
	}
	out := make([]cobra.Completion, 0, len(res.JSON200.List))
	for _, j := range res.JSON200.List {
		desc := j.DatasourceName + " " + string(j.JobStatus)
		if j.JobStartedAt != nil {
			desc += " " + *j.JobStartedAt
		}
		out = append(out, j.JobId+"\t"+desc)
	}
	return out, nil
}
