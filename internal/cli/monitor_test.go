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

// Norcube Monitor fixtures: three monitors (an HTTP one that is down with an
// open critical incident, an SSL one expiring soon, a domain one that is
// fine), two alert channels, one incident.
const (
	monShop   = "7d1c2f7e-6a1f-4f5e-9b8a-1f7c2b3d4e5f"
	monCert   = "2b3c4d5e-6f70-4a81-92a3-b4c5d6e7f809"
	monDomain = "9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d"
	monHook   = "0c2d4f6a-8b1e-4c3d-9e5f-7a6b5c4d3e2f"
	monPhone  = "5e6f7a8b-9c0d-4e1f-8a2b-3c4d5e6f7a8b"
	monInc    = "3f2c8a8e-6c1d-4a59-9a43-2b1f0f6f9d10"
	monUser   = "11111111-2222-4333-8444-555555555555"

	// A published status page with a draft beside it, a monitor component,
	// a manual one in a group, and one open posted incident.
	spAcme    = "6e0f4b1a-2c3d-4e5f-8a9b-0c1d2e3f4a5b"
	spDraft   = "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
	spSite    = "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5e"
	spDesk    = "b2c3d4e5-f6a7-4b8c-9d0e-1f2a3b4c5d6f"
	spGroup   = "c3d4e5f6-a7b8-4c9d-8e1f-2a3b4c5d6e70"
	spPost    = "8c7d6e5f-4a3b-4c2d-9e1f-0a1b2c3d4e5f"
	spSub     = "3f2a1b0c-9d8e-4f7a-b6c5-d4e3f2a1b0c9"
	spPostURL = "https://acme.norcubestatus.com/incidents/" + spPost
)

var (
	monShopJSON = `{"id":"` + monShop + `","name":"Shop","description":"","type":"http","variant":"keyword","target":"https://shop.example.com/health",
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
		"createdAt":"2026-10-01T09:00:00Z","createdBy":"` + monUser + `","updatedAt":"2026-10-01T09:00:00Z"}`
	monCertJSON = `{"id":"` + monCert + `","name":"API cert","description":"","type":"ssl","variant":"ssl","target":"api.example.com:443",
		"intervalSeconds":3600,"status":"warning","overallStatus":"warning","statusSince":"2026-11-01T00:00:00Z",
		"condition":{"code":"cert_expiring","message":"Certificate expires in 12 days","since":"2026-11-01T00:00:00Z","facts":{"daysRemaining":12,"thresholdDays":14,"expiresAt":"2026-11-15T12:00:00Z"},"alertedThresholds":[{"days":14,"alertedAt":"2026-11-01T00:00:00Z"}]},
		"lastCheck":{"at":"2026-11-03T08:00:00Z","severity":"warning","condition":"cert_expiring","totalMs":95},
		"aspects":[],"regions":[{"code":"eu-central-1","name":"Frankfurt"}],
		"ssl":{"host":"api.example.com","port":443,"expiryThresholdsDays":[30,14,7]},
		"createdAt":"2026-10-02T09:00:00Z","createdBy":"` + monUser + `","updatedAt":"2026-10-02T09:00:00Z"}`
	monDomainJSON = `{"id":"` + monDomain + `","name":"example.com","description":"Company domain","type":"domain","variant":"domain","target":"example.com",
		"intervalSeconds":43200,"status":"ok","overallStatus":"ok","statusSince":"2026-10-03T09:00:00Z",
		"condition":{"code":"ok","message":"Registered until 2027-08-01","since":"2026-10-03T09:00:00Z","facts":{"daysRemaining":271},"alertedThresholds":[]},
		"lastCheck":{"at":"2026-11-03T06:00:00Z","severity":"ok","condition":"ok","totalMs":420},
		"aspects":[],"regions":[{"code":"eu-central-1","name":"Frankfurt"}],
		"domain":{"domain":"example.com","expiryThresholdsDays":[60,30]},
		"createdAt":"2026-10-03T09:00:00Z","createdBy":"` + monUser + `","updatedAt":"2026-10-03T09:00:00Z"}`
	monIncidentJSON = `"id":"` + monInc + `","monitorId":"` + monShop + `","monitorName":"Shop","monitorType":"http","monitorVariant":"keyword",
		"monitorTarget":"https://shop.example.com/health","aspect":"availability","title":"Shop is down","status":"open","severity":"critical",
		"peakSeverity":"critical","condition":"unexpected_status","summary":"HTTP 503","facts":{"statusCode":503},
		"openedAt":"2026-11-03T08:01:00Z","durationSeconds":540`
	monHookJSON = `{"id":"` + monHook + `","kind":"webhook","name":"On-call webhook","isDefault":true,"enabled":true,"verified":true,
		"settings":{"url":"https://hooks.example.com/monitor"},"createdAt":"2026-10-01T09:00:00Z","updatedAt":"2026-10-01T09:00:00Z"}`
	monPhoneJSON = `{"id":"` + monPhone + `","kind":"sms","name":"On-call phone","isDefault":false,"enabled":true,"verified":false,
		"settings":{"phone":"+420601123456"},"createdAt":"2026-10-05T09:00:00Z","updatedAt":"2026-10-05T09:00:00Z"}`
)

var (
	spSettingsJSON = `"settings":{"timeZone":"Europe/Prague","uptimeDays":90,"showUptimeBars":true,"showUptimePercent":true,"showResponseTime":false,"historyDays":90,"searchIndexing":true,"autoPost":false}`
	spAcmeJSON     = `{"id":"` + spAcme + `","slug":"acme","name":"Acme status","description":"Our services","url":"https://acme.norcubestatus.com","published":true,"publishedAt":"2026-11-02T09:00:00Z",` +
		spSettingsJSON + `,"createdAt":"2026-11-01T09:00:00Z","updatedAt":"2026-11-02T09:00:00Z"}`
	spDraftJSON = `{"id":"` + spDraft + `","slug":"draft-page","name":"Draft","description":"","url":"https://draft-page.norcubestatus.com","published":false,` +
		spSettingsJSON + `,"createdAt":"2026-10-30T09:00:00Z","updatedAt":"2026-10-30T09:00:00Z"}`
	spLayoutJSON = `{"components":[{"id":"` + spSite + `","kind":"monitor","monitorId":"` + monShop + `","aspects":["availability","certificate"],"name":"Website"}],
		"groups":[{"id":"` + spGroup + `","name":"Support","collapsed":false,"components":[{"id":"` + spDesk + `","kind":"manual","manualStatus":"operational","name":"Support desk"}]}]}`
	spPostJSON = `{"id":"` + spPost + `","statusPageId":"` + spAcme + `","title":"Elevated API errors","status":"investigating","impact":"minor","startedAt":"2026-11-03T08:00:00Z",
		"components":[{"componentId":"` + spSite + `","status":"partial_outage"}],"monitorIncidentIds":["` + monInc + `"],
		"updates":[{"id":"d4e5f6a7-b8c9-4d0e-8f1a-2b3c4d5e6f70","status":"investigating","body":"We are looking into it.","createdAt":"2026-11-03T08:05:00Z"}],
		"createdAt":"2026-11-03T08:05:00Z","updatedAt":"2026-11-03T08:05:00Z"}`
	spSubConfirmedJSON = `{"id":"` + spSub + `","email":"jan@example.com","locale":"cs","status":"confirmed","componentIds":[],"createdAt":"2026-11-03T09:00:00Z","confirmedAt":"2026-11-03T09:05:00Z"}`
	spSubPendingJSON   = `{"id":"e5f6a7b8-c9d0-4e1f-8a2b-3c4d5e6f7a81","email":"eva@example.com","locale":"en","status":"pending","componentIds":["` + spSite + `","` + spDesk + `"],"createdAt":"2026-11-04T09:00:00Z"}`
)

func monList(items ...string) string {
	return `{"list":[` + strings.Join(items, ",") + `],"cursors":{},"meta":{}}`
}

