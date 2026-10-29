package domainradar

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/domainradar"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

func newSavedCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "saved",
		Aliases: []string{"saved-domains"},
		Short:   "Manage the organization's saved domains (its watchlist)",
		Long: `Saved domains are a watchlist: each keeps its latest check, so you can
see at a glance which names are still free. Saving needs a token with
DomainRadar manage access; CI tokens with only domainradar.check can list
but not change the list.`,
	}
	cmd.AddCommand(newSavedListCmd(d), newSavedAddCmd(d), newSavedRemoveCmd(d))
	return cmd
}

// savedSorts are the --sort values the API accepts.
var savedSorts = []string{
	string(domainradar.Domain),
	string(domainradar.CreatedAt),
	string(domainradar.ExpiryDate),
	string(domainradar.Registrar),
}

func newSavedListCmd(d deps) *cobra.Command {
	var (
		available, registered bool
		search, registrar     string
		tlds                  []string
		sortBy, sortDir       string
		pages                 cmdutil.PageFlags
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List saved domains with their latest check",
		Example: `  norcube domainradar saved list
  norcube domainradar saved list --available --tld com
  norcube domainradar saved list --sort expiry_date -o json --query "[].domain"`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if sortBy != "" && !slices.Contains(savedSorts, sortBy) {
				return clierr.Usage(fmt.Errorf("--sort must be one of %s", strings.Join(savedSorts, ", ")))
			}
			if sortDir != "asc" && sortDir != "desc" {
				return clierr.Usage(fmt.Errorf("--order must be asc or desc"))
			}
			c, err := d.client(cmd)
			if err != nil {
				return err
			}
			f := resultFilters{available: available, registered: registered, search: search, tlds: tlds}
			page, err := cmdutil.Paginate(cmd.Context(), pages, func(ctx context.Context, cursor string, size int) ([]domainradar.DtoSavedDomain, string, error) {
				params := &domainradar.GetDomainsSavedParams{
					Limit: &size, Available: f.availability(), Search: f.searchParam(), Tld: f.tldParam(),
				}
				if registrar != "" {
					params.Registrar = &registrar
				}
				if sortBy != "" {
					s := domainradar.GetDomainsSavedParamsSortBy(sortBy)
					o := domainradar.GetDomainsSavedParamsSortDir(sortDir)
					params.SortBy, params.SortDir = &s, &o
				}
				if cursor != "" {
					params.Cursor = &cursor
				}
				res, err := c.API.GetDomainsSavedWithResponse(ctx, params)
				if err != nil {
					return nil, "", err
				}
				if res.JSON200 == nil {
					return nil, "", apiError(res.HTTPResponse, res.Body)
				}
				return res.JSON200.List, nextCursor(res.JSON200.Cursors), nil
			})
			if err != nil {
				return err
			}
			return page.Print(cmd, output.Table[domainradar.DtoSavedDomain]{
				Headers:   []string{"DOMAIN", "AVAILABILITY", "EXPIRES", "REGISTRAR", "CHECKED", "NOTES"},
				MaxWidths: []int{0, 0, 0, 28, 0, 40},
				Style:     output.Style{StatusColumn: 1},
				Rows: func(s domainradar.DtoSavedDomain) []string {
					var last domainradar.DtoDomainCheck
					if s.LatestCheck != nil {
						last = *s.LatestCheck
					}
					return []string{
						cmdutil.Deref(s.Domain),
						availability(last),
						dateOnly(cmdutil.Deref(last.ExpiryDate)),
						cmdutil.Deref(last.Registrar),
						output.WhenString(cmdutil.Deref(last.CheckedAt)),
						cmdutil.Deref(s.Notes),
					}
				},
				Items: page.Items,
			})
		},
	}
	cmd.Flags().BoolVar(&available, "available", false, "Only domains that were available at their last check")
	cmd.Flags().BoolVar(&registered, "registered", false, "Only domains that were registered at their last check")
	cmd.MarkFlagsMutuallyExclusive("available", "registered")
	cmd.Flags().StringVar(&search, "search", "", "Only domains containing this text")
	cmd.Flags().StringVar(&registrar, "registrar", "", "Only domains with this registrar")
	cmd.Flags().StringSliceVar(&tlds, "tld", nil, "Only these TLDs (repeat or comma-separate)")
	cmd.Flags().StringVar(&sortBy, "sort", "", "Sort by "+strings.Join(savedSorts, ", ")+" (created_at is when it was saved; default: newest saved first)")
	cmd.Flags().StringVar(&sortDir, "order", "asc", "Sort order with --sort: asc or desc")
	_ = cmd.RegisterFlagCompletionFunc("sort", cobra.FixedCompletions(savedSorts, cobra.ShellCompDirectiveNoFileComp))
	cmdutil.AddPageFlags(cmd, &pages, defaultPageSize, maxItems)
	return cmd
}

func newSavedAddCmd(d deps) *cobra.Command {
	var notes string
	cmd := &cobra.Command{
		Use:   "add <domain>",
		Short: "Save a domain to the watchlist",
		Example: `  norcube domainradar saved add acme.io
  norcube domainradar saved add acme.io --notes "ask legal first"`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			domain := strings.ToLower(strings.TrimSpace(args[0]))
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			body := domainradar.PostDomainsSavedJSONRequestBody{Domain: &domain}
			if notes != "" {
				body.Notes = &notes
			}
			res, err := c.API.PostDomainsSavedWithResponse(cmd.Context(), body)
			if err != nil {
				return nil, err
			}
			if res.StatusCode() != http.StatusNoContent && res.StatusCode() != http.StatusOK {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			return output.Done{
				Message: fmt.Sprintf("Saved %s.", domain),
				Value:   map[string]any{"domain": domain, "saved": true, "notes": notes},
				Fields:  []output.Field{{Value: domain}},
			}, nil
		}),
	}
	cmd.Flags().StringVar(&notes, "notes", "", "A note to keep with the domain")
	return cmd
}

func newSavedRemoveCmd(d deps) *cobra.Command {
	return &cobra.Command{
		Use:     "remove <domain>",
		Aliases: []string{"rm"},
		Short:   "Remove a domain from the watchlist",
		Example: `  norcube domainradar saved remove acme.io`,
		Args:    cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			domain := strings.ToLower(strings.TrimSpace(args[0]))
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.DeleteDomainsSavedDomainWithResponse(cmd.Context(), domain)
			if err != nil {
				return nil, err
			}
			if res.StatusCode() != http.StatusNoContent && res.StatusCode() != http.StatusOK {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			return output.Done{
				Message: fmt.Sprintf("Removed %s from the saved domains.", domain),
				Value:   map[string]any{"domain": domain, "saved": false},
				Fields:  []output.Field{{Value: domain}},
			}, nil
		}),
	}
}
