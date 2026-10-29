package langsync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/langsync"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

func newGlossaryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "glossary",
		Short: "Manage the organization's glossary",
		Long: `The glossary holds the organization's fixed terminology: product names
that must never be translated, and words with an agreed translation per
language. AI translation follows it in every namespace.

The organization has one glossary; it is set up in the dashboard, which
also chooses the languages it covers.`,
	}
	items := &cobra.Command{
		Use:     "items",
		Aliases: []string{"item"},
		Short:   "List, add and remove glossary entries and their translations",
	}
	items.AddCommand(newGlossaryItemListCmd(), newGlossaryItemGetCmd(), newGlossaryItemCreateCmd(),
		newGlossaryItemTranslateCmd(), newGlossaryItemDeleteCmd())
	cmd.AddCommand(newGlossaryGetCmd(), items)
	return cmd
}

// glossaryItemTypes are the word classes the API accepts; n/a is "not
// applicable" (a product name, an abbreviation).
var glossaryItemTypes = []string{
	"noun", "verb", "adjective", "adverb", "pronoun", "preposition",
	"conjunction", "interjection", "phrase", "idiom", "n/a",
}

// glossaryInfo is what `glossary get` prints.
type glossaryInfo struct {
	ID                string `json:"id" yaml:"id"`
	DefaultLanguageID int    `json:"defaultLanguageId" yaml:"defaultLanguageId"`
	DefaultLanguage   string `json:"defaultLanguage" yaml:"defaultLanguage"`
}

func newGlossaryGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get",
		Short: "Show the organization's glossary",
		Example: `  norcube langsync glossary get
  norcube langsync glossary get -o json`,
		Args: cobra.NoArgs,
		RunE: cmdutil.Run(func(cmd *cobra.Command, _ []string) (any, error) {
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.GetGlossaryWithResponse(cmd.Context())
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, glossaryError(res.HTTPResponse, res.Body, res.JSON400, res.JSON401, res.JSON500)
			}
			codes, err := fetchLanguageCodesByID(cmd.Context(), c)
			if err != nil {
				return nil, err
			}
			g := glossaryInfo{ID: cmdutil.Deref(res.JSON200.Id), DefaultLanguageID: cmdutil.Deref(res.JSON200.DefaultLanguageId)}
			g.DefaultLanguage = languageCode(res.JSON200.DefaultLanguageId, codes)
			return output.Object{Value: g, Fields: []output.Field{
				{Name: "ID", Value: g.ID},
				{Name: "Default language", Value: g.DefaultLanguage},
			}}, nil
		}),
	}
}

// glossaryError is apiError for glossary endpoints, which answer 404 when
// the organization has no glossary yet.
func glossaryError(resp *http.Response, body []byte, typed ...*langsync.ResponseAPIError) error {
	err := apiError(resp, body, typed...)
	var e *clierr.Error
	if resp != nil && resp.StatusCode == http.StatusNotFound && errors.As(err, &e) && e.Hint == "" {
		e.Hint = "If the organization has no glossary yet, set it up in the LangSync dashboard."
	}
	return err
}

