package langsync

import (
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/huh"
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
	cmd.AddCommand(
		newNamespaceListCmd(),
		newNamespaceCreateCmd(),
		newNamespaceUpdateCmd(),
		newNamespaceDeleteCmd(),
	)
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

func newNamespaceCreateCmd() *cobra.Command {
	var (
		defaultLanguage string
		context         string
	)
	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create a new namespace in the active organization",
		Long: `Creates a new namespace. The name is the URL slug (case-sensitive)
that every other langsync command then accepts as --namespace.

The default language is the source of truth — it's the language users
write marks in. It's a language code like "en", "cs", "de"; the backend
maps it to an actual language record on creation.

Examples:
  norcube langsync namespace create web --default-language en
  norcube langsync namespace create marketing --default-language cs --context "Marketing site copy"

In an interactive shell, omitting any value pops a prompt; in a script
the name positional + --default-language are both required.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return err
			}
			var name string
			if len(args) == 1 {
				name = strings.TrimSpace(args[0])
			}
			name, defaultLanguage, context, err = resolveCreateFields(name, defaultLanguage, context)
			if err != nil {
				if errors.Is(err, ErrCancelled) {
					fmt.Fprintln(cmd.OutOrStdout(), "Cancelled.")
					return nil
				}
				return err
			}

			res, err := c.client.CreateNamespaceWithResponse(cmd.Context(), langsync.CreateNamespaceJSONRequestBody{
				Name:            name,
				DefaultLanguage: defaultLanguage,
				Context:         context,
			})
			if err != nil {
				return err
			}
			if res.JSON200 == nil {
				return apiError(res.HTTPResponse, res.Body,
					res.JSON400, res.JSON401, res.JSON403, res.JSON404, res.JSON500)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Created namespace %q (default language %q).\n", name, defaultLanguage)
			return nil
		},
	}
	cmd.Flags().StringVar(&defaultLanguage, "default-language", "", "Language code that source marks are written in (e.g. en, cs, de)")
	cmd.Flags().StringVar(&context, "context", "", "Free-form description shown next to the namespace in the picker")
	return cmd
}

// resolveCreateFields prompts for any missing required fields when stdin
// is a TTY, errors out otherwise. The cobra-level flags supply the
// scripted path; this fills in the interactive gaps.
func resolveCreateFields(name, lang, ctx string) (string, string, string, error) {
	missing := name == "" || lang == ""
	if !missing {
		return name, lang, ctx, nil
	}
	if !stdinIsInteractive() {
		switch {
		case name == "":
			return "", "", "", fmt.Errorf("namespace name is required: pass it as a positional argument or run interactively")
		default:
			return "", "", "", fmt.Errorf("--default-language is required (language code, e.g. en)")
		}
	}
	var (
		newName = name
		newLang = lang
		newCtx  = ctx
	)
	fields := []huh.Field{}
	if name == "" {
		fields = append(fields,
			huh.NewInput().
				Title("Namespace name").
				Description("URL slug, case-sensitive. This is what every --namespace flag will reference.").
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return fmt.Errorf("must not be empty")
					}
					return nil
				}).
				Value(&newName),
		)
	}
	if lang == "" {
		fields = append(fields,
			huh.NewInput().
				Title("Default language code").
				Description("Language source marks are written in (e.g. en, cs, de).").
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return fmt.Errorf("must not be empty")
					}
					return nil
				}).
				Value(&newLang),
		)
	}
	fields = append(fields,
		huh.NewInput().
			Title("Context (optional)").
			Description("Short description, shown in the picker. Press Enter to skip.").
			Value(&newCtx),
	)
	err := huh.NewForm(huh.NewGroup(fields...)).Run()
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return "", "", "", ErrCancelled
		}
		return "", "", "", err
	}
	return strings.TrimSpace(newName), strings.TrimSpace(newLang), strings.TrimSpace(newCtx), nil
}

func newNamespaceUpdateCmd() *cobra.Command {
	var (
		rename          string
		context         string
		defaultLanguage int
	)
	cmd := &cobra.Command{
		Use:   "update <name>",
		Short: "Update a namespace's name, context, or default language",
		Long: `Partial update of a namespace. Pass only the fields you want to
change — omitted flags leave existing values alone.

Note: --default-language here takes an integer language id (not a code),
because the underlying endpoint accepts the id form. Look up the id via
` + "`norcube langsync namespace list`" + ` first.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return err
			}
			name := strings.TrimSpace(args[0])
			if name == "" {
				return fmt.Errorf("namespace name must not be empty")
			}

			body := langsync.UpdateNamespaceJSONRequestBody{}
			if cmd.Flags().Changed("rename") {
				body.Name = &rename
			}
			if cmd.Flags().Changed("context") {
				body.Context = &context
			}
			if cmd.Flags().Changed("default-language") {
				body.DefaultLanguage = &defaultLanguage
			}
			if body.Name == nil && body.Context == nil && body.DefaultLanguage == nil {
				return fmt.Errorf("nothing to update — pass at least one of --rename, --context, --default-language")
			}

			res, err := c.client.UpdateNamespaceWithResponse(cmd.Context(), name, body)
			if err != nil {
				return err
			}
			if res.HTTPResponse.StatusCode != 200 && res.HTTPResponse.StatusCode != 204 {
				if isNamespaceForbidden(res.JSON403) || isNamespaceForbidden(res.JSON404) {
					return namespaceAccessError(c.cfg.ActiveOrg.Slug, name)
				}
				return apiError(res.HTTPResponse, res.Body,
					res.JSON400, res.JSON401, res.JSON403, res.JSON404, res.JSON500)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Updated namespace %q.\n", name)
			return nil
		},
	}
	cmd.Flags().StringVar(&rename, "rename", "", "New name for the namespace (URL slug)")
	cmd.Flags().StringVar(&context, "context", "", "New context description")
	cmd.Flags().IntVar(&defaultLanguage, "default-language", 0, "New default-language id (see `namespace list` to look up ids)")
	return cmd
}

func newNamespaceDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a namespace and every mark + translation in it",
		Long: `Permanently removes the namespace, every mark, and every per-language
translation. There is no undo. Use --yes to skip the confirmation prompt
in scripts.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return err
			}
			name := strings.TrimSpace(args[0])
			if name == "" {
				return fmt.Errorf("namespace name must not be empty")
			}
			ok, err := confirm(
				fmt.Sprintf("Delete namespace %q (and every mark + translation in it)? This cannot be undone.", name),
				yes,
			)
			if err != nil {
				if errors.Is(err, ErrCancelled) {
					fmt.Fprintln(cmd.OutOrStdout(), "Cancelled.")
					return nil
				}
				return err
			}
			if !ok {
				fmt.Fprintln(cmd.OutOrStdout(), "Cancelled.")
				return nil
			}

			res, err := c.client.DeleteNamespaceWithResponse(cmd.Context(), name)
			if err != nil {
				return err
			}
			if res.HTTPResponse.StatusCode != 200 && res.HTTPResponse.StatusCode != 204 {
				if isNamespaceForbidden(res.JSON403) || isNamespaceForbidden(res.JSON404) {
					return namespaceAccessError(c.cfg.ActiveOrg.Slug, name)
				}
				return apiError(res.HTTPResponse, res.Body,
					res.JSON400, res.JSON401, res.JSON403, res.JSON404, res.JSON500)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted namespace %q.\n", name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip the confirmation prompt (required when stdin isn't a TTY)")
	return cmd
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
