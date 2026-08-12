package backup

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/snapdb"
	"github.com/norcubeplatform/cli/internal/output"
)

// newHealthCmd renders restore health across the organization: for each
// datasource, how many of the recent restore tests proved the backups
// recoverable. This is the CLI face of the dashboard's Restore-health
// card, and the answer to "can we actually restore?" without opening a
// browser.
func newHealthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "health",
		Short: "Restore health per datasource: do the backups actually restore?",
		Long: `Show restore-test health for every datasource in the active organization.

Each datasource's score aggregates its recent restore tests: automated
drills that recover a real backup into a throwaway database, confirm the
data came back, and tear it down. A datasource with no tests yet shows a
dash; enable restore testing in the dashboard or run one now with
` + "`norcube backup restore-test run`" + `.`,
		Example: `  # Org-wide restore health
  norcube backup health

  # Machine-readable, e.g. for a CI gate
  norcube backup health -o json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newSnapdbContext(cmd)
			if err != nil {
				return err
			}

			res, err := c.client.ListWithResponse(cmd.Context(), &snapdb.ListParams{})
			if err != nil {
				return err
			}
			if res.JSON200 == nil {
				return apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON500)
			}

			items := res.JSON200.List
			return output.PrintPaged(cmd.OutOrStdout(), c.output, c.flags.NoPager, output.Table[snapdb.DtoDataSource]{
				Headers:   []string{"DATASOURCE", "ENGINE", "HEALTH", "TESTS", "WARNINGS", "LAST TESTED"},
				MaxWidths: []int{32, 0, 0, 0, 0, 0},
				Rows: func(d snapdb.DtoDataSource) []string {
					return healthRow(d)
				},
				Items: items,
			})
		},
	}
	return cmd
}

// healthRow renders one datasource's restore-health columns. A datasource
// without any terminal restore tests renders dashes rather than a fake
// 0 % — "never tested" and "failing" must not look alike.
func healthRow(d snapdb.DtoDataSource) []string {
	name := d.Name
	engine := d.Engine

	total := derefInt(d.RestoreCheckTotal)
	passed := derefInt(d.RestoreCheckPassed)
	warnings := derefInt(d.RestoreCheckWarnings)
	if total == 0 {
		return []string{name, engine, "—", "—", "—", "—"}
	}
	score := fmt.Sprintf("%d%%", int(float64(passed)/float64(total)*100+0.5))
	tests := fmt.Sprintf("%d/%d passed", passed, total)
	warn := "0"
	if warnings > 0 {
		warn = fmt.Sprintf("%d", warnings)
	}
	return []string{name, engine, score, tests, warn, formatTimestamp(d.RestoreCheckLastAt)}
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
