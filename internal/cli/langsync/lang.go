package langsync

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/langsync"
	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/output"
)

func newLangCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "lang",
		Aliases: []string{"langs", "language", "languages"},
		Short:   "Manage the languages attached to a namespace",
		Long: `A namespace has one default language (the source of truth for marks)
and zero or more "additional" languages — each translation row attaches
to one of those. These commands list, add, and remove the additional
languages on a given namespace.`,
	}
	cmd.AddCommand(
		newLangListCmd(),
		newLangAddCmd(),
		newLangRemoveCmd(),
		newLangCreateCmd(),
		newLangDeleteCmd(),
	)
	return cmd
}

func newLangListCmd() *cobra.Command {
	var namespaceName string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List languages — every language available to the active org by default; pass -n <namespace> for the namespace's attached subset",
		Long: `Without --namespace, lists every language visible to the active org:
every shared ("global") language plus every custom language this org
has created.

With --namespace <name>, lists only the languages attached to that one
namespace (the subset that translations can target inside it).`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return err
			}

			var items []langsync.DtoDTOLanguage
			if namespaceName != "" {
				items, err = listAttachedLanguages(cmd.Context(), c, namespaceName)
				if err != nil {
					return err
				}
			} else {
				items, err = listAllLanguages(cmd.Context(), c)
				if err != nil {
					return err
				}
			}

			flags := clictx.Get(cmd)
			return output.PrintPaged(cmd.OutOrStdout(), c.output, flags.NoPager, output.Table[langsync.DtoDTOLanguage]{
				Headers: []string{"CODE", "NAME", "KIND", "ID"},
				Rows: func(l langsync.DtoDTOLanguage) []string {
					return []string{deref(l.Code), deref(l.Name), langKind(l), intStr(l.Id)}
				},
				Items: items,
			})
		},
	}
	cmd.Flags().StringVarP(&namespaceName, "namespace", "n", "", "Narrow the list to the languages attached to this namespace (omit to list every language in the active org)")
	return cmd
}

// langKind renders a one-word label distinguishing shared from custom
// languages. The DTO's IsCustom field is the source of truth (it's set
// from the backend's `shared` column with the polarity flipped). The
// attached-to-namespace listing endpoint doesn't include IsCustom — in
// that case the field is nil and we render "—" rather than guessing.
func langKind(l langsync.DtoDTOLanguage) string {
	if l.IsCustom == nil {
		return "—"
	}
	if *l.IsCustom {
		return "custom"
	}
	return "shared"
}

func newLangCreateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create <code> <name>",
		Short: "Create a custom language scoped to the active organization",
		Long: `Creates a custom language inside the active organization. The code
must start with a letter and contain only lowercase letters, digits,
hyphens, or underscores (max 32 chars). Codes are unique per-org —
shared codes can coexist (the resolver prefers your custom one).

Example:
  norcube langsync lang create internal-en "Internal English"`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return err
			}
			code := strings.TrimSpace(args[0])
			name := strings.TrimSpace(args[1])
			if code == "" || name == "" {
				return fmt.Errorf("both <code> and <name> are required and non-empty")
			}

			res, err := c.client.CreateCustomLanguageWithResponse(cmd.Context(),
				langsync.CreateCustomLanguageJSONRequestBody{
					Code: code,
					Name: name,
				})
			if err != nil {
				return err
			}
			if res.JSON201 == nil {
				return apiError(res.HTTPResponse, res.Body,
					res.JSON400, res.JSON401, res.JSON409, res.JSON500)
			}
			created := *res.JSON201
			fmt.Fprintf(cmd.OutOrStdout(), "Created custom language %q (%s), id %s.\n",
				name, code, intStr(created.Id))
			return nil
		},
	}
	return cmd
}

func newLangDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <id>",
		Aliases: []string{"rm"},
		Short:   "Delete a custom language owned by the active organization",
		Long: `Permanently removes a custom language. Shared (global) languages are
never deletable through this command — the backend ignores the id.
Fails with a 409 if the language is still attached to any namespace
(detach it from those namespaces first via "lang remove").

Use --yes to skip confirmation in scripts.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return err
			}
			id, err := strconv.Atoi(strings.TrimSpace(args[0]))
			if err != nil || id <= 0 {
				return fmt.Errorf("language id must be a positive integer (got %q)", args[0])
			}
			ok, err := confirm(
				fmt.Sprintf("Delete custom language id %d? This cannot be undone.", id),
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

			res, err := c.client.DeleteCustomLanguageWithResponse(cmd.Context(), id)
			if err != nil {
				return err
			}
			status := res.HTTPResponse.StatusCode
			if status != 200 && status != 204 {
				return apiError(res.HTTPResponse, res.Body,
					res.JSON400, res.JSON401, res.JSON404, res.JSON409, res.JSON500)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted custom language id %d.\n", id)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip the confirmation prompt (required when stdin isn't a TTY)")
	return cmd
}

func newLangAddCmd() *cobra.Command {
	var namespaceName string
	cmd := &cobra.Command{
		Use:   "add [code-or-id]",
		Short: "Attach a language to a namespace",
		Long: `Adds a language to the namespace so terms can be translated into it.
You can pass either a language code ("en", "cs", "de") or the numeric
language id. With no positional argument and an interactive shell, a
picker lists every language that isn't already attached.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return err
			}
			ns, err := resolveNamespace(cmd.Context(), c, namespaceName, "Add a language to which namespace?")
			if err != nil {
				if errors.Is(err, ErrCancelled) {
					fmt.Fprintln(cmd.OutOrStdout(), "Cancelled.")
					return nil
				}
				return err
			}

			// Build the request body. With the backend's new polymorphic
			// fields we no longer need to resolve code → id client-side:
			// pass whichever form the user supplied straight through.
			body := langsync.AddLanguageJSONRequestBody{}
			var langLabel string
			if len(args) == 1 {
				input := strings.TrimSpace(args[0])
				if input == "" {
					return fmt.Errorf("language code or id is required")
				}
				if id, parseErr := strconv.Atoi(input); parseErr == nil && id > 0 {
					body.LanguageId = &id
					langLabel = fmt.Sprintf("language id %d", id)
				} else {
					body.LanguageCode = &input
					langLabel = fmt.Sprintf("language %q", input)
				}
			} else {
				// Picker still needs the list-of-languages call for UI; the
				// chosen value comes back as a numeric id either way.
				langID, label, perr := pickLanguageNotAttached(cmd.Context(), c, ns)
				if perr != nil {
					if errors.Is(perr, ErrCancelled) {
						fmt.Fprintln(cmd.OutOrStdout(), "Cancelled.")
						return nil
					}
					return perr
				}
				body.LanguageId = &langID
				langLabel = label
			}

			res, err := c.client.AddLanguageWithResponse(cmd.Context(), ns, body)
			if err != nil {
				return err
			}
			if res.JSON200 == nil {
				if isNamespaceForbidden(res.JSON403) || isNamespaceForbidden(res.JSON404) {
					return namespaceAccessError(c.cfg.ActiveOrg.Slug, ns)
				}
				return apiError(res.HTTPResponse, res.Body,
					res.JSON400, res.JSON401, res.JSON403, res.JSON404, res.JSON500)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Attached %s to namespace %q.\n", langLabel, ns)
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespaceName, "namespace", "n", "", "Namespace name; picker opens when omitted and stdin is a TTY")
	return cmd
}

