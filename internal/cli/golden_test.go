package cli

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/config"
)

// Golden tests pin what every read command prints in each format, against
// a fake Norcube (Auth, Backup and LangSync on one server). Regenerate the
// files after an intended change with:
//
//	go test ./internal/cli -run TestGolden -update
var update = flag.Bool("update", false, "rewrite the golden files")

// goldenData is what the fake services answer, keyed by "METHOD path".
var goldenData = map[string]string{ // #nosec G101 -- API fixtures, no credentials
	"GET /auth/me": `{"id":"user-1","name":"Dev Eloper","email":"dev@example.com","avatarUrl":""}`,
	"GET /auth/me/sessions": `[
		{"id":"sess-1","kind":"cli","hostname":"dev-laptop","client":{"name":"norcube","version":"0.17.0","os":"darwin","arch":"arm64"},"ip_address":"203.0.113.24","created_at":"2026-10-16T09:00:00Z","last_used_at":"2026-10-28T11:42:00Z","expires_at":"2026-11-27T11:42:00Z","current":true},
		{"id":"sess-2","kind":"cli","hostname":"ci-runner-04","client":{"name":"norcube","version":"0.16.0","os":"linux","arch":"amd64"},"ip_address":"198.51.100.7","created_at":"2026-09-28T08:00:00Z","last_used_at":"2026-10-27T03:10:00Z","expires_at":"2026-11-26T03:10:00Z","current":false},
		{"id":"sess-3","kind":"cli","hostname":null,"client":{"name":null,"version":null,"os":null,"arch":null},"ip_address":null,"created_at":"2026-08-29T10:00:00Z","last_used_at":"2026-10-19T10:00:00Z","expires_at":"2026-11-18T10:00:00Z","current":false}
	]`,
	"GET /iam/ci-tokens": `[
		{"id":"0b7a4c8e-2d3f-4a1b-9c6d-5e4f3a2b1c0d","name":"GitHub Actions deploy","preview":"x7Kq","scopes":["langsync.sync"],"created_by":"user-1","created_at":"2026-10-01T09:00:00Z","expires_at":"2026-12-30T09:00:00Z","last_used_at":"2026-10-28T11:42:00Z","last_used_ip":"203.0.113.24","revoked_at":null,"revoked_by":null,"status":"active"},
		{"id":"5c1d2e3f-4a5b-4c6d-8e7f-9a0b1c2d3e4f","name":"nightly backup check","preview":"Pq2w","scopes":["backup.read"],"created_by":"user-1","created_at":"2026-08-01T09:00:00Z","expires_at":"2026-09-01T09:00:00Z","last_used_at":null,"last_used_ip":null,"revoked_at":null,"revoked_by":null,"status":"expired"}
	]`,
	"GET /iam/ci-tokens/scopes": `[
		{"scope":"langsync.sync","label":"Sync translation files","description":"Run nrc langsync sync.","product":"langsync","write":true,"available":true},
		{"scope":"backup.read","label":"Read backups","description":"List data sources and backup jobs.","product":"backup","write":false,"available":true},
		{"scope":"cms.write","label":"Edit content","description":"Edit and publish CMS content.","product":"cms","write":true,"available":false}
	]`,
	"DELETE /auth/me/sessions/sess-2":      ``,
	"POST /auth/me/sessions/revoke-others": `{"revoked":2}`,
	"GET /backups": `{"list":[
		{"datasourceEngine":"postgres","datasourceId":"ds-1","datasourceName":"prod-db","destinationId":"dst-1","destinationIsManaged":true,"destinationRegion":"eu-central-1","jobBytesWritten":1048576,"jobCreatedAt":"2026-10-01T10:00:00Z","jobDurationMs":42000,"jobFinishedAt":"2026-10-01T10:00:42Z","jobId":"job-1","jobScheduledFor":"2026-10-01T10:00:00Z","jobStartedAt":"2026-10-01T10:00:00Z","jobStatus":"success","jobTrigger":"schedule","policyCompression":"gzip","policyEncryption":"sse-kms","policyId":"pol-1","policyMethod":"logical","policyName":"Nightly","policyRetentionDays":30,"verifyPassed":true,"verifyStatus":"success"},
		{"datasourceEngine":"postgres","datasourceId":"ds-1","datasourceName":"prod-db","destinationId":"dst-1","destinationIsManaged":true,"destinationRegion":"eu-central-1","jobBytesWritten":0,"jobCreatedAt":"2026-09-30T10:00:00Z","jobErrorSummary":"connection refused","jobId":"job-2","jobScheduledFor":"2026-09-30T10:00:00Z","jobStatus":"failed","jobTrigger":"manual","policyCompression":"gzip","policyEncryption":"sse-kms","policyId":"pol-1","policyMethod":"logical","policyName":"Nightly","policyRetentionDays":30}
	],"cursors":{"next":"cur-2"},"meta":{}}`,
	"GET /datasources": `{"list":[
		{"id":"ds-1","name":"prod-db","engine":"postgres","environment":"production","isActive":true,"organizationId":"org-1","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z","restoreCheckTotal":4,"restoreCheckPassed":3,"restoreCheckWarnings":1,"restoreCheckLastAt":"2026-10-01T11:00:00Z"},
		{"id":"ds-2","name":"analytics","engine":"mongodb","environment":"staging","isActive":false,"organizationId":"org-1","createdAt":"2026-02-01T00:00:00Z","updatedAt":"2026-02-01T00:00:00Z"}
	],"cursors":{},"meta":{}}`,
	"GET /datasources/ds-1": `{"id":"ds-1","name":"prod-db","engine":"postgres","environment":"production","isActive":true,"organizationId":"org-1","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}`,
	"GET /datasources/ds-1/policies": `[
		{"id":"att-1","isEnabled":true,"priority":1,"jitterSeconds":60,"createdAt":"2026-01-01T00:00:00Z","nextRunAt":"2026-10-02T02:00:00Z","backupPolicy":{"id":"pol-1","name":"Nightly","cronExpression":"0 2 * * *","retentionDays":30}}
	]`,
	"GET /namespaces": `[
		{"id":"ns-1","name":"web","context":"Marketing site","defaultLanguageId":4,"languages":["en","cs"],"organization_id":"org-1","progress":50,"total_terms":12,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z","created_by":"user-1"}
	]`,
	"GET /languages": `[
		{"id":4,"code":"en","name":"English","isCustom":false},
		{"id":9,"code":"cs","name":"Czech","isCustom":false},
		{"id":101,"code":"internal-en","name":"Internal English","isCustom":true,"organizationId":"org-1"}
	]`,
	"GET /namespaces/web/terms": `{"list":[
		{"term":{"id":7,"mark":"Save changes","context":"button","created_at":"2026-03-04T05:06:07Z"},"translations":[{"lang":{"code":"en","id":4},"language_to_namespace_id":40,"value":"Save changes","aiGenerated":false},{"lang":{"code":"cs","id":9},"language_to_namespace_id":90,"value":"Uložit změny","aiGenerated":true}]}
	],"cursors":{},"meta":{}}`,
}

