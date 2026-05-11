package langsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/norcubeplatform/cli/internal/api/langsync"
	"github.com/norcubeplatform/cli/internal/cli/langsync/projectcfg"
	"github.com/norcubeplatform/cli/internal/cli/langsync/projectcfg/translations"
)

// preflightAttachLanguages runs before runParallelSync starts the
// dashboard. For each target namespace, it discovers local
// <code>.json files whose lang isn't yet attached and resolves
// each one through a three-tier match:
//
//  1. Exact code match (case-insensitive) → silent attach, no alias.
//  2. Separator-only match (cs_cz ↔ cs-CZ) OR a single other
//     primary-subtag candidate (fr ↔ fr-FR with no fr-CA in the
//     org) → silent attach, record an alias in .langsync.json so
//     subsequent syncs apply the mapping without prompting.
//  3. Multiple candidates OR no candidates → prompt the user with
//     a searchable picker (matches surfaced at top, then
//     "Create custom" and "Skip", then the full org lang list).
//
// New aliases collected during the run are persisted back to
// .langsync.json before the function returns. Sync then submits
// per-language data under the server-known code, and pull-back
// writes files under the local (aliased) code so the user's
// preferred spelling stays on disk.
func preflightAttachLanguages(ctx context.Context, c *langsyncContext, cfg *projectcfg.File, configPath string, targets []projectcfg.Namespace) error {
	if !stdinIsInteractive() {
		return warnUnattachedLocalLangs(ctx, c, cfg, configPath, targets)
	}

	orgLangs, err := fetchOrgLanguageList(ctx, c)
	if err != nil {
		return err
	}

	cfgDirty := false
	for i := range targets {
		ns := &targets[i]
		added, err := preflightAttachOne(ctx, c, ns, configPath, orgLangs)
		if err != nil {
			return err
		}
		if len(added) > 0 {
			// Mirror the additions into the cfg.Namespaces slice
			// (targets are projectcfg.Namespace values, but they
			// came from cfg.Namespaces — find by name and update).
			for j := range cfg.Namespaces {
				if cfg.Namespaces[j].Namespace == ns.Namespace {
					if cfg.Namespaces[j].LanguageAliases == nil {
						cfg.Namespaces[j].LanguageAliases = map[string]string{}
					}
					for k, v := range added {
						cfg.Namespaces[j].LanguageAliases[k] = v
					}
					cfgDirty = true
					break
				}
			}
			// Also mirror into the targets[i] copy so the caller's
			// subsequent sync work (which reads ns directly) picks
			// up the new aliases.
			if ns.LanguageAliases == nil {
				ns.LanguageAliases = map[string]string{}
			}
			for k, v := range added {
				ns.LanguageAliases[k] = v
			}
		}
	}
	if cfgDirty {
		if err := projectcfg.Save(configPath, cfg); err != nil {
			return fmt.Errorf("save aliases to %s: %w", configPath, err)
		}
	}
	return nil
}

