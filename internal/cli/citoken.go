package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api"
	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/orgs"
	"github.com/norcubeplatform/cli/internal/output"
)

// CI tokens: organization-scoped secrets for automation, the same as
// IAM › CI tokens in the dashboard. Managing them needs your own sign-in;
// using one only needs NORCUBE_TOKEN.
func newAuthCITokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ci-token",
		Aliases: []string{"ci-tokens"},
		Short:   "Create, list and revoke CI tokens for automation",
		Long: `A CI token lets a CI job or script call Norcube without a person's login.
It belongs to an organization, carries only the scopes you give it, and
always expires. Set it as NORCUBE_TOKEN (or pass --token-file) and every
command uses it:

  NORCUBE_TOKEN=nrc_ci_… norcube langsync sync

The token keeps working when the person who created it leaves the
organization. Creating one needs the IAM permission to manage CI tokens,
and you can only grant scopes your own role allows.`,
	}
	cmd.AddCommand(newCITokenCreateCmd(), newCITokenListCmd(), newCITokenRevokeCmd(), newCITokenScopesCmd())
	return cmd
}

// CI token lifetimes Norcube Auth accepts.
const (
	ciTokenMinLifetime     = time.Hour
	ciTokenMaxLifetime     = 366 * 24 * time.Hour
	ciTokenDefaultLifetime = 90 * 24 * time.Hour
)

func newCITokenCreateCmd() *cobra.Command {
	var name, expires string
	var scopes []string
	cmd := &cobra.Command{
		Use:   "create --name <name> --scopes <scope,...> [--expires 90d]",
		Short: "Create a CI token; the secret is printed once",
		Long: `Creates a CI token in the organization (--org, else the profile's) and
prints its secret. Norcube stores only a hash: copy the secret into your CI
system's secrets now, as NORCUBE_TOKEN.

--scopes takes scope names from ` + "`norcube auth ci-token scopes`" + `, for example
langsync.sync (run ` + "`norcube langsync sync`" + `) or backup.read. --expires takes a
duration in hours, days or weeks (12h, 90d, 4w) or a date (2027-01-31); the
default is 90 days, the maximum 366.

With -o text only the secret is printed, so it can go straight into a
secret store:

  norcube auth ci-token create --name deploy --scopes langsync.sync -o text | gh secret set NORCUBE_TOKEN`,
		Example: `  norcube auth ci-token create --name "GitHub Actions" --scopes langsync.sync
  norcube auth ci-token create --name nightly-backup-check --scopes backup.read --expires 30d
  norcube auth ci-token create --name deploy --scopes langsync.sync -o text | gh secret set NORCUBE_TOKEN`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			name = strings.TrimSpace(name)
			if name == "" {
				return clierr.Usage(fmt.Errorf("--name is required: say where the token is used, e.g. \"GitHub Actions\""))
			}
			scopes = cleanScopes(scopes)
			if len(scopes) == 0 {
				return clierr.Usage(fmt.Errorf("--scopes is required; `norcube auth ci-token scopes` lists them"))
			}
			expiresAt, err := parseExpiry(expires, time.Now())
			if err != nil {
				return clierr.Usage(err)
			}
			client, org, err := ciTokenClient(cmd)
			if err != nil {
				return err
			}
			created, err := client.CreateCIToken(cmd.Context(), name, scopes, expiresAt)
			if err != nil {
				return err
			}
			msg := fmt.Sprintf(`Created CI token %q in %s, expires %s.
Scopes: %s

  %s

This is the only time the token is shown. Store it in your CI system as the
secret NORCUBE_TOKEN; every norcube command there will use it.`,
				created.Token.Name, orgDisplay(org), output.When(created.Token.ExpiresAt),
				strings.Join(created.Token.Scopes, ", "), created.Secret)
			return cmdutil.Print(cmd, output.Done{
				Message: msg,
				Value:   created,
				Fields:  []output.Field{{Value: created.Secret}},
			})
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "What the token is for, e.g. \"GitHub Actions deploy\" (unique among the organization's live tokens)")
	cmd.Flags().StringSliceVar(&scopes, "scopes", nil, "Comma-separated scopes (see `norcube auth ci-token scopes`)")
	cmd.Flags().StringVar(&expires, "expires", "90d", "Lifetime (12h, 90d, 4w) or expiry date (2027-01-31); at most 366 days")
	return cmd
}

func newCITokenListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the organization's CI tokens",
		Example: `  norcube auth ci-token list
  norcube auth ci-token list -o json --query "[?status=='active'].name"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, _, err := ciTokenClient(cmd)
			if err != nil {
				return err
			}
			list, err := client.ListCITokens(cmd.Context())
			if err != nil {
				return err
			}
			return cmdutil.Print(cmd, output.Table[api.CIToken]{
				Headers: []string{"NAME", "SCOPES", "STATUS", "EXPIRES", "LAST USED", "TOKEN", "ID"},
				Rows: func(t api.CIToken) []string {
					last := "never"
					if t.LastUsedAt != nil {
						last = output.When(*t.LastUsedAt)
					}
					return []string{
						t.Name,
						strings.Join(t.Scopes, ","),
						t.Status,
						output.When(t.ExpiresAt),
						last,
						"…" + t.Preview,
						t.ID,
					}
				},
				Items: list,
				Style: output.NoStyle,
			})
		},
	}
}

func newCITokenRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <id|name>",
		Short: "Revoke a CI token; jobs using it fail from then on",
		Long: `Revokes a CI token by its id or its name. Norcube Auth refuses the token