func init() {
	for k, v := range map[string]string{
		"GET /monitors":              monList(monShopJSON, monCertJSON, monDomainJSON),
		"GET /monitors/" + monShop:   monShopJSON,
		"GET /monitors/" + monCert:   monCertJSON,
		"GET /monitors/" + monDomain: monDomainJSON,
		"GET /monitors/" + monShop + "/results": monList(
			`{"id":9002,"checkedAt":"2026-11-03T08:10:00Z","severity":"critical","condition":"unexpected_status","statusCode":503,"errorMessage":"HTTP 503","region":"eu-central-1","timings":{"dnsMs":4,"connectMs":12,"tlsMs":30,"ttfbMs":170,"totalMs":180}}`,
			`{"id":9001,"checkedAt":"2026-11-03T07:59:00Z","severity":"ok","condition":"ok","statusCode":200,"region":"eu-central-1","timings":{"dnsMs":3,"connectMs":11,"tlsMs":28,"ttfbMs":100,"totalMs":110}}`),
		"GET /monitors/" + monShop + "/status-changes": monList(
			`{"id":2,"from":"ok","to":"critical","changedAt":"2026-11-03T08:00:00Z","durationSeconds":600,"condition":"unexpected_status","statusCode":503,"message":"HTTP 503","checkResultId":9000,"events":[]}`,
			`{"id":1,"from":"pending","to":"ok","changedAt":"2026-10-01T09:01:00Z","endedAt":"2026-11-03T08:00:00Z","durationSeconds":2847540,"condition":"ok","checkResultId":1,"events":[]}`),
		"GET /monitors/" + monShop + "/stats": `{"from":"2026-11-02T08:00:00Z","to":"2026-11-03T08:10:00Z","granularity":"hour","checks":1440,"ok":1420,"warning":0,"critical":20,"inconclusive":0,"uptimePercent":98.61,"avgMs":120,"maxMs":980,"series":[]}`,
		"GET /monitors/" + monShop + "/availability": `{"from":"2026-11-01","to":"2026-11-03","checks":4300,"ok":4260,"warning":0,"critical":40,"inconclusive":0,"uptimePercent":99.07,"downtimeMinutes":40,"days":[
			{"date":"2026-11-01","checks":1440,"ok":1440,"warning":0,"critical":0,"inconclusive":0,"uptimePercent":100,"downtimeMinutes":0,"noData":false},
			{"date":"2026-11-02","checks":1440,"ok":1420,"warning":0,"critical":20,"inconclusive":0,"uptimePercent":98.61,"downtimeMinutes":20,"noData":false},
			{"date":"2026-11-03","checks":0,"ok":0,"warning":0,"critical":0,"inconclusive":0,"downtimeMinutes":0,"noData":true}]}`,
		"GET /monitors/" + monCert + "/certificate": `{"resultId":77,"checkedAt":"2026-11-03T08:00:00Z","severity":"warning","condition":"cert_expiring","daysRemaining":12,
			"thresholdsDays":[30,14,7],"nextThresholdAt":"2026-11-08T12:00:00Z","nextThresholdDays":7,
			"certificate":{"subject":"CN=api.example.com","issuer":"CN=Example CA","dnsNames":["api.example.com"],"notBefore":"2026-08-17T12:00:00Z","notAfter":"2026-11-15T12:00:00Z",
				"daysRemaining":12,"chainValid":true,"hostnameValid":true,"serialNumber":"04ab","fingerprintSha256":"aa:bb"}}`,
		"GET /monitors/" + monShop + "/alerts": `{"useDefaults":false,"confirmFailures":2,"confirmSuccesses":1,"reminderMinutes":60,
			"routes":[{"channelId":"` + monHook + `","events":["down","recovered"]}],"applicableEvents":["down","warning","expiring","expired","recovered"]}`,
		"GET /incidents": monList(`{` + monIncidentJSON + `}`),
		"GET /incidents/" + monInc: `{` + monIncidentJSON + `,"timeline":[{"type":"opened","at":"2026-11-03T08:01:00Z"}],
			"alerts":[{"alertId":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","queuedAt":"2026-11-03T08:01:00Z","event":"down","channelId":"` + monHook + `","channelName":"On-call webhook","channelKind":"webhook","status":"sent","attempts":1,"sentAt":"2026-11-03T08:01:02Z"}]}`,
		"GET /incidents/summary":          `{"open":{"total":1,"critical":1,"warning":0,"unacknowledged":1},"resolvedLast7d":4,"resolvedLast30d":12,"mttrLast30dSeconds":1260}`,
		"GET /alert-channels":             monList(monHookJSON, monPhoneJSON),
		"GET /alert-channels/" + monHook:  monHookJSON,
		"GET /alert-channels/" + monPhone: monPhoneJSON,
		"GET /alert-channels/" + monHook + "/deliveries": monList(
			`{"id":"bbbbbbbb-cccc-4ddd-8eee-ffffffffffff","event":"down","monitorId":"` + monShop + `","monitorName":"Shop","incidentId":"` + monInc + `","status":"sent","attempts":1,"createdAt":"2026-11-03T08:01:00Z","sentAt":"2026-11-03T08:01:02Z"}`),
		"GET /status-pages":                                    monList(spAcmeJSON, spDraftJSON),
		"GET /status-pages/" + spAcme:                          spAcmeJSON,
		"GET /status-pages/" + spDraft:                         spDraftJSON,
		"GET /status-pages/" + spAcme + "/layout":              spLayoutJSON,
		"GET /status-pages/" + spAcme + "/incidents":           monList(spPostJSON),
		"GET /status-pages/" + spAcme + "/incidents/" + spPost: spPostJSON,
		"GET /status-pages/" + spAcme + "/subscribers":         monList(spSubConfirmedJSON, spSubPendingJSON),
	} {
		goldenData[k] = v
	}
}

