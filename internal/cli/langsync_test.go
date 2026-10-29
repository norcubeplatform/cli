package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/norcubeplatform/cli/internal/clierr"
)

// LangSync answers of the fake backend (see golden_test.go) for the
// commands beyond namespaces, languages and terms.
func init() {
	for k, v := range map[string]string{ // #nosec G101 -- API fixtures, no credentials
		"GET /namespaces/web/languages": `[
			{"connection_id":40,"language_id":4,"language_code":"en","language_name":"English","is_default":true,"language_shared":true},
			{"connection_id":90,"language_id":9,"language_code":"cs","language_name":"Czech","is_default":false,"language_shared":true}
		]`,
		"GET /translations/custom-prompts": `[
			{"id":"p-1","name":"Friendly tone","prompt":"Use an informal, friendly tone.\nAddress the reader as you.","isPrivate":false,"organizationId":"org-1","createdBy":"user-1","createdAt":"2026-10-01T09:00:00Z","updatedAt":"2026-10-02T09:00:00Z"},
			{"id":"p-2","name":"Legal","prompt":"Keep legal terms exact.","isPrivate":true,"organizationId":"org-1","createdBy":"user-1","createdAt":"2026-10-03T09:00:00Z","updatedAt":"2026-10-03T09:00:00Z"}
		]`,
		"GET /glossary": `{"id":"g-1","organizationId":"org-1","defaultLanguageId":4}`,
		"GET /glossary/items": `{"list":[
			{"id":"gi-1","glossaryId":"g-1","defaultLanguageValue":"Norcube","context":"the company","type":"n/a","isCaseSensitive":true,"isTranslatable":false,"translatedCount":0,"enabledLanguagesCount":2,"translationPercentage":0,"createdAt":"2026-10-01T09:00:00Z"},
			{"id":"gi-2","glossaryId":"g-1","defaultLanguageValue":"invoice","context":"a bill sent to a customer","type":"noun","isCaseSensitive":false,"isTranslatable":true,"translatedCount":1,"enabledLanguagesCount":2,"translationPercentage":50,"createdAt":"2026-10-02T09:00:00Z"}
		],"cursors":{},"meta":{}}`,
		"GET /glossary/items/gi-2": `{"glossaryItem":{"id":"gi-2","glossaryId":"g-1","defaultLanguageValue":"invoice","context":"a bill sent to a customer","type":"noun","isCaseSensitive":false,"isTranslatable":true,"translatedCount":1,"enabledLanguagesCount":2,"translationPercentage":50,"createdAt":"2026-10-02T09:00:00Z"},
			"glossaryItemTranslations":[{"glossaryItemId":"gi-2","languageId":9,"languageCode":"cs","languageName":"Czech","value":"faktura","createdAt":"2026-10-02T10:00:00Z"}]}`,
		"GET /glossary/items/gi-1": `{"glossaryItem":{"id":"gi-1","glossaryId":"g-1","defaultLanguageValue":"Norcube","context":"the company","type":"n/a","isCaseSensitive":true,"isTranslatable":false,"translatedCount":0,"enabledLanguagesCount":2,"translationPercentage":0,"createdAt":"2026-10-01T09:00:00Z"}}`,
		"GET /apikeys": `{"list":[
			{"id":"k-1","name":"production-web","preview":"lsk_8f2a","hash":"x","organizationId":"org-1","createdBy":"user-1","createdAt":"2026-09-01T09:00:00Z","lastUsedAt":"2026-10-28T11:00:00Z"},
			{"id":"k-2","name":"old-preview","preview":"lsk_1b9c","hash":"y","organizationId":"org-1","createdBy":"user-1","createdAt":"2026-08-01T09:00:00Z","revokedAt":"2026-09-02T09:00:00Z","revokedBy":"user-1"}
		],"cursors":{},"meta":{}}`,
	} {
		goldenData[k] = v
	}
}

// auditHandler answers GET /audit-log with two pages and records the
// queries it got.
func auditHandler(queries *[]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*queries = append(*queries, r.URL.RawQuery)
		if r.URL.Query().Get("cursorId") == "" {
			fmt.Fprint(w, `{"list":[
				{"id":"a-1","action":"namespace.updated","actorId":"user-1","actorName":"Dev Eloper","actorEmail":"dev@example.com","actorType":"user","targetType":"namespace","targetId":"ns-1","createdAt":"2026-10-28T09:00:00Z","metadata":{"name":"web"}},
				{"id":"a-2","action":"sync_job.completed","actorType":"system","targetType":"sync_job","targetId":"job-9","createdAt":"2026-10-27T09:00:00Z"}
			],"nextCursorCreatedAt":"2026-10-27T09:00:00Z","nextCursorId":"a-2"}`)
			return
		}
		fmt.Fprint(w, `{"list":[{"id":"a-3","action":"apikey.created","actorId":"user-1","actorName":"Dev Eloper","targetType":"apikey","targetId":"k-1","createdAt":"2026-09-01T09:00:00Z"}]}`)
	}
}

