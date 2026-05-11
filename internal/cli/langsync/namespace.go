package langsync

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/langsync"
	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/output"
)

func newNamespaceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "namespace",
		Aliases: []string{"namespaces", "ns"},
		Short:   "Manage Langsync namespaces in the active organization",
	}
	cmd.AddCommand(newNamespaceListCmd())
	return cmd
}

func newNamespaceListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List namespaces in the active organization",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return err
			}
			res, err := c.client.ListNamespacesWithResponse(cmd.Context())
			if err != nil {
				return err
			}
			if res.JSON200 == nil {
				return apiError(res.HTTPResponse, res.Body,
					res.JSON400, res.JSON401, res.JSON403, res.JSON404, res.JSON500)
			}
			items := *res.JSON200

			flags := clictx.Get(cmd)
			return output.PrintPaged(cmd.OutOrStdout(), c.output, flags.NoPager, output.Table[langsync.DtoDTONamespace]{
				Headers:   []string{"NAME", "DEFAULT_LANG", "LANGUAGES", "CONTEXT", "ID"},
				MaxWidths: []int{32, 0, 0, 40, 0},
				Rows: func(n langsync.DtoDTONamespace) []string {
					return []string{
						deref(n.Name),
						intStr(n.DefaultLanguageId),
						languageCount(n.Languages),
						deref(n.Context),
						deref(n.Id),
					}
				},
				Items: items,
			})
		},
	}
}

func deref(p *string) string {
	if p == nil || *p == "" {
		return "—"
	}
	return *p
}

func intStr(p *int) string {
	if p == nil {
		return "—"
	}
	return fmt.Sprintf("%d", *p)
}

func languageCount(p *[]string) string {
	if p == nil {
		return "0"
	}
	return fmt.Sprintf("%d", len(*p))
}