// preflightAttachOne handles one namespace. Returns the new aliases
// collected during this run (disk code → server code), to be merged
// into the namespace's LanguageAliases by the caller.
func preflightAttachOne(ctx context.Context, c *langsyncContext, ns *projectcfg.Namespace, configPath string, orgLangs []langsync.DtoDTOLanguage) (map[string]string, error) {
	dir := localDirFor(*ns, configPath)
	localCodes, _, err := translations.ListLangsInDir(dir)
	if err != nil {
		return nil, fmt.Errorf("[%s] scan local files: %w", ns.Namespace, err)
	}
	if len(localCodes) == 0 {
		return nil, nil
	}

	attachedServerCodes, err := fetchAttachedLanguageCodes(ctx, c, ns.Namespace)
	if err != nil {
		return nil, fmt.Errorf("[%s] list attached languages: %w", ns.Namespace, err)
	}
	attachedSet := map[string]bool{}
	for _, code := range attachedServerCodes {
		attachedSet[code] = true
	}

	newAliases := map[string]string{}
	// Existing aliases already tell us where a local code lives on
	// the server. Use them up-front so an already-resolved file
	// (e.g. cs_cz → cs-CZ from a past run) doesn't get prompted
	// again.
	for _, code := range localCodes {
		serverCode := code
		if ns.LanguageAliases != nil {
			if mapped, ok := ns.LanguageAliases[code]; ok && mapped != "" {
				serverCode = mapped
			}
		}
		if attachedSet[serverCode] {
			continue
		}

		match := classifyLangMatch(code, orgLangs)
		switch {
		case match.exact != nil:
			// Silent attach. Local file code == server lang code
			// (case-insensitive); no alias needed.
			if err := attachOneLanguage(ctx, c, ns.Namespace, *(match.exact.Code), match.exact); err != nil {
				fmt.Fprintf(os.Stderr, "[%s] auto-attach %s failed: %v\n", ns.Namespace, code, err)
				continue
			}
			fmt.Fprintf(os.Stderr, "[%s] attached %s\n", ns.Namespace, languageDisplayLabel(*match.exact))
			attachedSet[*match.exact.Code] = true

		case len(match.unique()) == 1:
			// Exactly one candidate (separator-only OR single
			// primary-subtag match) → silent attach + alias.
			cand := match.unique()[0]
			if err := attachOneLanguage(ctx, c, ns.Namespace, *cand.Code, &cand); err != nil {
				fmt.Fprintf(os.Stderr, "[%s] auto-attach %s failed: %v\n", ns.Namespace, code, err)
				continue
			}
			newAliases[code] = *cand.Code
			fmt.Fprintf(os.Stderr,
				"[%s] mapped %s.json → %s and attached (alias persisted to .langsync.json)\n",
				ns.Namespace, code, languageDisplayLabel(cand))
			attachedSet[*cand.Code] = true

		default:
			// Multiple candidates or none at all → prompt.
			alias, err := promptResolveAmbiguousLang(ctx, c, ns.Namespace, code, match.unique(), orgLangs)
			if err != nil {
				if errors.Is(err, ErrCancelled) {
					fmt.Fprintf(os.Stderr, "[%s] attach prompt cancelled — remaining files will be skipped\n", ns.Namespace)
					return newAliases, nil
				}
				return newAliases, err
			}
			if alias.serverCode != "" {
				// Picker resolved either "create custom <code>"
				// (alias.serverCode == code, no real alias needed)
				// or "map to existing X" (alias.serverCode == X).
				if alias.serverCode != code {
					newAliases[code] = alias.serverCode
				}
				attachedSet[alias.serverCode] = true
			}
		}
	}
	return newAliases, nil
}

// langMatchResult groups the org-language candidates for one local
// code into four tiers. exact wins outright; the other three buckets
// feed into the "single deduped candidate → silent" rule (any
// combination counts, as long as the total is exactly one lang).
type langMatchResult struct {
	exact         *langsync.DtoDTOLanguage
	separatorOnly []langsync.DtoDTOLanguage // cs_cz ↔ cs-CZ
	primarySubtag []langsync.DtoDTOLanguage // fr ↔ fr-FR, cs-CZ ↔ cs
	nameMatch     []langsync.DtoDTOLanguage // czech.json ↔ "Czech"
}

// unique returns the deduplicated candidate list across the three
// non-exact tiers. Order is separator-only → primary-subtag →
// name-match: that's the order rows surface in the picker, with
// the most-confident matches at the top.
func (m langMatchResult) unique() []langsync.DtoDTOLanguage {
	seen := map[string]bool{}
	out := []langsync.DtoDTOLanguage{}
	add := func(l langsync.DtoDTOLanguage) {
		if l.Code == nil {
			return
		}
		key := strings.ToLower(*l.Code)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, l)
	}
	for _, l := range m.separatorOnly {
		add(l)
	}
	for _, l := range m.primarySubtag {
		add(l)
	}
	for _, l := range m.nameMatch {
		add(l)
	}
	return out
}