func TestGoldenLangSync(t *testing.T) {
	commands := []struct {
		name string
		args []string
	}{
		{"langsync-prompts-list", []string{"langsync", "prompts", "list"}},
		{"langsync-prompts-get", []string{"langsync", "prompts", "get", "p-1"}},
		{"langsync-glossary-get", []string{"langsync", "glossary", "get"}},
		{"langsync-glossary-items-list", []string{"langsync", "glossary", "items", "list"}},
		{"langsync-glossary-items-get", []string{"langsync", "glossary", "items", "get", "gi-2"}},
		{"langsync-api-keys-list", []string{"langsync", "api-keys", "list"}},
		{"langsync-audit-list", []string{"langsync", "audit", "list", "--all-pages"}},
	}
	for _, c := range commands {
		for format, ext := range goldenFormats {
			t.Run(c.name+"/"+format, func(t *testing.T) {
				url := goldenEnv(t)
				var queries []string
				goldenHandlers(t, map[string]http.HandlerFunc{"GET /audit-log": auditHandler(&queries)})
				out, stderr, err := run(t, append(c.args, "-o", format, "--no-pager")...)
				if err != nil {
					t.Fatalf("%v\n%s", err, stderr)
				}
				checkGolden(t, c.name+"."+ext, out, url)
			})
		}
	}
}

// jsonBody decodes a request body into a map.
func jsonBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		t.Errorf("request body: %v", err)
	}
	return m
}

func TestLangSyncAuditFollowsTheTwoPartCursor(t *testing.T) {
	goldenEnv(t)
	var queries []string
	goldenHandlers(t, map[string]http.HandlerFunc{"GET /audit-log": auditHandler(&queries)})
	out, stderr, err := run(t, "langsync", "audit", "list", "--since", "2026-10-01T00:00:00Z", "--action", "namespace.updated,apikey.created", "--all-pages", "-o", "json", "--query", "[].id")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if strings.Join(strings.Fields(out), "") != `["a-1","a-2","a-3"]` {
		t.Fatalf("ids %s", out)
	}
	if len(queries) != 2 || !strings.Contains(queries[1], "cursorCreatedAt=2026-10-27T09%3A00%3A00Z") || !strings.Contains(queries[1], "cursorId=a-2") {
		t.Fatalf("queries %q", queries)
	}
	for _, want := range []string{"since=2026-10-01T00%3A00%3A00Z", "actions=namespace.updated", "actions=apikey.created"} {
		if !strings.Contains(queries[0], want) {
			t.Errorf("first query %q lacks %s", queries[0], want)
		}
	}

	// A page hint is one --cursor value that resumes the listing.
	_, stderr, err = run(t, "langsync", "audit", "list", "-o", "json")
	if err != nil || !strings.Contains(stderr, "2026-10-27T09:00:00Z_a-2") {
		t.Fatalf("hint %q, %v", stderr, err)
	}
	queries = nil
	if _, stderr, err := run(t, "langsync", "audit", "list", "--cursor", "2026-10-27T09:00:00Z_a-2", "-o", "json"); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if len(queries) != 1 || !strings.Contains(queries[0], "cursorId=a-2") {
		t.Fatalf("queries %q", queries)
	}
	if _, _, err := run(t, "langsync", "audit", "list", "--cursor", "garbage"); !clierr.IsUsage(err) {
		t.Fatalf("bad cursor: %v", err)
	}
	if _, _, err := run(t, "langsync", "audit", "list", "--since", "last tuesday"); !clierr.IsUsage(err) {
		t.Fatalf("bad --since: %v", err)
	}
}

