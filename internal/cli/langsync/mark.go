package langsync

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/langsync"
	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/output"
)

func newMarkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "mark",
		Aliases: []string{"marks", "term", "terms"},
		Short:   "Manage marks (source strings) inside a namespace",
		Long: `In Langsync, a "mark" is the source string that other translations
target — stored on the term row as the ` + "`mark`" + ` column. Each mark
belongs to exactly one namespace within your active organization.`,
	}
	cmd.AddCommand(newMarkAddCmd(), newMarkListCmd(), newMarkDeleteCmd())
	return cmd
}

func newMarkAddCmd() *cobra.Command {
	var (
		namespaceName string
		context       string
		defaultValue  string
		autoTranslate bool
	)
	cmd := &cobra.Command{
		Use:   `add ["<source string>"]`,
		Short: "Add a new mark to a namespace",
		Long: `Creates a new mark in the named namespace and, optionally, the value
for the namespace's default language. With --auto-translate, the backend
will try to fill in every other configured language using your AI
translation provider.

Examples:
  norcube langsync mark add --namespace web "Save changes"
  norcube langsync mark add --namespace web "Email" --context "form label" --default-value "E-mail"
  norcube langsync mark add --namespace web "Welcome" --default-value "Welcome" --auto-translate

The positional source string and --namespace are both optional in
interactive shells — pickers/prompts fill in anything that's missing.
In scripts and pipes (stdin not a terminal), both must be passed
explicitly or the command errors out before touching the network.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return err
			}

			// Resolve the namespace first so the picker (if any) shows
			// before the mark prompt — keeps the conversational order
			// natural: "where?" then "what?".
			ns, err := resolveNamespace(cmd.Context(), c, namespaceName, "Add the new mark to which namespace?")
			if err != nil {
				if errors.Is(err, ErrCancelled) {
					fmt.Fprintln(cmd.OutOrStdout(), "Cancelled.")
					return nil
				}
				return err
			}

			mark, err := resolveMark(args)
			if err != nil {
				if errors.Is(err, ErrCancelled) {
					fmt.Fprintln(cmd.OutOrStdout(), "Cancelled.")
					return nil
				}
				return err
			}

			res, err := c.client.AddTermWithResponse(cmd.Context(), ns, langsync.AddTermJSONRequestBody{
				Mark:                   mark,
				Context:                context,
				DefaultLanguageValue:   defaultValue,
				TranslateAutomatically: autoTranslate,
			})
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

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Added mark %q to namespace %q.\n", mark, ns)
			if autoTranslate {
				fmt.Fprintln(out, "Auto-translation requested; check `norcube langsync mark list` to see progress.")
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespaceName, "namespace", "n", "", "Namespace name (the URL slug); picker opens when omitted and stdin is a TTY")
	cmd.Flags().StringVar(&context, "context", "", "Free-form context note for translators (e.g. \"form label\", \"button on signup page\")")
	cmd.Flags().StringVar(&defaultValue, "default-value", "", "Value for the namespace's default language (often identical to the mark itself)")
	cmd.Flags().BoolVar(&autoTranslate, "auto-translate", false, "Ask the backend to AI-translate the mark into every other configured language")
	return cmd
}

// maxMarkItems caps how many marks the CLI accumulates in one invocation
// when --all-pages is set, so a huge namespace doesn't OOM the terminal.
const maxMarkItems = 10000

// defaultMarkPageLimit is sent as ?limit= when --limit isn't explicitly
// passed. Same rationale as snapdb's defaultPageLimit — the spec marks
// `limit` required, so we must always send something.
const defaultMarkPageLimit = 50

func newMarkListCmd() *cobra.Command {
	var (
		namespaceName     string
		search            string
		limit             int
		cursor            string
		allPages          bool
		maxItems          int
		hasUntranslated   bool
		untranslatedLang  int
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List marks in a namespace",
		Long: `Lists marks (source strings) in a namespace, with optional filters
for partially-translated marks and full-text search.

Examples:
  norcube langsync mark list --namespace web
  norcube langsync mark list --namespace web --search Save
  norcube langsync mark list --namespace web --has-untranslated
  norcube langsync mark list --namespace web --untranslated-lang 17

Pagination is cursor-based — re-run with the printed --cursor value to
fetch the next page, or pass --all-pages to follow until exhausted.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return err
			}
			ns, err := resolveNamespace(cmd.Context(), c, namespaceName, "List marks from which namespace?")
			if err != nil {
				if errors.Is(err, ErrCancelled) {
					fmt.Fprintln(cmd.OutOrStdout(), "Cancelled.")
					return nil
				}
				return err
			}

			cap := maxItems
			if cap <= 0 {
				cap = maxMarkItems
			}

			// Initialise non-nil so `-o json` on an empty result prints "[]"
			// rather than "null". Same rule as snapdb's backup list.
			items := []langsync.DtoDTOTerm{}
			nextCursor := cursor
			truncated := false

			for {
				effectiveLimit := limit
				if effectiveLimit <= 0 {
					effectiveLimit = defaultMarkPageLimit
				}
				params := &langsync.ListByNamespaceParams{
					Cursor: nextCursor,
					Limit:  effectiveLimit,
					Search: search,
				}
				if cmd.Flags().Changed("has-untranslated") {
					params.HasAnyUntranslated = &hasUntranslated
				}
				if cmd.Flags().Changed("untranslated-lang") {
					params.UntranslatedLangId = &untranslatedLang
				}

				res, err := c.client.ListByNamespaceWithResponse(cmd.Context(), ns, params)
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
				items = append(items, res.JSON200.List...)

				next := res.JSON200.Cursors.Next
				if !allPages || next == nil || *next == "" {
					if next != nil && *next != "" {
						nextCursor = *next
					} else {
						nextCursor = ""
					}
					break
				}
				if len(items) >= cap {
					truncated = true
					nextCursor = *next
					break
				}
				nextCursor = *next
			}

			flags := clictx.Get(cmd)
			err = output.PrintPaged(cmd.OutOrStdout(), c.output, flags.NoPager, output.Table[langsync.DtoDTOTerm]{
				Headers:   []string{"ID", "MARK", "CONTEXT", "TRANSLATIONS", "CREATED"},
				MaxWidths: []int{0, 40, 32, 0, 0},
				Rows: func(t langsync.DtoDTOTerm) []string {
					return []string{
						fmt.Sprintf("%d", t.Term.Id),
						t.Term.Mark,
						t.Term.Context,
						fmt.Sprintf("%d", len(t.Translations)),
						compactDate(t.Term.CreatedAt),
					}
				},
				Items: items,
			})
			if err != nil {
				return err
			}

			stderr := cmd.ErrOrStderr()
			if c.output == output.FormatTable && output.IsInteractive(stderr) {
				if truncated {
					fmt.Fprintf(stderr,
						"\nStopped at --max-items=%d. Re-run with --max-items 0 (no cap) or a higher value to fetch the rest.\n",
						cap)
				} else if !allPages && nextCursor != "" {
					fmt.Fprintf(stderr,
						"\nMore results available. Re-run with --cursor %s, or --all-pages to follow until exhausted.\n",
						nextCursor)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespaceName, "namespace", "n", "", "Namespace name; picker opens when omitted and stdin is a TTY")
	cmd.Flags().StringVar(&search, "search", "", "Full-text search across mark + context")
	cmd.Flags().BoolVar(&hasUntranslated, "has-untranslated", false, "Only marks with at least one untranslated language")
	cmd.Flags().IntVar(&untranslatedLang, "untranslated-lang", 0, "Only marks missing the given language id (overrides --has-untranslated semantics)")
	cmd.Flags().IntVar(&limit, "limit", 0, "Max items per page (0 = CLI default, currently 50)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "Continue from a previous page's cursor")
	cmd.Flags().BoolVar(&allPages, "all-pages", false, "Follow cursors until exhausted (capped by --max-items)")
	cmd.Flags().IntVar(&maxItems, "max-items", 0, "Safety cap for --all-pages (0 = built-in default, currently 10000)")
	return cmd
}

func newMarkDeleteCmd() *cobra.Command {
	var (
		namespaceName string
		yes           bool
	)
	cmd := &cobra.Command{
		Use:   "delete <termId>",
		Short: "Delete a mark (and all its translations) from a namespace",
		Long: `Permanently removes the mark and every translation attached to it.
This action is irreversible — the mark, its context, and every
per-language translation row are deleted from the database.

Use --yes to skip the interactive confirmation in scripts.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return err
			}
			termID, err := strconv.Atoi(strings.TrimSpace(args[0]))
			if err != nil || termID <= 0 {
				return fmt.Errorf("termId must be a positive integer (got %q)", args[0])
			}
			ns, err := resolveNamespace(cmd.Context(), c, namespaceName, "Delete a mark from which namespace?")
			if err != nil {
				if errors.Is(err, ErrCancelled) {
					fmt.Fprintln(cmd.OutOrStdout(), "Cancelled.")
					return nil
				}
				return err
			}

			ok, err := confirm(
				fmt.Sprintf("Delete mark #%d from namespace %q? This cannot be undone.", termID, ns),
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

			res, err := c.client.DeleteTermWithResponse(cmd.Context(), ns, termID)
			if err != nil {
				return err
			}
			status := res.HTTPResponse.StatusCode
			if status != 200 && status != 204 {
				if isNamespaceForbidden(res.JSON403) || isNamespaceForbidden(res.JSON404) {
					return namespaceAccessError(c.cfg.ActiveOrg.Slug, ns)
				}
				return apiError(res.HTTPResponse, res.Body,
					res.JSON400, res.JSON401, res.JSON403, res.JSON404, res.JSON500)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted mark #%d from namespace %q.\n", termID, ns)
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespaceName, "namespace", "n", "", "Namespace name; picker opens when omitted and stdin is a TTY")
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip the confirmation prompt (required when stdin isn't a TTY)")
	return cmd
}

// compactDate trims an RFC3339 timestamp to a more table-friendly form
// ("2026-05-11 09:42") when parseable; otherwise returns the first 16
// characters or the original, whichever is shorter.
func compactDate(s string) string {
	if s == "" {
		return "—"
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC().Format("2006-01-02 15:04")
	}
	if len(s) >= 16 {
		return s[:16]
	}
	return s
}

// resolveMark returns the mark string the user wants to add. Precedence:
//
//  1. the positional argument, if present
//  2. an interactive huh.Input prompt when stdin is a TTY
//  3. error "mark is required" otherwise
//
// Whitespace-only marks are rejected client-side. The server would reject
// them too, but failing locally gives a cleaner error and saves a round
// trip.
func resolveMark(args []string) (string, error) {
	if len(args) == 1 {
		v := strings.TrimSpace(args[0])
		if v == "" {
			return "", fmt.Errorf("mark (source string) must not be empty")
		}
		return v, nil
	}
	if !stdinIsInteractive() {
		return "", fmt.Errorf("mark (source string) is required: pass it as a positional argument or run interactively")
	}
	var v string
	err := huh.NewInput().
		Title("Mark (the source string)").
		Description("This is the text that other languages will translate. It must be unique within the namespace.").
		Prompt("> ").
		Validate(func(s string) error {
			if strings.TrimSpace(s) == "" {
				return fmt.Errorf("must not be empty")
			}
			return nil
		}).
		Value(&v).
		Run()
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return "", ErrCancelled
		}
		return "", err
	}
	return strings.TrimSpace(v), nil
}
