package backup

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/snapdb"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

// terminalJob reports whether a backup or restore job's status is final.
func terminalJob(s snapdb.ConstantsBackupJobStatus) bool {
	switch s {
	case snapdb.JobStatusSuccess, snapdb.JobStatusFailed, snapdb.JobStatusPartial, snapdb.JobStatusCanceled:
		return true
	}
	return false
}

// waitFor polls get until status reports a final state, showing the
// current status on stderr for a person, and returns the last value seen.
func waitFor[T any](cmd *cobra.Command, d deps, timeout time.Duration, get func(ctx context.Context) (T, error), status func(T) snapdb.ConstantsBackupJobStatus) (T, error) {
	var last T
	stderr := cmdutil.Stderr(cmd)
	human := cmdutil.Human(cmd) && output.IsInteractive(stderr)
	err := cmdutil.Poll(cmd.Context(), cmdutil.PollOptions{Timeout: timeout, Sleep: d.sleep},
		func(ctx context.Context) (bool, error) {
			v, err := get(ctx)
			if err != nil {
				return false, err
			}
			last = v
			if human {
				fmt.Fprintf(stderr, "\r\033[K%s…", status(v))
			}
			return terminalJob(status(v)), nil
		})
	if human {
		fmt.Fprint(stderr, "\r\033[K")
	}
	return last, err
}

func getBackupJob(ctx context.Context, c *snapdbContext, datasourceID, jobID string) (snapdb.DtoBackupJob, error) {
	res, err := c.API.GetBackupJobWithResponse(ctx, datasourceID, jobID)
	if err != nil {
		return snapdb.DtoBackupJob{}, err
	}
	if res.JSON200 == nil {
		return snapdb.DtoBackupJob{}, apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON404, res.JSON500)
	}
	return *res.JSON200, nil
}

func jobObject(j snapdb.DtoBackupJob) output.Object {
	return output.Object{Value: j, Fields: []output.Field{
		{Name: "Job ID", Value: j.JobId},
		{Name: "Datasource", Value: j.DatasourceName + " (" + j.DatasourceId + ")"},
		{Name: "Policy", Value: j.PolicyName},
		{Name: "Status", Value: string(j.JobStatus)},
		{Name: "Trigger", Value: string(j.JobTrigger)},
		{Name: "Started", Value: formatTimestamp(j.JobStartedAt)},
		{Name: "Finished", Value: formatTimestamp(j.JobFinishedAt)},
		{Name: "Duration", Value: formatDurationMs(j.JobDurationMs)},
		{Name: "Size", Value: formatBytes(j.JobBytesWritten)},
		{Name: "Restore test", Value: formatVerify(j)},
		{Name: "Error", Value: cmdutil.Deref(j.JobErrorSummary)},
	}}
}

// backupFailed is the --wait error for a backup that didn't succeed.
func backupFailed(j snapdb.DtoBackupJob) error {
	msg := fmt.Sprintf("backup job %s ended %s", j.JobId, j.JobStatus)
	if s := strings.TrimSpace(cmdutil.Deref(j.JobErrorSummary)); s != "" {
		msg += ": " + s
	}
	return cmdutil.JobFailed(msg, j)
}

func newJobGetCmd(d deps) *cobra.Command {
	var datasourceID string
	cmd := &cobra.Command{
		Use:   "get <job-id>",
		Short: "Show one backup job",
		Example: `  norcube backup jobs get 6a1b2c3d-… --datasource 9f8e7d6c-…
  norcube backup jobs get 6a1b2c3d-… -d 9f8e7d6c-… -o json --query jobStatus`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			j, err := getBackupJob(cmd.Context(), c, datasourceID, strings.TrimSpace(args[0]))
			if err != nil {
				return nil, err
			}
			return jobObject(j), nil
		}),
	}
	cmd.Flags().StringVarP(&datasourceID, "datasource", "d", "", "Data source the job belongs to (id)")
	_ = cmd.MarkFlagRequired("datasource")
	return cmd
}

func newJobRunCmd(d deps) *cobra.Command {
	var (
		datasourceID, policyID string
		wait                   cmdutil.WaitFlags
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Back up a data source now",
		Long: `Queues a backup of the data source right away, outside its schedule. It
uses the attached policy you name with --policy, or the data source's
highest-priority enabled policy.

The backup counts towards your Backup usage like a scheduled one. With
--wait the command waits until the job finishes and exits 1 (code
job_failed) when it didn't succeed, so a script can stop on a failed
backup.`,
		Example: `  norcube backup jobs run --datasource 9f8e7d6c-…
  norcube backup jobs run -d 9f8e7d6c-… --policy 1a2b3c4d-… --wait
  norcube backup jobs run -d 9f8e7d6c-… -o json --query jobId`,
		Args: cobra.NoArgs,
		RunE: cmdutil.Run(func(cmd *cobra.Command, _ []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			body := snapdb.RunBackupNowJSONRequestBody{}
			if policyID != "" {
				body.PolicyId = &policyID
			}
			res, err := c.API.RunBackupNowWithResponse(cmd.Context(), datasourceID, body)
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON402, res.JSON404, res.JSON500)
			}
			jobID := cmdutil.Deref(res.JSON200.JobId)
			if !wait.Wait {
				if cmdutil.Human(cmd) {
					fmt.Fprintf(cmdutil.Stderr(cmd), "Follow it with: norcube backup jobs get %s --datasource %s\n", jobID, datasourceID)
				}
				return output.Done{
					Message: fmt.Sprintf("Backup queued (job %s).", jobID),
					Value:   res.JSON200,
					Fields:  []output.Field{{Value: jobID}},
				}, nil
			}
			j, err := waitFor(cmd, d, wait.Timeout,
				func(ctx context.Context) (snapdb.DtoBackupJob, error) {
					return getBackupJob(ctx, c, datasourceID, jobID)
				},
				func(j snapdb.DtoBackupJob) snapdb.ConstantsBackupJobStatus { return j.JobStatus })
			if err != nil {
				return nil, err
			}
			if j.JobStatus != snapdb.JobStatusSuccess {
				return nil, backupFailed(j)
			}
			return jobObject(j), nil
		}),
	}
	cmd.Flags().StringVarP(&datasourceID, "datasource", "d", "", "Data source to back up (id)")
	cmd.Flags().StringVar(&policyID, "policy", "", "Attached backup policy to use (id; default: the highest-priority enabled one)")
	cmdutil.AddWaitFlags(cmd, &wait, "backup", 2*time.Hour)
	_ = cmd.MarkFlagRequired("datasource")
	return cmd
}