func TestLangSyncTranslationSetUsesTheConnectionID(t *testing.T) {
	goldenEnv(t)
	var path string
	var body map[string]any
	goldenHandlers(t, map[string]http.HandlerFunc{"PATCH /namespaces/web/terms/7/translations/90": func(w http.ResponseWriter, r *http.Request) {
		path, body = r.URL.Path, jsonBody(t, r)
		fmt.Fprint(w, `{"term":{"id":7,"mark":"Save changes","context":"","created_at":"2026-03-04T05:06:07Z"},"translations":[]}`)
	}})
	out, stderr, err := run(t, "langsync", "translations", "set", "7", "CS", "Uložit vše", "-n", "web", "-o", "json")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if path == "" || body["value"] != "Uložit vše" || body["aiGenerated"] != nil {
		t.Fatalf("path %q body %v", path, body)
	}
	var got map[string]any
	_ = json.Unmarshal([]byte(out), &got)
	if got["language"] != "cs" || got["aiGenerated"] != false || got["termId"] != float64(7) {
		t.Fatalf("output %s", out)
	}

	// --ai keeps the label; the value can come from stdin.
	if _, stderr, err := runIn(t, "Uložit\n", "langsync", "translations", "set", "7", "9", "--file", "-", "--ai", "-n", "web"); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if body["value"] != "Uložit" || body["aiGenerated"] != true {
		t.Fatalf("body %v", body)
	}
}

func TestLangSyncTranslationUsageErrorsBeforeAnyRequest(t *testing.T) {
	goldenEnv(t)
	var calls int32
	goldenHandlers(t, map[string]http.HandlerFunc{
		"PATCH /namespaces/web/terms/7/translations/90": func(http.ResponseWriter, *http.Request) { atomic.AddInt32(&calls, 1) },
	})
	for _, args := range [][]string{
		{"langsync", "translations", "set", "x", "cs", "v", "-n", "web"},
		{"langsync", "translations", "set", "7", "cs", "v", "--file", "f.txt", "-n", "web"},
		{"langsync", "translations", "set", "7", "cs", "-n", "web"},
		{"langsync", "translations", "set", "7", "de", "v", "-n", "web"},
		{"langsync", "translations", "suggest", "7", "cs", "-n", "web", "--prompt", "none"},
		{"langsync", "terms", "update", "7", "-n", "web"},
		{"langsync", "terms", "update", "7", "-n", "web", "--mark", " "},
		{"langsync", "namespaces", "update", "web"},
		{"langsync", "prompts", "create", "x"},
		{"langsync", "prompts", "create", "x", "--text", "a", "--file", "b"},
		{"langsync", "prompts", "update", "p-1"},
		{"langsync", "api-keys", "create"},
		{"langsync", "glossary", "items", "create", "x", "--type", "thing"},
		{"langsync", "glossary", "items", "translate", "gi-1", "cs", "x"},
	} {
		_, _, err := run(t, args...)
		if err == nil || (!clierr.IsUsage(err) && Describe(err).ExitCode() != clierr.ExitUsage) {
			t.Errorf("%v: want a usage error, got %v", args, err)
		}
	}
	if calls != 0 {
		t.Fatalf("%d requests were sent", calls)
	}
}

func TestLangSyncSuggestAndSave(t *testing.T) {
	goldenEnv(t)
	var recommend, saved map[string]any
	goldenHandlers(t, map[string]http.HandlerFunc{
		"POST /namespaces/web/terms/7/translations/9/recommend": func(w http.ResponseWriter, r *http.Request) {
			recommend = jsonBody(t, r)
			fmt.Fprint(w, `{"translation":"Uložit změny"}`)
		},
		"PATCH /namespaces/web/terms/7/translations/90": func(w http.ResponseWriter, r *http.Request) {
			saved = jsonBody(t, r)
			fmt.Fprint(w, `{"term":{"id":7,"mark":"Save changes","context":"","created_at":""},"translations":[]}`)
		},
	})
	out, stderr, err := run(t, "langsync", "translations", "suggest", "7", "cs", "-n", "web", "--from", "en", "--prompt", "p-1", "-o", "text")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if out != "Uložit změny\n" || recommend["customPromptId"] != "p-1" || recommend["sourceLangId"] != float64(4) {
		t.Fatalf("out %q body %v", out, recommend)
	}
	if saved != nil {
		t.Fatal("saved without --save")
	}
	if _, stderr, err := run(t, "langsync", "translations", "suggest", "7", "cs", "-n", "web", "--save"); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if saved["value"] != "Uložit změny" || saved["aiGenerated"] != true {
		t.Fatalf("saved %v", saved)
	}
}

