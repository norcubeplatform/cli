package backup

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newRestoreTestCmd is the CLI trigger for an on-demand restore test: the
// backend restores the given backup into a throwaway database (never the
// one being backed up), validates the data, and tears it down. The result
// lands next to the backup in `backup list` and in `health`.
func newRestoreTestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "restore-test",
		Aliases: []string{"drill"},
		Short:   "Run restore tests that prove a backup actually restores",
	}
	cmd.AddCommand(newRestoreTestRunCmd())
	return cmd
}

func newRestoreTestRunCmd() *cobra.Command {
	var datasourceID string

	cmd := &cobra.Command{
		Use:   "run <backup-job-id>",
		Short: "Restore-test one backup into a throwaway database, right now",
		Long: `Enqueue an on-demand restore test of a backup.

The backend restores the backup into a disposable database (one it spins
up just for the test, or the scratch server configured for the datasource),
confirms the data came back, and destroys the copy. The database being
backed up is never touched.

The verdict (Passed / Passed with warnings / Failed) appears next to the
backup in ` + "`norcube backup list`" + ` and rolls into
` + "`norcube backup health`" + `.`,
		Example: `  # Pick a backup id from ` + "`norcube backup list`" + `, then:
  norcube backup restore-test run 6a1b2c3d-… --datasource 9f8e7d6c-…`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			jobID := args[0]
			c, err := newSnapdbContext(cmd)
			if err != nil {
				return err
			}

			res, err := c.client.VerifyBackupNowWithResponse(cmd.Context(), datasourceID, jobID)
			if err != nil {
				return err
			}
			if res.JSON202 == nil {
				return apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON409, res.JSON500)
			}

			restoreJobID := deref(res.JSON202.RestoreJobId)
			fmt.Fprintf(cmd.OutOrStdout(), "Restore test queued (restore job %s).\n", restoreJobID)
			fmt.Fprintln(cmd.ErrOrStderr(), "Track it with `norcube backup list`; the verdict appears in the TEST column.")
			return nil
		},
	}
	cmd.Flags().StringVarP(&datasourceID, "datasource", "d", "", "Datasource the backup belongs to (id)")
	_ = cmd.MarkFlagRequired("datasource")
	return cmd
}