from then on: a job that already holds an access token from it can finish
its current requests (access tokens live at most 5 minutes), and the next
exchange fails.`,
		Example: `  norcube auth ci-token revoke "GitHub Actions deploy"
  norcube auth ci-token revoke 0b7a4c8e-2d3f-4a1b-9c6d-5e4f3a2b1c0d`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := ciTokenClient(cmd)
			if err != nil {
				return err
			}
			id := args[0]
			if !orgs.IsID(id) {
				list, err := client.ListCITokens(cmd.Context())
				if err != nil {
					return err
				}
				var match []api.CIToken
				for _, t := range list {
					if t.Status == "active" && strings.EqualFold(t.Name, id) {
						match = append(match, t)
					}
				}
				if len(match) != 1 {
					return &clierr.Error{Code: clierr.CodeNotFound, Message: fmt.Sprintf("no active CI token named %q", id), Hint: "`norcube auth ci-token list` shows the names and ids."}
				}
				id = match[0].ID
			}
			t, err := client.RevokeCIToken(cmd.Context(), id)
			if err != nil {
				return err
			}
			return cmdutil.Print(cmd, output.Done{
				Message: fmt.Sprintf("Revoked CI token %q.", t.Name),
				Value:   t,
				Fields:  []output.Field{{Value: t.ID}},
			})
		},
	}
}

func newCITokenScopesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "scopes",
		Short: "List the scopes a CI token can have, and which you may grant",
		Example: `  norcube auth ci-token scopes
  norcube auth ci-token scopes -o json --query "[?available].scope"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, _, err := ciTokenClient(cmd)
			if err != nil {
				return err
			}
			list, err := client.ListCITokenScopes(cmd.Context())
			if err != nil {
				return err
			}
			return cmdutil.Print(cmd, output.Table[api.CITokenScope]{
				Headers: []string{"SCOPE", "PRODUCT", "ACCESS", "YOU MAY GRANT", "DESCRIPTION"},
				Rows: func(s api.CITokenScope) []string {
					access := "read"
					if s.Write {
						access = "write"
					}
					grant := "no"
					if s.Available {
						grant = "yes"
					}
					return []string{s.Scope, s.Product, access, grant, s.Description}
				},
				Items: list,
				Style: output.NoStyle,
			})
		},
	}
}

// ciTokenClient is a Norcube Auth client for the organization whose CI
// tokens are managed (--org, else the profile's), with the user's own
// session: a CI token can't manage CI tokens.
func ciTokenClient(cmd *cobra.Command) (*api.AuthClient, api.Organization, error) {
	flags := clictx.Get(cmd)
	cfg, err := flags.Config()
	if err != nil {
		return nil, api.Organization{}, err
	}
	org, err := flags.Org(cmd.Context(), cfg)
	if err != nil {
		return nil, api.Organization{}, err
	}
	if org.ID == "" {
		return nil, api.Organization{}, clierr.Usage(fmt.Errorf("no organization. Run `norcube org use <slug>` or pass --org <slug>"))
	}
	client, err := flags.AuthClient(cfg, org.ID)
	return client, org, err
}

func orgDisplay(o api.Organization) string {
	if o.Slug != "" {
		return o.Slug
	}
	return o.ID
}

// cleanScopes trims, drops empties and de-duplicates scope names.
func cleanScopes(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.ToLower(strings.TrimSpace(s))
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// parseExpiry reads --expires: a lifetime (12h, 90d, 4w) or an expiry date
// (2027-01-31, or an RFC 3339 time), checked against the lifetimes Norcube
// Auth accepts.
func parseExpiry(v string, now time.Time) (time.Time, error) {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "" {
		return now.Add(ciTokenDefaultLifetime), nil
	}
	var at time.Time
	if n, unit := v[:len(v)-1], v[len(v)-1]; strings.ContainsRune("hdw", rune(unit)) {
		count, err := strconv.Atoi(n)
		if err != nil || count <= 0 {
			return time.Time{}, fmt.Errorf("--expires %q: use a lifetime like 12h, 90d or 4w, or a date like 2027-01-31", v)
		}
		per := map[byte]time.Duration{'h': time.Hour, 'd': 24 * time.Hour, 'w': 7 * 24 * time.Hour}[unit]
		at = now.Add(time.Duration(count) * per)
	} else if d, err := time.ParseInLocation("2006-01-02", v, time.Local); err == nil {
		at = d.Add(24*time.Hour - time.Second) // through the end of that day
	} else if t, err := time.Parse(time.RFC3339, strings.ToUpper(v)); err == nil {
		at = t
	} else {
		return time.Time{}, fmt.Errorf("--expires %q: use a lifetime like 12h, 90d or 4w, or a date like 2027-01-31", v)
	}
	switch life := at.Sub(now); {
	case life < ciTokenMinLifetime:
		return time.Time{}, fmt.Errorf("--expires %q: a CI token must live at least an hour", v)
	case life > ciTokenMaxLifetime:
		return time.Time{}, fmt.Errorf("--expires %q: a CI token can live at most 366 days; create a new one when it expires", v)
	}
	return at, nil
}
