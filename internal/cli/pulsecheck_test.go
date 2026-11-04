package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/norcubeplatform/cli/internal/clierr"
)

// PulseCheck fixtures: three monitors (an HTTP one that is down with an
// open critical incident, an SSL one expiring soon, a domain one that is
// fine), two alert channels, one incident.
const (
	pcShop   = "7d1c2f7e-6a1f-4f5e-9b8a-1f7c2b3d4e5f"
	pcCert   = "2b3c4d5e-6f70-4a81-92a3-b4c5d6e7f809"
	pcDomain = "9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d"
	pcHook   = "0c2d4f6a-8b1e-4c3d-9e5f-7a6b5c4d3e2f"
	pcPhone  = "5e6f7a8b-9c0d-4e1f-8a2b-3c4d5e6f7a8b"
	pcInc    = "3f2c8a8e-6c1d-4a59-9a43-2b1f0f6f9d10"
	pcUser   = "11111111-2222-4333-8444-555555555555"
)

var (
	pcShopJSON = `{"id":"` + pcShop + `","name":"Shop","description":"","type":"http","variant":"keyword","target":"https://shop.example.com/health",
		"intervalSeconds":60,"status":"critical","overallStatus":"critical","statusSince":"2026-11-03T08:00:00Z",
		"condition":{"code":"unexpected_status","message":"HTTP 503","since":"2026-11-03T08:00:00Z","facts":{"statusCode":503},"alertedThresholds":[]},
		"lastCheck":{"at":"2026-11-03T08:10:00Z","severity":"critical","condition":"unexpected_status","totalMs":180},
		"nextCheckAt":"2026-11-03T08:11:00Z",
		"last24h":{"from":"2026-11-02T08:00:00Z","checks":1440,"ok":1420,"warning":0,"critical":20,"inconclusive":0,"uptimePercent":98.61,"warningPercent":0,"avgMs":120,"p50Ms":110,"p95Ms":240},
		"aspects":[{"aspect":"certificate","subject":"shop.example.com","status":"ok","condition":"ok","daysRemaining":64,"thresholdsDays":[30,14,7],"alertedThresholds":[]}],
		"regions":[{"code":"eu-central-1","name":"Frankfurt"}],
		"http":{"url":"https://shop.example.com/health","method":"GET","expectedStatus":["2xx"],"timeoutMs":10000,"slowThresholdMs":1500,
			"followRedirects":true,"maxRedirects":5,"verifyTls":true,"keyword":"ok","keywordMode":"present","keywordCaseSensitive":false,
			"headers":[{"name":"Authorization","secret":true},{"name":"X-Env","secret":false,"value":"prod"}],
			"certificateExpiry":{"thresholdsDays":[30,14,7]}},
		"createdAt":"2026-10-01T09:00:00Z","createdBy":"` + pcUser + `","updatedAt":"2026-10-01T09:00:00Z"}`
	pcCertJSON = `{"id":"` + pcCert + `","name":"API cert","description":"","type":"ssl","variant":"ssl","target":"api.example.com:443",
		"intervalSeconds":3600,"status":"warning","overallStatus":"warning","statusSince":"2026-11-01T00:00:00Z",
		"condition":{"code":"cert_expiring","message":"Certificate expires in 12 days","since":"2026-11-01T00:00:00Z","facts":{"daysRemaining":12,"thresholdDays":14,"expiresAt":"2026-11-15T12:00:00Z"},"alertedThresholds":[{"days":14,"alertedAt":"2026-11-01T00:00:00Z"}]},
		"lastCheck":{"at":"2026-11-03T08:00:00Z","severity":"warning","condition":"cert_expiring","totalMs":95},
		"aspects":[],"regions":[{"code":"eu-central-1","name":"Frankfurt"}],
		"ssl":{"host":"api.example.com","port":443,"expiryThresholdsDays":[30,14,7]},
		"createdAt":"2026-10-02T09:00:00Z","createdBy":"` + pcUser + `","updatedAt":"2026-10-02T09:00:00Z"}`
	pcDomainJSON = `{"id":"` + pcDomain + `","name":"example.com","description":"Company domain","type":"domain","variant":"domain","target":"example.com",
		"intervalSeconds":43200,"status":"ok","overallStatus":"ok","statusSince":"2026-10-03T09:00:00Z",
		"condition":{"code":"ok","message":"Registered until 2027-08-01","since":"2026-10-03T09:00:00Z","facts":{"daysRemaining":271},"alertedThresholds":[]},
		"lastCheck":{"at":"2026-11-03T06:00:00Z","severity":"ok","condition":"ok","totalMs":420},
		"aspects":[],"regions":[{"code":"eu-central-1","name":"Frankfurt"}],
		"domain":{"domain":"example.com","expiryThresholdsDays":[60,30]},
		"createdAt":"2026-10-03T09:00:00Z","createdBy":"` + pcUser + `","updatedAt":"2026-10-03T09:00:00Z"}`
	pcIncidentJSON = `"id":"` + pcInc + `","monitorId":"` + pcShop + `","monitorName":"Shop","monitorType":"http","monitorVariant":"keyword",
		"monitorTarget":"https://shop.example.com/health","aspect":"availability","title":"Shop is down","status":"open","severity":"critical",
		"peakSeverity":"critical","condition":"unexpected_status","summary":"HTTP 503","facts":{"statusCode":503},
		"openedAt":"2026-11-03T08:01:00Z","durationSeconds":540`
	pcHookJSON = `{"id":"` + pcHook + `","kind":"webhook","name":"On-call webhook","isDefault":true,"enabled":true,"verified":true,
		"settings":{"url":"https://hooks.example.com/pulsecheck"},"createdAt":"2026-10-01T09:00:00Z","updatedAt":"2026-10-01T09:00:00Z"}`
	pcPhoneJSON = `{"id":"` + pcPhone + `","kind":"sms","name":"On-call phone","isDefault":false,"enabled":true,"verified":false,
		"settings":{"phone":"+420601123456"},"createdAt":"2026-10-05T09:00:00Z","updatedAt":"2026-10-05T09:00:00Z"}`
)

