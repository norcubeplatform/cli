package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/norcubeplatform/cli/internal/clierr"
)

// syncProject writes a LangSync project for the namespace "web" (files in
// locales/, en the source language) with the given context_file and
// files (paths relative to the project), and makes it the working
// directory.
func syncProject(t *testing.T, contextFile string, files map[string]string) {
	t.Helper()
	root := t.TempDir()
	ns := map[string]any{"namespace": "web", "dir": "locales", "format": "flat-json", "default_local_language": "en"}
	if contextFile != "" {
		ns["context_file"] = contextFile
	}
	cfg, err := json.Marshal(map[string]any{"version": 1, "namespaces": []any{ns}})
	if err != nil {
		t.Fatal(err)
	}
	files[".langsync.json"] = string(cfg)
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
}

// syncJobDone is a completed sync job with a refused translation, the
// entry for failures not listed, and translations nobody reviewed.
const syncJobDone = `{"id":"job-1","status":"completed","createdCount":0,"updatedCount":1,
	"resultPerLanguage":{"en":{"cart.items":"{count, plural, one {# item} other {# items}}","title":"Shop"},"cs":{"title":"Obchod"}},
	"unreviewedPerLanguage":{"cs":1,"de":0},
	"failures":[
		{"phase":"autotranslate","mark":"cart.items","languageCode":"cs","code":"plural_forms","argument":"count","missing":["few","many"],"error":"plural forms"},
		{"phase":"autotranslate","dropped":4,"error":"4 more"}
	]}`

// syncServer answers the sync endpoints and records the job request.
func syncServer(t *testing.T, create http.HandlerFunc) *map[string]any {
	t.Helper()
	var body map[string]any
	if create == nil {
		create = func(w http.ResponseWriter, r *http.Request) {
			body = jsonBody(t, r)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"id":"job-1"}`)
		}
	}
	goldenHandlers(t, map[string]http.HandlerFunc{
		"POST /namespaces/web/sync": create,
		"GET /namespaces/web/sync/job-1": func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, syncJobDone)
		},
	})
	return &body
}

func TestLangSyncSendsTheSourceKeysDescriptions(t *testing.T) {
	goldenEnv(t)
	body := syncServer(t, nil)
	syncProject(t, "messages/context/en.json", map[string]string{
		"locales/en.json":          `{"cart.items": "{count, plural, one {# item} other {# items}}", "title": "Shop"}`,
		"locales/cs.json":          `{"title": "Obchod"}`,
		"messages/context/en.json": `{"cart.items": "Items in the cart, under its total", "title": "", "removed.key": "left over"}`,
	})
	out, stderr, err := run(t, "langsync", "sync", "-o", "json")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}

	// Only the source file's keys; an empty description clears one.
	got, _ := json.Marshal((*body)["contexts"])
	if string(got) != `{"cart.items":"Items in the cart, under its total","title":""}` {
		t.Fatalf("contexts sent: %s", got)
	}
	if want := "[web] warning: 1 description in messages/context/en.json is for a key that en.json doesn't have; not sent"; strings.Count(stderr, want) != 1 {
		t.Fatalf("want one warning line %q in:\n%s", want, stderr)
	}

	// The summary for people names the key, language, code and facts,
	// the failures not listed, and what nobody reviewed.
	for _, want := range []string{
		"[web] autotranslate — cart.items [cs]: plural_forms: the plural forms don't fit the language (argument count; missing: few, many)",
		"[web] autotranslate — and 4 more",
		"▸ Not reviewed",
		"[web] 1 translation not reviewed: cs 1",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}

	// Scripts get the job's own fields.
	var summary []map[string]any
	if err := json.Unmarshal([]byte(out), &summary); err != nil || len(summary) != 1 {
		t.Fatalf("stdout %s: %v", out, err)
	}
	failures, _ := json.Marshal(summary[0]["failures"])
	unreviewed, _ := json.Marshal(summary[0]["unreviewedPerLanguage"])
	if !strings.Contains(string(failures), `"code":"plural_forms"`) || !strings.Contains(string(failures), `"dropped":4`) ||
		string(unreviewed) != `{"cs":1,"de":0}` {
		t.Fatalf("summary %s", out)
	}
}

func TestLangSyncWithoutAContextFileSendsNoContexts(t *testing.T) {
	goldenEnv(t)
	body := syncServer(t, nil)
	syncProject(t, "", map[string]string{"locales/en.json": `{"title": "Shop"}`})
	if _, stderr, err := run(t, "langsync", "sync", "-o", "json"); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if _, ok := (*body)["contexts"]; ok {
		t.Fatalf("contexts sent without a context_file: %v", *body)
	}
}

func TestLangSyncRefusesAContextFileItCannotUse(t *testing.T) {
	goldenEnv(t)
	var posts int32
	syncServer(t, func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&posts, 1)
		w.WriteHeader(http.StatusAccepted)
	})
	tooLong := strings.Repeat("x", 1001)
	for _, tc := range []struct {
		name, contextFile string
		files             map[string]string
		want              []string
	}{
		{"a missing file", "messages/context/en.json", map[string]string{}, []string{"messages/context/en.json", "does not exist"}},
		{"invalid JSON", "messages/context/en.json", map[string]string{"messages/context/en.json": `{"title": 1}`}, []string{"messages/context/en.json", "decode"}},
		{"a description that is too long", "messages/context/en.json", map[string]string{"messages/context/en.json": `{"title": "` + tooLong + `"}`}, []string{`key "title"`, "the most is 1000"}},
		{"a file the language scan reads", "locales/context.json", map[string]string{"locales/context.json": `{}`}, []string{filepath.Join("locales", "context.json"), "read as a language"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.files["locales/en.json"] = `{"title": "Shop"}`
			syncProject(t, tc.contextFile, tc.files)
			_, stderr, err := run(t, "langsync", "sync")
			if err == nil {
				t.Fatalf("no error\n%s", stderr)
			}
			for _, want := range tc.want {
				if !strings.Contains(filepath.ToSlash(err.Error()), filepath.ToSlash(want)) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
		})
	}
	if posts != 0 {
		t.Fatalf("%d sync jobs were submitted", posts)
	}
}

func TestLangSyncWordsARefusalOfTheDescriptions(t *testing.T) {
	goldenEnv(t)
	syncServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"type":"SYNC_CONTEXTS_INVALID","msg":"contexts invalid","data":{"reason":"unknown_key","key":"title"}}`)
	})
	syncProject(t, "messages/context/en.json", map[string]string{
		"locales/en.json":          `{"title": "Shop"}`,
		"messages/context/en.json": `{"title": "The shop's name"}`,
	})
	out, _, err := run(t, "langsync", "sync", "-o", "json")
	if err == nil {
		t.Fatal("want the sync to fail")
	}
	if !strings.Contains(out, `LangSync refused the key descriptions from messages/context/en.json: key \"title\" is not one of the source language's keys`) {
		t.Fatalf("summary %s", out)
	}
}

