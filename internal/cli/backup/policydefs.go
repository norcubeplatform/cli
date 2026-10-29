package backup

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/snapdb"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

// Policy settings the API accepts, as it names them.
var (
	policyMethods = []string{
		string(snapdb.MethodLogicalDump), string(snapdb.MethodPhysical), string(snapdb.MethodPITR),
	}
	policyCompressions = []string{
		string(snapdb.CompressionGzip), string(snapdb.CompressionZstd), string(snapdb.CompressionNone),
	}
)

func listPolicyDefinitions(cmd *cobra.Command, c *snapdbContext) error {
	res, err := c.API.GetBackupPoliciesWithResponse(cmd.Context())
	if err != nil {
		return err
	}
	if res.JSON200 == nil {
		return apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON500)
	}
	return cmdutil.Print(cmd, output.Table[snapdb.DtoBackupPolicy]{
		Headers:   []string{"NAME", "SCHEDULE", "TIMEZONE", "RETENTION", "KEEP_LAST", "METHOD", "ID"},
		MaxWidths: []int{32, 0, 0, 0, 0, 0, 0},
		Rows: func(p snapdb.DtoBackupPolicy) []string {
			return []string{
				p.Name, p.ScheduleCron, p.Timezone, fmt.Sprintf("%dd", p.RetentionDays),
				fmt.Sprint(p.KeepLastN), string(p.Method), p.Id,
			}
		},
		Items: res.JSON200.List,
	})
}

// policyFlags are the settings `policies create` and `policies update`
// take.
type policyFlags struct {
	name, schedule, timezone, method, compression string
	retentionDays, keepLast                       int
}

func addPolicyFlags(cmd *cobra.Command, f *policyFlags) {
	cmd.Flags().StringVar(&f.name, "name", "", "Policy name")
	cmd.Flags().StringVar(&f.schedule, "schedule", "", `When to back up, as a cron expression ("0 2 * * *" is 02:00 every day)`)
	cmd.Flags().StringVar(&f.timezone, "timezone", "", "Time zone of the schedule (IANA name, e.g. Europe/Prague or UTC)")
	cmd.Flags().IntVar(&f.retentionDays, "retention-days", 0, "Delete backups older than this many days")
	cmd.Flags().IntVar(&f.keepLast, "keep-last", 0, "Keep at most this many backups")
	cmd.Flags().StringVar(&f.method, "method", "", "Dump method: "+strings.Join(policyMethods, ", "))
	cmd.Flags().StringVar(&f.compression, "compression", "", "Compression: "+strings.Join(policyCompressions, ", "))
	_ = cmd.RegisterFlagCompletionFunc("method", cobra.FixedCompletions(policyMethods, cobra.ShellCompDirectiveNoFileComp))
	_ = cmd.RegisterFlagCompletionFunc("compression", cobra.FixedCompletions(policyCompressions, cobra.ShellCompDirectiveNoFileComp))
}

// validate checks the flags that were set.
func (f policyFlags) validate(cmd *cobra.Command) error {
	changed := cmd.Flags().Changed
	for _, s := range []struct {
		flag, value string
	}{{"name", f.name}, {"schedule", f.schedule}, {"timezone", f.timezone}} {
		if changed(s.flag) && strings.TrimSpace(s.value) == "" {
			return clierr.Usage(fmt.Errorf("--%s must not be empty", s.flag))
		}
	}
	if f.retentionDays < 0 || f.keepLast < 0 {
		return clierr.Usage(errors.New("--retention-days and --keep-last must not be negative"))
	}
	if changed("method") && !slices.Contains(policyMethods, f.method) {
		return clierr.Usage(fmt.Errorf("--method must be one of %s", strings.Join(policyMethods, ", ")))
	}
	if changed("compression") && !slices.Contains(policyCompressions, f.compression) {
		return clierr.Usage(fmt.Errorf("--compression must be one of %s", strings.Join(policyCompressions, ", ")))
	}
	return nil
}