// classifyLangMatch buckets the org's lang list according to how
// each lang relates to localCode. An org lang lands in the highest-
// confidence bucket that fits and not the others (exact > separator
// > primary subtag > name). Name-match needs the local code to be
// at least 3 characters — shorter ones like "en" already get caught
// by exact/primary-subtag tiers and would otherwise flag every
// "English X" lang as a candidate.
func classifyLangMatch(localCode string, orgLangs []langsync.DtoDTOLanguage) langMatchResult {
	var out langMatchResult
	lcLower := strings.ToLower(localCode)
	lcNorm := normaliseLangCode(localCode)
	lcPrimary := primaryLangSubtag(localCode)
	doNameMatch := len(localCode) >= 3
	for i, l := range orgLangs {
		if l.Code == nil {
			continue
		}
		olLower := strings.ToLower(*l.Code)
		switch {
		case olLower == lcLower:
			out.exact = &orgLangs[i]
		case normaliseLangCode(*l.Code) == lcNorm:
			out.separatorOnly = append(out.separatorOnly, l)
		case primaryLangSubtag(*l.Code) == lcPrimary && lcPrimary != "":
			out.primarySubtag = append(out.primarySubtag, l)
		case doNameMatch && l.Name != nil && strings.HasPrefix(strings.ToLower(*l.Name), lcLower):
			out.nameMatch = append(out.nameMatch, l)
		}
	}
	return out
}

// normaliseLangCode strips "-" and "_" and lowercases the result.
// Used for the separator-only tier (cs_cz ↔ cs-CZ).
func normaliseLangCode(code string) string {
	out := strings.ToLower(code)
	out = strings.ReplaceAll(out, "-", "")
	out = strings.ReplaceAll(out, "_", "")
	return out
}

// primaryLangSubtag returns the part of code before the first "-"
// or "_", lowercased. The BCP-47 primary subtag is the right
// abstraction for "is this the same language at all" — region/
// script suffixes vary but the primary tag identifies the
// language. Returns "" when code is empty.
func primaryLangSubtag(code string) string {
	if code == "" {
		return ""
	}
	for i, r := range code {
		if r == '-' || r == '_' {
			return strings.ToLower(code[:i])
		}
	}
	return strings.ToLower(code)
}

// languageDisplayLabel renders an org language as "Name (code)" or
// just "code" when the name is missing/equal. Used in stderr notes
// and picker rows.
func languageDisplayLabel(l langsync.DtoDTOLanguage) string {
	code := ""
	if l.Code != nil {
		code = *l.Code
	}
	name := ""
	if l.Name != nil {
		name = *l.Name
	}
	if name != "" && name != code {
		return fmt.Sprintf("%s (%s)", name, code)
	}
	if code != "" {
		return code
	}
	return "<unnamed language>"
}

// ambiguousResolution captures what the user picked in the prompt.
// serverCode is the eventual server-side language code:
//   - empty: user picked "skip"
//   - == fileCode: user picked "create custom <fileCode>"
//   - else: user picked "map to existing X" with X's code
type ambiguousResolution struct {
	serverCode string
}

