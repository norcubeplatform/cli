package snapdb

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/snapdb"
	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/output"
)

func newPolicyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "policy",
		Aliases: []string{"policies", "p"},
		Short:   "Manage backup-policy attachments on a data source",
		Long: `A "policy attachment" is the join row between a data source and a
backup policy. The same backup policy can be attached to many data
sources; the same data source can have many attached policies. These
commands toggle and remove specific attachments without touching the
underlying policy definition.`,
	}
	cmd.AddCommand(
		newPolicyListCmd(),
		newPolicyPauseCmd(),
		newPolicyResumeCmd(),
		newPolicyDetachCmd(),
	)
	return cmd
}

func newPolicyListCmd() *cobra.Command {
	var datasourceID string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List policy attachments on a data source",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newSnapdbContext(cmd)
			if err != nil {
				return err
			}
			if datasourceID == "" {
				return fmt.Errorf("--datasource is required")
			}

			res, err := c.client.ListPoliciesWithResponse(cmd.Context(), datasourceID)
			if err != nil {
				return err
			}
			if res.JSON200 == nil {
				return apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON500)
			}
			items := *res.JSON200

			flags := clictx.Get(cmd)
			return output.PrintPaged(cmd.OutOrStdout(), c.output, flags.NoPager, output.Table[snapdb.DtoDatasourcePolicy]{
				Headers:   []string{"POLICY", "ENABLED", "PRIORITY", "DESTINATION", "ATTACHMENT_ID"},
				MaxWidths: []int{32, 0, 0, 32, 0},
				Style:     output.Style{StatusColumn: 1}, // ENABLED
				Rows: func(p snapdb.DtoDatasourcePolicy) []string {
					return []string{
						p.BackupPolicy.Name,
						enabledStr(p.IsEnabled),
						fmt.Sprintf("%d", p.Priority),
						p.Destination.Name,
						p.Id,
					}
				},
				Items: items,
			})
		},
	}
	cmd.Flags().StringVar(&datasourceID, "datasource", "", "Data source ID (required)")
	_ = cmd.MarkFlagRequired("datasource")
	return cmd
}

func newPolicyPauseCmd() *cobra.Command {
	var datasourceID, policyID string
	cmd := &cobra.Command{
		Use:   "pause",
		Short: "Pause one policy on one data source (scheduler stops enqueuing this attachment)",
		Long: `Sets enabled=false on the attachment row. Schedule history, cron
expression, and jitter are preserved — flip it back with "resume".
Other policies on the same data source continue to run.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return setAttachmentEnabled(cmd, datasourceID, policyID, false, "Paused policy %q on data source %q.")
		},
	}
	cmd.Flags().StringVar(&datasourceID, "datasource", "", "Data source ID (required)")
	cmd.Flags().StringVar(&policyID, "policy", "", "Backup policy ID (required)")
	_ = cmd.MarkFlagRequired("datasource")
	_ = cmd.MarkFlagRequired("policy")
	return cmd
}

func newPolicyResumeCmd() *cobra.Command {
	var datasourceID, policyID string
	cmd := &cobra.Command{
		Use:   "resume",
		Short: "Re-enable a previously paused policy attachment",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return setAttachmentEnabled(cmd, datasourceID, policyID, true, "Resumed policy %q on data source %q.")
		},
	}
	cmd.Flags().StringVar(&datasourceID, "datasource", "", "Data source ID (required)")
	cmd.Flags().StringVar(&policyID, "policy", "", "Backup policy ID (required)")
	_ = cmd.MarkFlagRequired("datasource")
	_ = cmd.MarkFlagRequired("policy")
	return cmd
}

func setAttachmentEnabled(cmd *cobra.Command, dsID, polID string, enabled bool, successFmt string) error {
	c, err := newSnapdbContext(cmd)
	if err != nil {
		return err
	}
	res, err := c.client.UpdatePolicyAttachmentWithResponse(cmd.Context(), dsID, polID,
		snapdb.UpdatePolicyAttachmentJSONRequestBody{
			Enabled: &enabled,
		},
	)
	if err != nil {
		return err
	}
	if res.HTTPResponse.StatusCode != 204 && res.HTTPResponse.StatusCode != 200 {
		return apiError(res.HTTPResponse, res.Body, nil, nil)
	}
	fmt.Fprintf(cmd.OutOrStdout(), successFmt+"\n", polID, dsID)
	return nil
}

func newPolicyDetachCmd() *cobra.Command {
	var datasourceID, policyID string
	var yes bool
	cmd := &cobra.Command{
		Use:   "detach",
		Short: "Remove a policy attachment entirely (destructive)",
		Long: `Deletes the join row between a data source and a backup policy. The
backup policy definition is not affected; only this one attachment is
removed. Past backup jobs are unaffected.

Prefer "pause" if you only want to halt this schedule temporarily — it
preserves the cron expression and jitter values. Use "detach" when the
attachment was a mistake or is permanently obsolete.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newSnapdbContext(cmd)
			if err != nil {
				return err
			}
			ok, err := confirm(
				fmt.Sprintf("Detach policy %q from data source %q?", policyID, datasourceID),
				yes, cmd.ErrOrStderr(),
			)
			if err != nil {
				if err == ErrCancelled {
					fmt.Fprintln(cmd.OutOrStdout(), "Cancelled.")
					return nil
				}
				return err
			}
			if !ok {
				fmt.Fprintln(cmd.OutOrStdout(), "Cancelled.")
				return nil
			}

			res, err := c.client.DeleteDatasourcesDatasourceIdPoliciesPolicyIdWithResponse(
				cmd.Context(), datasourceID, policyID,
			)
			if err != nil {
				return err
			}
			if res.HTTPResponse.StatusCode != 204 && res.HTTPResponse.StatusCode != 200 {
				return apiError(res.HTTPResponse, res.Body, nil, nil)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Detached policy %q from data source %q.\n", policyID, datasourceID)
			return nil
		},
	}
	cmd.Flags().StringVar(&datasourceID, "datasource", "", "Data source ID (required)")
	cmd.Flags().StringVar(&policyID, "policy", "", "Backup policy ID (required)")
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip the confirmation prompt (required for non-interactive shells)")
	_ = cmd.MarkFlagRequired("datasource")
	_ = cmd.MarkFlagRequired("policy")
	return cmd
}

func enabledStr(b bool) string {
	if b {
		return "active"
	}
	return "paused"
}
