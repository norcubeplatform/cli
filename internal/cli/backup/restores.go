package backup

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/snapdb"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

// restoreModes are the ways a restore can recover a backup, as the API
// names them.
var restoreModes = []string{
	string(snapdb.RestoreModeNewDatabase),
	string(snapdb.RestoreModeNewServer),
	string(snapdb.RestoreModeOverwrite),
}

// Restore triggers the per-datasource list filters on.
const (
	restoreTriggerManual       = "manual"
	restoreTriggerVerification = "verification"
)

func newRestoresCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "restores",
		Aliases: []string{"restore"},
		Short:   "Restore backups into a database and follow the restores",
		Long: `A restore recovers one successful backup into a database: a new database
on a server you point at (new_database), the database named in a
connection string (new_server), or the data source's own database,
replacing its data (overwrite).

Restore tests (` + "`norcube backup restore-tests`" + `) are restores too, into a
throwaway database; ` + "`restores list --tests`" + ` lists them.`,
	}
	cmd.AddCommand(newRestoreListCmd(d), newRestoreGetCmd(d), newRestoreCreateCmd(d))
	return cmd
}

// restoreVerdict is the outcome column of a restore: its status, and for
// a restore test the validation verdict once it finished.
func restoreVerdict(r snapdb.DtoRestoreJob) string {
	if r.Status != snapdb.JobStatusSuccess || r.VerifyPassed == nil {
		return string(r.Status)
	}
	switch {
	case !*r.VerifyPassed:
		return "failed"
	case cmdutil.Deref(r.VerifyWarnings) != "":
		return "passed (warnings)"
	default:
		return "passed"
	}
}

func restoreTable(items []snapdb.DtoRestoreJob) output.Table[snapdb.DtoRestoreJob] {
	return output.Table[snapdb.DtoRestoreJob]{
		Headers:   []string{"ID", "STATUS", "MODE", "TARGET", "BACKUP", "STARTED", "DURATION", "RESTORED"},
		MaxWidths: []int{0, 0, 0, 40, 0, 0, 0, 0},
		Style:     output.Style{StatusColumn: 1},
		Rows: func(r snapdb.DtoRestoreJob) []string {
			target := cmdutil.Deref(r.TargetConnectionRef)
			if target == "" {
				target = cmdutil.Deref(r.TargetDatabase)
			}
			return []string{
				r.Id, restoreVerdict(r), string(r.Mode), target, cmdutil.Deref(r.SourceBackupId),
				formatTimestamp(r.StartedAt), formatDurationMs(r.DurationMs), formatBytes(cmdutil.Deref(r.BytesRestored)),
			}
		},
		Items: items,
	}
}

func restoreObject(r snapdb.DtoRestoreJob) output.Object {
	fields := []output.Field{
		{Name: "ID", Value: r.Id},
		{Name: "Datasource", Value: r.DataSourceName + " (" + r.DataSourceId + ")"},
		{Name: "Backup", Value: cmdutil.Deref(r.SourceBackupId)},
		{Name: "Mode", Value: string(r.Mode)},
		{Name: "Status", Value: restoreVerdict(r)},
		{Name: "Target", Value: cmdutil.Deref(r.TargetConnectionRef)},
		{Name: "Database", Value: cmdutil.Deref(r.TargetDatabase)},
		{Name: "Started", Value: formatTimestamp(r.StartedAt)},
		{Name: "Finished", Value: formatTimestamp(r.FinishedAt)},
		{Name: "Duration", Value: formatDurationMs(r.DurationMs)},
		{Name: "Restored", Value: formatBytes(cmdutil.Deref(r.BytesRestored))},
	}
	if r.VerifyLevel != nil {
		fields = append(fields, output.Field{Name: "Validation", Value: *r.VerifyLevel})
	}
	if w := cmdutil.Deref(r.VerifyWarnings); w != "" {
		fields = append(fields, output.Field{Name: "Warnings", Value: w})
	}
	if e := cmdutil.Deref(r.ErrorSummary); e != "" {
		fields = append(fields, output.Field{Name: "Error", Value: e})
	}
	return output.Object{Value: r, Fields: fields}
}

func getRestore(ctx context.Context, c *snapdbContext, id string) (snapdb.DtoRestoreJob, error) {
	res, err := c.API.GetRestoresIdWithResponse(ctx, id)
	if err != nil {
		return snapdb.DtoRestoreJob{}, err
	}
	if res.JSON200 == nil {
		return snapdb.DtoRestoreJob{}, apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON404, res.JSON500)
	}
	return *res.JSON200, nil
}

// waitForRestore polls a restore (or restore test) until it finishes.
func waitForRestore(cmd *cobra.Command, d deps, c *snapdbContext, id string, timeout time.Duration) (snapdb.DtoRestoreJob, error) {
	return waitFor(cmd, d, timeout,
		func(ctx context.Context) (snapdb.DtoRestoreJob, error) { return getRestore(ctx, c, id) },
		func(r snapdb.DtoRestoreJob) snapdb.ConstantsBackupJobStatus { return r.Status })
}