func newPolicyCreateCmd(d deps) *cobra.Command {
	var (
		f           policyFlags
		destination string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a backup policy",
		Long: `Creates a backup policy. It backs nothing up until you attach it to a
data source with ` + "`norcube backup policies attach`" + `.

--destination is the storage destination the backups land in; list them
with ` + "`norcube backup destinations list`" + `.`,
		Example: `  norcube backup policies create --name Nightly --schedule "0 2 * * *" --timezone Europe/Prague \
    --retention-days 30 --keep-last 30 --destination 5e6f7a8b-…`,
		Args: cobra.NoArgs,
		RunE: cmdutil.Run(func(cmd *cobra.Command, _ []string) (any, error) {
			if err := f.validate(cmd); err != nil {
				return nil, err
			}
			body := snapdb.PostBackupPoliciesJSONRequestBody{
				Name: strings.TrimSpace(f.name), ScheduleCron: strings.TrimSpace(f.schedule), Timezone: strings.TrimSpace(f.timezone),
				RetentionDays: f.retentionDays, KeepLastN: f.keepLast, DestinationId: strings.TrimSpace(destination),
			}
			if f.method != "" {
				body.Method = &f.method
			}
			if f.compression != "" {
				body.Compression = &f.compression
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.PostBackupPoliciesWithResponse(cmd.Context(), body)
			if err != nil {
				return nil, err
			}
			if res.JSON201 == nil {
				return nil, apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON500)
			}
			id := res.JSON201.Id
			return output.Done{
				Message: fmt.Sprintf("Created backup policy %q (%s).\nAttach it with `norcube backup policies attach --datasource <id> --policy %s`.", body.Name, id, id),
				Value:   map[string]any{"id": id, "name": body.Name},
				Fields:  []output.Field{{Value: id}},
			}, nil
		}),
	}
	addPolicyFlags(cmd, &f)
	cmd.Flags().StringVar(&destination, "destination", "", "Storage destination the backups land in (id)")
	for _, flag := range []string{"name", "schedule", "timezone", "retention-days", "keep-last", "destination"} {
		_ = cmd.MarkFlagRequired(flag)
	}
	return cmd
}

func newPolicyUpdateCmd(d deps) *cobra.Command {
	var f policyFlags
	cmd := &cobra.Command{
		Use:   "update <policy-id>",
		Short: "Change a backup policy",
		Long: `Changes only the settings you pass. Every data source the policy is
attached to follows the new settings from its next run.`,
		Example: `  norcube backup policies update 1a2b3c4d-… --schedule "0 3 * * *"
  norcube backup policies update 1a2b3c4d-… --retention-days 90 --keep-last 90`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			if err := f.validate(cmd); err != nil {
				return nil, err
			}
			changed := cmd.Flags().Changed
			var body snapdb.PatchBackupPoliciesIdJSONRequestBody
			str := func(flag, v string) *string {
				if !changed(flag) {
					return nil
				}
				v = strings.TrimSpace(v)
				return &v
			}
			body.Name, body.ScheduleCron, body.Timezone = str("name", f.name), str("schedule", f.schedule), str("timezone", f.timezone)
			body.Method, body.Compression = str("method", f.method), str("compression", f.compression)
			if changed("retention-days") {
				body.RetentionDays = &f.retentionDays
			}
			if changed("keep-last") {
				body.KeepLastN = &f.keepLast
			}
			if body == (snapdb.PatchBackupPoliciesIdJSONRequestBody{}) {
				return nil, clierr.Usage(errors.New("nothing to update: pass at least one of --name, --schedule, --timezone, --retention-days, --keep-last, --method, --compression"))
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			id := strings.TrimSpace(args[0])
			res, err := c.API.PatchBackupPoliciesIdWithResponse(cmd.Context(), id, body)
			if err != nil {
				return nil, err
			}
			if res.StatusCode() != http.StatusOK && res.StatusCode() != http.StatusNoContent {
				return nil, apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON404, res.JSON409, res.JSON500)
			}
			return output.Done{
				Message: fmt.Sprintf("Updated backup policy %s.", id),
				Value:   map[string]any{"id": id, "updated": true},
				Fields:  []output.Field{{Value: id}},
			}, nil
		}),
	}
	addPolicyFlags(cmd, &f)
	return cmd
}