// promptResolveAmbiguousLang shows one searchable Select per
// unmatched local file. Surfaces likely candidates (primary-subtag
// + separator-only matches) at the top, then Create / Skip, then
// the full org lang list.
//
// Apply order in the picker:
//   - "[Map to <Name> (<code>)]" — for each candidate
//   - "[Create custom <code> and attach]"
//   - "[Skip this file]"
//   - "Map to <Name> (<code>)" — for every other org lang, sorted
//     alphabetically by code, so the user can type to filter
func promptResolveAmbiguousLang(ctx context.Context, c *langsyncContext, namespace, fileCode string, candidates []langsync.DtoDTOLanguage, allOrgLangs []langsync.DtoDTOLanguage) (ambiguousResolution, error) {
	const (
		createSentinel = "__create__"
		skipSentinel   = "__skip__"
	)

	// Print the per-file heading so it's clear which file we're
	// resolving when there are multiple prompts in a row.
	fmt.Fprintf(os.Stderr, "\n[%s] resolve %s.json (no clean match)\n", namespace, fileCode)
	if len(candidates) > 0 {
		fmt.Fprintf(os.Stderr, "  candidates with matching primary subtag: ")
		labels := make([]string, 0, len(candidates))
		for _, l := range candidates {
			labels = append(labels, languageDisplayLabel(l))
		}
		fmt.Fprintln(os.Stderr, strings.Join(labels, ", "))
	}

	opts := make([]huh.Option[string], 0, 2+len(allOrgLangs)+len(candidates))

	// Recommended candidates first, prefixed so they stand out.
	candidateIDs := map[string]bool{}
	for _, l := range candidates {
		if l.Id == nil {
			continue
		}
		opts = append(opts, huh.NewOption(
			fmt.Sprintf("★ Map to %s", languageDisplayLabel(l)),
			fmt.Sprintf("id:%d", *l.Id),
		))
		candidateIDs[fmt.Sprintf("id:%d", *l.Id)] = true
	}

	opts = append(opts,
		huh.NewOption(fmt.Sprintf("+ Create custom language %q and attach", fileCode), createSentinel),
		huh.NewOption("× Skip this file (don't include in this sync)", skipSentinel),
	)

	// Full lang list sorted by code — already-surfaced candidates
	// are omitted to avoid duplication.
	sortedLangs := make([]langsync.DtoDTOLanguage, len(allOrgLangs))
	copy(sortedLangs, allOrgLangs)
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
		if l.Id == nil {
			continue
		}
		key := fmt.Sprintf("id:%d", *l.Id)
		if candidateIDs[key] {
			continue
		}
		opts = append(opts, huh.NewOption(
			fmt.Sprintf("Map to %s", languageDisplayLabel(l)),
			key,
		))
	}

	var picked string
	err := newWizard(huh.NewGroup(
		huh.NewSelect[string]().
			Title(fmt.Sprintf("Decide for %s.json", fileCode)).
			Description("Type to filter (search by language code or name). ★ = recommended based on code or language name match. Arrows move; Enter confirms.").
			Options(opts...).
			Filtering(true).
			Height(12).
			Value(&picked),
	)).Run()
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return ambiguousResolution{}, ErrCancelled
		}
		return ambiguousResolution{}, err
	}

	switch {
	case picked == skipSentinel:
		fmt.Fprintf(os.Stderr, "[%s] skipped %s.json\n", namespace, fileCode)
		return ambiguousResolution{}, nil
	case picked == createSentinel:
		if err := attachOneLanguage(ctx, c, namespace, fileCode, nil); err != nil {
			fmt.Fprintf(os.Stderr, "[%s] create+attach %s failed: %v\n", namespace, fileCode, err)
			return ambiguousResolution{}, nil
		}
		fmt.Fprintf(os.Stderr, "[%s] created custom language %q and attached\n", namespace, fileCode)
		return ambiguousResolution{serverCode: fileCode}, nil
	case strings.HasPrefix(picked, "id:"):
		var target *langsync.DtoDTOLanguage
		for i := range sortedLangs {
			if sortedLangs[i].Id == nil {
				continue
			}
			if fmt.Sprintf("id:%d", *sortedLangs[i].Id) == picked {
				target = &sortedLangs[i]
				break
			}
		}
		if target == nil {
			return ambiguousResolution{}, fmt.Errorf("internal: picked language id %s not found", picked)
		}
		if err := attachOneLanguage(ctx, c, namespace, derefOr(target.Code, fileCode), target); err != nil {
			fmt.Fprintf(os.Stderr, "[%s] attach %s failed: %v\n", namespace, languageDisplayLabel(*target), err)
			return ambiguousResolution{}, nil
		}
		fmt.Fprintf(os.Stderr, "[%s] mapped %s.json → %s and attached (alias persisted to .langsync.json)\n",
			namespace, fileCode, languageDisplayLabel(*target))
		return ambiguousResolution{serverCode: derefOr(target.Code, "")}, nil
	}
	return ambiguousResolution{}, fmt.Errorf("internal: unrecognised picker selection %q", picked)
}