func TestGoldenMonitor(t *testing.T) {
	commands := []struct {
		name string
		args []string
	}{
		{"monitor-status", []string{"monitor", "status"}},
		{"monitor-monitors-list", []string{"monitor", "monitors", "list"}},
		{"monitor-monitors-get", []string{"monitor", "monitors", "get", monShop}},
		{"monitor-monitors-get-ssl", []string{"monitor", "monitors", "get", monCert}},
		{"monitor-results-list", []string{"monitor", "monitors", "results", "list", monShop}},
		{"monitor-status-changes-list", []string{"monitor", "monitors", "status-changes", "list", monShop}},
		{"monitor-stats-get", []string{"monitor", "monitors", "stats", "get", monShop}},
		{"monitor-availability-get", []string{"monitor", "monitors", "availability", "get", monShop}},
		{"monitor-certificate-get", []string{"monitor", "monitors", "certificate", "get", monCert}},
		{"monitor-alerts-get", []string{"monitor", "monitors", "alerts", "get", monShop}},
		{"monitor-incidents-list", []string{"monitor", "incidents", "list"}},
		{"monitor-incidents-get", []string{"monitor", "incidents", "get", monInc}},
		{"monitor-incidents-summary", []string{"monitor", "incidents", "summary"}},
		{"monitor-channels-list", []string{"monitor", "channels", "list"}},
		{"monitor-channels-get", []string{"monitor", "channels", "get", monHook}},
		{"monitor-deliveries-list", []string{"monitor", "channels", "deliveries", "list", monHook}},
		{"monitor-status-pages-list", []string{"monitor", "status-pages", "list"}},
		{"monitor-status-pages-get", []string{"monitor", "status-pages", "get", "acme"}},
		{"monitor-status-pages-layout-get", []string{"monitor", "status-pages", "layout", "get", spAcme}},
		{"monitor-status-pages-incidents-list", []string{"monitor", "status-pages", "incidents", "list", spAcme}},
		{"monitor-status-pages-incidents-get", []string{"monitor", "status-pages", "incidents", "get", spAcme, spPost}},
		{"monitor-status-pages-subscribers-list", []string{"monitor", "status-pages", "subscribers", "list", "acme"}},
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

// The machine-readable tree of the whole Norcube Monitor group is pinned: it
// is what scripts and agents read to discover the commands.
func TestGoldenCommandsMonitor(t *testing.T) {
	commandsEnv(t)
	out, stderr, err := run(t, "commands", "monitor", "-o", "json")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	checkGolden(t, "commands-monitor.json", out, "http://unused")
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

func TestMonitorMonitorCreate(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /monitors": rec.handler(201, monShopJSON)})

	out, stderr, err := runIn(t, "Authorization: Bearer s3cret\n", "monitor", "monitors", "create", "Shop", "--type", "keyword",
		"--url", "https://shop.example.com/health", "--keyword", "ok", "--header", "X-Env: prod",
		"--secret-headers-file", "-", "--slow", "1500ms", "-o", "json")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if !strings.Contains(out, `"id": "`+monShop+`"`) {
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
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /monitors": rec.handler(201, monCertJSON)})
	if _, stderr, err := run(t, "monitor", "monitors", "create", "API cert", "--type", "ssl", "--host", "api.example.com", "--alert-days", "21,7"); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if s := fmt.Sprint(rec.only(t).body["ssl"]); s != "map[expiryThresholdsDays:[21 7] host:api.example.com port:443]" {
		t.Errorf("ssl %s", s)
	}
	rec = recorder{}
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /monitors": rec.handler(201, monDomainJSON)})
	if _, stderr, err := runIn(t, `{"type":"domain","name":"x","intervalSeconds":86400,"domain":{"domain":"example.com","expiryThresholdsDays":[30]}}`,
		"monitor", "monitors", "create", "example.com", "--from-file", "-"); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if b := rec.only(t).body; b["name"] != "example.com" || b["intervalSeconds"] != float64(86400) {
		t.Errorf("from-file body %v", b)
	}
}

func TestMonitorUsageErrorsBeforeAnyRequest(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{
		"POST /monitors":       rec.handler(201, monShopJSON),
		"POST /alert-channels": rec.handler(201, monHookJSON),
	})
	cases := [][]string{
		{"monitor", "monitors", "create", "x"},
		{"monitor", "monitors", "create", "x", "--type", "tcp"},
		{"monitor", "monitors", "create", "x", "--type", "keyword", "--url", "https://e.com"},
		{"monitor", "monitors", "create", "x", "--type", "ssl", "--url", "https://e.com", "--host", "e.com"},
		{"monitor", "monitors", "create", "x", "--type", "http"},
		{"monitor", "monitors", "create", "x", "--type", "http", "--url", "https://e.com", "--interval", "1500ms"},
		{"monitor", "monitors", "create", "x", "--type", "http", "--url", "https://e.com", "--header", "no colon"},
		{"monitor", "monitors", "create", "--from-file", "-", "--type", "http"},
		{"monitor", "monitors", "list", "--status", "down"},
		{"monitor", "monitors", "run", monShop, "--fail-on", "warning"},
		{"monitor", "monitors", "update", monShop},
		{"monitor", "monitors", "results", "list", monShop, "--since", "yesterday"},
		{"monitor", "monitors", "alerts", "set", monShop},
		{"monitor", "monitors", "alerts", "set", monShop, "--defaults", "--mute"},
		{"monitor", "monitors", "alerts", "set", monShop, "--route", "hook"},
		{"monitor", "monitors", "alerts", "set", monShop, "--route", "hook=paged"},
		{"monitor", "monitors", "bulk", "pause"},
		{"monitor", "monitors", "bulk", "pause", monShop, "--status", "critical"},
		{"monitor", "incidents", "get", "not-a-uuid"},
		{"monitor", "incidents", "list", "--acknowledged", "--unacknowledged"},
		{"monitor", "channels", "create", "x"},
		{"monitor", "channels", "create", "x", "--kind", "slack", "--url", "https://hooks.slack.com/services/T/B/x"},
		{"monitor", "channels", "create", "x", "--kind", "sms", "--address", "a@example.com"},
		{"monitor", "channels", "create", "x", "--kind", "webhook", "--url", "https://e.com/h", "--secret-file", "-"},
		{"monitor", "status", "--fail-on", "sometimes"},
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
func TestMonitorMonitorUpdateMergesSettings(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{"PATCH /monitors/" + monShop: rec.handler(200, monShopJSON)})
	if _, stderr, err := run(t, "monitor", "monitors", "update", monShop, "--timeout", "5s", "--name", "Shop EU"); err != nil {
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
	if _, _, err := run(t, "monitor", "monitors", "update", monShop, "--host", "x.example.com"); exitOf(err) != clierr.ExitUsage {
		t.Errorf("ssl flag on an http monitor: %v", err)
	}
}

// Monitors are named by id or exact name: one match is used, none is a
// 404 (exit 3), several are a usage error.
func TestMonitorMonitorNames(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /monitors/" + monShop + "/pause": rec.handler(200, monShopJSON)})
	out, stderr, err := run(t, "monitor", "monitors", "pause", "shop")
	if err != nil || !strings.Contains(out, "Paused monitor Shop") {
		t.Fatalf("%v %q %s", err, out, stderr)
	}
	if _, _, err := run(t, "monitor", "monitors", "pause", "nope"); exitOf(err) != clierr.ExitNotFound {
		t.Errorf("unknown name: %v", err)
	}
	twice := strings.Replace(monCertJSON, `"name":"API cert"`, `"name":"Shop"`, 1)
	goldenHandlers(t, map[string]http.HandlerFunc{"GET /monitors": rec.handler(200, monList(monShopJSON, twice))})
	if _, _, err := run(t, "monitor", "monitors", "get", "Shop"); exitOf(err) != clierr.ExitUsage || !strings.Contains(err.Error(), monCert) {
		t.Errorf("ambiguous name: %v", err)
	}
}

func TestMonitorMonitorDeleteConfirms(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{"DELETE /monitors/" + monShop: rec.handler(204, "")})
	if _, _, err := run(t, "monitor", "monitors", "delete", monShop); exitOf(err) != clierr.ExitUsage || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("without a terminal: %v", err)
	}
	if rec.count() != 0 {
		t.Fatal("deleted without confirmation")
	}
	out, _, err := run(t, "monitor", "monitors", "delete", monShop, "--yes", "-o", "json")
	if err != nil || !strings.Contains(out, `"deleted": true`) {
		t.Fatalf("%v %s", err, out)
	}
	// A linked monitor: 409 MANAGED_RESOURCE exits 8.
	goldenHandlers(t, map[string]http.HandlerFunc{"DELETE /monitors/" + monShop: rec.handler(409,
		`{"type":"https://docs.norcube.com/errors/managed-resource","title":"Managed by another service","status":409,"detail":"This monitor belongs to a DomainRadar saved domain.","code":"MANAGED_RESOURCE"}`)})
	if _, _, err := run(t, "monitor", "monitors", "delete", monShop, "--yes"); exitOf(err) != clierr.ExitConflict {
		t.Errorf("managed monitor: %v", err)
	}
}

// run --wait turns the check's verdict into the exit code: a critical
// result is job_failed (exit 1) with the result in details and nothing
// on stdout; an ok one exits 0 and prints the result.
func TestMonitorMonitorRunWait(t *testing.T) {
	goldenEnv(t)
	down := `{"id":9003,"checkedAt":"2026-11-03T08:12:00Z","severity":"critical","condition":"unexpected_status","statusCode":503,"region":"eu-central-1","timings":{"totalMs":150}}`
	slow := strings.NewReplacer(`"critical"`, `"warning"`, "unexpected_status", "slow_response").Replace(down)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /monitors/" + monShop + "/check": rec.handler(200, down)})

	out, _, err := run(t, "monitor", "monitors", "run", monShop, "-o", "json")
	if err != nil || !strings.Contains(out, `"severity": "critical"`) {
		t.Fatalf("without --wait: %v %s", err, out)
	}
	out, _, err = run(t, "monitor", "monitors", "run", "Shop", "--wait", "-o", "json")
	e := Describe(err)
	if e.Code != clierr.CodeJobFailed || e.ExitCode() != clierr.ExitError || out != "" || e.Details == nil {
		t.Fatalf("critical with --wait: %+v stdout %q", e, out)
	}
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /monitors/" + monShop + "/check": rec.handler(200, slow)})
	if _, _, err := run(t, "monitor", "monitors", "run", monShop, "--wait"); err != nil {
		t.Errorf("a warning passes by default: %v", err)
	}
	if _, _, err := run(t, "monitor", "monitors", "run", monShop, "--wait", "--fail-on", "warning"); Describe(err).Code != clierr.CodeJobFailed {
		t.Errorf("--fail-on warning: %v", err)
	}
}

func TestMonitorBulk(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	ok := `{"action":"pause","succeeded":2,"failed":0,"items":[{"monitorId":"` + monShop + `","ok":true},{"monitorId":"` + monCert + `","ok":true}]}`
	goldenHandlers(t, map[string]http.HandlerFunc{
		"GET /monitors":       rec.handler(200, monList(monShopJSON, monCertJSON)),
		"POST /monitors/bulk": rec.handler(200, ok),
	})
	if _, _, err := run(t, "monitor", "monitors", "bulk", "pause", "--search", "example.com"); exitOf(err) != clierr.ExitUsage {
		t.Fatalf("bulk without --yes: %v", err)
	}
	rec = recorder{}
	goldenHandlers(t, map[string]http.HandlerFunc{
		"GET /monitors":       rec.handler(200, monList(monShopJSON, monCertJSON)),
		"POST /monitors/bulk": rec.handler(200, ok),
	})
	out, stderr, err := run(t, "monitor", "monitors", "bulk", "pause", "--search", "example.com", "--status", "critical", "--yes", "-o", "json")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if len(rec.reqs) != 2 || !strings.Contains(rec.reqs[0].query, "q=example.com") || !strings.Contains(rec.reqs[0].query, "status=critical") {
		t.Fatalf("requests %+v", rec.reqs)
	}
	if fmt.Sprint(rec.reqs[1].body) != "map[action:pause monitorIds:["+monShop+" "+monCert+"]]" || !strings.Contains(out, `"ok": true`) {
		t.Errorf("bulk body %v, out %s", rec.reqs[1].body, out)
	}
	partial := `{"action":"delete","succeeded":1,"failed":1,"items":[{"monitorId":"` + monShop + `","ok":true},{"monitorId":"` + monCert + `","ok":false,"code":"MANAGED_RESOURCE","detail":"Linked."}]}`
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /monitors/bulk": rec.handler(200, partial)})
	out, _, err = run(t, "monitor", "monitors", "bulk", "delete", monShop, monCert, "--yes", "-o", "text")
	if exitOf(err) != clierr.ExitError || !strings.Contains(out, "MANAGED_RESOURCE") {
		t.Errorf("partial failure: %v %q", err, out)
	}
}

func TestMonitorIncidents(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{"GET /incidents": rec.handler(200, monList())})
	if _, stderr, err := run(t, "monitor", "incidents", "list", "--status", "open", "--severity", "critical", "--aspect", "availability",
		"--unacknowledged", "--monitor", monShop, "--search", "shop", "--since", "2026-11-01T00:00:00Z", "-o", "json"); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	q := rec.only(t).query
	for _, want := range []string{"status=open", "severity=critical", "aspect=availability", "acknowledged=false", "monitorId=" + monShop, "q=shop", "from=2026-11-01T00%3A00%3A00Z", "limit=50"} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q lacks %s", q, want)
		}
	}
	acked := strings.Replace(goldenData["GET /incidents/"+monInc], `"durationSeconds":540`, `"durationSeconds":540,"acknowledgedAt":"2026-11-03T08:05:00Z","acknowledgedBy":"`+monUser+`","acknowledgedByName":"Dev Eloper"`, 1)
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /incidents/" + monInc + "/acknowledge": rec.handler(200, acked)})
	out, _, err := run(t, "monitor", "incidents", "ack", monInc)
	if err != nil || !strings.Contains(out, "Acknowledged incident") {
		t.Fatalf("%v %q", err, out)
	}
}