// goldenErrors are API failures, keyed like goldenData.
var goldenErrors = map[string]struct {
	status int
	body   string
}{
	"GET /datasources/ds-404":           {http.StatusNotFound, `{"type":"NOT_FOUND","msg":"data source not found"}`},
	"DELETE /auth/me/sessions/sess-404": {http.StatusNotFound, `{"type":"NOT_FOUND","msg":"Session not found."}`},
}

type goldenBackend struct{ auth *fakeBackend }

func (g *goldenBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/oauth/token" || r.URL.Path == "/organizations" {
		g.auth.ServeHTTP(w, r)
		return
	}
	key := r.Method + " " + r.URL.Path
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-Id", "req-golden")
	goldenDynamicMu.Lock()
	h, ok := goldenDynamic[key]
	goldenDynamicMu.Unlock()
	if ok {
		h(w, r)
		return
	}
	if e, ok := goldenErrors[key]; ok {
		w.WriteHeader(e.status)
		fmt.Fprint(w, e.body)
		return
	}
	body, ok := goldenData[key]
	if !ok {
		http.NotFound(w, r)
		return
	}
	fmt.Fprint(w, body)
}

// goldenDynamic are per-test handlers for requests that need to look at
// the request or answer differently (registered with goldenHandlers).
var (
	goldenDynamicMu sync.Mutex
	goldenDynamic   = map[string]http.HandlerFunc{}
)

