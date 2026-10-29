package backup

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/snapdb"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

// newRestoreTestCmd groups on-demand restore tests, their history and the
// automated restore-test settings of a data source. A restore test
// restores a backup into a throwaway database (never the one being backed
// up), validates the data and tears it down. Results land next to the
// backup in `backup jobs list` and in `health`.
func newRestoreTestCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "restore-tests",
		Aliases: []string{"restore-test", "drill"},
		Short:   "Run restore tests that prove a backup actually restores",
	}
	settings := &cobra.Command{
		Use:   "settings",
		Short: "Show and change a data source's automated restore tests",
	}
	settings.AddCommand(newRestoreTestSettingsGetCmd(d), newRestoreTestSettingsUpdateCmd(d))
	cmd.AddCommand(newRestoreTestRunCmd(d), newRestoreTestListCmd(d), settings)
	return cmd
}

// maxLatestSearch bounds how many backup jobs `restore-tests run` looks
// through for the newest successful one.
const maxLatestSearch = 500

// latestSuccessfulBackup is the newest backup of the data source that
// succeeded and still exists.
func latestSuccessfulBackup(ctx context.Context, c *snapdbContext, datasourceID string) (snapdb.DtoBackupJob, error) {
	limit, cursor, seen := 100, "", 0
	for seen < maxLatestSearch {
		params := &snapdb.ListBackupJobsParams{Limit: &limit}
		if cursor != "" {
			params.Cursor = &cursor
		}
		res, err := c.API.ListBackupJobsWithResponse(ctx, datasourceID, params)
		if err != nil {
			return snapdb.DtoBackupJob{}, err
		}
		if res.JSON200 == nil {
			return snapdb.DtoBackupJob{}, apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON500)
		}
		for _, j := range res.JSON200.List {
			if j.JobStatus == snapdb.JobStatusSuccess && j.JobDeletedAt == nil {
				return j, nil
			}
		}
		seen += len(res.JSON200.List)
		cursor = cmdutil.Deref(res.JSON200.Cursors.Next)
		if cursor == "" || len(res.JSON200.List) == 0 {
			break
		}
	}
	return snapdb.DtoBackupJob{}, &clierr.Error{
		Code:    clierr.CodeNotFound,
		Message: fmt.Sprintf("data source %s has no successful backup to test (looked at the newest %d)", datasourceID, seen),
		Hint:    "Run one with `norcube backup jobs run --datasource " + datasourceID + "`, or name a backup job id.",
	}
}

// restoreTestPassed reports whether a finished restore test restored the
// data and validated it.
func restoreTestPassed(r snapdb.DtoRestoreJob) bool {
	return r.Status == snapdb.JobStatusSuccess && cmdutil.Deref(r.VerifyPassed)
}

func newRestoreTestRunCmd(d deps) *cobra.Command {
	var (
		datasourceID string
		wait         cmdutil.WaitFlags
	)
	cmd := &cobra.Command{
		Use:   "run [backup-job-id]",
		Short: "Restore-test one backup into a throwaway database, right now",
		Long: `Enqueue an on-demand restore test of a backup: the one you name, or the
data source's newest successful backup.

The backend restores the backup into a disposable database (one it spins
up just for the test, or the scratch server configured for the datasource),
confirms the data came back, and destroys the copy. The database being
backed up is never touched.

The verdict (Passed / Passed with warnings / Failed) appears next to the
backup in ` + "`norcube backup jobs list`" + ` and rolls into
` + "`norcube backup health`" + `. With --wait the command waits for the verdict
and exits 1 (code job_failed) unless the test passed, which makes it a
CI gate.`,
		Example: `  # Test the newest successful backup and wait for the verdict
  norcube backup restore-tests run --datasource 9f8e7d6c-… --wait

  # Test a specific backup (an id from ` + "`norcube backup jobs list`" + `)
  norcube backup restore-tests run 6a1b2c3d-… --datasource 9f8e7d6c-…`,
		Args: cobra.MaximumNArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			jobID := ""
			if len(args) == 1 {
				jobID = strings.TrimSpace(args[0])
			} else {
				latest, err := latestSuccessfulBackup(cmd.Context(), c, datasourceID)
				if err != nil {
					return nil, err
				}
				jobID = latest.JobId
				if cmdutil.Human(cmd) {
					fmt.Fprintf(cmdutil.Stderr(cmd), "Testing the newest successful backup, %s (%s).\n", jobID, formatTimestamp(latest.JobStartedAt))
				}
			}

			res, err := c.API.VerifyBackupNowWithResponse(cmd.Context(), datasourceID, jobID)
			if err != nil {
				return nil, err
			}
			if res.JSON202 == nil {
				return nil, apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON409, res.JSON500)
			}

			restoreJobID := cmdutil.Deref(res.JSON202.RestoreJobId)
			if !wait.Wait {
				if cmdutil.Human(cmd) {
					fmt.Fprintln(cmd.ErrOrStderr(), "Track it with `norcube backup jobs list`; the verdict appears in the TEST column.")
				}
				return output.Done{
					Message: fmt.Sprintf("Restore test queued (restore job %s).", restoreJobID),
					Value:   res.JSON202,
					Fields:  []output.Field{{Value: restoreJobID}},
				}, nil
			}
			r, err := waitForRestore(cmd, d, c, restoreJobID, wait.Timeout)
			if err != nil {
				return nil, err
			}
			if !restoreTestPassed(r) {
				return nil, restoreFailed("restore test", r)
			}
			return restoreObject(r), nil
		}),
	}
	cmd.Flags().StringVarP(&datasourceID, "datasource", "d", "", "Datasource the backup belongs to (id)")
	cmdutil.AddWaitFlags(cmd, &wait, "restore test", 2*time.Hour)
	_ = cmd.MarkFlagRequired("datasource")
	return cmd
}