// attachOneLanguage attaches an existing shared/custom language to
// the namespace, or creates a custom one (when existing is nil)
// and attaches the result. Used by the silent tiers AND by the
// prompt resolution path.
func attachOneLanguage(ctx context.Context, c *langsyncContext, namespace, code string, existing *langsync.DtoDTOLanguage) error {
	if existing == nil {
		createRes, err := c.client.CreateCustomLanguageWithResponse(ctx,
			langsync.CreateCustomLanguageJSONRequestBody{
				Code: code,
				Name: code,
			})
		if err != nil {
			return err
		}
		if createRes.JSON201 == nil {
			return apiError(createRes.HTTPResponse, createRes.Body,
				createRes.JSON400, createRes.JSON401, createRes.JSON409, createRes.JSON500)
		}
		existing = createRes.JSON201
	}

	body := langsync.AddLanguageJSONRequestBody{}
	if existing.Id != nil {
		id := *existing.Id
		body.LanguageId = &id
	} else {
		c := code
		body.LanguageCode = &c
	}
	res, err := c.client.AddLanguageWithResponse(ctx, namespace, body)
	if err != nil {
		return err
	}
	if res.JSON200 == nil {
		return apiError(res.HTTPResponse, res.Body,
			res.JSON400, res.JSON401, res.JSON403, res.JSON404, res.JSON500)
	}
	return nil
}

// warnUnattachedLocalLangs is the non-TTY counterpart of the
// pre-flight. One stderr line per skipped file with the exact
// command to fix it.
func warnUnattachedLocalLangs(ctx context.Context, c *langsyncContext, cfg *projectcfg.File, configPath string, targets []projectcfg.Namespace) error {
	for _, ns := range targets {
		dir := cfg.ResolveDir(configPath, ns)
		localCodes, _, err := translations.ListLangsInDir(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[%s] scan local files failed: %v\n", ns.Namespace, err)
			continue
		}
		attached, err := fetchAttachedLanguageCodes(ctx, c, ns.Namespace)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[%s] list attached languages failed: %v\n", ns.Namespace, err)
			continue
		}
		attachedSet := map[string]bool{}
		for _, code := range attached {
			attachedSet[code] = true
		}
		var unattached []string
		for _, code := range localCodes {
			serverCode := code
			if ns.LanguageAliases != nil {
				if mapped, ok := ns.LanguageAliases[code]; ok && mapped != "" {
					serverCode = mapped
				}
			}
			if attachedSet[serverCode] {
				continue
			}
			unattached = append(unattached, code)
		}
		if len(unattached) == 0 {
			continue
		}
		sort.Strings(unattached)
		fmt.Fprintf(os.Stderr,
			"[%s] warning: %d local lang file(s) (%s) have no matching attached language — they will be skipped this sync. Run `nrc langsync sync` interactively to resolve, or pre-attach with `nrc langsync lang add <code> -n %s`.\n",
			ns.Namespace, len(unattached), strings.Join(unattached, ", "), ns.Namespace)
	}
	return nil
}

func fetchAttachedLanguageCodes(ctx context.Context, c *langsyncContext, namespace string) ([]string, error) {
	res, err := c.client.GetLanguagesByNamespaceWithResponse(ctx, namespace)
	if err != nil {
		return nil, err
	}
	if res.JSON200 == nil {
		if isNamespaceForbidden(res.JSON403) || isNamespaceForbidden(res.JSON404) {
			return nil, namespaceAccessError(c.cfg.ActiveOrg.Slug, namespace)
		}
		return nil, apiError(res.HTTPResponse, res.Body,
			res.JSON400, res.JSON401, res.JSON403, res.JSON404, res.JSON500)
	}
	out := make([]string, 0, len(*res.JSON200))
	for _, conn := range *res.JSON200 {
		if conn.LanguageCode != nil && *conn.LanguageCode != "" {
			out = append(out, *conn.LanguageCode)
		}
	}
	return out, nil
}

func fetchOrgLanguageList(ctx context.Context, c *langsyncContext) ([]langsync.DtoDTOLanguage, error) {
	res, err := c.client.ListLanguagesForOrgWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	if res.JSON200 == nil {
		return nil, apiError(res.HTTPResponse, res.Body, res.JSON401, res.JSON500)
	}
	return *res.JSON200, nil
}

func derefOr(s *string, fallback string) string {
	if s == nil {
		return fallback
	}
	return *s
}