// goldenHandlers registers handlers (keyed "METHOD /path") for the test.
func goldenHandlers(t *testing.T, hs map[string]http.HandlerFunc) {
	t.Helper()
	goldenDynamicMu.Lock()
	defer goldenDynamicMu.Unlock()
	for k, h := range hs {
		goldenDynamic[k] = h
	}
	t.Cleanup(func() {
		goldenDynamicMu.Lock()
		defer goldenDynamicMu.Unlock()
		for k := range hs {
			delete(goldenDynamic, k)
		}
	})
}

// goldenEnv starts the fake services and a signed-in profile using them.
func goldenEnv(t *testing.T) string {
	t.Helper()
	g := &goldenBackend{auth: &fakeBackend{refresh: "refresh-0"}}
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	isolatedConfig(t, func(f *config.File) {
		p := f.Profile(config.DefaultProfileName)
		p.Environment = config.EnvCustom
		p.Endpoints = map[string]string{
			config.ServiceAuth: srv.URL, config.ServiceSnapDB: srv.URL, config.ServiceLangsync: srv.URL,
			config.ServiceDomainradar: srv.URL, config.ServiceBilling: srv.URL,
		}
		p.User = config.User{ID: "user-1", Name: "Dev Eloper", Email: "dev@example.com"}
		p.SetActiveOrg(config.Organization{ID: homeOrgID, Slug: "home", Name: "Home"})
	})
	t.Setenv("NORCUBE_LANGSYNC_URL", "")
	t.Setenv("NORCUBE_DOMAINRADAR_URL", "")
	t.Setenv("NORCUBE_BILLING_URL", "")
	if err := auth.SaveRefreshToken(config.DefaultProfileName, srv.URL, "refresh-0"); err != nil {
		t.Fatal(err)
	}
	// Each test starts with no cached access tokens or slugs.
	t.Cleanup(func() { _ = auth.DeleteCredentials(config.DefaultProfileName, srv.URL) })
	return srv.URL
}

// checkGolden compares got with testdata/golden/name, replacing the
// server's random URL so the files are stable.
func checkGolden(t *testing.T, name, got, serverURL string) {
	t.Helper()
	got = strings.ReplaceAll(got, serverURL, "http://backend")
	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from the golden file:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

var goldenFormats = map[string]string{"table": "txt", "json": "json", "yaml": "yaml", "text": "tsv"}

func TestGoldenOutput(t *testing.T) {
	commands := []struct {
		name string
		args []string
	}{
		{"whoami", []string{"whoami"}},
		{"org-list", []string{"org", "list"}},
		{"org-current", []string{"org", "current"}},
		{"backup-list", []string{"backup", "list"}},
		{"backup-datasource-list", []string{"backup", "datasource", "list"}},
		{"backup-datasource-get", []string{"backup", "datasource", "get", "ds-1"}},
		{"backup-policy-list", []string{"backup", "policy", "list", "--datasource", "ds-1"}},
		{"backup-health", []string{"backup", "health"}},
		{"langsync-namespace-list", []string{"langsync", "namespace", "list"}},
		{"langsync-lang-list", []string{"langsync", "lang", "list"}},
		{"langsync-mark-list", []string{"langsync", "mark", "list", "--namespace", "web"}},
		{"configure-get", []string{"configure", "get", "environment"}},
		{"auth-sessions-list", []string{"auth", "sessions", "list"}},
		{"auth-ci-token-list", []string{"auth", "ci-token", "list"}},
		{"auth-ci-token-scopes", []string{"auth", "ci-token", "scopes"}},
	}
	for _, c := range commands {
		for format, ext := range goldenFormats {
			t.Run(c.name+"/"+format, func(t *testing.T) {
				url := goldenEnv(t)
				out, stderr, err := run(t, append(c.args, "-o", format, "--no-pager")...)
				if err != nil {
					t.Fatalf("%v\n%s", err, stderr)
				}
				checkGolden(t, c.name+"."+ext, out, url)
			})
		}
	}
}

func TestGoldenQuery(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		query string
	}{
		{"query-failed-job-ids", []string{"backup", "list"}, "[?jobStatus=='failed'].jobId"},
		{"query-projection", []string{"backup", "datasource", "list"}, "[].{name: name, active: isActive}"},
		{"query-scalar", []string{"whoami"}, "user.email"},
		{"query-count", []string{"langsync", "lang", "list"}, "length([?isCustom])"},
	}
	for _, c := range cases {
		for format, ext := range goldenFormats {
			t.Run(c.name+"/"+format, func(t *testing.T) {
				url := goldenEnv(t)
				out, stderr, err := run(t, append(c.args, "-o", format, "--no-pager", "--query", c.query)...)
				if err != nil {
					t.Fatalf("%v\n%s", err, stderr)
				}
				checkGolden(t, c.name+"."+ext, out, url)
			})
		}
	}
}