func newPolicyAttachCmd(d deps) *cobra.Command {
	var (
		datasourceID, policyID string
		priority               int
		paused                 bool
	)
	cmd := &cobra.Command{
		Use:   "attach",
		Short: "Attach a backup policy to a data source",
		Long: `Attaches the policy, so the scheduler backs the data source up on the
policy's schedule. --paused attaches it without running it yet (resume it
later); --priority orders attachments for ` + "`backup jobs run`" + `, which uses
the highest-priority enabled one unless told otherwise.`,
		Example: `  norcube backup policies attach --datasource 9f8e7d6c-… --policy 1a2b3c4d-…
  norcube backup policies attach --datasource 9f8e7d6c-… --policy 1a2b3c4d-… --priority 2 --paused`,
		Args: cobra.NoArgs,
		RunE: cmdutil.Run(func(cmd *cobra.Command, _ []string) (any, error) {
			body := snapdb.PostDatasourcesDatasourceIdPoliciesJSONRequestBody{PolicyId: policyID, Enabled: !paused}
			if cmd.Flags().Changed("priority") {
				body.Priority = &priority
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.PostDatasourcesDatasourceIdPoliciesWithResponse(cmd.Context(), datasourceID, body)
			if err != nil {
				return nil, err
			}
			if res.JSON201 == nil {
				return nil, apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON500)
			}
			state := "active"
			if paused {
				state = "paused"
			}
			return output.Done{
				Message: fmt.Sprintf("Attached policy %s to data source %s (%s).", policyID, datasourceID, state),
				Value:   map[string]any{"id": res.JSON201.Id, "datasourceId": datasourceID, "policyId": policyID, "enabled": !paused},
				Fields:  []output.Field{{Value: res.JSON201.Id}},
			}, nil
		}),
	}
	cmd.Flags().StringVar(&datasourceID, "datasource", "", "Data source ID (required)")
	cmd.Flags().StringVar(&policyID, "policy", "", "Backup policy ID (required)")
	cmd.Flags().IntVar(&priority, "priority", 0, "Priority among the data source's policies")
	cmd.Flags().BoolVar(&paused, "paused", false, "Attach it paused: the scheduler skips it until you resume it")
	_ = cmd.MarkFlagRequired("datasource")
	_ = cmd.MarkFlagRequired("policy")
	return cmd
}

func newDestinationsCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "destinations",
		Aliases: []string{"destination"},
		Short:   "List the storage destinations backups can land in",
	}
	cmd.AddCommand(newDestinationListCmd(d))
	return cmd
}

func newDestinationListCmd(d deps) *cobra.Command {
	var managed, own bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List storage destinations",
		Long: `Lists the organization's storage destinations: storage Norcube manages,
and buckets of your own. A backup policy names one with --destination.`,
		Example: `  norcube backup destinations list
  norcube backup destinations list --managed -o json --query "[0].id"`,
		Args: cobra.NoArgs,
		RunE: cmdutil.Run(func(cmd *cobra.Command, _ []string) (any, error) {
			params := &snapdb.GetStorageDestinationsParams{}
			switch {
			case managed:
				o := snapdb.Managed
				params.Ownership = &o
			case own:
				o := snapdb.Byo
				params.Ownership = &o
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.GetStorageDestinationsWithResponse(cmd.Context(), params)
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON500)
			}
			return output.Table[snapdb.PolicyhandlerPayloadStorageDestinationResponse]{
				Headers:   []string{"NAME", "TYPE", "MANAGED", "REGION", "BUCKET", "ID"},
				MaxWidths: []int{32, 0, 0, 0, 40, 0},
				Rows: func(s snapdb.PolicyhandlerPayloadStorageDestinationResponse) []string {
					return []string{
						cmdutil.Deref(s.Name), cmdutil.Deref(s.Type), yesNo(cmdutil.Deref(s.IsManaged)),
						cmdutil.Deref(s.Region), cmdutil.Deref(s.Bucket), cmdutil.Deref(s.Id),
					}
				},
				Items: *res.JSON200,
			}, nil
		}),
	}
	cmd.Flags().BoolVar(&managed, "managed", false, "Only storage Norcube manages")
	cmd.Flags().BoolVar(&own, "own", false, "Only your own buckets")
	cmd.MarkFlagsMutuallyExclusive("managed", "own")
	return cmd
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