func TestMonitorAlertsSet(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{"PUT /monitors/" + monShop + "/alerts": rec.handler(200, goldenData["GET /monitors/"+monShop+"/alerts"])})
	if _, stderr, err := run(t, "monitor", "monitors", "alerts", "set", monShop, "--route", "on-call webhook=down,recovered", "--reminder", "30m"); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	b := rec.only(t).body
	if fmt.Sprint(b["routes"]) != "[map[channelId:"+monHook+" events:[down recovered]]]" || b["reminderMinutes"] != float64(30) ||
		b["confirmFailures"] != float64(2) || b["useDefaults"] != false {
		t.Errorf("body %v", b)
	}
	rec = recorder{}
	goldenHandlers(t, map[string]http.HandlerFunc{"PUT /monitors/" + monShop + "/alerts": rec.handler(200, goldenData["GET /monitors/"+monShop+"/alerts"])})
	if _, _, err := run(t, "monitor", "monitors", "alerts", "set", monShop, "--defaults"); err != nil || fmt.Sprint(rec.only(t).body) != "map[useDefaults:true]" {
		t.Errorf("--defaults: %v %v", err, rec.reqs)
	}
	rec = recorder{}
	goldenHandlers(t, map[string]http.HandlerFunc{"PUT /monitors/" + monShop + "/alerts": rec.handler(200, goldenData["GET /monitors/"+monShop+"/alerts"])})
	if _, _, err := run(t, "monitor", "monitors", "alerts", "set", monShop, "--mute"); err != nil || fmt.Sprint(rec.only(t).body["routes"]) != "[]" {
		t.Errorf("--mute: %v %v", err, rec.reqs)
	}
}

// A webhook's signing secret goes to the file named, never to stdout or
// stderr; Slack URLs come from a file or stdin.
func TestMonitorChannelCreateKeepsSecretsOffStdout(t *testing.T) {
	goldenEnv(t)
	const secret = "whsec_c2VjcmV0LXNpZ25pbmcta2V5" // #nosec G101 -- a test fixture, not a credential
	withSecret := strings.Replace(monHookJSON, `"isDefault":true`, `"isDefault":true,"signingSecret":"`+secret+`"`, 1)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /alert-channels": rec.handler(201, withSecret)})
	file := filepath.Join(t.TempDir(), "secret.txt")
	for _, format := range []string{"table", "json", "yaml", "text"} {
		_ = os.Remove(file)
		out, stderr, err := run(t, "monitor", "channels", "create", "On-call webhook", "--kind", "webhook",
			"--url", "https://hooks.example.com/monitor", "--secret-file", file, "-o", format)
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
	_, stderr, err := run(t, "monitor", "channels", "create", "Hook", "--kind", "webhook", "--url", "https://hooks.example.com/x")
	if err != nil || !strings.Contains(stderr, "--rotate-secret") || strings.Contains(stderr, secret) {
		t.Errorf("no --secret-file: %v %q", err, stderr)
	}

	rec = recorder{}
	slack := `{"id":"` + monHook + `","kind":"slack","name":"#alerts","isDefault":false,"enabled":true,"verified":true,"settings":{"hint":"hooks.slack.com/…x9z"},"createdAt":"2026-11-03T08:00:00Z","updatedAt":"2026-11-03T08:00:00Z"}`
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /alert-channels": rec.handler(201, slack)})
	out, stderr, err := runIn(t, "https://hooks.slack.com/services/T0/B0/x9z\n", "monitor", "channels", "create", "#alerts", "--kind", "slack", "--url-file", "-")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if b := rec.only(t).body; fmt.Sprint(b["settings"]) != "map[url:https://hooks.slack.com/services/T0/B0/x9z]" || strings.Contains(out, "services/T0") {
		t.Errorf("slack body %v out %q", b, out)
	}
}

func TestMonitorChannelVerify(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	verified := strings.Replace(monPhoneJSON, `"verified":false`, `"verified":true`, 1)
	goldenHandlers(t, map[string]http.HandlerFunc{
		"POST /alert-channels/" + monPhone + "/verify":         rec.handler(204, ""),
		"POST /alert-channels/" + monPhone + "/verify/confirm": rec.handler(200, verified),
	})
	// No terminal: the code is sent, and the command says how to confirm.
	out, stderr, err := run(t, "monitor", "channels", "verify", "On-call phone", "-o", "json")
	if err != nil || !strings.Contains(out, `"codeSent": true`) || !strings.Contains(stderr, "--code-file -") {
		t.Fatalf("send: %v %q %q", err, out, stderr)
	}
	out, _, err = runIn(t, "123456\n", "monitor", "channels", "verify", monPhone, "--code-file", "-", "-o", "json")
	if err != nil || !strings.Contains(out, `"verified": true`) || strings.Contains(out, "123456") {
		t.Fatalf("confirm: %v %q", err, out)
	}
	if rec.reqs[1].path != "/alert-channels/"+monPhone+"/verify/confirm" || rec.reqs[1].body["code"] != "123456" {
		t.Errorf("requests %+v", rec.reqs)
	}
	if _, _, err := runIn(t, "12ab\n", "monitor", "channels", "verify", monPhone, "--code-file", "-"); exitOf(err) != clierr.ExitUsage {
		t.Errorf("bad code: %v", err)
	}
	// An expired code: 409 CONFLICT, reason CODE_EXPIRED, exits 8 with the reason in details.
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /alert-channels/" + monPhone + "/verify/confirm": rec.handler(409,
		`{"title":"Conflict","status":409,"detail":"The code has expired. Send a new one.","code":"CONFLICT","reason":"CODE_EXPIRED"}`)})
	_, _, err = runIn(t, "123456", "monitor", "channels", "verify", monPhone, "--code-file", "-")
	if e := Describe(err); e.ExitCode() != clierr.ExitConflict || fmt.Sprint(e.Details) != "map[reason:CODE_EXPIRED]" {
		t.Errorf("expired: %+v", e)
	}
}

func TestMonitorChannelTestAndDelete(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{
		"POST /alert-channels/" + monPhone + "/test": rec.handler(200, `{"providerRef":"bg-123"}`),
		"POST /alert-channels/" + monHook + "/test":  rec.handler(200, `{"providerRef":"wh-1"}`),
		"DELETE /alert-channels/" + monHook:          rec.handler(204, ""),
	})
	if _, _, err := run(t, "monitor", "channels", "test", monPhone); exitOf(err) != clierr.ExitUsage {
		t.Fatalf("an SMS test needs --yes without a terminal: %v", err)
	}
	if out, _, err := run(t, "monitor", "channels", "test", monPhone, "--yes", "-o", "text"); err != nil || strings.TrimSpace(out) != "bg-123" {
		t.Fatalf("sms test: %v %q", err, out)
	}
	if _, _, err := run(t, "monitor", "channels", "test", "on-call webhook"); err != nil {
		t.Fatalf("webhook test: %v", err)
	}
	if _, _, err := run(t, "monitor", "channels", "delete", monHook, "--yes"); err != nil || rec.count() != 3 {
		t.Fatalf("delete: %v %d", err, rec.count())
	}
}

