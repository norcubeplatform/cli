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
		Use:     "terms",
		Aliases: []string{"term", "mark", "marks"},
		Short:   "Manage terms (source strings) inside a namespace",
		Long: `A term is a source string that translations target (the API calls its
text the "mark"). Each term belongs to exactly one namespace of the
organization.`,
	}
	cmd.AddCommand(newMarkAddCmd(), newMarkListCmd(), newMarkUpdateCmd(), newMarkDeleteCmd(), newImportCmd())
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
		Example: `  norcube langsync terms add --namespace web "Save changes"
  norcube langsync terms add --namespace web "Email" --context "form label" --default-value "E-mail"
  norcube langsync terms add --namespace web "Welcome" --default-value "Welcome" --auto-translate`,
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
				if errors.Is(err, cmdutil.ErrCancelled) {
					fmt.Fprintln(cmd.ErrOrStderr(), "Cancelled.")
					return nil
				}
				return err
			}

			mark, err := resolveMark(args)
			if err != nil {
				if errors.Is(err, cmdutil.ErrCancelled) {
					fmt.Fprintln(cmd.ErrOrStderr(), "Cancelled.")
					return nil
				}
				return err
			}

			res, err := c.API.AddTermWithResponse(cmd.Context(), ns, langsync.AddTermJSONRequestBody{
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
					return namespaceAccessError(c.OrgLabel(), ns)
				}
				return apiError(res.HTTPResponse, res.Body,
					res.JSON400, res.JSON401, res.JSON403, res.JSON404, res.JSON500)
			}

			msg := fmt.Sprintf("Added mark %q to namespace %q.", mark, ns)
			if autoTranslate {
				msg += "\nAuto-translation requested; check `norcube langsync terms list` to see progress."
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
exhausted.

AI counts the translations that are still exactly what AI translation
produced; a person editing one clears its label. With -o json each
translation carries the same flag as "aiGenerated".`,
		Example: `  norcube langsync terms list --namespace web
  norcube langsync terms list --namespace web --search Save
  norcube langsync terms list --namespace web --has-untranslated
  norcube langsync terms list --namespace web --untranslated-lang 17
  norcube langsync terms list -n web --all-pages -o json --query "[].term.mark"`,
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
				res, err := c.API.ListByNamespaceWithResponse(ctx, ns, params)
				if err != nil {
					return nil, "", err
				}
				if res.JSON200 == nil {
					if isNamespaceForbidden(res.JSON403) || isNamespaceForbidden(res.JSON404) {
						return nil, "", namespaceAccessError(c.OrgLabel(), ns)
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
				Headers: []string{"ID", "MARK", "CONTEXT", "TRANSLATIONS", "AI", "CREATED"},
				// Don't truncate MARK — the whole point is to see the
				// source text. CONTEXT still gets a cap because it's
				// freeform and routinely runs long ("the third button
				// from the top in the user-settings panel etc.").
				MaxWidths: []int{0, 0, 32, 0, 0, 0},
				Rows: func(t langsync.DtoDTOTerm) []string {
					return []string{
						fmt.Sprintf("%d", t.Term.Id),
						t.Term.Mark,
						t.Term.Context,
						fmt.Sprintf("%d", len(t.Translations)),
						strconv.Itoa(aiCount(t.Translations)),
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

// aiCount is how many translations still carry the AI label.
func aiCount(ts []langsync.DtoTranslationInfo) int {
	n := 0
	for _, t := range ts {
		if t.AiGenerated {
			n++
		}
	}
	return n
}

// termUpdated is what `terms update -o json` prints.
type termUpdated struct {
	ID        int     `json:"id" yaml:"id"`
	Namespace string  `json:"namespace" yaml:"namespace"`
	Mark      *string `json:"mark,omitempty" yaml:"mark,omitempty"`
	Context   *string `json:"context,omitempty" yaml:"context,omitempty"`
}

func newMarkUpdateCmd() *cobra.Command {
	var (
		namespaceName string
		mark, context string
	)
	cmd := &cobra.Command{
		Use:   "update <termId>",
		Short: "Change a term's source text or context",
		Long: `Changes the mark (source text) or the translator context of a term.
Pass only what changes; translations are kept. Find term ids with
` + "`norcube langsync terms list`" + `.`,
		Example: `  norcube langsync terms update 1234 --namespace web --mark "Save all changes"
  norcube langsync terms update 1234 -n web --context "button in the settings dialog"`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			termID, err := parseTermID(args[0])
			if err != nil {
				return nil, err
			}
			var body langsync.TermPayloadUpdateRequest
			if cmd.Flags().Changed("mark") {
				v := strings.TrimSpace(mark)
				if v == "" {
					return nil, clierr.Usage(errors.New("--mark must not be empty"))
				}
				body.Mark = &v
			}
			if cmd.Flags().Changed("context") {
				body.Context = &context
			}
			if body.Mark == nil && body.Context == nil {
				return nil, clierr.Usage(errors.New("nothing to update: pass --mark, --context or both"))
			}
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return nil, err
			}
			ns, err := resolveNamespace(cmd.Context(), c, namespaceName, "Update a term in which namespace?")
			if err != nil {
				return nil, cancelledOK(cmd, err)
			}
			res, err := c.API.UpdateTermWithResponse(cmd.Context(), ns, termID, body)
			if err != nil {
				return nil, err
			}
			if !ok2xx(res.HTTPResponse) {
				return nil, namespaceAPIError(c, ns, res.HTTPResponse, res.Body,
					res.JSON400, res.JSON401, res.JSON403, res.JSON404, res.JSON500)
			}
			return output.Done{
				Message: fmt.Sprintf("Updated term #%d in namespace %q.", termID, ns),
				Value:   termUpdated{ID: termID, Namespace: ns, Mark: body.Mark, Context: body.Context},
				Fields:  []output.Field{{Value: strconv.Itoa(termID)}},
			}, nil
		}),
	}
	cmd.Flags().StringVarP(&namespaceName, "namespace", "n", "", "Namespace name; picker opens when omitted and stdin is a TTY")
	cmd.Flags().StringVar(&mark, "mark", "", "New source text (unique within the namespace)")
	cmd.Flags().StringVar(&context, "context", "", `New context note for translators ("" clears it)`)
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
		Example: `  norcube langsync terms delete 1234 --namespace web
  norcube langsync terms delete 1234 --namespace web --yes   # scripts`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return err
			}
			termID, err := parseTermID(args[0])
			if err != nil {
				return err
			}
			ns, err := resolveNamespace(cmd.Context(), c, namespaceName, "Delete a mark from which namespace?")
			if err != nil {
				if errors.Is(err, cmdutil.ErrCancelled) {
					fmt.Fprintln(cmd.ErrOrStderr(), "Cancelled.")
					return nil
				}
				return err
			}

			ok, err := cmdutil.Confirm(
				fmt.Sprintf("Delete mark #%d from namespace %q? This cannot be undone.", termID, ns),
				yes,
			)
			if err != nil {
				if errors.Is(err, cmdutil.ErrCancelled) {
					fmt.Fprintln(cmd.ErrOrStderr(), "Cancelled.")
					return nil
				}
				return err
			}
			if !ok {
				fmt.Fprintln(cmd.ErrOrStderr(), "Cancelled.")
				return nil
			}

			res, err := c.API.DeleteTermWithResponse(cmd.Context(), ns, termID)
			if err != nil {
				return err
			}
			status := res.HTTPResponse.StatusCode
			if status != 200 && status != 204 {
				if isNamespaceForbidden(res.JSON403) || isNamespaceForbidden(res.JSON404) {
					return namespaceAccessError(c.OrgLabel(), ns)
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
			return "", cmdutil.ErrCancelled
		}
		return "", err
	}
	return strings.TrimSpace(v), nil
}
