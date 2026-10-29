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
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
	"github.com/norcubeplatform/cli/internal/term"
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

The positional source string and --namespace are both optional in
interactive shells — pickers/prompts fill in anything that's missing.
In scripts and pipes (stdin not a terminal), both must be passed
explicitly or the command errors out before touching the network.`,
		Example: `  norcube langsync mark add --namespace web "Save changes"
  norcube langsync mark add --namespace web "Email" --context "form label" --default-value "E-mail"
  norcube langsync mark add --namespace web "Welcome" --default-value "Welcome" --auto-translate`,
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
					fmt.Fprintln(cmd.ErrOrStderr(), "Cancelled.")
					return nil
				}
				return err
			}

			mark, err := resolveMark(args)
			if err != nil {
				if errors.Is(err, ErrCancelled) {
					fmt.Fprintln(cmd.ErrOrStderr(), "Cancelled.")
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

			msg := fmt.Sprintf("Added mark %q to namespace %q.", mark, ns)
			if autoTranslate {
				msg += "\nAuto-translation requested; check `norcube langsync mark list` to see progress."
			}
			return cmdutil.Print(cmd, output.Done{Message: msg, Value: res.JSON200})
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
		namespaceName    string
		search           string
		pages            cmdutil.PageFlags
		hasUntranslated  bool
		untranslatedLang int
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List marks in a namespace",
		Long: `Lists marks (source strings) in a namespace, with optional filters
for partially-translated marks and full-text search.

Pagination is cursor-based: re-run with the --cursor value printed to
stderr to fetch the next page, or pass --all-pages to follow until
exhausted.`,
		Example: `  norcube langsync mark list --namespace web
  norcube langsync mark list --namespace web --search Save
  norcube langsync mark list --namespace web --has-untranslated
  norcube langsync mark list --namespace web --untranslated-lang 17
  norcube langsync mark list -n web --all-pages -o json --query "[].term.mark"`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return err
			}
			ns, err := resolveNamespace(cmd.Context(), c, namespaceName, "List marks from which namespace?")
			if err != nil {
				return cancelledOK(cmd, err)
			}

			page, err := cmdutil.Paginate(cmd.Context(), pages, func(ctx context.Context, cursor string, size int) ([]langsync.DtoDTOTerm, string, error) {
				params := &langsync.ListByNamespaceParams{
					Cursor: cursor,
					Limit:  size,
					Search: search,
				}
				if cmd.Flags().Changed("has-untranslated") {
					params.HasAnyUntranslated = &hasUntranslated
				}
				if cmd.Flags().Changed("untranslated-lang") {
					params.UntranslatedLangId = &untranslatedLang
				}
				res, err := c.client.ListByNamespaceWithResponse(ctx, ns, params)
				if err != nil {
					return nil, "", err
				}
				if res.JSON200 == nil {
					if isNamespaceForbidden(res.JSON403) || isNamespaceForbidden(res.JSON404) {
						return nil, "", namespaceAccessError(c.cfg.ActiveOrg.Slug, ns)
					}
					return nil, "", apiError(res.HTTPResponse, res.Body,
						res.JSON400, res.JSON401, res.JSON403, res.JSON404, res.JSON500)
				}
				next := ""
				if n := res.JSON200.Cursors.Next; n != nil {
					next = *n
				}
				return res.JSON200.List, next, nil
			})
			if err != nil {
				return err
			}
			return page.Print(cmd, output.Table[langsync.DtoDTOTerm]{
				Headers: []string{"ID", "MARK", "CONTEXT", "TRANSLATIONS", "CREATED"},
				// Don't truncate MARK — the whole point is to see the
				// source text. CONTEXT still gets a cap because it's
				// freeform and routinely runs long ("the third button
				// from the top in the user-settings panel etc.").
				MaxWidths: []int{0, 0, 32, 0, 0},
				Rows: func(t langsync.DtoDTOTerm) []string {
					return []string{
						fmt.Sprintf("%d", t.Term.Id),
						t.Term.Mark,
						t.Term.Context,
						fmt.Sprintf("%d", len(t.Translations)),
						compactDate(t.Term.CreatedAt),
					}
				},
				Items: page.Items,
			})
		},
	}
	cmd.Flags().StringVarP(&namespaceName, "namespace", "n", "", "Namespace name; picker opens when omitted and stdin is a TTY")
	cmd.Flags().StringVar(&search, "search", "", "Full-text search across mark + context")
	cmd.Flags().BoolVar(&hasUntranslated, "has-untranslated", false, "Only marks with at least one untranslated language")
	cmd.Flags().IntVar(&untranslatedLang, "untranslated-lang", 0, "Only marks missing the given language id (overrides --has-untranslated semantics)")
	cmdutil.AddPageFlags(cmd, &pages, defaultMarkPageLimit, maxMarkItems)
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
		Example: `  norcube langsync mark delete 1234 --namespace web
  norcube langsync mark delete 1234 --namespace web --yes   # scripts`,
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
					fmt.Fprintln(cmd.ErrOrStderr(), "Cancelled.")
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
					fmt.Fprintln(cmd.ErrOrStderr(), "Cancelled.")
					return nil
				}
				return err
			}
			if !ok {
				fmt.Fprintln(cmd.ErrOrStderr(), "Cancelled.")
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
			return cmdutil.Print(cmd, output.Done{
				Message: fmt.Sprintf("Deleted mark #%d from namespace %q.", termID, ns),
				Value:   deletedResult{ID: strconv.Itoa(termID), Deleted: true},
				Fields:  []output.Field{{Value: strconv.Itoa(termID)}},
			})
		},
	}
	cmd.Flags().StringVarP(&namespaceName, "namespace", "n", "", "Namespace name; picker opens when omitted and stdin is a TTY")
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip the confirmation prompt (required when stdin isn't a TTY)")
	return cmd
}

// compactDate renders an RFC 3339 timestamp as a table cell
// ("2026-05-11 09:42" in the reader's local time zone, RFC 3339 UTC with
// -o text). Empty is "—"; an unparseable value is shown as it is.
func compactDate(s string) string {
	return output.WhenString(s)
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
	if !term.StdinIsTerminal() {
		return "", clierr.Usage(errors.New("mark (source string) is required: pass it as a positional argument or run interactively"))
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