func TestMonitorStatusFailOn(t *testing.T) {
	goldenEnv(t)
	out, _, err := run(t, "monitor", "status", "--fail-on", "critical", "-o", "json")
	e := Describe(err)
	if e.Code != clierr.CodeUnhealthy || e.ExitCode() != clierr.ExitError || !strings.Contains(out, `"status": "critical"`) {
		t.Fatalf("critical incident open: %+v %q", e, out)
	}
	if _, _, err := run(t, "monitor", "status"); err != nil {
		t.Errorf("default --fail-on none: %v", err)
	}
	var rec recorder
	quiet := `{"open":{"total":0,"critical":0,"warning":0,"unacknowledged":0},"resolvedLast7d":0,"resolvedLast30d":0}`
	goldenHandlers(t, map[string]http.HandlerFunc{"GET /incidents/summary": rec.handler(200, quiet)})
	if _, _, err := run(t, "monitor", "status", "--fail-on", "warning"); err != nil {
		t.Errorf("nothing open: %v", err)
	}
}

// Norcube Monitor's RFC 9457 problems map to the CLI's codes: a scope a token
// lacks exits 5 (insufficient_scope), invalid fields exit 2 with the
// field errors in the message and details.
func TestMonitorProblems(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{
		"POST /monitors/" + monShop + "/pause": rec.handler(403,
			`{"title":"Forbidden","status":403,"detail":"This token can't change monitors or incidents: it needs the monitor.write scope.","code":"FORBIDDEN","reason":"INSUFFICIENT_SCOPE"}`),
		"POST /monitors": rec.handler(400,
			`{"title":"Validation failed","status":400,"detail":"The request has invalid fields.","code":"VALIDATION_FAILED","errors":[{"pointer":"/http/url","rule":"public","detail":"must be a public address"}]}`),
	})
	_, _, err := run(t, "monitor", "monitors", "pause", monShop)
	if e := Describe(err); e.Code != clierr.CodeInsufficientScope || e.ExitCode() != clierr.ExitAccess || !strings.Contains(e.Message, "monitor.write") {
		t.Errorf("scope: %+v", e)
	}
	// A token without monitor.channels (monitor.write doesn't include it).
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /alert-channels": rec.handler(403,
		`{"title":"Forbidden","status":403,"detail":"This token can't change alert channels or send test alerts: it needs the monitor.channels scope.","code":"FORBIDDEN","reason":"INSUFFICIENT_SCOPE"}`)})
	_, _, err = run(t, "monitor", "channels", "create", "Ops", "--kind", "email", "--address", "ops@example.com")
	if e := Describe(err); e.Code != clierr.CodeInsufficientScope || e.ExitCode() != clierr.ExitAccess || !strings.Contains(e.Message, "monitor.channels") {
		t.Errorf("channels scope: %+v", e)
	}
	_, _, err = run(t, "monitor", "monitors", "create", "x", "--type", "http", "--url", "https://10.0.0.1/")
	e := Describe(err)
	var ce *clierr.Error
	if e.Code != clierr.CodeValidation || e.ExitCode() != clierr.ExitUsage || !strings.Contains(e.Message, "/http/url: must be a public address") || !errors.As(err, &ce) {
		t.Errorf("validation: %+v", e)
	}
}

// Monitor, channel and incident ids complete from the API, on arguments
// and on --monitor.
func TestMonitorCompletion(t *testing.T) {
	goldenEnv(t)
	clearCompletionCache(t)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"monitor", "monitors", "get", ""}, monShop + "," + monCert + "," + monDomain},
		{[]string{"monitor", "monitors", "results", "list", "7d"}, monShop},
		{[]string{"monitor", "monitors", "bulk", "pause", monShop, "2b"}, monCert},
		{[]string{"monitor", "incidents", "list", "--monitor", "9a"}, monDomain},
		{[]string{"monitor", "channels", "verify", "5e"}, monPhone},
		{[]string{"monitor", "incidents", "ack", ""}, monInc},
		{[]string{"monitor", "monitors", "list", "--status", ""}, "pending,ok,warning,critical,paused"},
		{[]string{"monitor", "status-pages", "get", ""}, "acme,draft-page"},
		{[]string{"monitor", "status-pages", "layout", "set", "dr"}, "draft-page"},
		{[]string{"monitor", "status-pages", "incidents", "get", "ac"}, "acme"},
		{[]string{"monitor", "status-pages", "incidents", "get", "acme", "8c"}, spPost},
		{[]string{"monitor", "status-pages", "incidents", "updates", "create", "acme", "8c"}, spPost},
		{[]string{"monitor", "status-pages", "create", "x", "--uptime-days", ""}, "30,60,90"},
		{[]string{"monitor", "status-pages", "subscribers", "list", "ac"}, "acme"},
		{[]string{"monitor", "status-pages", "subscribers", "delete", "ac"}, "acme"},
		// Addresses stay out of the on-disk completion cache.
		{[]string{"monitor", "status-pages", "subscribers", "delete", "acme", ""}, ""},
	}
	for _, c := range cases {
		got, _ := complete(t, c.args...)
		if strings.Join(got, ",") != c.want {
			t.Errorf("%v: %v, want %s", c.args, got, c.want)
		}
	}
}

// The remaining commands send what their flags say.
func TestMonitorRequests(t *testing.T) {
	goldenEnv(t)
	secretFile := filepath.Join(t.TempDir(), "rotated.txt")
	rotated := strings.Replace(monHookJSON, `"isDefault":true`, `"isDefault":false,"signingSecret":"whsec_bmV3LXNlY3JldA"`, 1)
	cases := []struct {
		name      string
		args      []string
		route     string
		status    int
		answer    string
		wantQuery string
		wantBody  string
	}{
		{"resume", []string{"monitors", "resume", "API cert"}, "POST /monitors/" + monCert + "/resume", 200, monCertJSON, "", ""},
		{"results range", []string{"monitors", "results", "list", monShop, "--since", "2026-11-01T00:00:00Z", "--until", "2026-11-02T00:00:00Z", "--limit", "10"},
			"GET /monitors/" + monShop + "/results", 200, monList(), "from=2026-11-01T00%3A00%3A00Z&limit=10&to=2026-11-02T00%3A00%3A00Z", ""},
		{"stats range", []string{"monitors", "stats", "get", monShop, "--range", "30d"}, "GET /monitors/" + monShop + "/stats", 200,
			goldenData["GET /monitors/"+monShop+"/stats"], "range=30d", ""},
		{"availability days", []string{"monitors", "availability", "get", monShop, "--days", "7"}, "GET /monitors/" + monShop + "/availability", 200,
			goldenData["GET /monitors/"+monShop+"/availability"], "days=7", ""},
		{"list filters", []string{"monitors", "list", "--type", "ssl", "--status", "warning", "--sort", "uptime", "--search", "api", "--history"},
			"GET /monitors", 200, monList(monCertJSON), "include=history&limit=50&q=api&sort=uptime&status=warning&type=ssl", ""},
		{"deliveries status", []string{"channels", "deliveries", "list", monHook, "--status", "dead"}, "GET /alert-channels/" + monHook + "/deliveries", 200,
			monList(), "limit=50&status=dead", ""},
		{"channel update", []string{"channels", "update", monHook, "--name", "Pager", "--default=false", "--disable", "--rotate-secret", "--secret-file", secretFile},
			"PATCH /alert-channels/" + monHook, 200, rotated, "", "map[enabled:false isDefault:false name:Pager rotateSecret:true]"},
		{"sms create", []string{"channels", "create", "Phone", "--kind", "sms", "--phone", "+420601123456"}, "POST /alert-channels", 201, monPhoneJSON, "",
			"map[kind:sms name:Phone settings:map[phone:+420601123456]]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var rec recorder
			goldenHandlers(t, map[string]http.HandlerFunc{c.route: rec.handler(c.status, c.answer)})
			out, stderr, err := run(t, append(append([]string{"monitor"}, c.args...), "-o", "json")...)
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

// A page is created with every setting spelled out: nothing is filled in,
// and a missing one is a usage error before any request.
func TestMonitorStatusPageCreate(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /status-pages": rec.handler(201, spDraftJSON)})

	settings := []string{"--slug", "draft-page", "--time-zone", "Europe/Prague", "--uptime-days", "60", "--history-days", "30",
		"--show-uptime-bars", "--show-uptime-percent=false", "--show-response-time=false", "--search-indexing=false"}

	out, stderr, err := run(t, append([]string{"monitor", "status-pages", "create", "Draft", "--description", "Ours", "-o", "json"}, settings...)...)
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if !strings.Contains(out, `"id": "`+spDraft+`"`) {
		t.Errorf("stdout: %s", out)
	}
	want := "map[description:Ours name:Draft settings:map[historyDays:30 searchIndexing:false showResponseTime:false showUptimeBars:true showUptimePercent:false timeZone:Europe/Prague uptimeDays:60] slug:draft-page]"
	if got := fmt.Sprint(rec.only(t).body); got != want {
		t.Errorf("body %s, want %s", got, want)
	}

	// --auto-post is the one setting create doesn't require, and goes only when given.
	rec = recorder{}
	if _, stderr, err := run(t, append([]string{"monitor", "status-pages", "create", "Draft", "--auto-post"}, settings...)...); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if got := fmt.Sprint(rec.only(t).body["settings"]); !strings.Contains(got, "autoPost:true") {
		t.Errorf("settings %s", got)
	}

	rec = recorder{}
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"a missing setting", append([]string{"Draft"}, settings[:len(settings)-1]...), "search-indexing"},
		{"no name", settings, "accepts 1 arg"},
		{"bad range", append(append([]string{"Draft"}, settings...), "--uptime-days", "45"), "--uptime-days"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := run(t, append([]string{"monitor", "status-pages", "create"}, c.args...)...)
			if err == nil || exitOf(err) != clierr.ExitUsage || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err %v (exit %d), want usage error about %s", err, exitOf(err), c.want)
			}
		})
	}
	badRange := append(append([]string{"monitor", "status-pages", "create", "Draft"}, settings...), "--history-days", "3")
	if _, _, err := run(t, badRange...); err == nil || exitOf(err) != clierr.ExitUsage {
		t.Errorf("--history-days 3: %v", err)
	}
	if rec.count() != 0 {
		t.Errorf("%d requests were sent", rec.count())
	}
}