func pcList(items ...string) string {
	return `{"list":[` + strings.Join(items, ",") + `],"cursors":{},"meta":{}}`
}

func init() {
	for k, v := range map[string]string{
		"GET /monitors":             pcList(pcShopJSON, pcCertJSON, pcDomainJSON),
		"GET /monitors/" + pcShop:   pcShopJSON,
		"GET /monitors/" + pcCert:   pcCertJSON,
		"GET /monitors/" + pcDomain: pcDomainJSON,
		"GET /monitors/" + pcShop + "/results": pcList(
			`{"id":9002,"checkedAt":"2026-11-03T08:10:00Z","severity":"critical","condition":"unexpected_status","statusCode":503,"errorMessage":"HTTP 503","region":"eu-central-1","timings":{"dnsMs":4,"connectMs":12,"tlsMs":30,"ttfbMs":170,"totalMs":180}}`,
			`{"id":9001,"checkedAt":"2026-11-03T07:59:00Z","severity":"ok","condition":"ok","statusCode":200,"region":"eu-central-1","timings":{"dnsMs":3,"connectMs":11,"tlsMs":28,"ttfbMs":100,"totalMs":110}}`),
		"GET /monitors/" + pcShop + "/status-changes": pcList(
			`{"id":2,"from":"ok","to":"critical","changedAt":"2026-11-03T08:00:00Z","durationSeconds":600,"condition":"unexpected_status","statusCode":503,"message":"HTTP 503","checkResultId":9000,"events":[]}`,
			`{"id":1,"from":"pending","to":"ok","changedAt":"2026-10-01T09:01:00Z","endedAt":"2026-11-03T08:00:00Z","durationSeconds":2847540,"condition":"ok","checkResultId":1,"events":[]}`),
		"GET /monitors/" + pcShop + "/stats": `{"from":"2026-11-02T08:00:00Z","to":"2026-11-03T08:10:00Z","granularity":"hour","checks":1440,"ok":1420,"warning":0,"critical":20,"inconclusive":0,"uptimePercent":98.61,"avgMs":120,"maxMs":980,"series":[]}`,
		"GET /monitors/" + pcShop + "/availability": `{"from":"2026-11-01","to":"2026-11-03","checks":4300,"ok":4260,"warning":0,"critical":40,"inconclusive":0,"uptimePercent":99.07,"downtimeMinutes":40,"days":[
			{"date":"2026-11-01","checks":1440,"ok":1440,"warning":0,"critical":0,"inconclusive":0,"uptimePercent":100,"downtimeMinutes":0,"noData":false},
			{"date":"2026-11-02","checks":1440,"ok":1420,"warning":0,"critical":20,"inconclusive":0,"uptimePercent":98.61,"downtimeMinutes":20,"noData":false},
			{"date":"2026-11-03","checks":0,"ok":0,"warning":0,"critical":0,"inconclusive":0,"downtimeMinutes":0,"noData":true}]}`,
		"GET /monitors/" + pcCert + "/certificate": `{"resultId":77,"checkedAt":"2026-11-03T08:00:00Z","severity":"warning","condition":"cert_expiring","daysRemaining":12,
			"thresholdsDays":[30,14,7],"nextThresholdAt":"2026-11-08T12:00:00Z","nextThresholdDays":7,
			"certificate":{"subject":"CN=api.example.com","issuer":"CN=Example CA","dnsNames":["api.example.com"],"notBefore":"2026-08-17T12:00:00Z","notAfter":"2026-11-15T12:00:00Z",
				"daysRemaining":12,"chainValid":true,"hostnameValid":true,"serialNumber":"04ab","fingerprintSha256":"aa:bb"}}`,
		"GET /monitors/" + pcShop + "/alerts": `{"useDefaults":false,"confirmFailures":2,"confirmSuccesses":1,"reminderMinutes":60,
			"routes":[{"channelId":"` + pcHook + `","events":["down","recovered"]}],"applicableEvents":["down","warning","expiring","expired","recovered"]}`,
		"GET /incidents": pcList(`{` + pcIncidentJSON + `}`),
		"GET /incidents/" + pcInc: `{` + pcIncidentJSON + `,"timeline":[{"type":"opened","at":"2026-11-03T08:01:00Z"}],
			"alerts":[{"alertId":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","queuedAt":"2026-11-03T08:01:00Z","event":"down","channelId":"` + pcHook + `","channelName":"On-call webhook","channelKind":"webhook","status":"sent","attempts":1,"sentAt":"2026-11-03T08:01:02Z"}]}`,
		"GET /incidents/summary":         `{"open":{"total":1,"critical":1,"warning":0,"unacknowledged":1},"resolvedLast7d":4,"resolvedLast30d":12,"mttrLast30dSeconds":1260}`,
		"GET /alert-channels":            pcList(pcHookJSON, pcPhoneJSON),
		"GET /alert-channels/" + pcHook:  pcHookJSON,
		"GET /alert-channels/" + pcPhone: pcPhoneJSON,
		"GET /alert-channels/" + pcHook + "/deliveries": pcList(
			`{"id":"bbbbbbbb-cccc-4ddd-8eee-ffffffffffff","event":"down","monitorId":"` + pcShop + `","monitorName":"Shop","incidentId":"` + pcInc + `","status":"sent","attempts":1,"createdAt":"2026-11-03T08:01:00Z","sentAt":"2026-11-03T08:01:02Z"}`),
	} {
		goldenData[k] = v
	}
}

