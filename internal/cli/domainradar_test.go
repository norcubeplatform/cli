package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/norcubeplatform/cli/internal/clierr"
)

// DomainRadar answers of the fake backend (see golden_test.go).
func init() {
	for k, v := range map[string]string{
		"GET /check/bulk": `{"list":[
			{"id":"bulk-1","status":"completed","mode":"verified","totalDomains":3,"completedDomains":3,"tlds":["com","io"],"createdAt":"2026-10-28T09:00:00Z"},
			{"id":"bulk-2","status":"running","mode":"bloom","totalDomains":1000,"completedDomains":250,"tlds":["dev"],"createdAt":"2026-10-28T10:00:00Z"}
		],"cursors":{"next":"cur-b2"},"meta":{}}`,
		"GET /check/bulk/bulk-1": `{"id":"bulk-1","status":"completed","mode":"verified","totalDomains":3,"completedDomains":3,"tlds":["com","io"],"createdAt":"2026-10-28T09:00:00Z"}`,
		"GET /check/bulk/bulk-1/results": `{"list":[
			{"domain":"acme.com","available":false,"status":"registered","method":"rdap","expiryDate":"2027-03-01T00:00:00Z","registrar":"Example Registrar, Inc.","cacheHit":true,"checkedAt":"2026-10-28T09:01:00Z"},
			{"domain":"acme.io","available":true,"status":"available","method":"rdap","checkedAt":"2026-10-28T09:01:00Z"},
			{"domain":"acme.zzz","error":"unsupported TLD","method":"whois","checkedAt":"2026-10-28T09:01:00Z"}
		],"cursors":{},"meta":{}}`,
		"GET /domains/saved": `{"list":[
			{"domain":"acme.dev","tld":"dev","notes":"ask legal first","savedAt":"2026-10-20T08:00:00Z","latestCheck":{"domain":"acme.dev","available":true,"method":"rdap","checkedAt":"2026-10-27T08:00:00Z"}},
			{"domain":"acme.com","tld":"com","savedAt":"2026-10-21T08:00:00Z","latestCheck":{"domain":"acme.com","available":false,"method":"dns","expiryDate":"2027-03-01T00:00:00Z","registrar":"Example Registrar, Inc.","checkedAt":"2026-10-27T08:00:00Z"}}
		],"cursors":{},"meta":{}}`,
		"GET /brand-monitor/watchers": `[
			{"id":"w-1","word":"acme","allowlist":["acme.com","acme.io"],"minNotificationScore":60,"aiContentCheck":true,"enabled":true,"notificationEmail":"security@acme.com","createdAt":"2026-10-01T08:00:00Z"},
			{"id":"w-2","word":"acmepay","enabled":false,"minNotificationScore":0,"aiContentCheck":false,"createdAt":"2026-10-02T08:00:00Z"}
		]`,
		"GET /brand-monitor/watchers/w-1": `{"id":"w-1","word":"acme","allowlist":["acme.com","acme.io"],"minNotificationScore":60,"aiContentCheck":true,"enabled":true,"notificationEmail":"security@acme.com","createdAt":"2026-10-01T08:00:00Z"}`,
		"GET /brand-monitor/hits": `{"list":[
			{"id":"h-1","watcherId":"w-1","domain":"acme-login.example","keyword":"acme","matchReason":"contains","score":88,"scoreLevel":"high","aiRisk":"high","issuer":"Example CA","createdAt":"2026-10-28T07:00:00Z"},
			{"id":"h-2","watcherId":"w-1","domain":"acmee.example","keyword":"acme","matchReason":"typo","score":41,"scoreLevel":"medium","issuer":"Example CA","createdAt":"2026-10-27T07:00:00Z"}
		],"next":null,"total":2}`,
		"GET /brand-monitor/hits/h-1": `{"id":"h-1","watcherId":"w-1","domain":"acme-login.example","keyword":"acme","matchReason":"contains","allDomains":["acme-login.example","www.acme-login.example"],"score":88,"scoreLevel":"high","aiRisk":"high","aiReasoning":"Login form imitating the brand.","issuer":"Example CA","registrationDate":"2026-10-26T00:00:00Z","createdAt":"2026-10-28T07:00:00Z"}`,
		"GET /name-generator/jobs": `{"list":[
			{"id":"n-1","status":"completed","description":"invoicing for freelancers","size":"small","totalNames":5120,"createdAt":"2026-10-25T08:00:00Z"}
		],"cursors":{},"meta":{}}`,
		"GET /name-generator/jobs/n-1":       `{"id":"n-1","status":"completed","description":"invoicing for freelancers","category":"fintech","style":"short","size":"small","maxLength":8,"totalNames":5120,"createdAt":"2026-10-25T08:00:00Z"}`,
		"GET /name-generator/jobs/n-1/names": `{"list":[{"name":"billo","length":5},{"name":"invoza","length":6}],"cursors":{"next":"cur-n2"},"meta":{}}`,
	} {
		goldenData[k] = v
	}
	goldenErrors["GET /check/bulk/bulk-404"] = struct {
		status int
		body   string
	}{http.StatusNotFound, `{"type":"NOT_FOUND","msg":"bulk check job not found"}`}
}

