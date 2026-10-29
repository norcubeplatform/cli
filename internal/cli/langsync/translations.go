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

func newTranslationsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "translations",
		Aliases: []string{"translation"},
		Short:   "Set, suggest and fill in translations of terms",
		Long: `A translation is a term's text in one of the namespace's languages.
Languages are named by code (cs) or numeric id and must be attached to
the namespace (see ` + "`norcube langsync languages list --namespace <name>`" + `).

Translations that AI produced carry an AI label until a person changes
them: the AI column of ` + "`terms list`" + `, "aiGenerated" with -o json.`,
	}
	cmd.AddCommand(newTranslationGetCmd(), newTranslationSetCmd(), newTranslationSuggestCmd(), newTranslateMissingCmd())
	return cmd
}

func newTranslationGetCmd() *cobra.Command {
	var namespaceName string
	cmd := &cobra.Command{
		Use:   "get <termId>",
		Short: "Show a term with its translation into every language",
		Long: `Shows one term (by its numeric id, the ID column of ` + "`terms list`" + `) with
its translation into each of the namespace's languages. "(AI)" after the
language marks a translation that is still exactly what AI produced
("aiGenerated" with -o json).`,
		Example: `  norcube langsync translations get 1234 --namespace web
  norcube langsync translations get 1234 -n web -o json --query "translations[?lang.code=='cs'].value | [0]"`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			termID, err := parseTermID(args[0])
			if err != nil {
				return nil, err
			}
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return nil, err
			}
			ns, err := resolveNamespace(cmd.Context(), c, namespaceName, "Show a term from which namespace?")
			if err != nil {
				return nil, cancelledOK(cmd, err)
			}
			res, err := c.API.GetTermWithResponse(cmd.Context(), ns, termID)
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, namespaceAPIError(c, ns, res.HTTPResponse, res.Body,
					res.JSON400, res.JSON401, res.JSON403, res.JSON404, res.JSON500)
			}
			t := *res.JSON200
			fields := []output.Field{
				{Name: "ID", Value: fmt.Sprint(t.Term.Id)},
				{Name: "Mark", Value: t.Term.Mark},
				{Name: "Context", Value: t.Term.Context},
				{Name: "Created", Value: compactDate(t.Term.CreatedAt)},
			}
			for _, tr := range t.Translations {
				name := tr.Lang.Code
				if tr.AiGenerated {
					name += " (AI)"
				}
				fields = append(fields, output.Field{Name: name, Value: tr.Value})
			}
			return output.Object{Value: t, Fields: fields}, nil
		}),
	}
	cmd.Flags().StringVarP(&namespaceName, "namespace", "n", "", "Namespace name; picker opens when omitted and stdin is a TTY")
	return cmd
}

// translationSet is what `translations set|suggest --save -o json` prints.
type translationSet struct {
	Namespace   string `json:"namespace" yaml:"namespace"`
	TermID      int    `json:"termId" yaml:"termId"`
	Language    string `json:"language" yaml:"language"`
	Value       string `json:"value" yaml:"value"`
	AIGenerated bool   `json:"aiGenerated" yaml:"aiGenerated"`
}

// setTranslation stores value as the term's translation into conn's
// language.
func setTranslation(cmd *cobra.Command, c *langsyncContext, ns string, termID int, conn langsync.DtoDTOLanguageConnection, value string, ai bool) (translationSet, error) {
	if conn.ConnectionId == nil {
		return translationSet{}, fmt.Errorf("language %q is missing its namespace connection id in the backend response", cmdutil.Deref(conn.LanguageCode))
	}
	body := langsync.TermPayloadUpdateTranslationRequest{Value: &value}
	if ai {
		body.AiGenerated = &ai
	}
	res, err := c.API.UpdateTermTranslationWithResponse(cmd.Context(), ns, termID, *conn.ConnectionId, body)
	if err != nil {
		return translationSet{}, err
	}
	if !ok2xx(res.HTTPResponse) {
		return translationSet{}, namespaceAPIError(c, ns, res.HTTPResponse, res.Body,
			res.JSON400, res.JSON401, res.JSON403, res.JSON404, res.JSON500)
	}
	return translationSet{
		Namespace: ns, TermID: termID, Language: cmdutil.Deref(conn.LanguageCode),
		Value: value, AIGenerated: ai,
	}, nil
}