func TestGoldenPulseCheck(t *testing.T) {
	commands := []struct {
		name string
		args []string
	}{
		{"pulsecheck-status", []string{"pulsecheck", "status"}},
		{"pulsecheck-monitors-list", []string{"pulsecheck", "monitors", "list"}},
		{"pulsecheck-monitors-get", []string{"pulsecheck", "monitors", "get", pcShop}},
		{"pulsecheck-monitors-get-ssl", []string{"pulsecheck", "monitors", "get", pcCert}},
		{"pulsecheck-results-list", []string{"pulsecheck", "monitors", "results", "list", pcShop}},
		{"pulsecheck-status-changes-list", []string{"pulsecheck", "monitors", "status-changes", "list", pcShop}},
		{"pulsecheck-stats-get", []string{"pulsecheck", "monitors", "stats", "get", pcShop}},
		{"pulsecheck-availability-get", []string{"pulsecheck", "monitors", "availability", "get", pcShop}},
		{"pulsecheck-certificate-get", []string{"pulsecheck", "monitors", "certificate", "get", pcCert}},
		{"pulsecheck-alerts-get", []string{"pulsecheck", "monitors", "alerts", "get", pcShop}},
		{"pulsecheck-incidents-list", []string{"pulsecheck", "incidents", "list"}},
		{"pulsecheck-incidents-get", []string{"pulsecheck", "incidents", "get", pcInc}},
		{"pulsecheck-incidents-summary", []string{"pulsecheck", "incidents", "summary"}},
		{"pulsecheck-channels-list", []string{"pulsecheck", "channels", "list"}},
		{"pulsecheck-channels-get", []string{"pulsecheck", "channels", "get", pcHook}},
		{"pulsecheck-deliveries-list", []string{"pulsecheck", "channels", "deliveries", "list", pcHook}},
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

// The machine-readable tree of the whole PulseCheck group is pinned: it
// is what scripts and agents read to discover the commands.
func TestGoldenCommandsPulseCheck(t *testing.T) {
	commandsEnv(t)
	out, stderr, err := run(t, "commands", "pulsecheck", "-o", "json")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	checkGolden(t, "commands-pulsecheck.json", out, "http://unused")
}

// recorder captures the requests a test's handlers saw.
type recorder struct {
	mu   sync.Mutex
	reqs []recorded
}

type recorded struct {
	method, path, query string
	body                map[string]any
}

func (r *recorder) handler(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		r.mu.Lock()
		r.reqs = append(r.reqs, recorded{req.Method, req.URL.Path, req.URL.RawQuery, m})
		r.mu.Unlock()
		if status >= 400 {
			w.Header().Set("Content-Type", "application/problem+json")
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}
}

func (r *recorder) only(t *testing.T) recorded {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.reqs) != 1 {
		t.Fatalf("%d requests, want 1: %+v", len(r.reqs), r.reqs)
	}
	return r.reqs[0]
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reqs)
}

func exitOf(err error) int { return Describe(err).ExitCode() }

func TestPulseCheckMonitorCreate(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /monitors": rec.handler(201, pcShopJSON)})

	out, stderr, err := runIn(t, "Authorization: Bearer s3cret\n", "pulsecheck", "monitors", "create", "Shop", "--type", "keyword",
		"--url", "https://shop.example.com/health", "--keyword", "ok", "--header", "X-Env: prod",
		"--secret-headers-file", "-", "--slow", "1500ms", "-o", "json")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if !strings.Contains(out, `"id": "`+pcShop+`"`) {
		t.Errorf("stdout: %s", out)
	}
	b := rec.only(t).body
	h, _ := b["http"].(map[string]any)
	if b["type"] != "http" || b["name"] != "Shop" || b["intervalSeconds"] != float64(60) || h == nil {
		t.Fatalf("body %v", b)
	}
	if h["method"] != "GET" || h["timeoutMs"] != float64(10000) || h["followRedirects"] != true || h["verifyTls"] != true ||
		h["keyword"] != "ok" || h["slowThresholdMs"] != float64(1500) {
		t.Errorf("http settings %v", h)
	}
	if h["certificateExpiry"] == nil || h["domainExpiry"] == nil {
		t.Errorf("an https URL watches its certificate and domain by default: %v", h)
	}
	headers, _ := h["headers"].([]any)
	if len(headers) != 2 || fmt.Sprint(headers[1]) != "map[name:Authorization secret:true value:Bearer s3cret]" {
		t.Errorf("headers %v", headers)
	}
	if strings.Contains(stderr, "s3cret") || strings.Contains(out, "s3cret") {
		t.Error("a secret header value reached the output")
	}

	// SSL and domain monitors, and a body from a file.
	rec = recorder{}
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /monitors": rec.handler(201, pcCertJSON)})
	if _, stderr, err := run(t, "pulsecheck", "monitors", "create", "API cert", "--type", "ssl", "--host", "api.example.com", "--alert-days", "21,7"); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if s := fmt.Sprint(rec.only(t).body["ssl"]); s != "map[expiryThresholdsDays:[21 7] host:api.example.com port:443]" {
		t.Errorf("ssl %s", s)
	}
	rec = recorder{}
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /monitors": rec.handler(201, pcDomainJSON)})
	if _, stderr, err := runIn(t, `{"type":"domain","name":"x","intervalSeconds":86400,"domain":{"domain":"example.com","expiryThresholdsDays":[30]}}`,
		"pulsecheck", "monitors", "create", "example.com", "--from-file", "-"); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if b := rec.only(t).body; b["name"] != "example.com" || b["intervalSeconds"] != float64(86400) {
		t.Errorf("from-file body %v", b)
	}
}