// checkHandler answers POST /check like DomainRadar: available unless the
// name starts with "acme".
func checkHandler(calls *int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			atomic.AddInt32(calls, 1)
		}
		var req struct {
			Domain string `json:"domain"`
			Mode   string `json:"mode"`
			MaxAge string `json:"maxAge"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		available := !strings.HasPrefix(req.Domain, "acme")
		fmt.Fprintf(w, `{"domain":%q,"available":%t,"method":"rdap","checkedAt":"2026-10-28T09:00:00Z","maxAge":%q}`, req.Domain, available, req.MaxAge)
	}
}

func TestGoldenDomainRadar(t *testing.T) {
	commands := []struct {
		name string
		args []string
	}{
		{"domainradar-check", []string{"domainradar", "check", "acme.com", "freshname.io"}},
		{"domainradar-bulk-list", []string{"domainradar", "bulk", "list"}},
		{"domainradar-bulk-get", []string{"domainradar", "bulk", "get", "bulk-1"}},
		{"domainradar-bulk-results", []string{"domainradar", "bulk", "results", "bulk-1"}},
		{"domainradar-saved-list", []string{"domainradar", "saved", "list"}},
		{"domainradar-watchers-list", []string{"domainradar", "watchers", "list"}},
		{"domainradar-watchers-get", []string{"domainradar", "watchers", "get", "w-1"}},
		{"domainradar-hits-list", []string{"domainradar", "hits", "list"}},
		{"domainradar-hits-get", []string{"domainradar", "hits", "get", "h-1"}},
		{"domainradar-names-list", []string{"domainradar", "names", "list"}},
		{"domainradar-names-get", []string{"domainradar", "names", "get", "n-1"}},
		{"domainradar-names-results", []string{"domainradar", "names", "results", "n-1"}},
	}
	for _, c := range commands {
		for format, ext := range goldenFormats {
			t.Run(c.name+"/"+format, func(t *testing.T) {
				url := goldenEnv(t)
				goldenHandlers(t, map[string]http.HandlerFunc{"POST /check": checkHandler(nil)})
				out, stderr, err := run(t, append(c.args, "-o", format, "--no-pager")...)
				if err != nil {
					t.Fatalf("%v\n%s", err, stderr)
				}
				checkGolden(t, c.name+"."+ext, out, url)
			})
		}
	}
}

// runIn runs the CLI with stdin.
func runIn(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()
	root := NewRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := root.ExecuteContext(ctx)
	return stdout.String(), stderr.String(), err
}

func TestDomainRadarCheckSendsModeAndMaxAge(t *testing.T) {
	goldenEnv(t)
	var got []map[string]any
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /check": func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got = append(got, body)
		fmt.Fprintf(w, `{"domain":%q,"available":true}`, body["domain"])
	}})
	if _, stderr, err := run(t, "domainradar", "check", "one.io", "--mode", "bloom", "--max-age", "24h", "-o", "json"); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if len(got) != 1 || got[0]["mode"] != "bloom" || got[0]["maxAge"] != "24h0m0s" {
		t.Fatalf("request bodies %v", got)
	}
}

func TestDomainRadarCheckReadsStdinAndDeduplicates(t *testing.T) {
	goldenEnv(t)
	var calls int32
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /check": checkHandler(&calls)})
	out, stderr, err := runIn(t, "# names\na.io\n\nb.io\na.io\n", "domainradar", "check", "-", "-o", "json", "--query", "[].domain")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if calls != 2 || !strings.Contains(out, `"a.io"`) || !strings.Contains(out, `"b.io"`) {
		t.Fatalf("calls=%d out=%s", calls, out)
	}
}

func TestDomainRadarUsageErrorsBeforeAnyRequest(t *testing.T) {
	goldenEnv(t)
	var calls int32
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /check": checkHandler(&calls)})
	many := make([]string, 101)
	for i := range many {
		many[i] = fmt.Sprintf("n%d.io", i)
	}
	cases := [][]string{
		{"domainradar", "check"},
		{"domainradar", "check", "a.io", "--mode", "deep"},
		append([]string{"domainradar", "check"}, many...),
		{"domainradar", "names", "check", "n-1", "x"},
		{"domainradar", "watchers", "update", "w-1"},
		{"domainradar", "bulk", "results", "bulk-1", "--available", "--registered"},
		{"domainradar", "bulk", "export", "bulk-1", "--format", "xml"},
	}
	for _, args := range cases {
		_, _, err := run(t, args...)
		if !clierr.IsUsage(err) && Describe(err).ExitCode() != clierr.ExitUsage {
			t.Errorf("%v: got %v, want a usage error", args, err)
		}
	}
	if calls != 0 {
		t.Fatalf("%d requests sent for invalid input", calls)
	}
}

func TestDomainRadarBulkCreateWaitsForTheJob(t *testing.T) {
	goldenEnv(t)
	var created struct {
		Domains []string `json:"domains"`
		Mode    string   `json:"mode"`
	}
	var polls int32
	goldenHandlers(t, map[string]http.HandlerFunc{
		"POST /check/bulk": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&created)
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"id":"bulk-9","status":"pending","mode":"bloom","totalDomains":2,"completedDomains":0}`)
		},
		"GET /check/bulk/bulk-9": func(w http.ResponseWriter, r *http.Request) {
			status, done := "running", 1
			if atomic.AddInt32(&polls, 1) >= 2 {
				status, done = "completed", 2
			}
			fmt.Fprintf(w, `{"id":"bulk-9","status":%q,"mode":"bloom","totalDomains":2,"completedDomains":%d}`, status, done)
		},
	})
	file := filepath.Join(t.TempDir(), "names.txt")
	if err := os.WriteFile(file, []byte("x.io\ny.io\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, stderr, err := run(t, "domainradar", "bulk", "create", "--file", file, "--mode", "bloom", "--wait", "-o", "json", "--query", "status")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if strings.TrimSpace(out) != `"completed"` || strings.Join(created.Domains, ",") != "x.io,y.io" || created.Mode != "bloom" {
		t.Fatalf("out=%s created=%+v", out, created)
	}
}