func newTranslationSetCmd() *cobra.Command {
	var (
		namespaceName, file string
		ai                  bool
	)
	cmd := &cobra.Command{
		Use:   "set <termId> <language> [value]",
		Short: "Set a term's translation into one language",
		Long: `Stores the value as the term's translation into the language. A value
you set is a person's translation: it clears the AI label, unless --ai
says the value is an AI suggestion you saved unchanged.

Pass the value as the third argument, or with --file (- reads stdin) for
long or multi-line text. An empty value clears the translation.`,
		Example: `  norcube langsync translations set 1234 cs "Uložit změny" --namespace web
  norcube langsync translations set 1234 de --file welcome.de.txt -n web
  pbpaste | norcube langsync translations set 1234 fr --file - -n web`,
		Args: cobra.RangeArgs(2, 3),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			termID, err := parseTermID(args[0])
			if err != nil {
				return nil, err
			}
			if (len(args) == 3) == (file != "") {
				return nil, clierr.Usage(errors.New("pass the value as an argument or with --file, not both"))
			}
			value := ""
			if len(args) == 3 {
				value = args[2]
			}
			value, err = cmdutil.ReadText(cmd.InOrStdin(), value, file, "the translation")
			if err != nil {
				return nil, err
			}
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return nil, err
			}
			ns, err := resolveNamespace(cmd.Context(), c, namespaceName, "Set a translation in which namespace?")
			if err != nil {
				return nil, cancelledOK(cmd, err)
			}
			conn, err := resolveConnection(cmd.Context(), c, ns, args[1])
			if err != nil {
				return nil, err
			}
			set, err := setTranslation(cmd, c, ns, termID, conn, value, ai)
			if err != nil {
				return nil, err
			}
			return output.Done{
				Message: fmt.Sprintf("Set the %s translation of term #%d in namespace %q.", set.Language, termID, ns),
				Value:   set,
				Fields:  []output.Field{{Value: value}},
			}, nil
		}),
	}
	cmd.Flags().StringVarP(&namespaceName, "namespace", "n", "", "Namespace name; picker opens when omitted and stdin is a TTY")
	cmd.Flags().StringVar(&file, "file", "", "Read the value from this file (- for stdin)")
	cmd.Flags().BoolVar(&ai, "ai", false, "Keep the AI label: the value is an AI suggestion saved without changes")
	return cmd
}

// translationSuggestion is what `translations suggest -o json` prints.
type translationSuggestion struct {
	Namespace  string `json:"namespace" yaml:"namespace"`
	TermID     int    `json:"termId" yaml:"termId"`
	Language   string `json:"language" yaml:"language"`
	Suggestion string `json:"suggestion" yaml:"suggestion"`
	Saved      bool   `json:"saved" yaml:"saved"`
}

func newTranslationSuggestCmd() *cobra.Command {
	var (
		namespaceName, prompt, from string
		save                        bool
	)
	cmd := &cobra.Command{
		Use:   "suggest <termId> <language>",
		Short: "Ask AI for a translation of a term (and optionally save it)",
		Long: `Asks AI translation for the term's text in the language and prints the
suggestion. Nothing is stored unless --save is set; a saved suggestion
keeps its AI label. Each suggestion counts toward the organization's AI
translation usage.

The suggestion uses the namespace's default prompt unless --prompt names
another, and translates from the namespace's default language unless
--from names another attached language.`,
		Example: `  norcube langsync translations suggest 1234 cs --namespace web
  norcube langsync translations suggest 1234 de -n web --from en --save
  norcube langsync translations suggest 1234 fr -n web --prompt 5f0c2a9e-1d2b-4c3d-8e9f-0a1b2c3d4e5f -o text`,
		Args: cobra.ExactArgs(2),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			termID, err := parseTermID(args[0])
			if err != nil {
				return nil, err
			}
			promptID, err := optionalPrompt(cmd.Flags().Changed("prompt"), prompt)
			if err != nil {
				return nil, err
			}
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return nil, err
			}
			ns, err := resolveNamespace(cmd.Context(), c, namespaceName, "Suggest a translation in which namespace?")
			if err != nil {
				return nil, cancelledOK(cmd, err)
			}
			target, err := resolveConnection(cmd.Context(), c, ns, args[1])
			if err != nil {
				return nil, err
			}
			if target.LanguageId == nil {
				return nil, fmt.Errorf("language %q is missing an id in the backend response", args[1])
			}
			// Prompt is required by the request schema but unused by the
			// server; custom instructions go through --prompt.
			body := langsync.TermPayloadRecommendMagicTranslationRequest{CustomPromptId: promptID}
			if from != "" {
				src, err := resolveConnection(cmd.Context(), c, ns, from)
				if err != nil {
					return nil, err
				}
				body.SourceLangId = src.LanguageId
			}
			res, err := c.API.RecommendMagicTranslationWithResponse(cmd.Context(), ns, termID, *target.LanguageId, body)
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, namespaceAPIError(c, ns, res.HTTPResponse, res.Body,
					res.JSON400, res.JSON401, res.JSON403, res.JSON404, res.JSON500)
			}
			out := translationSuggestion{
				Namespace: ns, TermID: termID, Language: cmdutil.Deref(target.LanguageCode),
				Suggestion: res.JSON200.Translation,
			}
			msg := out.Suggestion
			if save {
				if _, err := setTranslation(cmd, c, ns, termID, target, out.Suggestion, true); err != nil {
					return nil, fmt.Errorf("the suggestion was made but not saved: %w", err)
				}
				out.Saved = true
				msg += fmt.Sprintf("\nSaved as the %s translation of term #%d.", out.Language, termID)
			}
			return output.Done{Message: msg, Value: out, Fields: []output.Field{{Value: out.Suggestion}}}, nil
		}),
	}
	cmd.Flags().StringVarP(&namespaceName, "namespace", "n", "", "Namespace name; picker opens when omitted and stdin is a TTY")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Custom prompt id to use instead of the namespace's default (see `norcube langsync prompts list`)")
	cmd.Flags().StringVar(&from, "from", "", "Source language code or id (default: the namespace's default language)")
	cmd.Flags().BoolVar(&save, "save", false, "Store the suggestion as the translation, with its AI label")
	return cmd
}