func TestPulseCheckUsageErrorsBeforeAnyRequest(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{
		"POST /monitors":       rec.handler(201, pcShopJSON),
		"POST /alert-channels": rec.handler(201, pcHookJSON),
	})
	cases := [][]string{
		{"pulsecheck", "monitors", "create", "x"},
		{"pulsecheck", "monitors", "create", "x", "--type", "tcp"},
		{"pulsecheck", "monitors", "create", "x", "--type", "keyword", "--url", "https://e.com"},
		{"pulsecheck", "monitors", "create", "x", "--type", "ssl", "--url", "https://e.com", "--host", "e.com"},
		{"pulsecheck", "monitors", "create", "x", "--type", "http"},
		{"pulsecheck", "monitors", "create", "x", "--type", "http", "--url", "https://e.com", "--interval", "1500ms"},
		{"pulsecheck", "monitors", "create", "x", "--type", "http", "--url", "https://e.com", "--header", "no colon"},
		{"pulsecheck", "monitors", "create", "--from-file", "-", "--type", "http"},
		{"pulsecheck", "monitors", "list", "--status", "down"},
		{"pulsecheck", "monitors", "run", pcShop, "--fail-on", "warning"},
		{"pulsecheck", "monitors", "update", pcShop},
		{"pulsecheck", "monitors", "results", "list", pcShop, "--since", "yesterday"},
		{"pulsecheck", "monitors", "alerts", "set", pcShop},
		{"pulsecheck", "monitors", "alerts", "set", pcShop, "--defaults", "--mute"},
		{"pulsecheck", "monitors", "alerts", "set", pcShop, "--route", "hook"},
		{"pulsecheck", "monitors", "alerts", "set", pcShop, "--route", "hook=paged"},
		{"pulsecheck", "monitors", "bulk", "pause"},
		{"pulsecheck", "monitors", "bulk", "pause", pcShop, "--status", "critical"},
		{"pulsecheck", "incidents", "get", "not-a-uuid"},
		{"pulsecheck", "incidents", "list", "--acknowledged", "--unacknowledged"},
		{"pulsecheck", "channels", "create", "x"},
		{"pulsecheck", "channels", "create", "x", "--kind", "slack", "--url", "https://hooks.slack.com/services/T/B/x"},
		{"pulsecheck", "channels", "create", "x", "--kind", "sms", "--address", "a@example.com"},
		{"pulsecheck", "channels", "create", "x", "--kind", "webhook", "--url", "https://e.com/h", "--secret-file", "-"},
		{"pulsecheck", "status", "--fail-on", "sometimes"},
	}
	for _, args := range cases {
		_, _, err := run(t, args...)
		if err == nil || exitOf(err) != clierr.ExitUsage {
			t.Errorf("%v: got %v (exit %d), want a usage error", args, err, exitOf(err))
		}
	}
	if n := rec.count(); n != 0 {
		t.Fatalf("%d requests sent for invalid input", n)
	}
}

// update starts from the monitor's settings: a changed flag keeps every
// other setting, and a secret header keeps its stored value (no value
// sent).
func TestPulseCheckMonitorUpdateMergesSettings(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{"PATCH /monitors/" + pcShop: rec.handler(200, pcShopJSON)})
	if _, stderr, err := run(t, "pulsecheck", "monitors", "update", pcShop, "--timeout", "5s", "--name", "Shop EU"); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	b := rec.only(t).body
	h, _ := b["http"].(map[string]any)
	if b["name"] != "Shop EU" || h["timeoutMs"] != float64(5000) || h["keyword"] != "ok" || h["slowThresholdMs"] != float64(1500) {
		t.Fatalf("body %v", b)
	}
	if hs := fmt.Sprint(h["headers"]); hs != "[map[name:Authorization secret:true] map[name:X-Env secret:false value:prod]]" {
		t.Errorf("headers %s", hs)
	}
	if _, _, err := run(t, "pulsecheck", "monitors", "update", pcShop, "--host", "x.example.com"); exitOf(err) != clierr.ExitUsage {
		t.Errorf("ssl flag on an http monitor: %v", err)
	}
}

// Monitors are named by id or exact name: one match is used, none is a
// 404 (exit 3), several are a usage error.
func TestPulseCheckMonitorNames(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /monitors/" + pcShop + "/pause": rec.handler(200, pcShopJSON)})
	out, stderr, err := run(t, "pulsecheck", "monitors", "pause", "shop")
	if err != nil || !strings.Contains(out, "Paused monitor Shop") {
		t.Fatalf("%v %q %s", err, out, stderr)
	}
	if _, _, err := run(t, "pulsecheck", "monitors", "pause", "nope"); exitOf(err) != clierr.ExitNotFound {
		t.Errorf("unknown name: %v", err)
	}
	twice := strings.Replace(pcCertJSON, `"name":"API cert"`, `"name":"Shop"`, 1)
	goldenHandlers(t, map[string]http.HandlerFunc{"GET /monitors": rec.handler(200, pcList(pcShopJSON, twice))})
	if _, _, err := run(t, "pulsecheck", "monitors", "get", "Shop"); exitOf(err) != clierr.ExitUsage || !strings.Contains(err.Error(), pcCert) {
		t.Errorf("ambiguous name: %v", err)
	}
}