// restoreFailed is the --wait error for a restore, or a restore test, that
// didn't end with the data back (and, for a test, validated).
func restoreFailed(what string, r snapdb.DtoRestoreJob) error {
	msg := fmt.Sprintf("%s %s ended %s", what, r.Id, restoreVerdict(r))
	if s := strings.TrimSpace(cmdutil.Deref(r.ErrorSummary)); s != "" {
		msg += ": " + s
	}
	return cmdutil.JobFailed(msg, r)
}

// listRestores pages through a data source's restores of one trigger.
func listRestores(cmd *cobra.Command, c *snapdbContext, pages cmdutil.PageFlags, datasourceID, trigger string) (cmdutil.Page[snapdb.DtoRestoreJob], error) {
	return cmdutil.Paginate(cmd.Context(), pages, func(ctx context.Context, cursor string, size int) ([]snapdb.DtoRestoreJob, string, error) {
		params := &snapdb.GetDatasourcesDatasourceIdRestoresParams{Limit: &size, Trigger: &trigger}
		if cursor != "" {
			params.Cursor = &cursor
		}
		res, err := c.API.GetDatasourcesDatasourceIdRestoresWithResponse(ctx, datasourceID, params)
		if err != nil {
			return nil, "", err
		}
		if res.JSON200 == nil {
			return nil, "", apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON500)
		}
		return res.JSON200.List, cmdutil.Deref(res.JSON200.Cursors.Next), nil
	})
}