// translateMissingStarted is what `translate-missing -o json` prints.
type translateMissingStarted struct {
	Namespace string   `json:"namespace" yaml:"namespace"`
	Languages []string `json:"languages" yaml:"languages"`
	PromptID  *string  `json:"promptId,omitempty" yaml:"promptId,omitempty"`
	Started   bool     `json:"started" yaml:"started"`
}

func newTranslateMissingCmd() *cobra.Command {
	var (
		namespaceName, prompt string
		languages             []string
	)
	cmd := &cobra.Command{
		Use:   "translate-missing",
		Short: "AI-translate every missing translation in a namespace",
		Long: `Starts AI translation of every term that has no translation yet, in all
of the namespace's languages or only those --languages names. Existing
translations are not touched. The run continues on the server after the
command returns; follow it with
` + "`norcube langsync terms list --namespace <name> --has-untranslated`" + `.

AI translations count toward the organization's AI translation usage.`,
		Example: `  norcube langsync translations translate-missing --namespace web
  norcube langsync translations translate-missing -n web --languages cs,de
  norcube langsync translations translate-missing -n web --prompt 5f0c2a9e-1d2b-4c3d-8e9f-0a1b2c3d4e5f`,
		Args: cobra.NoArgs,
		RunE: cmdutil.Run(func(cmd *cobra.Command, _ []string) (any, error) {
			promptID, err := optionalPrompt(cmd.Flags().Changed("prompt"), prompt)
			if err != nil {
				return nil, err
			}
			c, err := newLangsyncContext(cmd)
			if err != nil {
				return nil, err
			}
			ns, err := resolveNamespace(cmd.Context(), c, namespaceName, "Translate missing translations in which namespace?")
			if err != nil {
				return nil, cancelledOK(cmd, err)
			}
			body := langsync.TermTranslateMissingTranslationsRequest{CustomPromptId: promptID}
			out := translateMissingStarted{Namespace: ns, Languages: []string{}, PromptID: promptID, Started: true}
			if len(languages) > 0 {
				ids := make([]int, 0, len(languages))
				for _, l := range languages {
					conn, err := resolveConnection(cmd.Context(), c, ns, l)
					if err != nil {
						return nil, err
					}
					if conn.LanguageId == nil {
						return nil, fmt.Errorf("language %q is missing an id in the backend response", l)
					}
					ids = append(ids, *conn.LanguageId)
					out.Languages = append(out.Languages, cmdutil.Deref(conn.LanguageCode))
				}
				body.LanguageIds = &ids
			}
			res, err := c.API.TranslateMissingTranslationsWithResponse(cmd.Context(), ns, body)
			if err != nil {
				return nil, err
			}
			if !ok2xx(res.HTTPResponse) {
				return nil, namespaceAPIError(c, ns, res.HTTPResponse, res.Body,
					res.JSON400, res.JSON401, res.JSON403, res.JSON404, res.JSON500)
			}
			scope := "every language"
			if len(out.Languages) > 0 {
				scope = strings.Join(out.Languages, ", ")
			}
			return output.Done{
				Message: fmt.Sprintf("Started AI translation of the missing translations in namespace %q (%s).\nFollow it with `norcube langsync terms list --namespace %s --has-untranslated`.", ns, scope, ns),
				Value:   out,
				Fields:  []output.Field{{Value: ns}, {Value: strings.Join(out.Languages, ",")}},
			}, nil
		}),
	}
	cmd.Flags().StringVarP(&namespaceName, "namespace", "n", "", "Namespace name; picker opens when omitted and stdin is a TTY")
	cmd.Flags().StringSliceVar(&languages, "languages", nil, "Only these languages (codes or ids, comma-separated; default: all of the namespace's languages)")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Custom prompt id to use instead of the namespace's default (see `norcube langsync prompts list`)")
	return cmd
}
