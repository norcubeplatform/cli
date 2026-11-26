package langsync

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/norcubeplatform/cli/internal/api/langsync"
	"github.com/norcubeplatform/cli/internal/cli/langsync/projectcfg"
	"github.com/norcubeplatform/cli/internal/cli/langsync/projectcfg/translations"
	"github.com/norcubeplatform/cli/internal/cmdutil"
)

// syncResult captures everything we need to know about a namespace's
// sync run, post-run. Used by both the dashboard's summary block
// and the Issues diagnostic.
type syncResult struct {
	namespace string
	stats     namespaceSyncStats
	err       error
	finalJob  *langsync.DtoDTOSyncJob
}

// collectIssues is the post-run diagnosis pass. For each namespace
// that ended with non-fatal problems, it returns one or more
// human-readable, actionable lines for the dashboard's Issues
// block.
//
// The main consumer right now is the "X cells still empty after
// autotranslate" case — which almost always means the default-lang
// source value is blank, so the LLM had nothing to translate from.
// We can detect that client-side without backend changes by:
//
//  1. asking the backend which marks still have untranslated cells
//  2. checking the local default-lang file for those marks
//  3. labelling each: "no source value (add it to <lang>.json)"
//     or "LLM skipped — try rerun later".
//
// This gives the user an actionable next step today; a future
// backend pass to surface per-cell skip reasons into the failures
// array would refine the categorisation.
func collectIssues(ctx context.Context, c *langsyncContext, cfg *projectcfg.File, configPath string, results []syncResult) []string {
	var lines []string
	for _, r := range results {
		if r.err != nil {
			continue // already shown as a row failure
		}
		if r.finalJob == nil {
			continue
		}
		// Backend-reported failures from the failures array.
		if r.finalJob.Failures != nil {
			for _, f := range *r.finalJob.Failures {
				lines = append(lines, failureLine(r.namespace, f))
			}
		}

		// "X cells still empty" diagnosis.
		stillEmpty := cmdutil.Deref(r.finalJob.StillUntranslatedCount)
		if stillEmpty == 0 {
			continue
		}
		ns, ok := cfg.FindNamespace(r.namespace)
		if !ok {
			lines = append(lines, fmt.Sprintf("[%s] %d cells still empty after autotranslate", r.namespace, stillEmpty))
			continue
		}
		diagnose := diagnoseEmptyCells(ctx, c, ns, configPath)
		if len(diagnose) == 0 {
			lines = append(lines, fmt.Sprintf("[%s] %d cells still empty after autotranslate — re-run sync later", r.namespace, stillEmpty))
			continue
		}
		// Cap at the first few diagnoses so a 50-empty namespace
		// doesn't drown the dashboard.
		const maxShown = 6
		for i, line := range diagnose {
			if i >= maxShown {
				lines = append(lines, fmt.Sprintf("[%s] …and %d more empty cells (run `nrc langsync terms list --namespace %s --has-untranslated` to see all)", r.namespace, len(diagnose)-maxShown, r.namespace))
				break
			}
			lines = append(lines, fmt.Sprintf("[%s] %s", r.namespace, line))
		}
	}
	return lines
}

// failureLine is one entry of the failures array for people. An entry
// with a code (a translation LangSync didn't store because of its text)
// names the key, the language, the code and its facts in words; an entry
// with dropped stands for the failures the server didn't list; any other
// keeps the server's English error.
func failureLine(namespace string, f langsync.DtoDTOSyncJobFailure) string {
	prefix := fmt.Sprintf("[%s] %s", namespace, cmdutil.Deref(f.Phase))
	if n := cmdutil.Deref(f.Dropped); n > 0 {
		return fmt.Sprintf("%s — and %d more", prefix, n)
	}
	mark := cmdutil.Deref(f.Mark)
	if lang := cmdutil.Deref(f.LanguageCode); lang != "" {
		mark = strings.TrimSpace(mark + " [" + lang + "]")
	}
	what := cmdutil.Deref(f.Error)
	if f.Code != nil {
		what = describeTranslationProblem(f)
	}
	if mark != "" {
		return fmt.Sprintf("%s — %s: %s", prefix, mark, what)
	}
	return fmt.Sprintf("%s — %s", prefix, what)
}

// translationProblemWords say what each code of a refused translation
// means. A code this CLI doesn't know yet is shown with the server's
// English error.
var translationProblemWords = map[langsync.ConstantsTermTranslationProblem]string{
	langsync.TermTranslationProblemPlaceholdersChanged: "the variables or placeholders changed",
	langsync.TermTranslationProblemICUInvalid:          "not a valid ICU message",
	langsync.TermTranslationProblemPluralForms:         "the plural forms don't fit the language",
	langsync.TermTranslationProblemTagsChanged:         "the tags changed",
	langsync.TermTranslationProblemGlossaryTermMissing: "a glossary term that stays as it is is missing",
	langsync.TermTranslationProblemTooManyVariants:     "too many plural and select variants (split the message)",
	langsync.TermTranslationProblemUntranslated:        "the model answered with the source text",
	langsync.TermTranslationProblemModelFailed:         "the model failed to translate it",
}