func TestInvalidQueryIsUsageErrorBeforeAnyRequest(t *testing.T) {
	goldenEnv(t)
	_, _, err := run(t, "backup", "list", "--query", "[?jobStatus==")
	if !clierr.IsUsage(err) {
		t.Fatalf("got %v, want a usage error", err)
	}
	if e := Describe(err); e.Code != clierr.CodeUsage || e.ExitCode() != clierr.ExitUsage {
		t.Errorf("code %q exit %d", e.Code, e.ExitCode())
	}
}

// The next cursor of a list goes to stderr; stdout stays a plain array.
func TestNextCursorOnStderr(t *testing.T) {
	goldenEnv(t)
	out, stderr, err := run(t, "backup", "list", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "[") {
		t.Errorf("stdout is not an array:\n%s", out)
	}
	if strings.TrimSpace(stderr) != "nextCursor=cur-2" {
		t.Errorf("stderr: %q", stderr)
	}
}

func TestGoldenErrors(t *testing.T) {
	url := goldenEnv(t)
	_, _, err := run(t, "backup", "datasource", "get", "ds-404", "-o", "json")
	if err == nil {
		t.Fatal("want an error")
	}
	var buf bytes.Buffer
	if exit := PrintError(&buf, err, true); exit != clierr.ExitNotFound {
		t.Errorf("exit %d, want %d", exit, clierr.ExitNotFound)
	}
	checkGolden(t, "error-not-found.json", buf.String(), url)

	buf.Reset()
	PrintError(&buf, err, false)
	checkGolden(t, "error-not-found.txt", buf.String(), url)

	_, _, err = run(t, "backup", "list", "--org", "nope")
	var ce *clierr.Error
	buf.Reset()
	PrintError(&buf, err, true)
	if errors.As(err, &ce) || !strings.Contains(buf.String(), `"code": "org_not_found"`) {
		t.Errorf("unknown org: %s", buf.String())
	}
	checkGolden(t, "error-org-not-found.json", buf.String(), url)
}

// Revoking sessions: one by id, every other one, and an unknown id (exit 3).
func TestAuthSessionsRevoke(t *testing.T) {
	goldenEnv(t)
	out, stderr, err := run(t, "auth", "sessions", "revoke", "sess-2", "-o", "json")
	if err != nil {
		t.Fatalf("revoke: %v\n%s", err, stderr)
	}
	if !strings.Contains(out, `"id": "sess-2"`) || !strings.Contains(out, `"revoked": 1`) {
		t.Errorf("revoke result: %s", out)
	}
	out, _, err = run(t, "auth", "sessions", "revoke", "--others")
	if err != nil || !strings.Contains(out, "Revoked 2 other session(s)") {
		t.Errorf("revoke --others: %v %q", err, out)
	}
	_, _, err = run(t, "auth", "sessions", "revoke", "sess-404")
	if e := Describe(err); e.ExitCode() != clierr.ExitNotFound {
		t.Errorf("unknown session: exit %d (%v), want %d", e.ExitCode(), err, clierr.ExitNotFound)
	}
	for _, args := range [][]string{{"auth", "sessions", "revoke"}, {"auth", "sessions", "revoke", "sess-2", "--others"}} {
		if _, _, err := run(t, args...); !clierr.IsUsage(err) {
			t.Errorf("%v: got %v, want a usage error", args, err)
		}
	}
}