func TestPulseCheckMonitorDeleteConfirms(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{"DELETE /monitors/" + pcShop: rec.handler(204, "")})
	if _, _, err := run(t, "pulsecheck", "monitors", "delete", pcShop); exitOf(err) != clierr.ExitUsage || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("without a terminal: %v", err)
	}
	if rec.count() != 0 {
		t.Fatal("deleted without confirmation")
	}
	out, _, err := run(t, "pulsecheck", "monitors", "delete", pcShop, "--yes", "-o", "json")
	if err != nil || !strings.Contains(out, `"deleted": true`) {
		t.Fatalf("%v %s", err, out)
	}
	// A linked monitor: 409 MANAGED_RESOURCE exits 8.
	goldenHandlers(t, map[string]http.HandlerFunc{"DELETE /monitors/" + pcShop: rec.handler(409,
		`{"type":"https://docs.norcube.com/errors/managed-resource","title":"Managed by another service","status":409,"detail":"This monitor belongs to a DomainRadar saved domain.","code":"MANAGED_RESOURCE"}`)})
	if _, _, err := run(t, "pulsecheck", "monitors", "delete", pcShop, "--yes"); exitOf(err) != clierr.ExitConflict {
		t.Errorf("managed monitor: %v", err)
	}
}

// run --wait turns the check's verdict into the exit code: a critical
// result is job_failed (exit 1) with the result in details and nothing
// on stdout; an ok one exits 0 and prints the result.
func TestPulseCheckMonitorRunWait(t *testing.T) {
	goldenEnv(t)
	down := `{"id":9003,"checkedAt":"2026-11-03T08:12:00Z","severity":"critical","condition":"unexpected_status","statusCode":503,"region":"eu-central-1","timings":{"totalMs":150}}`
	slow := strings.NewReplacer(`"critical"`, `"warning"`, "unexpected_status", "slow_response").Replace(down)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /monitors/" + pcShop + "/check": rec.handler(200, down)})

	out, _, err := run(t, "pulsecheck", "monitors", "run", pcShop, "-o", "json")
	if err != nil || !strings.Contains(out, `"severity": "critical"`) {
		t.Fatalf("without --wait: %v %s", err, out)
	}
	out, _, err = run(t, "pulsecheck", "monitors", "run", "Shop", "--wait", "-o", "json")
	e := Describe(err)
	if e.Code != clierr.CodeJobFailed || e.ExitCode() != clierr.ExitError || out != "" || e.Details == nil {
		t.Fatalf("critical with --wait: %+v stdout %q", e, out)
	}
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /monitors/" + pcShop + "/check": rec.handler(200, slow)})
	if _, _, err := run(t, "pulsecheck", "monitors", "run", pcShop, "--wait"); err != nil {
		t.Errorf("a warning passes by default: %v", err)
	}
	if _, _, err := run(t, "pulsecheck", "monitors", "run", pcShop, "--wait", "--fail-on", "warning"); Describe(err).Code != clierr.CodeJobFailed {
		t.Errorf("--fail-on warning: %v", err)
	}
}

func TestPulseCheckBulk(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	ok := `{"action":"pause","succeeded":2,"failed":0,"items":[{"monitorId":"` + pcShop + `","ok":true},{"monitorId":"` + pcCert + `","ok":true}]}`
	goldenHandlers(t, map[string]http.HandlerFunc{
		"GET /monitors":       rec.handler(200, pcList(pcShopJSON, pcCertJSON)),
		"POST /monitors/bulk": rec.handler(200, ok),
	})
	if _, _, err := run(t, "pulsecheck", "monitors", "bulk", "pause", "--search", "example.com"); exitOf(err) != clierr.ExitUsage {
		t.Fatalf("bulk without --yes: %v", err)
	}
	rec = recorder{}
	goldenHandlers(t, map[string]http.HandlerFunc{
		"GET /monitors":       rec.handler(200, pcList(pcShopJSON, pcCertJSON)),
		"POST /monitors/bulk": rec.handler(200, ok),
	})
	out, stderr, err := run(t, "pulsecheck", "monitors", "bulk", "pause", "--search", "example.com", "--status", "critical", "--yes", "-o", "json")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if len(rec.reqs) != 2 || !strings.Contains(rec.reqs[0].query, "q=example.com") || !strings.Contains(rec.reqs[0].query, "status=critical") {
		t.Fatalf("requests %+v", rec.reqs)
	}
	if fmt.Sprint(rec.reqs[1].body) != "map[action:pause monitorIds:["+pcShop+" "+pcCert+"]]" || !strings.Contains(out, `"ok": true`) {
		t.Errorf("bulk body %v, out %s", rec.reqs[1].body, out)
	}
	partial := `{"action":"delete","succeeded":1,"failed":1,"items":[{"monitorId":"` + pcShop + `","ok":true},{"monitorId":"` + pcCert + `","ok":false,"code":"MANAGED_RESOURCE","detail":"Linked."}]}`
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /monitors/bulk": rec.handler(200, partial)})
	out, _, err = run(t, "pulsecheck", "monitors", "bulk", "delete", pcShop, pcCert, "--yes", "-o", "text")
	if exitOf(err) != clierr.ExitError || !strings.Contains(out, "MANAGED_RESOURCE") {
		t.Errorf("partial failure: %v %q", err, out)
	}
}