func TestDomainRadarWatchStreamsAndDeduplicates(t *testing.T) {
	goldenEnv(t)
	var opened int32
	goldenHandlers(t, map[string]http.HandlerFunc{"GET /check/bulk/bulk-1/stream": func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if atomic.AddInt32(&opened, 1) == 1 {
			// The first connection drops after one result.
			fmt.Fprint(w, "data: {\"domain\":\"a.io\",\"available\":true}\n\n")
			return
		}
		// A reconnect replays what was sent, then continues.
		fmt.Fprint(w, "data: {\"domain\":\"a.io\",\"available\":true}\n\n"+
			"data: {\"domain\":\"b.io\",\"available\":false}\n\n"+
			"event: done\ndata: {\"id\":\"bulk-1\",\"totalDomains\":2,\"completedDomains\":2}\n\n")
	}})
	out, stderr, err := run(t, "domainradar", "bulk", "watch", "bulk-1", "-o", "json", "--query", "[].domain")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	var domains []string
	if err := json.Unmarshal([]byte(out), &domains); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if strings.Join(domains, ",") != "a.io,b.io" || opened != 2 {
		t.Fatalf("domains=%v opened=%d", domains, opened)
	}

	// Table output prints each result as it arrives, one header.
	out, _, err = run(t, "domainradar", "bulk", "watch", "bulk-1", "--no-pager")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "DOMAIN") != 1 || !strings.Contains(out, "b.io") {
		t.Fatalf("table stream:\n%s", out)
	}
}

