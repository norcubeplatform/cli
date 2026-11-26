package langsync

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/norcubeplatform/cli/internal/api/langsync"
	"github.com/norcubeplatform/cli/internal/cli/langsync/projectcfg"
	"github.com/norcubeplatform/cli/internal/clierr"
)

// specEnum reads an enum of the checked-in LangSync spec, the contract
// the generated client comes from.
func specEnum(t *testing.T, schema string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "specs", "langsync.openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Components struct {
			Schemas map[string]struct {
				Enum []string `json:"enum"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	values := spec.Components.Schemas[schema].Enum
	if len(values) == 0 {
		t.Fatalf("no enum %s in the spec", schema)
	}
	return values
}

func TestMessageSyntaxesAreTheAPIs(t *testing.T) {
	var got []string
	for _, v := range messageSyntaxes {
		got = append(got, string(v))
	}
	want := specEnum(t, "constants.NamespaceMessageSyntax")
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("--message-syntax offers %v, the API has %v", got, want)
	}
}

func TestParseMessageSyntax(t *testing.T) {
	for in, want := range map[string]langsync.ConstantsNamespaceMessageSyntax{
		"icu": langsync.NamespaceMessageSyntaxICU, " ICU ": langsync.NamespaceMessageSyntaxICU, "plain": langsync.NamespaceMessageSyntaxPlain,
	} {
		if got, err := parseMessageSyntax(in); err != nil || got != want {
			t.Errorf("%q: got %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "icu4j", "mf2"} {
		_, err := parseMessageSyntax(in)
		var ue *clierr.UsageError
		if !errors.As(err, &ue) || !strings.Contains(err.Error(), "plain|icu") {
			t.Errorf("%q: got %v, want a usage error naming the values", in, err)
		}
	}
}

func TestEveryTranslationProblemHasWords(t *testing.T) {
	for _, code := range specEnum(t, "constants.TermTranslationProblem") {
		if _, ok := translationProblemWords[langsync.ConstantsTermTranslationProblem(code)]; !ok {
			t.Errorf("no words for %s", code)
		}
	}
}

func TestFailureLine(t *testing.T) {
	str := func(s string) *string { return &s }
	num := func(n int) *int { return &n }
	list := func(s ...string) *[]string { return &s }
	code := func(c langsync.ConstantsTermTranslationProblem) *langsync.ConstantsTermTranslationProblem { return &c }
	tests := []struct {
		name string
		f    langsync.DtoDTOSyncJobFailure
		want string
	}{
		{"plural forms with their facts",
			langsync.DtoDTOSyncJobFailure{Phase: str("autotranslate"), Mark: str("cart.items"), LanguageCode: str("cs"),
				Code: code(langsync.TermTranslationProblemPluralForms), Argument: str("count"), Missing: list("few", "many"), Error: str("english")},
			"[web] autotranslate — cart.items [cs]: plural_forms: the plural forms don't fit the language (argument count; missing: few, many)"},
		{"placeholders missing and unexpected",
			langsync.DtoDTOSyncJobFailure{Phase: str("autotranslate"), Mark: str("hello"), LanguageCode: str("de"),
				Code: code(langsync.TermTranslationProblemPlaceholdersChanged), Missing: list("{name}"), Unexpected: list("{nme}")},
			"[web] autotranslate — hello [de]: placeholders_changed: the variables or placeholders changed (missing: {name}; unexpected: {nme})"},
		{"an ICU parse error at an offset, from a push",
			langsync.DtoDTOSyncJobFailure{Phase: str("push"), Mark: str("cart.total"), LanguageCode: str("cs"),
				Code: code(langsync.TermTranslationProblemICUInvalid), Offset: num(0)},
			"[web] push — cart.total [cs]: icu_invalid: not a valid ICU message (at offset 0)"},
		{"a code without facts",
			langsync.DtoDTOSyncJobFailure{Phase: str("autotranslate"), Mark: str("title"), LanguageCode: str("cs"),
				Code: code(langsync.TermTranslationProblemUntranslated)},
			"[web] autotranslate — title [cs]: untranslated: the model answered with the source text"},
		{"a code this CLI doesn't know keeps the server's words",
			langsync.DtoDTOSyncJobFailure{Phase: str("autotranslate"), Mark: str("title"), LanguageCode: str("cs"),
				Code: code("new_reason"), Error: str("something new")},
			"[web] autotranslate — title [cs]: new_reason: something new"},
		{"the entry for failures not listed",
			langsync.DtoDTOSyncJobFailure{Phase: str("autotranslate"), Dropped: num(12), Error: str("12 more failures")},
			"[web] autotranslate — and 12 more"},
		{"an entry without a code reads as before",
			langsync.DtoDTOSyncJobFailure{Phase: str("push"), Mark: str("x"), Error: str("op failed")},
			"[web] push — x: op failed"},
		{"an entry without a key",
			langsync.DtoDTOSyncJobFailure{Phase: str("autotranslate"), Error: str("budget reached")},
			"[web] autotranslate — budget reached"},
	}
	for _, tt := range tests {
		if got := failureLine("web", tt.f); got != tt.want {
			t.Errorf("%s:\n got %s\nwant %s", tt.name, got, tt.want)
		}
	}
}

func TestUnreviewedLine(t *testing.T) {
	if got := unreviewedLine("web", map[string]int{"de": 3, "cs": 12, "fr": 0}); got != "[web] 15 translations not reviewed: cs 12, de 3" {
		t.Fatalf("got %q", got)
	}
	if got := unreviewedLine("web", map[string]int{"cs": 1}); got != "[web] 1 translation not reviewed: cs 1" {
		t.Fatalf("got %q", got)
	}
	if got := unreviewedLine("web", map[string]int{"cs": 0}); got != "" {
		t.Fatalf("all reviewed should print nothing, got %q", got)
	}
	if got := unreviewedLine("web", nil); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestSourceContexts(t *testing.T) {
	kept, dropped := sourceContexts(
		map[string]string{"a": "first", "b": "", "gone": "old key"},
		map[string]string{"a": "A", "b": "B", "c": "C"},
	)
	if dropped != 1 || len(kept) != 2 || kept["a"] != "first" || kept["b"] != "" {
		t.Fatalf("kept %v dropped %d", kept, dropped)
	}
	if kept, dropped := sourceContexts(map[string]string{"a": "x"}, nil); len(kept) != 0 || dropped != 1 {
		t.Fatalf("no source file: kept %v dropped %d", kept, dropped)
	}
}

func TestSyncContextsErrorNamesTheFileAndKey(t *testing.T) {
	str := func(s string) *string { return &s }
	num := func(n int) *int { return &n }
	base := func() error {
		return &clierr.Error{Code: clierr.CodeValidation, Message: "contexts invalid", Type: "SYNC_CONTEXTS_INVALID", HTTPStatus: 400}
	}
	for _, tc := range []struct {
		p    langsync.DtoSyncContextsProblem
		want string
	}{
		{langsync.DtoSyncContextsProblem{Reason: langsync.SyncContextsProblemUnknownKey, Key: str("old.key")}, `key "old.key" is not one of the source language's keys`},
		{langsync.DtoSyncContextsProblem{Reason: langsync.SyncContextsProblemTooLong, Key: str("k"), MaxChars: num(1000)}, `the description of key "k" is too long (the most is 1000 characters)`},
		{langsync.DtoSyncContextsProblem{Reason: langsync.SyncContextsProblemTooMany, Max: num(3)}, "there are more descriptions than source keys (the most is 3)"},
		{langsync.DtoSyncContextsProblem{Reason: langsync.SyncContextsProblemNotText, Key: str("k")}, `the description of key "k" is not text`},
	} {
		err := syncContextsError(base(), "messages/context/en.json", &tc.p)
		var ce *clierr.Error
		if !errors.As(err, &ce) || ce.Code != clierr.CodeValidation ||
			ce.Message != "LangSync refused the key descriptions from messages/context/en.json: "+tc.want {
			t.Errorf("%s: got %v", tc.p.Reason, err)
		}
	}
}

func TestInitKeepsWhatAPersonWroteIntoAnEntry(t *testing.T) {
	name, langID := "web", 4
	existing := &projectcfg.File{Version: 1, Namespaces: []projectcfg.Namespace{{
		Namespace: "web", Dir: "locales", Format: projectcfg.FormatFlatJSON, DefaultLocalLanguage: "en",
		LanguageAliases: map[string]string{"cs_cz": "cs-CZ"}, ContextFile: "messages/context/en.json",
	}}}
	got, err := buildInitEntries([]langsync.DtoDTONamespace{{Name: &name, DefaultLanguageId: &langID}},
		map[int]string{4: "en"}, existing, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Dir != "locales" || got[0].ContextFile != "messages/context/en.json" || got[0].LanguageAliases["cs_cz"] != "cs-CZ" {
		t.Fatalf("got %+v", got)
	}
}