// Pages are named by id or exact slug: a slug that matches nothing exits 3,
// without guessing from a partial match.
func TestMonitorStatusPageNames(t *testing.T) {
	goldenEnv(t)
	if _, _, err := run(t, "monitor", "status-pages", "get", "acm"); err == nil || exitOf(err) != clierr.ExitNotFound {
		t.Errorf("partial slug: %v", err)
	}
	out, _, err := run(t, "monitor", "status-pages", "get", "draft-page", "-o", "json", "--query", "id")
	if err != nil || !strings.Contains(out, spDraft) {
		t.Errorf("%v %q", err, out)
	}
}

func TestMonitorStatusPageWrites(t *testing.T) {
	goldenEnv(t)
	cases := []struct {
		name      string
		args      []string
		route     string
		status    int
		answer    string
		wantQuery string
		wantBody  string
		wantOut   string
	}{
		{"update slug and setting", []string{"update", "acme", "--slug", "acme-inc", "--show-uptime-bars=false", "--history-days", "7"},
			"PATCH /status-pages/" + spAcme, 200, spAcmeJSON, "", "map[settings:map[historyDays:7 showUptimeBars:false] slug:acme-inc]", "Public within a minute at https://acme.norcubestatus.com."},
		{"auto-post on", []string{"update", "acme", "--auto-post"}, "PATCH /status-pages/" + spAcme, 200, spAcmeJSON, "", "map[settings:map[autoPost:true]]", "Updated status page acme"},
		{"auto-post off", []string{"update", "acme", "--auto-post=false"}, "PATCH /status-pages/" + spAcme, 200, spAcmeJSON, "", "map[settings:map[autoPost:false]]", "Updated status page acme"},
		{"update name only", []string{"update", spDraft, "--name", " New "},
			"PATCH /status-pages/" + spDraft, 200, spDraftJSON, "", "map[name:New]", "Updated status page draft-page"},
		{"publish", []string{"publish", "acme"}, "POST /status-pages/" + spAcme + "/publish", 200, spAcmeJSON, "", "", "public within a minute at https://acme.norcubestatus.com"},
		{"unpublish", []string{"unpublish", "acme", "--yes"}, "POST /status-pages/" + spAcme + "/unpublish", 200, spDraftJSON, "", "", "Unpublished status page"},
		{"preview link", []string{"preview-link", "acme"}, "POST /status-pages/" + spAcme + "/preview-link", 200,
			`{"url":"https://preview.norcubestatus.com/__preview/token","expiresAt":"2026-11-03T08:15:00Z"}`, "", "", "https://preview.norcubestatus.com/__preview/token"},
		{"list by monitor", []string{"list", "--monitor", monShop}, "GET /status-pages", 200, monList(spAcmeJSON), "limit=50&monitorId=" + monShop, "", "acme"},
		{"incidents open", []string{"incidents", "list", "acme", "--status", "open"}, "GET /status-pages/" + spAcme + "/incidents", 200, monList(), "limit=50&status=open", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var rec recorder
			goldenHandlers(t, map[string]http.HandlerFunc{c.route: rec.handler(c.status, c.answer)})
			out, stderr, err := run(t, append([]string{"monitor", "status-pages"}, c.args...)...)
			if err != nil {
				t.Fatalf("%v\n%s", err, stderr)
			}
			r := rec.only(t)
			if r.query != c.wantQuery {
				t.Errorf("query %q, want %q", r.query, c.wantQuery)
			}
			if c.wantBody != "" && fmt.Sprint(r.body) != c.wantBody {
				t.Errorf("body %v, want %s", r.body, c.wantBody)
			}
			if !strings.Contains(out, c.wantOut) {
				t.Errorf("stdout %q lacks %q", out, c.wantOut)
			}
		})
	}
	if _, _, err := run(t, "monitor", "status-pages", "update", "acme"); err == nil || exitOf(err) != clierr.ExitUsage {
		t.Errorf("nothing to change: %v", err)
	}
}

// Taking a page down asks first; without a terminal it fails and names
// --yes, before anything is changed.
func TestMonitorStatusPageConfirms(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{
		"DELETE /status-pages/" + spAcme:              rec.handler(204, ""),
		"POST /status-pages/" + spAcme + "/unpublish": rec.handler(200, spDraftJSON),
	})
	for _, verb := range []string{"delete", "unpublish"} {
		_, _, err := run(t, "monitor", "status-pages", verb, "acme")
		if err == nil || exitOf(err) != clierr.ExitUsage || !strings.Contains(err.Error(), "--yes") {
			t.Errorf("%s without --yes: %v", verb, err)
		}
	}
	if rec.count() != 0 {
		t.Fatalf("%d requests without --yes", rec.count())
	}
	out, stderr, err := run(t, "monitor", "status-pages", "delete", "acme", "--yes", "-o", "json")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if !strings.Contains(out, `"deleted": true`) || !strings.Contains(out, spAcme) {
		t.Errorf("stdout: %s", out)
	}
}