// describeTranslationProblem is "<code>: <words> (<facts>)".
func describeTranslationProblem(f langsync.DtoDTOSyncJobFailure) string {
	code := cmdutil.Deref(f.Code)
	words, ok := translationProblemWords[code]
	if !ok {
		words = cmdutil.Deref(f.Error)
	}
	var facts []string
	if f.Argument != nil && *f.Argument != "" {
		facts = append(facts, "argument "+*f.Argument)
	}
	if f.Offset != nil {
		facts = append(facts, fmt.Sprintf("at offset %d", *f.Offset))
	}
	if f.Missing != nil && len(*f.Missing) > 0 {
		facts = append(facts, "missing: "+strings.Join(*f.Missing, ", "))
	}
	if f.Unexpected != nil && len(*f.Unexpected) > 0 {
		facts = append(facts, "unexpected: "+strings.Join(*f.Unexpected, ", "))
	}
	out := strings.TrimSpace(string(code) + ": " + words)
	if len(facts) > 0 {
		out += " (" + strings.Join(facts, "; ") + ")"
	}
	return out
}

// unreviewedLine says how many translations per language nobody has
// marked reviewed, or "" when there are none.
func unreviewedLine(namespace string, counts map[string]int) string {
	codes := make([]string, 0, len(counts))
	total := 0
	for code, n := range counts {
		if n > 0 {
			codes = append(codes, code)
			total += n
		}
	}
	if total == 0 {
		return ""
	}
	sort.Strings(codes)
	parts := make([]string, len(codes))
	for i, code := range codes {
		parts[i] = fmt.Sprintf("%s %d", code, counts[code])
	}
	return fmt.Sprintf("[%s] %d %s not reviewed: %s", namespace, total,
		plural(total, "translation", "translations"), strings.Join(parts, ", "))
}

// diagnoseEmptyCells fetches the namespace's still-untranslated
// marks and labels each by likely cause. Most common cause: the
// mark's source value (in the default language) is blank.
//
// One round trip per namespace via the existing `terms list
// --has-untranslated` path; bounded by maxDiagnosePages so a huge
// namespace doesn't trigger a long blocking diagnostic.
func diagnoseEmptyCells(ctx context.Context, c *langsyncContext, ns projectcfg.Namespace, configPath string) []string {
	const (
		pageLimit        = 100
		maxDiagnosePages = 5 // 500 untranslated marks max
	)

	// Load local default-lang to check for empty source values.
	dir := localDirFor(ns, configPath)
	localPath := filepath.Join(dir, translations.LangFileName(ns.DefaultLocalLanguage))
	local, _ := translations.ReadFlatJSON(localPath) // ignore error → treat as empty map

	var out []string
	cursor := ""
	truthy := true
	for page := 0; page < maxDiagnosePages; page++ {
		params := &langsync.ListByNamespaceParams{
			Cursor:             optional(cursor),
			Limit:              optional(pageLimit),
			HasAnyUntranslated: &truthy,
		}
		res, err := c.API.ListByNamespaceWithResponse(ctx, ns.Namespace, params)
		if err != nil || res.JSON200 == nil {
			return out
		}
		for _, t := range res.JSON200.List {
			missingLangs := []string{}
			defaultValueOnServer := ""
			for _, tr := range t.Translations {
				if tr.Lang.Code == ns.DefaultLocalLanguage {
					defaultValueOnServer = tr.Value
					continue
				}
				if tr.Value == "" {
					missingLangs = append(missingLangs, tr.Lang.Code)
				}
			}
			if len(missingLangs) == 0 {
				continue
			}
			sort.Strings(missingLangs)
			localVal, hasLocal := local[t.Term.Mark]
			switch {
			case (!hasLocal || localVal == "") && defaultValueOnServer == "":
				out = append(out, fmt.Sprintf("%q — empty source value (add it to %s and rerun sync); missing: %s",
					t.Term.Mark, ns.DefaultLocalLanguage+".json", joinComma(missingLangs)))
			case defaultValueOnServer == "":
				// Local has it but the server doesn't yet — the
				// push presumably happened but autotranslate
				// already ran before push finished. Re-running
				// sync will pick it up.
				out = append(out, fmt.Sprintf("%q — server missing the %s source value (re-run sync); missing: %s",
					t.Term.Mark, ns.DefaultLocalLanguage, joinComma(missingLangs)))
			default:
				out = append(out, fmt.Sprintf("%q — LLM skipped/failed for: %s (re-run sync later or check backend logs)",
					t.Term.Mark, joinComma(missingLangs)))
			}
		}
		if res.JSON200.Cursors.Next == nil || *res.JSON200.Cursors.Next == "" {
			break
		}
		cursor = *res.JSON200.Cursors.Next
	}
	return out
}

// localDirFor mirrors projectcfg.File.ResolveDir but takes a single
// namespace + configPath, so callers don't need the parent File
// reference handy.
func localDirFor(ns projectcfg.Namespace, configPath string) string {
	if filepath.IsAbs(ns.Dir) {
		return filepath.Clean(ns.Dir)
	}
	return filepath.Clean(filepath.Join(filepath.Dir(configPath), ns.Dir))
}
