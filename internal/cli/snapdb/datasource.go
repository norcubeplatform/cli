package snapdb

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/snapdb"
	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/output"
)

func newDataSourceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "datasource",
		Aliases: []string{"datasources", "ds"},
		Short:   "Manage SnapDB data sources",
	}
	cmd.AddCommand(
		newDataSourceListCmd(),
		newDataSourceGetCmd(),
		newDataSourcePauseCmd(),
		newDataSourceResumeCmd(),
	)
	return cmd
}

func newDataSourcePauseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pause [id]",
		Short: "Halt every backup policy attached to a data source (master switch)",
		Long: `Sets the data source's isActive flag to false. The scheduler stops
enqueuing jobs for any policy attached to this data source until you run
"resume". History and attachments are preserved.

If no id is given and stdin is a terminal, an interactive picker is shown.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setDataSourceActive(cmd, args, false, "Pause which data source?", "Paused %q.")
		},
	}
}

func newDataSourceResumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resume [id]",
		Short: "Re-enable a previously paused data source",
		Long: `Sets the data source's isActive flag back to true. The scheduler will
pick up its attached policies on the next tick.

If no id is given and stdin is a terminal, an interactive picker is shown.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setDataSourceActive(cmd, args, true, "Resume which data source?", "Resumed %q.")
		},
	}
}

// setDataSourceActive is the common implementation for pause/resume: same
// PATCH /datasources/:id with different isActive value, same picker
// fallback, same success message shape.
func setDataSourceActive(cmd *cobra.Command, args []string, active bool, pickerTitle, successFmt string) error {
	c, err := newSnapdbContext(cmd)
	if err != nil {
		return err
	}
	id, pickedName, err := resolveDataSourceID(cmd.Context(), c, args, pickerTitle)
	if err != nil {
		if err == ErrCancelled {
			fmt.Fprintln(cmd.OutOrStdout(), "Cancelled.")
			return nil
		}
		return err
	}

	res, err := c.client.UpdateDataSourceWithResponse(cmd.Context(), id, snapdb.UpdateDataSourceJSONRequestBody{
		IsActive: &active,
	})
	if err != nil {
		return err
	}
	if res.HTTPResponse.StatusCode != 204 && res.HTTPResponse.StatusCode != 200 {
		return apiError(res.HTTPResponse, res.Body, nil, nil)
	}

	// If we already have the name from the picker, use it; otherwise fall
	// back to the id so the success line is still useful.
	display := pickedName
	if display == "" {
		display = id
	}
	fmt.Fprintf(cmd.OutOrStdout(), successFmt+"\n", display)
	return nil
}

func newDataSourceListCmd() *cobra.Command {
	var query string
	var cursor string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List data sources in the active organization",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newSnapdbContext(cmd)
			if err != nil {
				return err
			}

			params := &snapdb.ListParams{}
			if query != "" {
				params.Query = &query
			}
			if cursor != "" {
				params.Cursor = &cursor
			}

			res, err := c.client.ListWithResponse(cmd.Context(), params)
			if err != nil {
				return err
			}
			if res.JSON200 == nil {
				return apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON500)
			}

			items := res.JSON200.List
			flags := clictx.Get(cmd)
			return output.PrintPaged(cmd.OutOrStdout(), c.output, flags.NoPager, output.Table[snapdb.DtoDataSource]{
				Headers:   []string{"NAME", "ENGINE", "ENV", "ACTIVE", "ID"},
				MaxWidths: []int{40, 0, 0, 0, 0},
				Style:     output.Style{StatusColumn: 3}, // ACTIVE (yes/no rendered via known statuses)
				Rows: func(d snapdb.DtoDataSource) []string {
					return []string{
						d.Name,
						d.Engine,
						d.Environment,
						activeStr(d.IsActive),
						d.Id,
					}
				},
				Items: items,
			})
		},
	}
	cmd.Flags().StringVar(&query, "query", "", "Filter data sources by name substring")
	cmd.Flags().StringVar(&cursor, "cursor", "", "Cursor for the next page (from a prior list response)")
	return cmd
}

func newDataSourceGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Show a single data source",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newSnapdbContext(cmd)
			if err != nil {
				return err
			}
			res, err := c.client.GetWithResponse(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			// /datasources/:id has no typed 200 wrapper for a single item in
			// this generation pass (the swagger response schema is empty).
			// Fall back to printing the body verbatim.
			if res.HTTPResponse.StatusCode != 200 {
				return fmt.Errorf("snapdb returned %d: %s", res.HTTPResponse.StatusCode, string(res.Body))
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(res.Body))
			return nil
		},
	}
}

func activeStr(b bool) string {
	if b {
		return "active"
	}
	return "inactive"
}