func newLangRemoveCmd() *cobra.Command {
	var (
		namespaceName string
		yes           bool
	)
	cmd := &cobra.Command{
		Use:     "remove [code-or-id]",
		Aliases: []string{"rm"},
		Short:   "Remove a language from a namespace (and every translation in that language)",
		Long: `Detaches a language from the namespace. The translation rows for
that language are removed by the backend; the source marks themselves
are unaffected. Use --yes to skip the confirmation in scripts.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return err
			}
			ns, err := resolveNamespace(cmd.Context(), c, namespaceName, "Remove a language from which namespace?")
			if err != nil {
				if errors.Is(err, ErrCancelled) {
					fmt.Fprintln(cmd.OutOrStdout(), "Cancelled.")
					return nil
				}
				return err
			}

			var langID int
			var langLabel string
			if len(args) == 1 {
				// Resolve from the namespace's attached set so misspellings
				// fail fast and the success message can use the proper name.
				langID, langLabel, err = resolveLanguageAttached(cmd.Context(), c, ns, args[0])
				if err != nil {
					return err
				}
			} else {
				langID, langLabel, err = pickLanguageAttached(cmd.Context(), c, ns)
				if err != nil {
					if errors.Is(err, ErrCancelled) {
						fmt.Fprintln(cmd.OutOrStdout(), "Cancelled.")
						return nil
					}
					return err
				}
			}

			ok, err := confirm(
				fmt.Sprintf("Remove %s from namespace %q? Every translation in that language will be deleted.", langLabel, ns),
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

			res, err := c.client.RemoveLanguageWithResponse(cmd.Context(), ns, langID)
			if err != nil {
				return err
			}
			if res.HTTPResponse.StatusCode != 200 && res.HTTPResponse.StatusCode != 204 {
				if isNamespaceForbidden(res.JSON403) || isNamespaceForbidden(res.JSON404) {
					return namespaceAccessError(c.cfg.ActiveOrg.Slug, ns)
				}
				return apiError(res.HTTPResponse, res.Body,
					res.JSON400, res.JSON401, res.JSON403, res.JSON404, res.JSON500)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed %s from namespace %q.\n", langLabel, ns)
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespaceName, "namespace", "n", "", "Namespace name; picker opens when omitted and stdin is a TTY")
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip the confirmation prompt (required when stdin isn't a TTY)")
	return cmd
}

// resolveLanguage looks up a language by either numeric id (passed as a
// stringified int) or language code (case-insensitive). Returns the
// id and a human label for success messages.
func resolveLanguage(ctx context.Context, c *langsyncContext, input string) (int, string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return 0, "", fmt.Errorf("language code or id is required")
	}
	// If it parses as a positive int, treat it as an id and trust the
	// backend to validate.
	if id, err := strconv.Atoi(input); err == nil && id > 0 {
		return id, fmt.Sprintf("language id %d", id), nil
	}
	// Otherwise treat as a code; resolve via the global /languages list.
	all, err := listAllLanguages(ctx, c)
	if err != nil {
		return 0, "", err
	}
	lc := strings.ToLower(input)
	for _, l := range all {
		if l.Code != nil && strings.ToLower(*l.Code) == lc {
			if l.Id == nil {
				return 0, "", fmt.Errorf("language %q is missing an id in the backend response", input)
			}
			return *l.Id, languageLabel(l), nil
		}
	}
	return 0, "", fmt.Errorf("no language found with code %q — run `norcube langsync lang list` to see what's available", input)
}

// resolveLanguageAttached is like resolveLanguage but scopes the lookup
// to the languages already attached to ns, so removing a non-attached
// language fails fast with a useful error.
func resolveLanguageAttached(ctx context.Context, c *langsyncContext, ns, input string) (int, string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return 0, "", fmt.Errorf("language code or id is required")
	}
	attached, err := listAttachedLanguages(ctx, c, ns)
	if err != nil {
		return 0, "", err
	}
	// Numeric form first.
	if id, err := strconv.Atoi(input); err == nil && id > 0 {
		for _, l := range attached {
			if l.Id != nil && *l.Id == id {
				return id, languageLabel(l), nil
			}
		}
		return 0, "", fmt.Errorf("language id %d isn't attached to namespace %q", id, ns)
	}
	// Code form.
	lc := strings.ToLower(input)
	for _, l := range attached {
		if l.Code != nil && strings.ToLower(*l.Code) == lc {
			if l.Id == nil {
				return 0, "", fmt.Errorf("language %q is missing an id in the backend response", input)
			}
			return *l.Id, languageLabel(l), nil
		}
	}
	return 0, "", fmt.Errorf("language %q isn't attached to namespace %q", input, ns)
}

func pickLanguageNotAttached(ctx context.Context, c *langsyncContext, ns string) (int, string, error) {
	if !stdinIsInteractive() {
		return 0, "", fmt.Errorf("language is required: pass code or id as positional arg or run interactively")
	}
	all, err := listAllLanguages(ctx, c)
	if err != nil {
		return 0, "", err
	}
	attached, err := listAttachedLanguages(ctx, c, ns)
	if err != nil {
		return 0, "", err
	}
	skip := map[int]bool{}
	for _, l := range attached {
		if l.Id != nil {
			skip[*l.Id] = true
		}
	}
	var opts []huh.Option[int]
	for _, l := range all {
		if l.Id == nil || skip[*l.Id] {
			continue
		}
		opts = append(opts, huh.NewOption(languageLabel(l), *l.Id))
	}
	if len(opts) == 0 {
		return 0, "", fmt.Errorf("every available language is already attached to namespace %q", ns)
	}
	var pickedID int
	if err := huh.NewSelect[int]().Title("Add which language?").Options(opts...).Value(&pickedID).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return 0, "", ErrCancelled
		}
		return 0, "", err
	}
	for _, l := range all {
		if l.Id != nil && *l.Id == pickedID {
			return pickedID, languageLabel(l), nil
		}
	}
	return pickedID, fmt.Sprintf("language id %d", pickedID), nil
}

func pickLanguageAttached(ctx context.Context, c *langsyncContext, ns string) (int, string, error) {
	if !stdinIsInteractive() {
		return 0, "", fmt.Errorf("language is required: pass code or id as positional arg or run interactively")
	}
	attached, err := listAttachedLanguages(ctx, c, ns)
	if err != nil {
		return 0, "", err
	}
	if len(attached) == 0 {
		return 0, "", fmt.Errorf("no additional languages are attached to namespace %q", ns)
	}
	var opts []huh.Option[int]
	for _, l := range attached {
		if l.Id == nil {
			continue
		}
		opts = append(opts, huh.NewOption(languageLabel(l), *l.Id))
	}
	var pickedID int
	if err := huh.NewSelect[int]().Title("Remove which language?").Options(opts...).Value(&pickedID).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return 0, "", ErrCancelled
		}
		return 0, "", err
	}
	for _, l := range attached {
		if l.Id != nil && *l.Id == pickedID {
			return pickedID, languageLabel(l), nil
		}
	}
	return pickedID, fmt.Sprintf("language id %d", pickedID), nil
}

func listAllLanguages(ctx context.Context, c *langsyncContext) ([]langsync.DtoDTOLanguage, error) {
	// GET /languages now returns shared + this org's custom languages
	// (operationId renamed to `listLanguagesForOrg`). Authentication is
	// the standard Bearer token; org context comes from the JWT.
	res, err := c.client.ListLanguagesForOrgWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	if res.JSON200 == nil {
		return nil, apiError(res.HTTPResponse, res.Body, res.JSON401, res.JSON500)
	}
	return *res.JSON200, nil
}

func listAttachedLanguages(ctx context.Context, c *langsyncContext, ns string) ([]langsync.DtoDTOLanguage, error) {
	res, err := c.client.GetLanguagesByNamespaceWithResponse(ctx, ns)
	if err != nil {
		return nil, err
	}
	if res.JSON200 == nil {
		if isNamespaceForbidden(res.JSON403) || isNamespaceForbidden(res.JSON404) {
			return nil, namespaceAccessError(c.cfg.ActiveOrg.Slug, ns)
		}
		return nil, apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON401, res.JSON403, res.JSON404, res.JSON500)
	}
	// The per-namespace endpoint returns Connection DTOs (the join row
	// between language and namespace). Project the language-specific
	// fields out so callers can treat both endpoints (org-wide and
	// per-namespace) uniformly. We lose ConnectionID + IsDefault here
	// because none of the current commands need them; expose those via
	// a dedicated listing if a future command does.
	out := make([]langsync.DtoDTOLanguage, 0, len(*res.JSON200))
	for _, conn := range *res.JSON200 {
		out = append(out, langsync.DtoDTOLanguage{
			Id:       conn.LanguageId,
			Code:     conn.LanguageCode,
			Name:     conn.LanguageName,
			IsCustom: deriveIsCustom(conn.LanguageShared),
		})
	}
	return out, nil
}

// deriveIsCustom flips a *bool `shared` flag into the inverse `isCustom`
// flag. Returns nil when shared is unknown so downstream renderers can
// show "—" rather than guessing.
func deriveIsCustom(shared *bool) *bool {
	if shared == nil {
		return nil
	}
	v := !*shared
	return &v
}

func languageLabel(l langsync.DtoDTOLanguage) string {
	name := deref(l.Name)
	code := deref(l.Code)
	if name != "—" && code != "—" {
		return fmt.Sprintf("%s (%s)", name, code)
	}
	if name != "—" {
		return name
	}
	if code != "—" {
		return code
	}
	if l.Id != nil {
		return fmt.Sprintf("language id %d", *l.Id)
	}
	return "<unknown language>"
}
