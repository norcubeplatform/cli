package langsync

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
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
			// Only fetch the org's lang list when the picker
			// might actually need it: --default-language is empty
			// and the shell is interactive. Skips the round trip
			// on every scripted invocation.
			var orgLangs []langsync.DtoDTOLanguage
			if defaultLanguage == "" && stdinIsInteractive() {
				orgLangs, err = fetchOrgLanguageList(cmd.Context(), c)
				if err != nil {
					return err
				}
			}
			name, defaultLanguage, context, err = resolveCreateFields(name, defaultLanguage, context, orgLangs)
			if err != nil {
				if errors.Is(err, ErrCancelled) {
					fmt.Fprintln(cmd.OutOrStdout(), "Cancelled.")
					return nil
				}
				return err
			}

			// Pick the right new explicit field based on what the user
			// typed: numeric → defaultLanguageId, else → defaultLanguageCode.
			// The backend resolver does the org-scoped lookup.
			body := langsync.CreateNamespaceJSONRequestBody{
				Name:    name,
				Context: context,
			}
			if id, parseErr := strconv.Atoi(strings.TrimSpace(defaultLanguage)); parseErr == nil && id > 0 {
				body.DefaultLanguageId = &id
			} else {
				code := strings.TrimSpace(defaultLanguage)
				body.DefaultLanguageCode = &code
			}

			res, err := c.client.CreateNamespaceWithResponse(cmd.Context(), body)
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

// resolveCreateFields prompts for any missing required fields when
// stdin is a TTY, errors out otherwise. The cobra-level flags
// supply the scripted path; this fills in the interactive gaps.
//
// orgLangs is the active org's full lang list. When the user needs
// to pick a default language interactively, the picker filters this
// list — searchable by code AND name so a user who doesn't know
// codes can type "english" / "czech" and find it. Callers pass nil
// when --default-language was supplied (no picker needed).
func resolveCreateFields(name, lang, ctx string, orgLangs []langsync.DtoDTOLanguage) (string, string, string, error) {
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

	// Wizard structure: one field per group → huh renders each
	// step as its own screen, eliminating the "multiple cursors"
	// problem and giving us the "1/N" pagination footer for free.
	// Step headers via NewNote keep the title visible even when a
	// filterable Select hides its own title mid-typing (huh #510).
	totalSteps := countSteps(name == "", lang == "", true)
	stepNo := 1
	var groups []*huh.Group

	if name == "" {
		groups = append(groups, huh.NewGroup(
			stepNote(stepNo, totalSteps, "Namespace name", "URL slug, case-sensitive. This is what every --namespace flag will reference."),
			huh.NewInput().
				Title("Namespace name").
				Placeholder("e.g. web, marketing, mobile-app").
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return fmt.Errorf("must not be empty")
					}
					return nil
				}).
				Value(&newName),
		))
		stepNo++
	}

	if lang == "" {
		if len(orgLangs) > 0 {
			// Searchable Select. Pre-select "en" when present
			// since most projects start there; the user can still
			// type to filter to anything else.
			opts := make([]huh.Option[string], 0, len(orgLangs))
			sortedLangs := make([]langsync.DtoDTOLanguage, len(orgLangs))
			copy(sortedLangs, orgLangs)
			sort.Slice(sortedLangs, func(i, j int) bool {
				ci, cj := "", ""
				if sortedLangs[i].Code != nil {
					ci = *sortedLangs[i].Code
				}
				if sortedLangs[j].Code != nil {
					cj = *sortedLangs[j].Code
				}
				return ci < cj
			})
			for _, l := range sortedLangs {
				if l.Code == nil || *l.Code == "" {
					continue
				}
				label := *l.Code
				if l.Name != nil && *l.Name != "" && *l.Name != *l.Code {
					label = fmt.Sprintf("%s — %s", *l.Code, *l.Name)
				}
				opts = append(opts, huh.NewOption(label, *l.Code))
				if *l.Code == "en" && newLang == "" {
					newLang = "en"
				}
			}
			groups = append(groups, huh.NewGroup(
				stepNote(stepNo, totalSteps,
					"Default language",
					"Source-of-truth language — its file (e.g. en.json) is what the AI translates from."),
				huh.NewSelect[string]().
					Title("Pick the default language").
					Description("Type to filter by code (en, cs-CZ) or name (English, Czech). Arrows move; Enter confirms.").
					Options(opts...).
					Filtering(true).
					Height(12).
					Value(&newLang),
			))
		} else {
			// Fallback — no org lang list available (offline /
			// error). Plain Input keeps the command usable.
			groups = append(groups, huh.NewGroup(
				stepNote(stepNo, totalSteps, "Default language code",
					"Code of the language source marks are written in."),
				huh.NewInput().
					Title("Default language code").
					Placeholder("en, cs, de…").
					Validate(func(s string) error {
						if strings.TrimSpace(s) == "" {
							return fmt.Errorf("must not be empty")
						}
						return nil
					}).
					Value(&newLang),
			))
		}
		stepNo++
	}

	// Context is always shown as the final step (it's optional, so
	// the form rules out cancellation by accident — Enter advances
	// past an empty input).
	groups = append(groups, huh.NewGroup(
		stepNote(stepNo, totalSteps, "Context (optional)",
			"Short description shown next to the namespace in pickers and lists. Press Enter to skip."),
		huh.NewInput().
			Title("Context").
			Placeholder("Press Enter to skip").
			Value(&newCtx),
	))

	err := newWizard(groups...).Run()
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return "", "", "", ErrCancelled
		}
		return "", "", "", err
	}
	return strings.TrimSpace(newName), strings.TrimSpace(newLang), strings.TrimSpace(newCtx), nil
}

// countSteps tallies how many wizard steps will be presented based
// on which fields the caller pre-populated. Pure-function helper
// kept here so resolveCreateFields stays readable.
func countSteps(askName, askLang, askContext bool) int {
	n := 0
	if askName {
		n++
	}
	if askLang {
		n++
	}
	if askContext {
		n++
	}
	return n
}

func newNamespaceUpdateCmd() *cobra.Command {
	var (
		rename          string
		context         string
		defaultLanguage string // accept code OR id; resolve below
	)
	cmd := &cobra.Command{
		Use:   "update <name>",
		Short: "Update a namespace's name, context, or default language",
		Long: `Partial update of a namespace. Pass only the fields you want to
change — omitted flags leave existing values alone.

--default-language accepts either a language code (e.g. en, cs) or the
numeric language id. The CLI resolves codes via /languages before
sending the request — the underlying endpoint wants the id form.`,
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
				// Send whichever new explicit field matches the user's
				// input. Server resolves to a Language and applies the
				// access check — no client-side /languages lookup.
				if id, parseErr := strconv.Atoi(strings.TrimSpace(defaultLanguage)); parseErr == nil && id > 0 {
					body.DefaultLanguageId = &id
				} else {
					code := strings.TrimSpace(defaultLanguage)
					body.DefaultLanguageCode = &code
				}
			}
			if body.Name == nil && body.Context == nil && body.DefaultLanguageId == nil && body.DefaultLanguageCode == nil {
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
	cmd.Flags().StringVar(&defaultLanguage, "default-language", "", "New default language — accepts either the code (e.g. en) or the numeric id; codes are resolved to ids client-side")
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