func newRestoreTestListCmd(d deps) *cobra.Command {
	var (
		datasourceID string
		pages        cmdutil.PageFlags
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List a data source's restore tests, newest first",
		Long: `Lists the restore tests of a data source, automated and on-demand, with
their verdict: passed, passed (warnings) or failed when the data was
validated, or the restore's own status (queued, running, failed) when it
didn't get that far. ` + "`norcube backup health`" + ` sums them up.`,
		Example: `  norcube backup restore-tests list --datasource 9f8e7d6c-…
  norcube backup restore-tests list -d 9f8e7d6c-… -o json --query "[?status=='failed'].id"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cmdutil.CheckQuery(cmd); err != nil {
				return err
			}
			c, err := d.client(cmd)
			if err != nil {
				return err
			}
			page, err := listRestores(cmd, c, pages, datasourceID, restoreTriggerVerification)
			if err != nil {
				return err
			}
			return page.Print(cmd, output.Table[snapdb.DtoRestoreJob]{
				Headers: []string{"ID", "VERDICT", "LEVEL", "BACKUP", "STARTED", "DURATION"},
				Style:   output.Style{StatusColumn: 1},
				Rows: func(r snapdb.DtoRestoreJob) []string {
					return []string{
						r.Id, restoreVerdict(r), cmdutil.Deref(r.VerifyLevel), cmdutil.Deref(r.SourceBackupId),
						formatTimestamp(r.StartedAt), formatDurationMs(r.DurationMs),
					}
				},
				Items: page.Items,
			})
		},
	}
	cmd.Flags().StringVarP(&datasourceID, "datasource", "d", "", "Data source the restore tests belong to (id)")
	cmdutil.AddPageFlags(cmd, &pages, defaultPageSize, maxItems)
	_ = cmd.MarkFlagRequired("datasource")
	return cmd
}

// Automated restore-test settings the API accepts. The spec types them as
// plain strings, so the CLI keeps the lists.
var (
	restoreTestTargets = []string{"managed_ephemeral", "customer_scratch"}
	restoreTestLevels  = []string{"l1", "l2", "l3"}
)

func getRestoreTestSettings(ctx context.Context, c *snapdbContext, datasourceID string) (snapdb.DtoRestoreCheck, error) {
	res, err := c.API.GetRestoreCheckWithResponse(ctx, datasourceID)
	if err != nil {
		return snapdb.DtoRestoreCheck{}, err
	}
	if res.JSON200 == nil {
		return snapdb.DtoRestoreCheck{}, apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON500)
	}
	return *res.JSON200, nil
}

// cadence describes when automated tests run, for a person.
func cadence(s snapdb.DtoRestoreCheck) string {
	if !cmdutil.Deref(s.Enabled) {
		return "off"
	}
	switch s.Mode {
	case "every_nth":
		return fmt.Sprintf("every %d successful backups", cmdutil.Deref(s.Nth))
	case "schedule":
		return fmt.Sprintf("on schedule %q (%s)", cmdutil.Deref(s.ScheduleCron), cmdutil.DerefOr(s.ScheduleTimezone, "UTC"))
	case "sample":
		return fmt.Sprintf("%.0f%% of backups", float64(cmdutil.Deref(s.SampleProbability))*100)
	default:
		return s.Mode
	}
}

func settingsObject(datasourceID string, s snapdb.DtoRestoreCheck) output.Object {
	scratch := "—"
	if s.Target == "customer_scratch" {
		scratch = "not set"
		if cmdutil.Deref(s.HasScratchCredentials) {
			scratch = "set"
		}
	}
	return output.Object{Value: s, Fields: []output.Field{
		{Name: "Datasource", Value: datasourceID},
		{Name: "Automated tests", Value: cadence(s)},
		{Name: "Target", Value: s.Target},
		{Name: "Scratch credentials", Value: scratch},
		{Name: "Validation", Value: s.ValidationLevel},
		{Name: "Validation query", Value: cmdutil.Deref(s.ValidationQuery)},
		{Name: "Last run", Value: formatTimestamp(s.LastRunAt)},
	}}
}

func newRestoreTestSettingsGetCmd(d deps) *cobra.Command {
	var datasourceID string
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Show a data source's automated restore-test settings",
		Example: `  norcube backup restore-tests settings get --datasource 9f8e7d6c-…
  norcube backup restore-tests settings get -d 9f8e7d6c-… -o json`,
		Args: cobra.NoArgs,
		RunE: cmdutil.Run(func(cmd *cobra.Command, _ []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			s, err := getRestoreTestSettings(cmd.Context(), c, datasourceID)
			if err != nil {
				return nil, err
			}
			return settingsObject(datasourceID, s), nil
		}),
	}
	cmd.Flags().StringVarP(&datasourceID, "datasource", "d", "", "Data source (id)")
	_ = cmd.MarkFlagRequired("datasource")
	return cmd
}

// restoreTestSettingsFlags are what `settings update` can change.
type restoreTestSettingsFlags struct {
	enable, disable            bool
	every                      int
	schedule, timezone, sample string
	target, scratchFile        string
	level, query               string
}

// apply validates the flags that were set and merges them into the
// current settings: the API replaces the whole configuration, so what the
// command doesn't change is sent as it was.
func (f restoreTestSettingsFlags) apply(cmd *cobra.Command, cur snapdb.DtoRestoreCheck) (snapdb.PutRestoreCheckJSONRequestBody, error) {
	changed := cmd.Flags().Changed
	body := snapdb.PutRestoreCheckJSONRequestBody{
		Enabled: cur.Enabled, Mode: &cur.Mode, Nth: cur.Nth, ScheduleCron: cur.ScheduleCron,
		ScheduleTimezone: cur.ScheduleTimezone, SampleProbability: cur.SampleProbability,
		Target: &cur.Target, ValidationLevel: &cur.ValidationLevel, ValidationQuery: cur.ValidationQuery,
	}
	modes := 0
	setMode := func(m string) {
		modes++
		body.Mode = &m
		on := true
		body.Enabled = &on
	}
	if changed("every") {
		if f.every < 1 {
			return body, clierr.Usage(errors.New("--every must be at least 1"))
		}
		setMode("every_nth")
		body.Nth = &f.every
	}
	if changed("schedule") {
		if strings.TrimSpace(f.schedule) == "" {
			return body, clierr.Usage(errors.New("--schedule must not be empty"))
		}
		setMode("schedule")
		body.ScheduleCron = &f.schedule
	}
	if changed("sample") {
		p, err := parseShare(f.sample)
		if err != nil {
			return body, err
		}
		setMode("sample")
		body.SampleProbability = &p
	}
	if modes > 1 {
		return body, clierr.Usage(errors.New("pass one of --every, --schedule and --sample"))
	}
	if changed("timezone") {
		body.ScheduleTimezone = &f.timezone
	}
	if f.disable {
		off := false
		body.Enabled = &off
	}
	if f.enable {
		on := true
		body.Enabled = &on
		if cmdutil.Deref(body.Mode) == "off" || cmdutil.Deref(body.Mode) == "" {
			return body, clierr.Usage(errors.New("--enable needs a cadence: pass --every, --schedule or --sample"))
		}
	}
	if changed("target") {
		if !slices.Contains(restoreTestTargets, f.target) {
			return body, clierr.Usage(fmt.Errorf("--target must be one of %s", strings.Join(restoreTestTargets, ", ")))
		}
		body.Target = &f.target
	}
	if f.scratchFile != "" {
		if cmdutil.Deref(body.Target) != "customer_scratch" {
			return body, clierr.Usage(errors.New("--scratch-uri-file is for --target customer_scratch"))
		}
		uri, err := cmdutil.ReadText(cmd.InOrStdin(), "", f.scratchFile, "the scratch connection string")
		if err != nil {
			return body, err
		}
		uri = strings.TrimSpace(uri)
		if uri == "" {
			return body, clierr.Usage(fmt.Errorf("%s holds no connection string", f.scratchFile))
		}
		body.ScratchConnectionString = &uri
	}
	if changed("level") {
		if !slices.Contains(restoreTestLevels, f.level) {
			return body, clierr.Usage(fmt.Errorf("--level must be one of %s", strings.Join(restoreTestLevels, ", ")))
		}
		body.ValidationLevel = &f.level
	}
	if changed("validation-query") {
		body.ValidationQuery = &f.query
	}
	return body, nil
}

// parseShare reads --sample: a fraction (0.25) or a percentage (25%).
func parseShare(s string) (float32, error) {
	s = strings.TrimSpace(s)
	pct := strings.HasSuffix(s, "%")
	v, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 32)
	if err == nil && pct {
		v /= 100
	}
	if err != nil || v <= 0 || v > 1 {
		return 0, clierr.Usage(fmt.Errorf("--sample %q: want a share of backups, as 0.25 or 25%%", s))
	}
	return float32(v), nil
}

func newRestoreTestSettingsUpdateCmd(d deps) *cobra.Command {
	var (
		datasourceID string
		f            restoreTestSettingsFlags
	)
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Change a data source's automated restore tests",
		Long: `Changes only what you pass; the rest stays as it is.

How often automated tests run (one of):
  --every N        one in every N successful backups
  --schedule CRON  on a cron schedule (in --timezone)
  --sample SHARE   a random share of backups (0.25 or 25%)
Any of them turns the tests on; --disable turns them off and keeps the
settings, --enable turns them back on.

Where a test restores (--target): managed_ephemeral, a database Norcube
spins up for the test, or customer_scratch, a server of yours given with
--scratch-uri-file (a file, - for standard input; it must not be the
server being backed up). --level sets how deep the validation goes: l1
(the restore succeeds), l2 (the data is there), l3 (also runs
--validation-query).`,
		Example: `  norcube backup restore-tests settings update -d 9f8e7d6c-… --every 7
  norcube backup restore-tests settings update -d 9f8e7d6c-… --schedule "0 4 * * 0" --timezone Europe/Prague --level l2
  norcube backup restore-tests settings update -d 9f8e7d6c-… --target customer_scratch --scratch-uri-file scratch.uri
  norcube backup restore-tests settings update -d 9f8e7d6c-… --disable`,
		Args: cobra.NoArgs,
		RunE: cmdutil.Run(func(cmd *cobra.Command, _ []string) (any, error) {
			changedAny := false
			for _, flag := range []string{"enable", "disable", "every", "schedule", "timezone", "sample", "target", "scratch-uri-file", "level", "validation-query"} {
				changedAny = changedAny || cmd.Flags().Changed(flag)
			}
			if !changedAny {
				return nil, clierr.Usage(errors.New("nothing to update: pass at least one setting (see --help)"))
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			cur, err := getRestoreTestSettings(cmd.Context(), c, datasourceID)
			if err != nil {
				return nil, err
			}
			body, err := f.apply(cmd, cur)
			if err != nil {
				return nil, err
			}
			res, err := c.API.PutRestoreCheckWithResponse(cmd.Context(), datasourceID, body)
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON500)
			}
			return settingsObject(datasourceID, *res.JSON200), nil
		}),
	}
	cmd.Flags().StringVarP(&datasourceID, "datasource", "d", "", "Data source (id)")
	cmd.Flags().BoolVar(&f.enable, "enable", false, "Turn automated restore tests on")
	cmd.Flags().BoolVar(&f.disable, "disable", false, "Turn automated restore tests off (the settings are kept)")
	cmd.Flags().IntVar(&f.every, "every", 0, "Test one in every N successful backups")
	cmd.Flags().StringVar(&f.schedule, "schedule", "", "Test on this cron schedule")
	cmd.Flags().StringVar(&f.timezone, "timezone", "", "Time zone of --schedule (IANA name)")
	cmd.Flags().StringVar(&f.sample, "sample", "", "Test this share of backups (0.25 or 25%)")
	cmd.Flags().StringVar(&f.target, "target", "", "Where tests restore: "+strings.Join(restoreTestTargets, ", "))
	cmd.Flags().StringVar(&f.scratchFile, "scratch-uri-file", "", "Read the scratch server's connection string from this file (- for standard input)")
	cmd.Flags().StringVar(&f.level, "level", "", "Validation depth: "+strings.Join(restoreTestLevels, ", "))
	cmd.Flags().StringVar(&f.query, "validation-query", "", "Query an l3 validation runs against the restored data")
	cmd.MarkFlagsMutuallyExclusive("enable", "disable")
	_ = cmd.RegisterFlagCompletionFunc("target", cobra.FixedCompletions(restoreTestTargets, cobra.ShellCompDirectiveNoFileComp))
	_ = cmd.RegisterFlagCompletionFunc("level", cobra.FixedCompletions(restoreTestLevels, cobra.ShellCompDirectiveNoFileComp))
	_ = cmd.MarkFlagRequired("datasource")
	return cmd
}
