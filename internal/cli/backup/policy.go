package backup

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/snapdb"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

func newPolicyCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "policies",
		Aliases: []string{"policy", "p"},
		Short:   "Manage backup policies and their attachments to data sources",
		Long: `A backup policy says when and how a data source is backed up: a cron
schedule in a time zone, how long backups are kept, the storage
destination and the dump method. Create one with "create", then attach it
to data sources with "attach".

A "policy attachment" is the join row between a data source and a
backup policy. The same backup policy can be attached to many data
sources; the same data source can have many attached policies. pause,
resume and detach change one attachment without touching the policy.`,
	}
	cmd.AddCommand(
		newPolicyListCmd(d),
		newPolicyCreateCmd(d),
		newPolicyUpdateCmd(d),
		newPolicyAttachCmd(d),
		newPolicyPauseCmd(d),
		newPolicyResumeCmd(d),
		newPolicyDetachCmd(d),
	)
	return cmd
}

func newPolicyListCmd(d deps) *cobra.Command {
	var datasourceID string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the organization's backup policies, or the ones attached to a data source",
		Long: `Without --datasource, lists every backup policy of the organization.
With --datasource, lists the policies attached to that data source, with
each attachment's state and priority.`,
		Example: `  norcube backup policies list
  norcube backup policies list --datasource 9f8e7d6c-…
  norcube backup policies list --datasource 9f8e7d6c-… -o json --query "[?!isEnabled].id"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cmdutil.CheckQuery(cmd); err != nil {
				return err
			}
			c, err := d.client(cmd)
			if err != nil {
				return err
			}
			if datasourceID == "" {
				return listPolicyDefinitions(cmd, c)
			}

			res, err := c.API.ListPoliciesWithResponse(cmd.Context(), datasourceID)
			if err != nil {
				return err
			}
			if res.JSON200 == nil {
				return apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON500)
			}
			items := *res.JSON200

			return cmdutil.Print(cmd, output.Table[snapdb.DtoDatasourcePolicy]{
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
	cmd.Flags().StringVar(&datasourceID, "datasource", "", "List the policies attached to this data source (id)")
	return cmd
}

func newPolicyPauseCmd(d deps) *cobra.Command {
	var datasourceID, policyID string
	cmd := &cobra.Command{
		Use:   "pause",
		Short: "Pause one policy on one data source (scheduler stops enqueuing this attachment)",
		Long: `Sets enabled=false on the attachment row. Schedule history, cron
expression, and jitter are preserved — flip it back with "resume".
Other policies on the same data source continue to run.`,
		Example: `  norcube backup policies pause --datasource 9f8e7d6c-… --policy 1a2b3c4d-…`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return setAttachmentEnabled(cmd, d, datasourceID, policyID, false, "Paused policy %q on data source %q.")
		},
	}
	cmd.Flags().StringVar(&datasourceID, "datasource", "", "Data source ID (required)")
	cmd.Flags().StringVar(&policyID, "policy", "", "Backup policy ID (required)")
	_ = cmd.MarkFlagRequired("datasource")
	_ = cmd.MarkFlagRequired("policy")
	return cmd
}

func newPolicyResumeCmd(d deps) *cobra.Command {
	var datasourceID, policyID string
	cmd := &cobra.Command{
		Use:     "resume",
		Short:   "Re-enable a previously paused policy attachment",
		Example: `  norcube backup policies resume --datasource 9f8e7d6c-… --policy 1a2b3c4d-…`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return setAttachmentEnabled(cmd, d, datasourceID, policyID, true, "Resumed policy %q on data source %q.")
		},
	}
	cmd.Flags().StringVar(&datasourceID, "datasource", "", "Data source ID (required)")
	cmd.Flags().StringVar(&policyID, "policy", "", "Backup policy ID (required)")
	_ = cmd.MarkFlagRequired("datasource")
	_ = cmd.MarkFlagRequired("policy")
	return cmd
}

func setAttachmentEnabled(cmd *cobra.Command, d deps, dsID, polID string, enabled bool, successFmt string) error {
	c, err := d.client(cmd)
	if err != nil {
		return err
	}
	res, err := c.API.UpdatePolicyAttachmentWithResponse(cmd.Context(), dsID, polID,
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
	return cmdutil.Print(cmd, output.Done{
		Message: fmt.Sprintf(successFmt, polID, dsID),
		Value:   attachmentState{DatasourceID: dsID, PolicyID: polID, Enabled: enabled},
		Fields:  []output.Field{{Value: dsID}, {Value: polID}, {Value: enabledStr(enabled)}},
	})
}

func newPolicyDetachCmd(d deps) *cobra.Command {
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
		Example: `  norcube backup policies detach --datasource 9f8e7d6c-… --policy 1a2b3c4d-…
  norcube backup policies detach --datasource 9f8e7d6c-… --policy 1a2b3c4d-… --yes   # scripts`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := d.client(cmd)
			if err != nil {
				return err
			}
			ok, err := cmdutil.Confirm(
				fmt.Sprintf("Detach policy %q from data source %q?", policyID, datasourceID),
				yes,
			)
			if err != nil {
				if err == cmdutil.ErrCancelled {
					fmt.Fprintln(cmd.ErrOrStderr(), "Cancelled.")
					return nil
				}
				return err
			}
			if !ok {
				fmt.Fprintln(cmd.ErrOrStderr(), "Cancelled.")
				return nil
			}

			res, err := c.API.DeleteDatasourcesDatasourceIdPoliciesPolicyIdWithResponse(
				cmd.Context(), datasourceID, policyID,
			)
			if err != nil {
				return err
			}
			if res.HTTPResponse.StatusCode != 204 && res.HTTPResponse.StatusCode != 200 {
				return apiError(res.HTTPResponse, res.Body, nil, nil)
			}
			return cmdutil.Print(cmd, output.Done{
				Message: fmt.Sprintf("Detached policy %q from data source %q.", policyID, datasourceID),
				Value:   attachmentDetached{DatasourceID: datasourceID, PolicyID: policyID, Detached: true},
				Fields:  []output.Field{{Value: datasourceID}, {Value: policyID}},
			})
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

// attachmentState is what policy pause/resume print with -o json.
type attachmentState struct {
	DatasourceID string `json:"datasourceId" yaml:"datasourceId"`
	PolicyID     string `json:"policyId" yaml:"policyId"`
	Enabled      bool   `json:"enabled" yaml:"enabled"`
}

// attachmentDetached is what policy detach prints with -o json.
type attachmentDetached struct {
	DatasourceID string `json:"datasourceId" yaml:"datasourceId"`
	PolicyID     string `json:"policyId" yaml:"policyId"`
	Detached     bool   `json:"detached" yaml:"detached"`
}