func newGlossaryItemListCmd() *cobra.Command {
	var pages cmdutil.PageFlags
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List glossary entries",
		Long: `Lists the glossary's entries. TRANSLATED is how many of the glossary's
languages have a translation; entries that are not translatable (KEEP)
stay the same in every language.`,
		Example: `  norcube langsync glossary items list
  norcube langsync glossary items list --all-pages -o json --query "[?!isTranslatable].defaultLanguageValue"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return err
			}
			page, err := cmdutil.Paginate(cmd.Context(), pages, func(ctx context.Context, cursor string, size int) ([]langsync.DtoGlossaryItem, string, error) {
				params := &langsync.ListGlossaryItemsParams{Limit: &size}
				if cursor != "" {
					params.Cursor = &cursor
				}
				res, err := c.API.ListGlossaryItemsWithResponse(ctx, params)
				if err != nil {
					return nil, "", err
				}
				if res.JSON200 == nil {
					return nil, "", glossaryError(res.HTTPResponse, res.Body, res.JSON400, res.JSON401, res.JSON500)
				}
				return res.JSON200.List, cmdutil.Deref(res.JSON200.Cursors.Next), nil
			})
			if err != nil {
				return err
			}
			return page.Print(cmd, output.Table[langsync.DtoGlossaryItem]{
				Headers:   []string{"ID", "TERM", "TYPE", "CASE", "KEEP", "TRANSLATED", "CONTEXT"},
				MaxWidths: []int{0, 0, 0, 0, 0, 0, 40},
				Rows: func(i langsync.DtoGlossaryItem) []string {
					translated := fmt.Sprintf("%d/%d", i.TranslatedCount, i.EnabledLanguagesCount)
					if !i.IsTranslatable {
						translated = "—"
					}
					return []string{
						i.Id, i.DefaultLanguageValue, string(i.Type),
						caseLabel(i.IsCaseSensitive), yesNo(!i.IsTranslatable), translated, i.Context,
					}
				},
				Items: page.Items,
			})
		},
	}
	cmdutil.AddPageFlags(cmd, &pages, defaultMarkPageLimit, maxMarkItems)
	return cmd
}

func caseLabel(sensitive bool) string {
	if sensitive {
		return "exact"
	}
	return "any"
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func getGlossaryItem(cmd *cobra.Command, c *langsyncContext, id string) (*langsync.GlossaryPayloadGetItemResponse, error) {
	res, err := c.API.GetGlossaryItemWithResponse(cmd.Context(), id)
	if err != nil {
		return nil, err
	}
	if res.JSON200 == nil {
		return nil, glossaryError(res.HTTPResponse, res.Body, res.JSON400, res.JSON401, res.JSON500)
	}
	return res.JSON200, nil
}

func newGlossaryItemGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <itemId>",
		Short: "Show a glossary entry with its translations",
		Example: `  norcube langsync glossary items get 3b9e6f1c-7a2d-4e5f-9c8b-1a2b3c4d5e6f
  norcube langsync glossary items get 3b9e6f1c-7a2d-4e5f-9c8b-1a2b3c4d5e6f -o json --query "glossaryItemTranslations[].languageCode"`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return nil, err
			}
			got, err := getGlossaryItem(cmd, c, strings.TrimSpace(args[0]))
			if err != nil {
				return nil, err
			}
			i := got.GlossaryItem
			fields := []output.Field{
				{Name: "ID", Value: i.Id},
				{Name: "Term", Value: i.DefaultLanguageValue},
				{Name: "Type", Value: string(i.Type)},
				{Name: "Case", Value: caseLabel(i.IsCaseSensitive)},
				{Name: "Translatable", Value: yesNo(i.IsTranslatable)},
				{Name: "Context", Value: i.Context},
				{Name: "Created", Value: compactDate(i.CreatedAt)},
			}
			if got.GlossaryItemTranslations != nil {
				for _, t := range *got.GlossaryItemTranslations {
					fields = append(fields, output.Field{Name: t.LanguageCode, Value: t.Value})
				}
			}
			return output.Object{Value: got, Fields: fields}, nil
		}),
	}
}

func newGlossaryItemCreateCmd() *cobra.Command {
	var (
		context, itemType          string
		caseSensitive, untranslate bool
	)
	cmd := &cobra.Command{
		Use:   "create <term>",
		Short: "Add an entry to the glossary",
		Long: `Adds the term, written in the glossary's default language. Use
--keep for names that must stay the same in every language, and
` + "`glossary items translate`" + ` to give a translatable entry its agreed
translations.`,
		Example: `  norcube langsync glossary items create Norcube --keep --case-sensitive
  norcube langsync glossary items create invoice --type noun --context "a bill sent to a customer"`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			term := strings.TrimSpace(args[0])
			if term == "" {
				return nil, clierr.Usage(errors.New("the term must not be empty"))
			}
			if !slices.Contains(glossaryItemTypes, itemType) {
				return nil, clierr.Usage(fmt.Errorf("--type must be one of %s", strings.Join(glossaryItemTypes, ", ")))
			}
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return nil, err
			}
			body := langsync.GlossaryCreateItemRequest{
				DefaultLanguageValue: term,
				Type:                 langsync.ConstantsGlossaryItemType(itemType),
				IsCaseSensitive:      caseSensitive,
				IsTranslatable:       !untranslate,
			}
			if context != "" {
				body.Context = &context
			}
			res, err := c.API.CreateGlossaryItemWithResponse(cmd.Context(), body)
			if err != nil {
				return nil, err
			}
			if res.JSON201 == nil {
				return nil, glossaryError(res.HTTPResponse, res.Body, res.JSON400, res.JSON401, res.JSON500)
			}
			return output.Done{
				Message: fmt.Sprintf("Added %q to the glossary (%s).", term, res.JSON201.ItemId),
				Value:   map[string]any{"id": res.JSON201.ItemId, "term": term, "type": itemType, "isTranslatable": !untranslate, "isCaseSensitive": caseSensitive},
				Fields:  []output.Field{{Value: res.JSON201.ItemId}},
			}, nil
		}),
	}
	cmd.Flags().StringVar(&context, "context", "", "What the term means here, for translators and AI translation")
	cmd.Flags().StringVar(&itemType, "type", "n/a", "Word class: "+strings.Join(glossaryItemTypes, ", "))
	cmd.Flags().BoolVar(&caseSensitive, "case-sensitive", false, "Match the term only with this exact capitalization")
	cmd.Flags().BoolVar(&untranslate, "keep", false, "Never translate the term (a product or brand name)")
	_ = cmd.RegisterFlagCompletionFunc("type", cobra.FixedCompletions(glossaryItemTypes, cobra.ShellCompDirectiveNoFileComp))
	return cmd
}

// glossaryTranslation is what `glossary items translate -o json` prints.
type glossaryTranslation struct {
	ItemID   string `json:"itemId" yaml:"itemId"`
	Language string `json:"language" yaml:"language"`
	Value    string `json:"value" yaml:"value"`
}

func newGlossaryItemTranslateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "translate <itemId> <language> <value>",
		Short: "Set a glossary entry's agreed translation into one language",
		Long: `Sets the entry's translation into the language (code or id), adding it
or replacing the one there. The language must be one the glossary
covers.`,
		Example: `  norcube langsync glossary items translate 3b9e6f1c-7a2d-4e5f-9c8b-1a2b3c4d5e6f cs faktura
  norcube langsync glossary items translate 3b9e6f1c-7a2d-4e5f-9c8b-1a2b3c4d5e6f de Rechnung`,
		Args: cobra.ExactArgs(3),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			id, value := strings.TrimSpace(args[0]), args[2]
			if strings.TrimSpace(value) == "" {
				return nil, clierr.Usage(errors.New("the translation must not be empty"))
			}
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return nil, err
			}
			lang, err := resolveOrgLanguage(cmd.Context(), c, args[1])
			if err != nil {
				return nil, err
			}
			langID := cmdutil.Deref(lang.Id)
			got, err := getGlossaryItem(cmd, c, id)
			if err != nil {
				return nil, err
			}
			if !got.GlossaryItem.IsTranslatable {
				return nil, clierr.Usage(fmt.Errorf("glossary entry %q is kept as it is in every language; it has no translations", got.GlossaryItem.DefaultLanguageValue))
			}
			exists := got.GlossaryItemTranslations != nil && slices.ContainsFunc(*got.GlossaryItemTranslations,
				func(t langsync.DtoGlossaryItemTranslation) bool { return t.LanguageId == langID })
			var resp *http.Response
			var body []byte
			var typed []*langsync.ResponseAPIError
			if exists {
				res, err := c.API.UpdateGlossaryItemTranslationWithResponse(cmd.Context(), id, langID, langsync.GlossaryUpdateTranslationRequest{Value: value})
				if err != nil {
					return nil, err
				}
				resp, body, typed = res.HTTPResponse, res.Body, []*langsync.ResponseAPIError{res.JSON400, res.JSON401, res.JSON500}
			} else {
				res, err := c.API.AddTranslationToGlossaryItemWithResponse(cmd.Context(), id, langsync.GlossaryAddTranslationRequest{LanguageId: langID, Value: value})
				if err != nil {
					return nil, err
				}
				resp, body, typed = res.HTTPResponse, res.Body, []*langsync.ResponseAPIError{res.JSON400, res.JSON401, res.JSON500}
			}
			if !ok2xx(resp) {
				return nil, glossaryError(resp, body, typed...)
			}
			code := cmdutil.Deref(lang.Code)
			return output.Done{
				Message: fmt.Sprintf("Set the %s translation of %q to %q.", code, got.GlossaryItem.DefaultLanguageValue, value),
				Value:   glossaryTranslation{ItemID: id, Language: code, Value: value},
				Fields:  []output.Field{{Value: id}, {Value: code}, {Value: value}},
			}, nil
		}),
	}
}

func newGlossaryItemDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <itemId>",
		Aliases: []string{"rm"},
		Short:   "Remove an entry (and its translations) from the glossary",
		Example: `  norcube langsync glossary items delete 3b9e6f1c-7a2d-4e5f-9c8b-1a2b3c4d5e6f
  norcube langsync glossary items delete 3b9e6f1c-7a2d-4e5f-9c8b-1a2b3c4d5e6f --yes   # scripts`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			id := strings.TrimSpace(args[0])
			ok, err := cmdutil.Confirm(fmt.Sprintf("Delete glossary entry %s and its translations?", id), yes)
			if err != nil || !ok {
				return nil, cancelledOK(cmd, cancelledIfDeclined(err))
			}
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.DeleteGlossaryItemWithResponse(cmd.Context(), id)
			if err != nil {
				return nil, err
			}
			if !ok2xx(res.HTTPResponse) {
				return nil, glossaryError(res.HTTPResponse, res.Body, res.JSON400, res.JSON401, res.JSON500)
			}
			return output.Done{
				Message: fmt.Sprintf("Deleted glossary entry %s.", id),
				Value:   deletedResult{ID: id, Deleted: true},
				Fields:  []output.Field{{Value: id}},
			}, nil
		}),
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip the confirmation prompt (required when stdin isn't a TTY)")
	return cmd
}