func TestDomainRadarWatchReportsAPIErrors(t *testing.T) {
	goldenEnv(t)
	goldenHandlers(t, map[string]http.HandlerFunc{"GET /check/bulk/bulk-404/stream": func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"type":"NOT_FOUND","msg":"bulk check job not found"}`)
	}})
	_, _, err := run(t, "domainradar", "bulk", "watch", "bulk-404")
	if e := Describe(err); e.Code != clierr.CodeNotFound || e.ExitCode() != clierr.ExitNotFound {
		t.Fatalf("got %v (code %s)", err, e.Code)
	}
}

func TestDomainRadarExport(t *testing.T) {
	goldenEnv(t)
	var query string
	goldenHandlers(t, map[string]http.HandlerFunc{"GET /check/bulk/bulk-1/export": func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.Header().Set("Content-Type", "text/csv")
		fmt.Fprint(w, "domain,available\nacme.io,true\n")
	}})
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "free.csv")
	out, stderr, err := run(t, "domainradar", "bulk", "export", "bulk-1", "--available", "--tld", ".IO", "--to", csvPath, "-o", "json")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	b, _ := os.ReadFile(csvPath)
	if string(b) != "domain,available\nacme.io,true\n" || !strings.Contains(query, "available=true") || !strings.Contains(query, "tld=io") {
		t.Fatalf("file=%q query=%q", b, query)
	}
	if !strings.Contains(out, `"sha256"`) || !strings.Contains(out, `"bytes": 30`) {
		t.Fatalf("result: %s", out)
	}

	// An existing file is never overwritten.
	if _, _, err := run(t, "domainradar", "bulk", "export", "bulk-1", "--to", csvPath); !clierr.IsUsage(err) {
		t.Fatalf("got %v, want a usage error for an existing file", err)
	}

	// JSON pages through the results; --to - writes only the data.
	out, _, err = run(t, "domainradar", "bulk", "export", "bulk-1", "--format", "json", "--to", "-")
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 3 {
		t.Fatalf("json export: %v %s", err, out)
	}
}

func TestDomainRadarDestructiveCommandsNeedYes(t *testing.T) {
	goldenEnv(t)
	var deletes int32
	goldenHandlers(t, map[string]http.HandlerFunc{
		"DELETE /check/bulk/bulk-1": func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&deletes, 1)
			w.WriteHeader(http.StatusNoContent)
		},
		"POST /check/bulk/bulk-2/cancel": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"id":"bulk-2","status":"cancelled","totalDomains":1000,"completedDomains":250}`)
		},
	})
	// Without a terminal and without --yes: a usage error, nothing sent.
	if _, _, err := run(t, "domainradar", "bulk", "delete", "bulk-1"); !clierr.IsUsage(err) || deletes != 0 {
		t.Fatalf("got %v, deletes %d", err, deletes)
	}
	out, stderr, err := run(t, "domainradar", "bulk", "delete", "bulk-1", "--yes")
	if err != nil || deletes != 1 || !strings.Contains(out, "Deleted bulk check bulk-1.") {
		t.Fatalf("%v %s %s", err, out, stderr)
	}
	out, _, err = run(t, "domainradar", "bulk", "cancel", "bulk-2", "--yes", "-o", "json", "--query", "status")
	if err != nil || strings.TrimSpace(out) != `"cancelled"` {
		t.Fatalf("%v %s", err, out)
	}
}

