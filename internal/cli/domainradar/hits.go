package domainradar

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/domainradar"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

func newHitsCmd(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "hits",
		Aliases: []string{"hit", "brand-hits"},
		Short:   "List lookalike domains your brand watchers found",
		Long: `A hit is a domain from a newly issued TLS certificate that looks like a
watched brand word, scored 0-100 by how likely it is to be an
impersonation attempt.`,
	}
	cmd.AddCommand(newHitListCmd(d), newHitGetCmd(d))
	return cmd
}

func newHitListCmd(d deps) *cobra.Command {
	var (
		watchers           []string
		minScore, maxScore int
		search             string
		pages              cmdutil.PageFlags
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List hits, newest first",
		Example: `  norcube domainradar hits list
  norcube domainradar hits list --watcher 7d2e… --min-score 70
  norcube domainradar hits list --all-pages -o json --query "[?aiRisk=='high'].domain"`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if minScore < 0 || maxScore < 0 || minScore > 100 || maxScore > 100 || (maxScore > 0 && minScore > maxScore) {
				return clierr.Usage(fmt.Errorf("--min-score and --max-score must be between 0 and 100, min not above max"))
			}
			c, err := d.client(cmd)
			if err != nil {
				return err
			}
			page, err := cmdutil.Paginate(cmd.Context(), pages, func(ctx context.Context, cursor string, size int) ([]domainradar.DtoMonitorHit, string, error) {
				params := &domainradar.ListBrandHitsParams{Limit: &size}
				if len(watchers) > 0 {
					params.WatcherId = &watchers
				}
				if cmd.Flags().Changed("min-score") {
					params.ScoreMin = &minScore
				}
				if cmd.Flags().Changed("max-score") {
					params.ScoreMax = &maxScore
				}
				if search != "" {
					params.Search = &search
				}
				if cursor != "" {
					params.Cursor = &cursor
				}
				res, err := c.API.ListBrandHitsWithResponse(ctx, params)
				if err != nil {
					return nil, "", err
				}
				if res.JSON200 == nil {
					return nil, "", apiError(res.HTTPResponse, res.Body)
				}
				return cmdutil.Deref(res.JSON200.List), cmdutil.Deref(res.JSON200.Next), nil
			})
			if err != nil {
				return err
			}
			return page.Print(cmd, output.Table[domainradar.DtoMonitorHit]{
				Headers:   []string{"DOMAIN", "SCORE", "AI RISK", "KEYWORD", "ISSUER", "SEEN", "ID"},
				MaxWidths: []int{48, 0, 0, 0, 28, 0, 0},
				Rows: func(h domainradar.DtoMonitorHit) []string {
					return []string{
						cmdutil.Deref(h.Domain),
						fmt.Sprintf("%d %s", cmdutil.Deref(h.Score), cmdutil.Deref(h.ScoreLevel)),
						string(cmdutil.Deref(h.AiRisk)),
						cmdutil.Deref(h.Keyword),
						cmdutil.Deref(h.Issuer),
						output.WhenString(cmdutil.Deref(h.CreatedAt)),
						cmdutil.Deref(h.Id),
					}
				},
				Items: page.Items,
			})
		},
	}
	cmd.Flags().StringSliceVar(&watchers, "watcher", nil, "Only hits of these watchers (repeat or comma-separate ids)")
	cmd.Flags().IntVar(&minScore, "min-score", 0, "Only hits scoring at least this (0-100)")
	cmd.Flags().IntVar(&maxScore, "max-score", 0, "Only hits scoring at most this (0-100)")
	cmd.Flags().StringVar(&search, "search", "", "Only hits whose domain contains this text")
	cmdutil.AddPageFlags(cmd, &pages, defaultPageSize, maxItems)
	return cmd
}

func newHitGetCmd(d deps) *cobra.Command {
	return &cobra.Command{
		Use:     "get <hit-id>",
		Short:   "Show a hit with its score and AI review",
		Example: `  norcube domainradar hits get 91ab…`,
		Args:    cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.GetBrandHitWithResponse(cmd.Context(), args[0])
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			h := *res.JSON200
			return output.Object{Value: h, Fields: []output.Field{
				{Name: "Domain", Value: cmdutil.Deref(h.Domain)},
				{Name: "Score", Value: fmt.Sprintf("%d (%s)", cmdutil.Deref(h.Score), cmdutil.Deref(h.ScoreLevel))},
				{Name: "Match", Value: cmdutil.Deref(h.MatchReason)},
				{Name: "Keyword", Value: cmdutil.Deref(h.Keyword)},
				{Name: "Watcher", Value: cmdutil.Deref(h.WatcherId)},
				{Name: "Issuer", Value: cmdutil.Deref(h.Issuer)},
				{Name: "Registered", Value: dateOnly(cmdutil.Deref(h.RegistrationDate))},
				{Name: "AI risk", Value: string(cmdutil.Deref(h.AiRisk))},
				{Name: "AI review", Value: cmdutil.Deref(h.AiReasoning)},
				{Name: "Other names", Value: joinList(h.AllDomains)},
				{Name: "Seen", Value: output.WhenString(cmdutil.Deref(h.CreatedAt))},
				{Name: "ID", Value: cmdutil.Deref(h.Id)},
			}}, nil
		}),
	}
}
