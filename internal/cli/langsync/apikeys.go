package langsync

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/langsync"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

func newAPIKeysCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "api-keys",
		Aliases: []string{"api-key", "apikeys"},
		Short:   "Manage API keys your applications use to fetch translations",
		Long: `API keys authenticate your applications to the read-only LangSync
Public API, which serves translations at runtime. They belong to the
organization, don't expire and keep working when people leave; revoke
one to stop it.

They are not for the CLI: in CI, use a CI token
(` + "`norcube auth ci-token create`" + `).`,
	}
	cmd.AddCommand(newAPIKeyListCmd(), newAPIKeyCreateCmd(), newAPIKeyRevokeCmd())
	return cmd
}

func apiKeyStatus(k langsync.DtoAPIKey) string {
	if k.RevokedAt != nil && *k.RevokedAt != "" {
		return "revoked"
	}
	return "active"
}

// apiKeyView is an API key as the CLI prints it: without the key's hash,
// which the API also returns but nobody needs.
type apiKeyView struct {
	ID         string  `json:"id" yaml:"id"`
	Name       string  `json:"name" yaml:"name"`
	Preview    string  `json:"preview,omitempty" yaml:"preview,omitempty"`
	Status     string  `json:"status" yaml:"status"`
	CreatedAt  string  `json:"createdAt" yaml:"createdAt"`
	CreatedBy  string  `json:"createdBy" yaml:"createdBy"`
	LastUsedAt *string `json:"lastUsedAt" yaml:"lastUsedAt"`
	RevokedAt  *string `json:"revokedAt" yaml:"revokedAt"`
	RevokedBy  *string `json:"revokedBy,omitempty" yaml:"revokedBy,omitempty"`
}

func toAPIKeyView(k langsync.DtoAPIKey) apiKeyView {
	return apiKeyView{
		ID: k.Id, Name: k.Name, Preview: cmdutil.Deref(k.Preview), Status: apiKeyStatus(k),
		CreatedAt: k.CreatedAt, CreatedBy: k.CreatedBy, LastUsedAt: k.LastUsedAt,
		RevokedAt: k.RevokedAt, RevokedBy: k.RevokedBy,
	}
}

func newAPIKeyListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the organization's API keys",
		Long: `Lists active and revoked keys. LAST USED shows whether a key is still
in use, which tells you when a rotation is complete.`,
		Example: `  norcube langsync api-keys list
  norcube langsync api-keys list -o json --query "[?status=='active'].name"`,
		Args: cobra.NoArgs,
		RunE: cmdutil.Run(func(cmd *cobra.Command, _ []string) (any, error) {
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.ListAPIKeysWithResponse(cmd.Context())
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON401, res.JSON403, res.JSON404, res.JSON500)
			}
			keys := make([]apiKeyView, len(res.JSON200.List))
			for i, k := range res.JSON200.List {
				keys[i] = toAPIKeyView(k)
			}
			return output.Table[apiKeyView]{
				Headers: []string{"ID", "NAME", "PREVIEW", "STATUS", "CREATED", "LAST USED"},
				Style:   output.Style{StatusColumn: 3},
				Rows: func(k apiKeyView) []string {
					return []string{
						k.ID, k.Name, k.Preview, k.Status,
						compactDate(k.CreatedAt), compactDate(cmdutil.Deref(k.LastUsedAt)),
					}
				},
				Items: keys,
			}, nil
		}),
	}
}

// apiKeyCreated is what `api-keys create -o json` prints, the key's value
// included (it is never shown again).
type apiKeyCreated struct {
	ID    string `json:"id" yaml:"id"`
	Name  string `json:"name" yaml:"name"`
	Value string `json:"value" yaml:"value"`
}

func newAPIKeyCreateCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create an API key; its value is printed once",
		Long: `Creates an API key and prints its value. LangSync stores only a hash:
copy the value into your application's secrets now. Name the key after
where it will live.

With -o text only the value is printed, so it can go straight into a
secret store.`,
		Example: `  norcube langsync api-keys create --name production-web
  norcube langsync api-keys create --name production-web -o text | gh secret set LANGSYNC_API_KEY`,
		Args: cobra.NoArgs,
		RunE: cmdutil.Run(func(cmd *cobra.Command, _ []string) (any, error) {
			name = strings.TrimSpace(name)
			if name == "" {
				return nil, clierr.Usage(errors.New("--name is required: say where the key will be used, e.g. production-web"))
			}
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.CreateAPIKeyWithResponse(cmd.Context(), langsync.ApikeyPayloadCreateRequest{Name: &name})
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil || res.JSON200.Value == nil {
				return nil, apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON401, res.JSON403, res.JSON404, res.JSON500)
			}
			k := apiKeyCreated{ID: cmdutil.Deref(res.JSON200.Id), Name: name, Value: *res.JSON200.Value}
			return output.Done{
				Message: fmt.Sprintf(`Created API key %q (%s).

  %s

This is the only time the key is shown. Store it in your application's
secrets; it reads every translation in the organization.`, name, k.ID, k.Value),
				Value:  k,
				Fields: []output.Field{{Value: k.Value}},
			}, nil
		}),
	}
	cmd.Flags().StringVar(&name, "name", "", "Where the key will be used, e.g. production-web (required)")
	return cmd
}

func newAPIKeyRevokeCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "revoke <keyId>",
		Aliases: []string{"delete"},
		Short:   "Revoke an API key at once",
		Long: `Revokes the key: its next request fails with 401. Deploy the
replacement first. The key stays in the list, marked revoked.`,
		Example: `  norcube langsync api-keys revoke 0b7a4c8e-2d3f-4a1b-9c6d-5e4f3a2b1c0d
  norcube langsync api-keys revoke 0b7a4c8e-2d3f-4a1b-9c6d-5e4f3a2b1c0d --yes   # scripts`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			id := strings.TrimSpace(args[0])
			ok, err := cmdutil.Confirm(fmt.Sprintf("Revoke API key %s? Applications using it stop working immediately.", id), yes)
			if err != nil || !ok {
				return nil, cancelledOK(cmd, cancelledIfDeclined(err))
			}
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.DeleteAPIKeyWithResponse(cmd.Context(), id)
			if err != nil {
				return nil, err
			}
			if !ok2xx(res.HTTPResponse) {
				return nil, apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON401, res.JSON403, res.JSON404, res.JSON500)
			}
			return output.Done{
				Message: fmt.Sprintf("Revoked API key %s.", id),
				Value:   map[string]any{"id": id, "revoked": true},
				Fields:  []output.Field{{Value: id}},
			}, nil
		}),
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip the confirmation prompt (required when stdin isn't a TTY)")
	return cmd
}