func newRestoreListCmd(d deps) *cobra.Command {
	var (
		datasourceID string
		tests        bool
		pages        cmdutil.PageFlags
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List a data source's restores, newest first",
		Example: `  norcube backup restores list --datasource 9f8e7d6c-…
  norcube backup restores list -d 9f8e7d6c-… --tests
  norcube backup restores list -d 9f8e7d6c-… -o json --query "[?status=='failed'].id"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cmdutil.CheckQuery(cmd); err != nil {
				return err
			}
			c, err := d.client(cmd)
			if err != nil {
				return err
			}
			trigger := restoreTriggerManual
			if tests {
				trigger = restoreTriggerVerification
			}
			page, err := listRestores(cmd, c, pages, datasourceID, trigger)
			if err != nil {
				return err
			}
			return page.Print(cmd, restoreTable(page.Items))
		},
	}
	cmd.Flags().StringVarP(&datasourceID, "datasource", "d", "", "Data source the restores belong to (id)")
	cmd.Flags().BoolVar(&tests, "tests", false, "List restore tests instead of restores")
	cmdutil.AddPageFlags(cmd, &pages, defaultPageSize, maxItems)
	_ = cmd.MarkFlagRequired("datasource")
	return cmd
}

func newRestoreGetCmd(d deps) *cobra.Command {
	return &cobra.Command{
		Use:   "get <restore-id>",
		Short: "Show one restore or restore test",
		Example: `  norcube backup restores get 4c5d6e7f-…
  norcube backup restores get 4c5d6e7f-… -o json --query status`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			r, err := getRestore(cmd.Context(), c, strings.TrimSpace(args[0]))
			if err != nil {
				return nil, err
			}
			return restoreObject(r), nil
		}),
	}
}

// restoreRequest is a validated `restores create` invocation.
type restoreRequest struct {
	datasourceID, backupID, mode string
	targetURIFile, targetDB      string
}

func (r restoreRequest) validate() error {
	if !slices.Contains(restoreModes, r.mode) {
		return clierr.Usage(fmt.Errorf("--mode must be one of %s", strings.Join(restoreModes, ", ")))
	}
	switch snapdb.ConstantsRestoreMode(r.mode) {
	case snapdb.RestoreModeOverwrite:
		if r.targetURIFile != "" || r.targetDB != "" {
			return clierr.Usage(errors.New("--mode overwrite restores into the data source's own database: drop --target-uri-file and --target-database"))
		}
	case snapdb.RestoreModeNewDatabase:
		if r.targetURIFile == "" || r.targetDB == "" {
			return clierr.Usage(errors.New("--mode new_database needs --target-uri-file (a server where you may create databases) and --target-database (the database to create)"))
		}
	case snapdb.RestoreModeNewServer:
		if r.targetURIFile == "" {
			return clierr.Usage(errors.New("--mode new_server needs --target-uri-file (the connection string of the database to restore into)"))
		}
		if r.targetDB != "" {
			return clierr.Usage(errors.New("--mode new_server restores into the database named in the connection string: drop --target-database"))
		}
	}
	return nil
}

// question is the confirmation a person answers before the restore runs.
func (r restoreRequest) question() string {
	switch snapdb.ConstantsRestoreMode(r.mode) {
	case snapdb.RestoreModeOverwrite:
		return fmt.Sprintf("Overwrite the database of data source %s with backup %s? Its current data is replaced", r.datasourceID, r.backupID)
	case snapdb.RestoreModeNewDatabase:
		return fmt.Sprintf("Restore backup %s into a new database %q on the target server?", r.backupID, r.targetDB)
	default:
		return fmt.Sprintf("Restore backup %s into the database in the connection string? Data already there may be replaced", r.backupID)
	}
}

func newRestoreCreateCmd(d deps) *cobra.Command {
	var (
		req  restoreRequest
		yes  bool
		wait cmdutil.WaitFlags
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Restore a backup into a database",
		Long: `Starts a restore of one successful backup (` + "`norcube backup jobs list`" + `
shows them). --mode picks where the data goes:

  new_database  create --target-database on the server in --target-uri-file
                (the connection string needs the right to create databases)
  new_server    restore into the database named in --target-uri-file
  overwrite     restore over the data source's own database with its stored
                credentials (they need write access); its data is replaced

Connection strings are read from a file (--target-uri-file, - for standard
input), never from the command line, so they stay out of shell history.
They are stored encrypted and never shown again.

A restore changes data, so the command asks first; --yes answers for
scripts, and without a terminal --yes is required. With --wait it waits
for the restore to finish and exits 1 (code job_failed) when it failed.`,
		Example: `  # Into a fresh database next to production
  norcube backup restores create -d 9f8e7d6c-… --backup 6a1b2c3d-… \
    --mode new_database --target-uri-file admin.uri --target-database app_restored --wait

  # Roll the data source back to a backup (replaces its data)
  norcube backup restores create -d 9f8e7d6c-… --backup 6a1b2c3d-… --mode overwrite --yes`,
		Args: cobra.NoArgs,
		RunE: cmdutil.Run(func(cmd *cobra.Command, _ []string) (any, error) {
			if err := req.validate(); err != nil {
				return nil, err
			}
			body := snapdb.PostRestoresJSONRequestBody{
				DataSourceId: req.datasourceID, SourceBackupId: req.backupID, Mode: req.mode,
			}
			if req.targetURIFile != "" {
				uri, err := cmdutil.ReadText(cmd.InOrStdin(), "", req.targetURIFile, "the target connection string")
				if err != nil {
					return nil, err
				}
				uri = strings.TrimSpace(uri)
				if uri == "" {
					return nil, clierr.Usage(fmt.Errorf("%s holds no connection string", req.targetURIFile))
				}
				body.TargetUri = &uri
			}
			if req.targetDB != "" {
				body.TargetDatabase = &req.targetDB
			}
			ok, err := cmdutil.Confirm(req.question(), yes)
			if err != nil || !ok {
				return nil, cmdutil.Cancelled(cmd, err)
			}
			if snapdb.ConstantsRestoreMode(req.mode) == snapdb.RestoreModeOverwrite {
				confirmed := true
				body.ConfirmOverwrite = &confirmed
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.PostRestoresWithResponse(cmd.Context(), body)
			if err != nil {
				return nil, err
			}
			if res.JSON201 == nil {
				return nil, apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON409, res.JSON500)
			}
			id := res.JSON201.Id
			if !wait.Wait {
				if cmdutil.Human(cmd) {
					fmt.Fprintf(cmdutil.Stderr(cmd), "Follow it with: norcube backup restores get %s\n", id)
				}
				return output.Done{
					Message: fmt.Sprintf("Restore started (%s).", id),
					Value:   res.JSON201,
					Fields:  []output.Field{{Value: id}},
				}, nil
			}
			r, err := waitForRestore(cmd, d, c, id, wait.Timeout)
			if err != nil {
				return nil, err
			}
			if r.Status != snapdb.JobStatusSuccess {
				return nil, restoreFailed("restore", r)
			}
			return restoreObject(r), nil
		}),
	}
	cmd.Flags().StringVarP(&req.datasourceID, "datasource", "d", "", "Data source the backup belongs to (id)")
	cmd.Flags().StringVar(&req.backupID, "backup", "", "Backup job to restore (id)")
	cmd.Flags().StringVar(&req.mode, "mode", "", "Where the data goes: "+strings.Join(restoreModes, ", "))
	cmd.Flags().StringVar(&req.targetURIFile, "target-uri-file", "", "Read the target connection string from this file (- for standard input)")
	cmd.Flags().StringVar(&req.targetDB, "target-database", "", "Name of the database to create (new_database only)")
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip the confirmation prompt (required when stdin isn't a terminal)")
	cmdutil.AddWaitFlags(cmd, &wait, "restore", 6*time.Hour)
	_ = cmd.RegisterFlagCompletionFunc("mode", cobra.FixedCompletions(restoreModes, cobra.ShellCompDirectiveNoFileComp))
	for _, f := range []string{"datasource", "backup", "mode"} {
		_ = cmd.MarkFlagRequired(f)
	}
	return cmd
}