func TestPulseCheckIncidents(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{"GET /incidents": rec.handler(200, pcList())})
	if _, stderr, err := run(t, "pulsecheck", "incidents", "list", "--status", "open", "--severity", "critical", "--aspect", "availability",
		"--unacknowledged", "--monitor", pcShop, "--search", "shop", "--since", "2026-11-01T00:00:00Z", "-o", "json"); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	q := rec.only(t).query
	for _, want := range []string{"status=open", "severity=critical", "aspect=availability", "acknowledged=false", "monitorId=" + pcShop, "q=shop", "from=2026-11-01T00%3A00%3A00Z", "limit=50"} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q lacks %s", q, want)
		}
	}
	acked := strings.Replace(goldenData["GET /incidents/"+pcInc], `"durationSeconds":540`, `"durationSeconds":540,"acknowledgedAt":"2026-11-03T08:05:00Z","acknowledgedBy":"`+pcUser+`","acknowledgedByName":"Dev Eloper"`, 1)
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /incidents/" + pcInc + "/acknowledge": rec.handler(200, acked)})
	out, _, err := run(t, "pulsecheck", "incidents", "ack", pcInc)
	if err != nil || !strings.Contains(out, "Acknowledged incident") {
		t.Fatalf("%v %q", err, out)
	}
}

func TestPulseCheckAlertsSet(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{"PUT /monitors/" + pcShop + "/alerts": rec.handler(200, goldenData["GET /monitors/"+pcShop+"/alerts"])})
	if _, stderr, err := run(t, "pulsecheck", "monitors", "alerts", "set", pcShop, "--route", "on-call webhook=down,recovered", "--reminder", "30m"); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	b := rec.only(t).body
	if fmt.Sprint(b["routes"]) != "[map[channelId:"+pcHook+" events:[down recovered]]]" || b["reminderMinutes"] != float64(30) ||
		b["confirmFailures"] != float64(2) || b["useDefaults"] != false {
		t.Errorf("body %v", b)
	}
	rec = recorder{}
	goldenHandlers(t, map[string]http.HandlerFunc{"PUT /monitors/" + pcShop + "/alerts": rec.handler(200, goldenData["GET /monitors/"+pcShop+"/alerts"])})
	if _, _, err := run(t, "pulsecheck", "monitors", "alerts", "set", pcShop, "--defaults"); err != nil || fmt.Sprint(rec.only(t).body) != "map[useDefaults:true]" {
		t.Errorf("--defaults: %v %v", err, rec.reqs)
	}
	rec = recorder{}
	goldenHandlers(t, map[string]http.HandlerFunc{"PUT /monitors/" + pcShop + "/alerts": rec.handler(200, goldenData["GET /monitors/"+pcShop+"/alerts"])})
	if _, _, err := run(t, "pulsecheck", "monitors", "alerts", "set", pcShop, "--mute"); err != nil || fmt.Sprint(rec.only(t).body["routes"]) != "[]" {
		t.Errorf("--mute: %v %v", err, rec.reqs)
	}
}

// A webhook's signing secret goes to the file named, never to stdout or
// stderr; Slack URLs come from a file or stdin.
func TestPulseCheckChannelCreateKeepsSecretsOffStdout(t *testing.T) {
	goldenEnv(t)
	const secret = "whsec_c2VjcmV0LXNpZ25pbmcta2V5" // #nosec G101 -- a test fixture, not a credential
	withSecret := strings.Replace(pcHookJSON, `"isDefault":true`, `"isDefault":true,"signingSecret":"`+secret+`"`, 1)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /alert-channels": rec.handler(201, withSecret)})
	file := filepath.Join(t.TempDir(), "secret.txt")
	for _, format := range []string{"table", "json", "yaml", "text"} {
		_ = os.Remove(file)
		out, stderr, err := run(t, "pulsecheck", "channels", "create", "On-call webhook", "--kind", "webhook",
			"--url", "https://hooks.example.com/pulsecheck", "--secret-file", file, "-o", format)
		if err != nil {
			t.Fatalf("%s: %v\n%s", format, err, stderr)
		}
		if strings.Contains(out, secret) || strings.Contains(stderr, secret) || strings.Contains(out, "signingSecret") {
			t.Fatalf("%s: the secret reached the output:\n%s\n%s", format, out, stderr)
		}
	}
	got, err := os.ReadFile(file) // #nosec G304 -- test file
	if err != nil || strings.TrimSpace(string(got)) != secret {
		t.Fatalf("secret file: %v %q", err, got)
	}
	if info, _ := os.Stat(file); info.Mode().Perm()&0o077 != 0 {
		t.Errorf("secret file mode %v", info.Mode().Perm())
	}
	// Without --secret-file the secret is dropped, with a note.
	_, stderr, err := run(t, "pulsecheck", "channels", "create", "Hook", "--kind", "webhook", "--url", "https://hooks.example.com/x")
	if err != nil || !strings.Contains(stderr, "--rotate-secret") || strings.Contains(stderr, secret) {
		t.Errorf("no --secret-file: %v %q", err, stderr)
	}

	rec = recorder{}
	slack := `{"id":"` + pcHook + `","kind":"slack","name":"#alerts","isDefault":false,"enabled":true,"verified":true,"settings":{"hint":"hooks.slack.com/…x9z"},"createdAt":"2026-11-03T08:00:00Z","updatedAt":"2026-11-03T08:00:00Z"}`
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /alert-channels": rec.handler(201, slack)})
	out, stderr, err := runIn(t, "https://hooks.slack.com/services/T0/B0/x9z\n", "pulsecheck", "channels", "create", "#alerts", "--kind", "slack", "--url-file", "-")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if b := rec.only(t).body; fmt.Sprint(b["settings"]) != "map[url:https://hooks.slack.com/services/T0/B0/x9z]" || strings.Contains(out, "services/T0") {
		t.Errorf("slack body %v out %q", b, out)
	}
}