func TestLangSyncPullDoesNotReadTheContextFile(t *testing.T) {
	goldenEnv(t)
	body := syncServer(t, nil)
	syncProject(t, "messages/context/en.json", map[string]string{"locales/en.json": `{"title": "Shop"}`})
	if _, stderr, err := run(t, "langsync", "pull", "-o", "json"); err != nil {
		t.Fatalf("pull pushes nothing, so a missing context file doesn't matter: %v\n%s", err, stderr)
	}
	if _, ok := (*body)["contexts"]; ok || (*body)["strategy"] != "server" {
		t.Fatalf("body %v", *body)
	}
}

func TestLangSyncNamespaceMessageSyntax(t *testing.T) {
	goldenEnv(t)
	var body map[string]any
	record := func(w http.ResponseWriter, r *http.Request) {
		body = jsonBody(t, r)
		fmt.Fprint(w, `{}`)
	}
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /namespaces": record, "PATCH /namespaces/web": record})

	out, stderr, err := run(t, "langsync", "namespaces", "create", "app", "--default-language", "en", "--message-syntax", "icu", "-o", "json")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if body["messageSyntax"] != "icu" || !strings.Contains(out, `"messageSyntax": "icu"`) {
		t.Fatalf("create: body %v out %s", body, out)
	}
	if _, _, err := run(t, "langsync", "namespaces", "create", "app", "--default-language", "en"); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["messageSyntax"]; ok {
		t.Fatalf("without the flag the server decides: %v", body)
	}

	out, stderr, err = run(t, "langsync", "namespaces", "update", "web", "--message-syntax", "plain", "-o", "json")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if body["messageSyntax"] != "plain" || len(body) != 1 || !strings.Contains(out, `"messageSyntax": "plain"`) {
		t.Fatalf("update: body %v out %s", body, out)
	}

	body = nil
	for _, args := range [][]string{
		{"langsync", "namespaces", "create", "app", "--default-language", "en", "--message-syntax", "mf2"},
		{"langsync", "namespaces", "update", "web", "--message-syntax", ""},
	} {
		_, _, err := run(t, args...)
		if err == nil || Describe(err).ExitCode() != clierr.ExitUsage || !strings.Contains(err.Error(), "plain|icu") {
			t.Errorf("%v: want a usage error naming the values, got %v", args, err)
		}
	}
	if body != nil {
		t.Fatalf("a request was sent: %v", body)
	}
}