// layout set sends the document as it is and prints the saved layout on
// stdout, with the note about publishing on stderr.
func TestMonitorStatusPageLayoutSet(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{"PUT /status-pages/" + spAcme + "/layout": rec.handler(200, spLayoutJSON)})

	doc := `{"components":[{"kind":"manual","manualStatus":"operational","name":"Support desk"}],"groups":[]}`
	path := filepath.Join(t.TempDir(), "layout.json")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	out, stderr, err := run(t, "monitor", "status-pages", "layout", "set", "acme", "--file", path, "-o", "json")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	r := rec.only(t)
	if got := fmt.Sprint(r.body); got != "map[components:[map[kind:manual manualStatus:operational name:Support desk]] groups:[]]" {
		t.Errorf("body %s", got)
	}
	if !strings.Contains(out, spSite) || strings.Contains(out, "public within") {
		t.Errorf("stdout carries the saved layout only: %s", out)
	}
	if !strings.Contains(stderr, "public within a minute") {
		t.Errorf("stderr %q", stderr)
	}

	// Standard input, and what `layout get -o json` prints goes back as is.
	rec = recorder{}
	goldenHandlers(t, map[string]http.HandlerFunc{"PUT /status-pages/" + spAcme + "/layout": rec.handler(200, spLayoutJSON)})
	got, _, err := run(t, "monitor", "status-pages", "layout", "get", "acme", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if _, stderr, err := runIn(t, got, "monitor", "status-pages", "layout", "set", "acme", "--file", "-"); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if s := fmt.Sprint(rec.only(t).body); !strings.Contains(s, spSite) || !strings.Contains(s, spGroup) {
		t.Errorf("round trip lost ids: %s", s)
	}

	// Mistakes never reach the API.
	rec = recorder{}
	for name, bad := range map[string]string{
		"not json":       `{`,
		"unknown member": `{"components":[],"groups":[],"extra":1}`,
		"no groups":      `{"components":[]}`,
		"group without":  `{"components":[],"groups":[{"name":"API"}]}`,
	} {
		if _, _, err := runIn(t, bad, "monitor", "status-pages", "layout", "set", "acme", "--file", "-"); err == nil || exitOf(err) != clierr.ExitUsage {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, _, err := run(t, "monitor", "status-pages", "layout", "set", "acme"); err == nil || exitOf(err) != clierr.ExitUsage {
		t.Errorf("no --file: %v", err)
	}
	if rec.count() != 0 {
		t.Errorf("%d requests were sent", rec.count())
	}
}

func TestMonitorStatusPageIncidentCreate(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /status-pages/" + spAcme + "/incidents": rec.handler(201, spPostJSON)})

	// A component by public name is looked up in the layout; stdout says
	// where the public reads it.
	out, stderr, err := runIn(t, "We are looking into it.\n", "monitor", "status-pages", "incidents", "create", "acme",
		"--title", "Elevated API errors", "--status", "investigating", "--impact", "minor", "--message-file", "-",
		"--component", "website=partial_outage", "--component", spDesk+"=operational", "--monitor-incident", monInc,
		"--started-at", "2026-11-03T08:00:00Z")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if !strings.Contains(out, spPostURL) || !strings.Contains(out, "Public within a minute") {
		t.Errorf("stdout: %s", out)
	}
	b := rec.only(t).body
	if b["title"] != "Elevated API errors" || b["status"] != "investigating" || b["impact"] != "minor" ||
		b["body"] != "We are looking into it." || b["startedAt"] != "2026-11-03T08:00:00Z" {
		t.Errorf("body %v", b)
	}
	want := "[map[componentId:" + spSite + " status:partial_outage] map[componentId:" + spDesk + " status:operational]]"
	if got := fmt.Sprint(b["components"]); got != want {
		t.Errorf("components %s, want %s", got, want)
	}
	if got := fmt.Sprint(b["monitorIncidentIds"]); got != "["+monInc+"]" {
		t.Errorf("links %s", got)
	}

	// On a draft the post says nobody sees it yet.
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /status-pages/" + spDraft + "/incidents": (&recorder{}).handler(201, spPostJSON)})
	out, _, err = run(t, "monitor", "status-pages", "incidents", "create", spDraft, "--title", "T", "--status", "resolved", "--impact", "none", "--message", "m")
	if err != nil || !strings.Contains(out, "isn't published") || strings.Contains(out, "/incidents/") {
		t.Errorf("%v %q", err, out)
	}

	// --notify is sent only when given, and the line says who hears of it.
	var notified recorder
	goldenHandlers(t, map[string]http.HandlerFunc{
		"POST /status-pages/" + spAcme + "/incidents":  notified.handler(201, spPostJSON),
		"POST /status-pages/" + spDraft + "/incidents": notified.handler(201, spPostJSON),
	})
	out, _, err = run(t, "monitor", "status-pages", "incidents", "create", "acme", "--title", "T", "--status", "resolved", "--impact", "none", "--message", "m", "--notify")
	if err != nil || !strings.Contains(out, "Subscribers get it by email") {
		t.Errorf("%v %q", err, out)
	}
	if got := notified.only(t).body["notify"]; got != true {
		t.Errorf("notify %v", got)
	}
	notified = recorder{}
	out, _, err = run(t, "monitor", "status-pages", "incidents", "create", spDraft, "--title", "T", "--status", "resolved", "--impact", "none", "--message", "m", "--notify")
	if err != nil || !strings.Contains(out, "No email goes out") {
		t.Errorf("%v %q", err, out)
	}
	notified = recorder{}
	out, _, err = run(t, "monitor", "status-pages", "incidents", "create", "acme", "--title", "T", "--status", "resolved", "--impact", "none", "--message", "m")
	if err != nil || strings.Contains(out, "email") {
		t.Errorf("%v %q", err, out)
	}
	if _, sent := notified.only(t).body["notify"]; sent {
		t.Error("notify sent without --notify")
	}
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /status-pages/" + spAcme + "/incidents": rec.handler(201, spPostJSON)})

	// Mistakes never reach the API.
	rec = recorder{}
	base := []string{"monitor", "status-pages", "incidents", "create", "acme"}
	ok := []string{"--title", "T", "--status", "monitoring", "--impact", "minor", "--message", "m"}
	for name, args := range map[string][]string{
		"no message":       {"--title", "T", "--status", "monitoring", "--impact", "minor"},
		"no title":         {"--status", "monitoring", "--impact", "minor", "--message", "m"},
		"bad status":       {"--title", "T", "--status", "fixed", "--impact", "minor", "--message", "m"},
		"bad impact":       {"--title", "T", "--status", "monitoring", "--impact", "huge", "--message", "m"},
		"blank message":    {"--title", "T", "--status", "monitoring", "--impact", "minor", "--message", "  "},
		"two messages":     append(append([]string{}, ok...), "--message-file", "-"),
		"component syntax": append(append([]string{}, ok...), "--component", "Website"),
		"component status": append(append([]string{}, ok...), "--component", "Website=unknown"),
		"bad link":         append(append([]string{}, ok...), "--monitor-incident", "nope"),
		"bad time":         append(append([]string{}, ok...), "--started-at", "soon"),
	} {
		if _, _, err := run(t, append(append([]string{}, base...), args...)...); err == nil || exitOf(err) != clierr.ExitUsage {
			t.Errorf("%s: %v", name, err)
		}
	}
	if rec.count() != 0 {
		t.Errorf("%d requests were sent", rec.count())
	}

	// A component name that isn't on the page exits 3.
	if _, _, err := run(t, append(append(append([]string{}, base...), ok...), "--component", "Nothing=major_outage")...); err == nil || exitOf(err) != clierr.ExitNotFound {
		t.Errorf("unknown component: %v", err)
	}
}

func TestMonitorStatusPageIncidentUpdates(t *testing.T) {
	goldenEnv(t)
	route := "/status-pages/" + spAcme + "/incidents/" + spPost
	cases := []struct {
		name     string
		args     []string
		route    string
		status   int
		wantBody string
	}{
		{"correct", []string{"update", "acme", spPost, "--title", "New", "--impact", "major", "--started-at", "2026-11-03T07:00:00Z"},
			"PATCH " + route, 200, "map[impact:major startedAt:2026-11-03T07:00:00Z title:New]"},
		{"replace components", []string{"update", "acme", spPost, "--component", spSite + "=major_outage"},
			"PATCH " + route, 200, "map[components:[map[componentId:" + spSite + " status:major_outage]]]"},
		{"clear components and links", []string{"update", "acme", spPost, "--no-components", "--no-monitor-incidents"},
			"PATCH " + route, 200, "map[components:[] monitorIncidentIds:[]]"},
		{"post an update", []string{"updates", "create", "acme", spPost, "--status", "monitoring", "--message", "A fix is rolling out."},
			"POST " + route + "/updates", 201, "map[body:A fix is rolling out. status:monitoring]"},
		{"post an update and email it", []string{"updates", "create", "acme", spPost, "--status", "monitoring", "--message", "m", "--notify"},
			"POST " + route + "/updates", 201, "map[body:m notify:true status:monitoring]"},
		{"resolve, clearing components", []string{"updates", "create", "acme", spPost, "--status", "resolved", "--message", "All clear.", "--no-components"},
			"POST " + route + "/updates", 201, "map[body:All clear. components:[] status:resolved]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var rec recorder
			goldenHandlers(t, map[string]http.HandlerFunc{c.route: rec.handler(c.status, spPostJSON)})
			out, stderr, err := run(t, append([]string{"monitor", "status-pages", "incidents"}, c.args...)...)
			if err != nil {
				t.Fatalf("%v\n%s", err, stderr)
			}
			if got := fmt.Sprint(rec.only(t).body); got != c.wantBody {
				t.Errorf("body %s, want %s", got, c.wantBody)
			}
			if !strings.Contains(out, spPostURL) {
				t.Errorf("stdout %q lacks the public address", out)
			}
		})
	}

	if _, _, err := run(t, "monitor", "status-pages", "incidents", "update", "acme", spPost); err == nil || exitOf(err) != clierr.ExitUsage {
		t.Errorf("nothing to change: %v", err)
	}
	// Corrections take no --notify: the API has no such field on them.
	if _, _, err := run(t, "monitor", "status-pages", "incidents", "update", "acme", spPost, "--title", "T", "--notify"); err == nil || exitOf(err) != clierr.ExitUsage {
		t.Errorf("update --notify: %v", err)
	}
	if _, _, err := run(t, "monitor", "status-pages", "incidents", "get", "acme", "nope"); err == nil || exitOf(err) != clierr.ExitUsage {
		t.Errorf("an incident is named by id: %v", err)
	}

	// A resolved post takes no more updates: the API's 409 exits 8.
	goldenHandlers(t, map[string]http.HandlerFunc{"POST " + route + "/updates": (&recorder{}).handler(409,
		`{"title":"Conflict","status":409,"detail":"This incident is resolved. Post a new one instead.","code":"CONFLICT","reason":"INCIDENT_RESOLVED"}`)})
	_, _, err := run(t, "monitor", "status-pages", "incidents", "updates", "create", "acme", spPost, "--status", "monitoring", "--message", "m")
	if e := Describe(err); e.Code != clierr.CodeConflict || e.ExitCode() != clierr.ExitConflict || !strings.Contains(e.Message, "resolved") {
		t.Errorf("resolved: %+v", e)
	}
}

// A token without monitor.statuspages exits 5 and is told which scope it
// lacks: monitor.write doesn't include it.
func TestMonitorStatusPageScope(t *testing.T) {
	goldenEnv(t)
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /status-pages/" + spAcme + "/publish": (&recorder{}).handler(403,
		`{"title":"Forbidden","status":403,"detail":"This token can't change status pages or post on them: it needs the monitor.statuspages scope.","code":"FORBIDDEN","reason":"INSUFFICIENT_SCOPE"}`)})
	_, _, err := run(t, "monitor", "status-pages", "publish", "acme")
	if e := Describe(err); e.Code != clierr.CodeInsufficientScope || e.ExitCode() != clierr.ExitAccess || !strings.Contains(e.Message, "monitor.statuspages") {
		t.Errorf("scope: %+v", e)
	}
}