func TestPulseCheckChannelVerify(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	verified := strings.Replace(pcPhoneJSON, `"verified":false`, `"verified":true`, 1)
	goldenHandlers(t, map[string]http.HandlerFunc{
		"POST /alert-channels/" + pcPhone + "/verify":         rec.handler(204, ""),
		"POST /alert-channels/" + pcPhone + "/verify/confirm": rec.handler(200, verified),
	})
	// No terminal: the code is sent, and the command says how to confirm.
	out, stderr, err := run(t, "pulsecheck", "channels", "verify", "On-call phone", "-o", "json")
	if err != nil || !strings.Contains(out, `"codeSent": true`) || !strings.Contains(stderr, "--code-file -") {
		t.Fatalf("send: %v %q %q", err, out, stderr)
	}
	out, _, err = runIn(t, "123456\n", "pulsecheck", "channels", "verify", pcPhone, "--code-file", "-", "-o", "json")
	if err != nil || !strings.Contains(out, `"verified": true`) || strings.Contains(out, "123456") {
		t.Fatalf("confirm: %v %q", err, out)
	}
	if rec.reqs[1].path != "/alert-channels/"+pcPhone+"/verify/confirm" || rec.reqs[1].body["code"] != "123456" {
		t.Errorf("requests %+v", rec.reqs)
	}
	if _, _, err := runIn(t, "12ab\n", "pulsecheck", "channels", "verify", pcPhone, "--code-file", "-"); exitOf(err) != clierr.ExitUsage {
		t.Errorf("bad code: %v", err)
	}
	// An expired code: 409 CONFLICT, reason CODE_EXPIRED, exits 8 with the reason in details.
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /alert-channels/" + pcPhone + "/verify/confirm": rec.handler(409,
		`{"title":"Conflict","status":409,"detail":"The code has expired. Send a new one.","code":"CONFLICT","reason":"CODE_EXPIRED"}`)})
	_, _, err = runIn(t, "123456", "pulsecheck", "channels", "verify", pcPhone, "--code-file", "-")
	if e := Describe(err); e.ExitCode() != clierr.ExitConflict || fmt.Sprint(e.Details) != "map[reason:CODE_EXPIRED]" {
		t.Errorf("expired: %+v", e)
	}
}

func TestPulseCheckChannelTestAndDelete(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{
		"POST /alert-channels/" + pcPhone + "/test": rec.handler(200, `{"providerRef":"bg-123"}`),
		"POST /alert-channels/" + pcHook + "/test":  rec.handler(200, `{"providerRef":"wh-1"}`),
		"DELETE /alert-channels/" + pcHook:          rec.handler(204, ""),
	})
	if _, _, err := run(t, "pulsecheck", "channels", "test", pcPhone); exitOf(err) != clierr.ExitUsage {
		t.Fatalf("an SMS test needs --yes without a terminal: %v", err)
	}
	if out, _, err := run(t, "pulsecheck", "channels", "test", pcPhone, "--yes", "-o", "text"); err != nil || strings.TrimSpace(out) != "bg-123" {
		t.Fatalf("sms test: %v %q", err, out)
	}
	if _, _, err := run(t, "pulsecheck", "channels", "test", "on-call webhook"); err != nil {
		t.Fatalf("webhook test: %v", err)
	}
	if _, _, err := run(t, "pulsecheck", "channels", "delete", pcHook, "--yes"); err != nil || rec.count() != 3 {
		t.Fatalf("delete: %v %d", err, rec.count())
	}
}

func TestPulseCheckStatusFailOn(t *testing.T) {
	goldenEnv(t)
	out, _, err := run(t, "pulsecheck", "status", "--fail-on", "critical", "-o", "json")
	e := Describe(err)
	if e.Code != clierr.CodeUnhealthy || e.ExitCode() != clierr.ExitError || !strings.Contains(out, `"status": "critical"`) {
		t.Fatalf("critical incident open: %+v %q", e, out)
	}
	if _, _, err := run(t, "pulsecheck", "status"); err != nil {
		t.Errorf("default --fail-on none: %v", err)
	}
	var rec recorder
	quiet := `{"open":{"total":0,"critical":0,"warning":0,"unacknowledged":0},"resolvedLast7d":0,"resolvedLast30d":0}`
	goldenHandlers(t, map[string]http.HandlerFunc{"GET /incidents/summary": rec.handler(200, quiet)})
	if _, _, err := run(t, "pulsecheck", "status", "--fail-on", "warning"); err != nil {
		t.Errorf("nothing open: %v", err)
	}
}

// PulseCheck's RFC 9457 problems map to the CLI's codes: a scope a token
// lacks exits 5 (insufficient_scope), invalid fields exit 2 with the
// field errors in the message and details.
func TestPulseCheckProblems(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{
		"POST /monitors/" + pcShop + "/pause": rec.handler(403,
			`{"title":"Forbidden","status":403,"detail":"This token can't change monitors or incidents: it needs the pulsecheck.write scope.","code":"FORBIDDEN","reason":"INSUFFICIENT_SCOPE"}`),
		"POST /monitors": rec.handler(400,
			`{"title":"Validation failed","status":400,"detail":"The request has invalid fields.","code":"VALIDATION_FAILED","errors":[{"pointer":"/http/url","rule":"public","detail":"must be a public address"}]}`),
	})
	_, _, err := run(t, "pulsecheck", "monitors", "pause", pcShop)
	if e := Describe(err); e.Code != clierr.CodeInsufficientScope || e.ExitCode() != clierr.ExitAccess || !strings.Contains(e.Message, "pulsecheck.write") {
		t.Errorf("scope: %+v", e)
	}
	// A token without pulsecheck.channels (pulsecheck.write doesn't include it).
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /alert-channels": rec.handler(403,
		`{"title":"Forbidden","status":403,"detail":"This token can't change alert channels or send test alerts: it needs the pulsecheck.channels scope.","code":"FORBIDDEN","reason":"INSUFFICIENT_SCOPE"}`)})
	_, _, err = run(t, "pulsecheck", "channels", "create", "Ops", "--kind", "email", "--address", "ops@example.com")
	if e := Describe(err); e.Code != clierr.CodeInsufficientScope || e.ExitCode() != clierr.ExitAccess || !strings.Contains(e.Message, "pulsecheck.channels") {
		t.Errorf("channels scope: %+v", e)
	}
	_, _, err = run(t, "pulsecheck", "monitors", "create", "x", "--type", "http", "--url", "https://10.0.0.1/")
	e := Describe(err)
	var ce *clierr.Error
	if e.Code != clierr.CodeValidation || e.ExitCode() != clierr.ExitUsage || !strings.Contains(e.Message, "/http/url: must be a public address") || !errors.As(err, &ce) {
		t.Errorf("validation: %+v", e)
	}
}