func TestLangSyncTranslateMissing(t *testing.T) {
	goldenEnv(t)
	var body map[string]any
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /namespaces/web/terms/translate-missing": func(w http.ResponseWriter, r *http.Request) {
		body = jsonBody(t, r)
		fmt.Fprint(w, `{}`)
	}})
	out, stderr, err := run(t, "langsync", "translations", "translate-missing", "-n", "web", "--languages", "cs", "--prompt", "p-2", "-o", "json")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if fmt.Sprint(body["languageIds"]) != "[9]" || body["customPromptId"] != "p-2" || !strings.Contains(out, `"started": true`) {
		t.Fatalf("body %v out %s", body, out)
	}
	if _, _, err := run(t, "langsync", "translations", "translate-missing", "-n", "web"); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["languageIds"]; ok {
		t.Fatalf("all languages should send no filter, got %v", body)
	}
}

func TestLangSyncNamespaceDefaultPrompt(t *testing.T) {
	goldenEnv(t)
	var body map[string]any
	goldenHandlers(t, map[string]http.HandlerFunc{"PATCH /namespaces/web": func(w http.ResponseWriter, r *http.Request) {
		body = jsonBody(t, r)
		fmt.Fprint(w, `{}`)
	}})
	if _, stderr, err := run(t, "langsync", "namespaces", "update", "web", "--default-prompt", "p-1"); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if body["defaultCustomPromptId"] != "p-1" || len(body) != 1 {
		t.Fatalf("body %v", body)
	}
	out, _, err := run(t, "langsync", "namespaces", "update", "web", "--default-prompt", "none", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := body["defaultCustomPromptId"]; !ok || v != "" || !strings.Contains(out, `"defaultPromptId": ""`) {
		t.Fatalf("none should send an empty id: body %v out %s", body, out)
	}
}

func TestLangSyncTermsUpdate(t *testing.T) {
	goldenEnv(t)
	var body map[string]any
	goldenHandlers(t, map[string]http.HandlerFunc{"PATCH /namespaces/web/terms/7": func(w http.ResponseWriter, r *http.Request) {
		body = jsonBody(t, r)
		fmt.Fprint(w, `{}`)
	}})
	if _, stderr, err := run(t, "langsync", "terms", "update", "7", "-n", "web", "--context", ""); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if v, ok := body["context"]; !ok || v != "" || body["mark"] != nil {
		t.Fatalf("body %v", body)
	}
}

func TestLangSyncPrompts(t *testing.T) {
	goldenEnv(t)
	var created, patched map[string]any
	var deleted int32
	goldenHandlers(t, map[string]http.HandlerFunc{
		"POST /translations/custom-prompts": func(w http.ResponseWriter, r *http.Request) {
			created = jsonBody(t, r)
			fmt.Fprint(w, `{"id":"p-9"}`)
		},
		"PATCH /translations/custom-prompts/p-1": func(w http.ResponseWriter, r *http.Request) {
			patched = jsonBody(t, r)
			fmt.Fprint(w, `{}`)
		},
		"DELETE /translations/custom-prompts/p-1": func(w http.ResponseWriter, _ *http.Request) {
			atomic.AddInt32(&deleted, 1)
			fmt.Fprint(w, `{}`)
		},
	})
	out, stderr, err := runIn(t, "Be brief.\n", "langsync", "prompts", "create", "Brief", "--file", "-", "--private", "-o", "text")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if out != "p-9\n" || created["name"] != "Brief" || created["prompt"] != "Be brief." || created["isPrivate"] != true {
		t.Fatalf("out %q body %v", out, created)
	}
	if _, _, err := run(t, "langsync", "prompts", "update", "p-1", "--private=false"); err != nil {
		t.Fatal(err)
	}
	if patched["isPrivate"] != false || len(patched) != 1 {
		t.Fatalf("patch %v", patched)
	}
	if _, _, err := run(t, "langsync", "prompts", "delete", "p-1"); !clierr.IsUsage(err) || deleted != 0 {
		t.Fatalf("delete without --yes: %v (%d requests)", err, deleted)
	}
	if _, _, err := run(t, "langsync", "prompts", "delete", "p-1", "--yes"); err != nil || deleted != 1 {
		t.Fatalf("delete: %v (%d requests)", err, deleted)
	}
	_, _, err = run(t, "langsync", "prompts", "get", "p-404")
	if e := Describe(err); e.ExitCode() != clierr.ExitNotFound {
		t.Fatalf("unknown prompt: %v (exit %d)", err, e.ExitCode())
	}
}

func TestLangSyncGlossaryTranslateAddsOrUpdates(t *testing.T) {
	goldenEnv(t)
	var added, updated map[string]any
	goldenHandlers(t, map[string]http.HandlerFunc{
		"POST /glossary/items/gi-2/translations": func(w http.ResponseWriter, r *http.Request) {
			added = jsonBody(t, r)
			fmt.Fprint(w, `{}`)
		},
		"PATCH /glossary/items/gi-2/translations/9": func(w http.ResponseWriter, r *http.Request) {
			updated = jsonBody(t, r)
			fmt.Fprint(w, `{}`)
		},
	})
	if _, stderr, err := run(t, "langsync", "glossary", "items", "translate", "gi-2", "cs", "účet"); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if updated["value"] != "účet" || added != nil {
		t.Fatalf("existing cs translation: updated %v added %v", updated, added)
	}
	updated = nil
	if _, stderr, err := run(t, "langsync", "glossary", "items", "translate", "gi-2", "en", "bill"); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if added["value"] != "bill" || added["languageId"] != float64(4) || updated != nil {
		t.Fatalf("new en translation: added %v updated %v", added, updated)
	}
}

func TestLangSyncGlossaryCreate(t *testing.T) {
	goldenEnv(t)
	var body map[string]any
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /glossary/items": func(w http.ResponseWriter, r *http.Request) {
		body = jsonBody(t, r)
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"itemId":"gi-9"}`)
	}})
	out, stderr, err := run(t, "langsync", "glossary", "items", "create", "Norcube", "--keep", "--case-sensitive", "-o", "text")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if out != "gi-9\n" || body["type"] != "n/a" || body["isTranslatable"] != false || body["isCaseSensitive"] != true {
		t.Fatalf("out %q body %v", out, body)
	}
	if _, ok := body["context"]; ok {
		t.Fatalf("empty context should be left out: %v", body)
	}
}

func TestLangSyncAPIKeys(t *testing.T) {
	goldenEnv(t)
	var revoked int32
	goldenHandlers(t, map[string]http.HandlerFunc{
		"POST /apikeys": func(w http.ResponseWriter, r *http.Request) {
			if jsonBody(t, r)["name"] != "production-web" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, `{"id":"k-9","value":"lsk_secret_value"}`)
		},
		"DELETE /apikeys/k-1": func(w http.ResponseWriter, _ *http.Request) {
			atomic.AddInt32(&revoked, 1)
			w.WriteHeader(http.StatusNoContent)
		},
	})
	out, stderr, err := run(t, "langsync", "api-keys", "create", "--name", "production-web", "-o", "text")
	if err != nil || out != "lsk_secret_value\n" {
		t.Fatalf("out %q: %v\n%s", out, err, stderr)
	}
	out, _, err = run(t, "langsync", "api-keys", "create", "--name", "production-web")
	if err != nil || !strings.Contains(out, "lsk_secret_value") || !strings.Contains(out, "only time") {
		t.Fatalf("table output %q: %v", out, err)
	}
	if _, _, err := run(t, "langsync", "api-keys", "revoke", "k-1", "--yes"); err != nil || revoked != 1 {
		t.Fatalf("revoke: %v (%d)", err, revoked)
	}
}

func TestLangSyncAccessErrors(t *testing.T) {
	goldenEnv(t)
	goldenHandlers(t, map[string]http.HandlerFunc{
		"POST /apikeys": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"type":"INSUFFICIENT_SCOPE","msg":"This token is missing the scope this request needs."}`)
		},
		"PATCH /namespaces/web/terms/7": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"type":"NOT_FOUND","msg":"term not found"}`)
		},
	})
	_, _, err := run(t, "langsync", "api-keys", "create", "--name", "x")
	if e := Describe(err); e.ExitCode() != clierr.ExitAccess {
		t.Fatalf("scope: %v (exit %d)", err, e.ExitCode())
	}
	_, _, err = run(t, "langsync", "terms", "update", "7", "-n", "web", "--mark", "x")
	if e := Describe(err); e.ExitCode() != clierr.ExitNotFound {
		t.Fatalf("missing term: %v (exit %d)", err, e.ExitCode())
	}
}

// importServer records the import calls: the uploaded file and the
// finish request.
type importServer struct {
	upload string
	name   string
	finish map[string]any
}

func (s *importServer) handlers(t *testing.T) map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"POST /namespaces/web/terms/imports": func(w http.ResponseWriter, r *http.Request) {
			_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil {
				t.Errorf("content type: %v", err)
				return
			}
			part, err := multipart.NewReader(r.Body, params["boundary"]).NextPart()
			if err != nil || part.FormName() != "file" {
				t.Errorf("part %v: %v", part, err)
				return
			}
			b, _ := io.ReadAll(part)
			s.upload, s.name = string(b), part.FileName()
			header := strings.Split(strings.SplitN(s.upload, "\n", 2)[0], ",")
			cols, _ := json.Marshal(header)
			fmt.Fprintf(w, `{"importId":"imp-1","format":"csv","totalRows":2,"details":{"csv":{"columnNames":%s}}}`, cols)
		},
		"POST /namespaces/web/terms/imports/imp-1/finish": func(w http.ResponseWriter, r *http.Request) {
			s.finish = jsonBody(t, r)
			fmt.Fprint(w, `{"status":"processing"}`)
		},
	}
}

func writeCSV(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLangSyncImportMapsColumns(t *testing.T) {
	goldenEnv(t)
	srv := &importServer{}
	goldenHandlers(t, srv.handlers(t))
	path := writeCSV(t, "strings.csv", "Key,Czech,en,Notes\nsave,Uložit,Save,button\ncancel,Zrušit,Cancel,\n")
	out, stderr, err := run(t, "langsync", "terms", "import", path, "-n", "web", "--column", "notes=context", "--overwrite", "--auto-translate", "-o", "json")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if srv.name != "strings.csv" || !strings.HasPrefix(srv.upload, "Key,Czech") {
		t.Fatalf("upload %q as %q", srv.upload, srv.name)
	}
	setup, _ := srv.finish["setup"].(map[string]any)
	got, _ := json.Marshal(setup["csv"])
	want := `{"columnSetup":[{"type":"mark"},{"translation":{"languageId":9},"type":"translation"},{"translation":{"languageId":4},"type":"translation"},{"type":"context"}]}`
	if string(got) != want || setup["overwriteExisting"] != true || setup["translateAutomatically"] != true {
		t.Fatalf("setup %s, %v", got, setup)
	}
	var res map[string]any
	_ = json.Unmarshal([]byte(out), &res)
	if res["importId"] != "imp-1" || res["status"] != "processing" || res["rows"] != float64(2) {
		t.Fatalf("output %s", out)
	}
}

func TestLangSyncImportRefusesToGuess(t *testing.T) {
	goldenEnv(t)
	srv := &importServer{}
	goldenHandlers(t, srv.handlers(t))
	for name, c := range map[string]struct {
		file string
		args []string
		want string
	}{
		"unknown header":   {"key,cs,Owner\na,b,c\n", nil, `"Owner"`},
		"no mark":          {"cs,en\na,b\n", nil, "no column holds the mark"},
		"two marks":        {"key,term\na,b\n", nil, "two columns hold the mark"},
		"language twice":   {"key,cs,Czech\na,b,c\n", nil, "two columns hold the cs translations"},
		"missing header":   {"key,cs\na,b\n", []string{"--column", "Owner=ignore"}, "doesn't have: owner"},
		"bad mapping":      {"key,Owner\na,b\n", []string{"--column", "Owner=de"}, "not mark, context, ignore"},
		"unparsable":       {"key,cs\n\"a,b\n", nil, "strings.csv"},
		"empty":            {"", nil, "needs a header row"},
		"malformed column": {"key\na\n", []string{"--column", "key"}, "want"},
	} {
		t.Run(name, func(t *testing.T) {
			path := writeCSV(t, "strings.csv", c.file)
			_, _, err := run(t, append([]string{"langsync", "terms", "import", path, "-n", "web"}, c.args...)...)
			if !clierr.IsUsage(err) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want a usage error with %q", err, c.want)
			}
		})
	}
	if _, _, err := run(t, "langsync", "terms", "import", writeCSV(t, "strings.txt", "key\na\n"), "-n", "web"); !clierr.IsUsage(err) {
		t.Fatalf("non-csv: %v", err)
	}
	if srv.upload != "" {
		t.Fatal("a file was uploaded")
	}
}

func TestLangSyncImportDryRunUploadsNothing(t *testing.T) {
	goldenEnv(t)
	srv := &importServer{}
	goldenHandlers(t, srv.handlers(t))
	path := writeCSV(t, "strings.csv", "\ufeffmark,context,cs\na,b,c\n")
	out, stderr, err := run(t, "langsync", "terms", "import", path, "-n", "web", "--dry-run")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if srv.upload != "" || !strings.Contains(out, "Would import 1 row from strings.csv") || !strings.Contains(out, "cs translation") {
		t.Fatalf("upload %q out %q", srv.upload, out)
	}
}