// The help of every write that publishes says so.
func TestMonitorStatusPageHelpSaysWhatGoesPublic(t *testing.T) {
	goldenEnv(t)
	for _, path := range [][]string{
		{"publish"}, {"update"}, {"layout", "set"}, {"incidents", "create"}, {"incidents", "update"}, {"incidents", "updates", "create"},
	} {
		out, _, err := run(t, append(append([]string{"monitor", "status-pages"}, path...), "--help")...)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.ReplaceAll(out, "\n", " "), "public within a minute") {
			t.Errorf("%v help doesn't say it goes public: %s", path, out)
		}
	}
}

// --all-pages follows the API's cursor, and a page is named by slug or id.
func TestMonitorStatusPageSubscriberList(t *testing.T) {
	goldenEnv(t)
	var (
		mu      sync.Mutex
		queries []string
	)
	goldenHandlers(t, map[string]http.HandlerFunc{"GET /status-pages/" + spAcme + "/subscribers": func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.RawQuery)
		mu.Unlock()
		if r.URL.Query().Get("cursor") == "" {
			fmt.Fprint(w, `{"list":[`+spSubConfirmedJSON+`],"cursors":{"next":"abc"},"meta":{}}`)
			return
		}
		fmt.Fprint(w, monList(spSubPendingJSON))
	}})
	for _, page := range []string{"acme", spAcme} {
		mu.Lock()
		queries = nil
		mu.Unlock()
		out, stderr, err := run(t, "monitor", "status-pages", "subscribers", "list", page, "--all-pages", "-o", "json", "--query", "[].email")
		if err != nil {
			t.Fatalf("%v\n%s", err, stderr)
		}
		if !strings.Contains(out, "jan@example.com") || !strings.Contains(out, "eva@example.com") {
			t.Errorf("%s, stdout: %s", page, out)
		}
		mu.Lock()
		if len(queries) != 2 || !strings.Contains(queries[1], "cursor=abc") {
			t.Errorf("%s, queries: %v", page, queries)
		}
		mu.Unlock()
	}
}

func TestMonitorStatusPageSubscriberDelete(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	route := "DELETE /status-pages/" + spAcme + "/subscribers/" + spSub
	goldenHandlers(t, map[string]http.HandlerFunc{route: rec.handler(204, "")})
	base := []string{"monitor", "status-pages", "subscribers", "delete", "acme"}

	// Without a terminal it asks for --yes and sends nothing; so do mistakes.
	_, _, err := run(t, append(append([]string{}, base...), spSub)...)
	if err == nil || exitOf(err) != clierr.ExitUsage || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("without --yes: %v", err)
	}
	_, _, err = run(t, append(append([]string{}, base...), "jan@example.com", "--yes")...)
	if err == nil || exitOf(err) != clierr.ExitUsage || !strings.Contains(err.Error(), "subscriber id") {
		t.Errorf("not an id: %v", err)
	}
	if rec.count() != 0 {
		t.Fatalf("%d requests were sent", rec.count())
	}

	out, stderr, err := run(t, append(append([]string{}, base...), spSub, "--yes", "-o", "json")...)
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if !strings.Contains(out, `"deleted": true`) || !strings.Contains(out, spSub) {
		t.Errorf("stdout: %s", out)
	}
	if rec.count() != 1 {
		t.Errorf("%d requests", rec.count())
	}

	// A subscriber that isn't there exits 3.
	goldenHandlers(t, map[string]http.HandlerFunc{route: (&recorder{}).handler(404,
		`{"title":"Not Found","status":404,"detail":"Subscriber not found.","code":"NOT_FOUND"}`)})
	_, _, err = run(t, append(append([]string{}, base...), spSub, "--yes")...)
	if e := Describe(err); e.Code != clierr.CodeNotFound || e.ExitCode() != clierr.ExitNotFound {
		t.Errorf("not found: %+v", e)
	}
}

// Addresses are personal data: a token without monitor.statuspages exits 5 and
// is told what was refused and why, on the read as on the delete, and a person
// without the permission is told which one it takes.
func TestMonitorStatusPageSubscriberRefusals(t *testing.T) {
	goldenEnv(t)
	listRoute := "GET /status-pages/" + spAcme + "/subscribers"
	deleteRoute := "DELETE /status-pages/" + spAcme + "/subscribers/" + spSub
	scope := `{"title":"Forbidden","status":403,"detail":"This token can't change status pages or post on them: it needs the monitor.statuspages scope.","code":"FORBIDDEN","reason":"INSUFFICIENT_SCOPE"}`
	iam := `{"title":"Forbidden","status":403,"detail":"You don't have access to this in Norcube Monitor.","code":"FORBIDDEN"}`
	commands := map[string][]string{
		"list":   {"monitor", "status-pages", "subscribers", "list", "acme"},
		"delete": {"monitor", "status-pages", "subscribers", "delete", "acme", spSub, "--yes"},
	}
	for name, args := range commands {
		goldenHandlers(t, map[string]http.HandlerFunc{listRoute: (&recorder{}).handler(403, scope), deleteRoute: (&recorder{}).handler(403, scope)})
		_, _, err := run(t, args...)
		e := Describe(err)
		if e.Code != clierr.CodeInsufficientScope || e.ExitCode() != clierr.ExitAccess ||
			!strings.Contains(e.Message, "monitor.statuspages") || !strings.Contains(e.Message, "personal data") || strings.Contains(e.Message, "post on them") {
			t.Errorf("%s, scope: %+v", name, e)
		}

		goldenHandlers(t, map[string]http.HandlerFunc{listRoute: (&recorder{}).handler(403, iam), deleteRoute: (&recorder{}).handler(403, iam)})
		_, _, err = run(t, args...)
		e = Describe(err)
		if e.Code != clierr.CodePermissionDenied || e.ExitCode() != clierr.ExitAccess || !strings.Contains(e.Hint, "monitor:ManageStatusPageSubscribers") {
			t.Errorf("%s, permission: %+v", name, e)
		}
	}
}

// A post the system wrote says so, and whether it still runs it; a person's
// post shows no such line.
func TestMonitorStatusPageAutoPostShown(t *testing.T) {
	goldenEnv(t)
	route := "GET /status-pages/" + spAcme + "/incidents/" + spPost
	auto := func(managed string) string {
		return strings.Replace(spPostJSON, `"statusPageId"`, `"autoPosted":true,"autoManaged":`+managed+`,"statusPageId"`, 1)
	}
	args := []string{"monitor", "status-pages", "incidents", "get", "acme", spPost, "-o", "table"}
	for managed, want := range map[string]string{"true": "the system still runs it", "false": "a person has taken it over"} {
		goldenHandlers(t, map[string]http.HandlerFunc{route: (&recorder{}).handler(200, auto(managed))})
		out, stderr, err := run(t, args...)
		if err != nil {
			t.Fatalf("%v\n%s", err, stderr)
		}
		if !strings.Contains(out, "Posted automatically") || !strings.Contains(out, want) {
			t.Errorf("autoManaged %s: %s", managed, out)
		}
	}
	goldenHandlers(t, map[string]http.HandlerFunc{route: (&recorder{}).handler(200, spPostJSON)})
	out, _, err := run(t, args...)
	if err != nil || strings.Contains(out, "Posted automatically") {
		t.Errorf("%v %q", err, out)
	}
}