// Monitor, channel and incident ids complete from the API, on arguments
// and on --monitor.
func TestPulseCheckCompletion(t *testing.T) {
	goldenEnv(t)
	clearCompletionCache(t)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"pulsecheck", "monitors", "get", ""}, pcShop + "," + pcCert + "," + pcDomain},
		{[]string{"pulsecheck", "monitors", "results", "list", "7d"}, pcShop},
		{[]string{"pulsecheck", "monitors", "bulk", "pause", pcShop, "2b"}, pcCert},
		{[]string{"pulsecheck", "incidents", "list", "--monitor", "9a"}, pcDomain},
		{[]string{"pulsecheck", "channels", "verify", "5e"}, pcPhone},
		{[]string{"pulsecheck", "incidents", "ack", ""}, pcInc},
		{[]string{"pulsecheck", "monitors", "list", "--status", ""}, "pending,ok,warning,critical,paused"},
	}
	for _, c := range cases {
		got, _ := complete(t, c.args...)
		if strings.Join(got, ",") != c.want {
			t.Errorf("%v: %v, want %s", c.args, got, c.want)
		}
	}
}

// The remaining commands send what their flags say.
func TestPulseCheckRequests(t *testing.T) {
	goldenEnv(t)
	secretFile := filepath.Join(t.TempDir(), "rotated.txt")
	rotated := strings.Replace(pcHookJSON, `"isDefault":true`, `"isDefault":false,"signingSecret":"whsec_bmV3LXNlY3JldA"`, 1)
	cases := []struct {
		name      string
		args      []string
		route     string
		status    int
		answer    string
		wantQuery string
		wantBody  string
	}{
		{"resume", []string{"monitors", "resume", "API cert"}, "POST /monitors/" + pcCert + "/resume", 200, pcCertJSON, "", ""},
		{"results range", []string{"monitors", "results", "list", pcShop, "--since", "2026-11-01T00:00:00Z", "--until", "2026-11-02T00:00:00Z", "--limit", "10"},
			"GET /monitors/" + pcShop + "/results", 200, pcList(), "from=2026-11-01T00%3A00%3A00Z&limit=10&to=2026-11-02T00%3A00%3A00Z", ""},
		{"stats range", []string{"monitors", "stats", "get", pcShop, "--range", "30d"}, "GET /monitors/" + pcShop + "/stats", 200,
			goldenData["GET /monitors/"+pcShop+"/stats"], "range=30d", ""},
		{"availability days", []string{"monitors", "availability", "get", pcShop, "--days", "7"}, "GET /monitors/" + pcShop + "/availability", 200,
			goldenData["GET /monitors/"+pcShop+"/availability"], "days=7", ""},
		{"list filters", []string{"monitors", "list", "--type", "ssl", "--status", "warning", "--sort", "uptime", "--search", "api", "--history"},
			"GET /monitors", 200, pcList(pcCertJSON), "include=history&limit=50&q=api&sort=uptime&status=warning&type=ssl", ""},
		{"deliveries status", []string{"channels", "deliveries", "list", pcHook, "--status", "dead"}, "GET /alert-channels/" + pcHook + "/deliveries", 200,
			pcList(), "limit=50&status=dead", ""},
		{"channel update", []string{"channels", "update", pcHook, "--name", "Pager", "--default=false", "--disable", "--rotate-secret", "--secret-file", secretFile},
			"PATCH /alert-channels/" + pcHook, 200, rotated, "", "map[enabled:false isDefault:false name:Pager rotateSecret:true]"},
		{"sms create", []string{"channels", "create", "Phone", "--kind", "sms", "--phone", "+420601123456"}, "POST /alert-channels", 201, pcPhoneJSON, "",
			"map[kind:sms name:Phone settings:map[phone:+420601123456]]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var rec recorder
			goldenHandlers(t, map[string]http.HandlerFunc{c.route: rec.handler(c.status, c.answer)})
			out, stderr, err := run(t, append(append([]string{"pulsecheck"}, c.args...), "-o", "json")...)
			if err != nil {
				t.Fatalf("%v\n%s", err, stderr)
			}
			r := rec.only(t)
			if c.wantQuery != "" && r.query != c.wantQuery {
				t.Errorf("query %q, want %q", r.query, c.wantQuery)
			}
			if c.wantBody != "" && fmt.Sprint(r.body) != c.wantBody {
				t.Errorf("body %v, want %s", r.body, c.wantBody)
			}
			if strings.Contains(out+stderr, "whsec_") {
				t.Errorf("a signing secret reached the output")
			}
		})
	}
	if got, _ := os.ReadFile(secretFile); strings.TrimSpace(string(got)) != "whsec_bmV3LXNlY3JldA" { // #nosec G304 -- test file
		t.Errorf("rotated secret file %q", got)
	}
}
