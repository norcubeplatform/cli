package billing

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/billing"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

func newInvoicesCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "invoices",
		Aliases: []string{"invoice"},
		Short:   "List invoices and download them as PDF",
	}
	cmd.AddCommand(newInvoiceListCmd(d), newInvoiceDownloadCmd(d))
	return cmd
}

func newInvoiceListCmd(d deps) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the organization's invoices",
		Long: `Lists the organization's invoices with their billing period, status and
total. Download one with ` + "`norcube billing invoices download <id>`" + `.`,
		Example: `  norcube billing invoices list
  norcube billing invoices list -o json --query "[].{id: id, total: total, currency: currency}"`,
		Args: cobra.NoArgs,
		RunE: cmdutil.Run(func(cmd *cobra.Command, _ []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.ListInvoicesWithResponse(cmd.Context())
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON500)
			}
			if cmdutil.Deref(res.JSON200.Cursors.Next) != "" && cmdutil.Human(cmd) {
				fmt.Fprintln(cmdutil.Stderr(cmd), "Older invoices are in the dashboard.")
			}
			items := res.JSON200.List
			if items == nil {
				items = []billing.DtoInvoice{}
			}
			return output.Table[billing.DtoInvoice]{
				Headers: []string{"ID", "PERIOD", "STATUS", "SUBTOTAL", "TAX", "TOTAL", "BILLED_TO"},
				Style:   output.Style{StatusColumn: 2},
				Rows: func(i billing.DtoInvoice) []string {
					return []string{
						i.Id, dateOnly(i.PeriodStart) + " – " + dateOnly(i.PeriodEnd), string(i.Status),
						money(i.Subtotal, i.Currency), money(i.Tax, i.Currency), money(i.Total, i.Currency), i.BillingName,
					}
				},
				Items: items,
			}, nil
		}),
	}
}

func newInvoiceDownloadCmd(d deps) *cobra.Command {
	var to string
	cmd := &cobra.Command{
		Use:   "download <invoice-id>",
		Short: "Download an invoice as PDF",
		Long: `Saves the invoice as a PDF file, invoice-<id>.pdf in the current
directory unless --file names another (- writes it to standard output).
An existing file is never overwritten.`,
		Example: `  norcube billing invoices download 1042
  norcube billing invoices download 1042 --file ~/Documents/norcube-2026-10.pdf`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			id, err := strconv.Atoi(strings.TrimSpace(args[0]))
			if err != nil || id <= 0 {
				return nil, clierr.Usage(fmt.Errorf("invoice id must be a positive number (got %q); see `norcube billing invoices list`", args[0]))
			}
			dest := to
			if dest == "" {
				dest = fmt.Sprintf("invoice-%d.pdf", id)
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			api, err := d.streaming(c)
			if err != nil {
				return nil, err
			}
			resp, err := api.GetInvoicePDF(cmd.Context(), id)
			if err != nil {
				return nil, err
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
				return nil, apiError(resp, body)
			}
			saved, err := cmdutil.SaveStream(cmd, resp.Body, dest, 0o600)
			if err != nil || saved == nil {
				return nil, err
			}
			return output.Done{
				Message: fmt.Sprintf("Saved invoice %d to %s.", id, saved.Path),
				Value:   saved,
				Fields:  []output.Field{{Value: saved.Path}, {Value: fmt.Sprint(saved.Bytes)}, {Value: saved.SHA256}},
			}, nil
		}),
	}
	cmd.Flags().StringVar(&to, "file", "", "File to write (- for standard output; default invoice-<id>.pdf)")
	return cmd
}