func TestDomainRadarMissingScopeExits5(t *testing.T) {
	goldenEnv(t)
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /domains/saved": func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"type":"INSUFFICIENT_SCOPE","msg":"This token can read and run checks, not change saved domains."}`)
	}})
	_, _, err := run(t, "domainradar", "saved", "add", "acme.io")
	if e := Describe(err); e.Code != clierr.CodeInsufficientScope || e.ExitCode() != clierr.ExitAccess {
		t.Fatalf("got %v (code %s, exit %d)", err, e.Code, e.ExitCode())
	}
}

func TestDomainRadarSavedAndWatchers(t *testing.T) {
	goldenEnv(t)
	var saved, updated map[string]any
	var removed string
	goldenHandlers(t, map[string]http.HandlerFunc{
		"POST /domains/saved": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&saved)
			w.WriteHeader(http.StatusNoContent)
		},
		"DELETE /domains/saved/acme.io": func(w http.ResponseWriter, r *http.Request) {
			removed = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		},
		"PUT /brand-monitor/watchers/w-1": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&updated)
			fmt.Fprint(w, `{"id":"w-1","word":"acme","enabled":false}`)
		},
	})
	if _, stderr, err := run(t, "domainradar", "saved", "add", "ACME.io", "--notes", "legal"); err != nil {
		t.Fatalf("%v %s", err, stderr)
	}
	if saved["domain"] != "acme.io" || saved["notes"] != "legal" {
		t.Fatalf("saved %v", saved)
	}
	if _, _, err := run(t, "domainradar", "saved", "remove", "acme.io"); err != nil || removed == "" {
		t.Fatalf("%v removed=%q", err, removed)
	}
	if _, _, err := run(t, "domainradar", "watchers", "update", "w-1", "--disable", "--min-score", "70"); err != nil {
		t.Fatal(err)
	}
	if updated["enabled"] != false || updated["minNotificationScore"] != float64(70) || len(updated) != 2 {
		t.Fatalf("update body %v (only the changed settings are sent)", updated)
	}
}

func TestDomainRadarNamesCheckMultipliesTLDs(t *testing.T) {
	goldenEnv(t)
	var body struct {
		Names []string `json:"names"`
		Tlds  []string `json:"tlds"`
	}
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /name-generator/jobs/n-1/check": func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &body); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"id":"bulk-7","status":"pending","totalDomains":4}`)
	}})
	out, stderr, err := runIn(t, "billo\ninvoza\n", "domainradar", "names", "check", "n-1", "--file", "-", "--tld", "com,.IO")
	if err != nil {
		t.Fatalf("%v %s", err, stderr)
	}
	if strings.Join(body.Names, ",") != "billo,invoza" || strings.Join(body.Tlds, ",") != "com,io" {
		t.Fatalf("body %+v", body)
	}
	if !strings.Contains(stderr, "4 domains") || !strings.Contains(out, "bulk-7") {
		t.Fatalf("out=%s stderr=%s", out, stderr)
	}
}

// The DomainRadar commands never write to the real network or files
// outside the test's temp dir; this guards the fake from silently
// answering 404 for a path a command was supposed to call.
func TestDomainRadarUnknownJobIsNotFound(t *testing.T) {
	goldenEnv(t)
	_, _, err := run(t, "domainradar", "bulk", "get", "bulk-404")
	var e *clierr.Error
	if !errors.As(err, &e) || e.ExitCode() != clierr.ExitNotFound {
		t.Fatalf("got %v", err)
	}
}
