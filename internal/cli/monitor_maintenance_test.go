package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/norcubeplatform/cli/internal/clierr"
)

// Maintenance window fixtures: one in progress holding Shop and showing on the
// published page, one scheduled with no public effect.
const (
	mwDeploy  = "4d3c2b1a-0f9e-4d8c-b7a6-5f4e3d2c1b0a"
	mwUpgrade = "5e4d3c2b-1a0f-4e9d-8c7b-6a5f4e3d2c1b"
)

var (
	mwDeployJSON = `{"id":"` + mwDeploy + `","state":"in_progress","title":"Deploy","description":"Short interruptions are expected.",
		"startsAt":"2026-11-03T08:00:00Z","endsAt":"2026-11-03T08:15:00Z","finishedEarly":false,
		"notifyScheduled":true,"notifyStarted":true,"notifyFinished":true,
		"targets":{"monitorIds":["` + monShop + `"],"componentIds":[]},
		"statusPages":[{"id":"` + spAcme + `","name":"Acme status","published":true,"componentIds":["` + spSite + `"]}],
		"createdBy":"` + monUser + `","createdByName":"Dev Owner","createdAt":"2026-11-03T08:00:00Z","updatedAt":"2026-11-03T08:00:00Z"}`
	mwUpgradeJSON = `{"id":"` + mwUpgrade + `","state":"scheduled","title":"Database upgrade","description":"",
		"startsAt":"2026-11-07T14:00:00Z","endsAt":"2026-11-07T16:00:00Z","finishedEarly":false,
		"notifyScheduled":false,"notifyStarted":false,"notifyFinished":false,
		"targets":{"monitorIds":["` + monCert + `"],"componentIds":["` + spDesk + `"]},
		"statusPages":[],"createdAt":"2026-11-03T09:00:00Z","updatedAt":"2026-11-03T09:00:00Z"}`
	mwTargetsJSON = `{"monitors":[],"statusPages":[],"components":[
		{"id":"` + spDesk + `","name":"Support desk","statusPageId":"` + spAcme + `"},
		{"id":"` + spSite + `","name":"Twin","statusPageId":"` + spAcme + `"},
		{"id":"` + spGroup + `","name":"Twin","statusPageId":"` + spDraft + `"}]}`
	mwHold = `"maintenance":{"windowId":"` + mwDeploy + `","title":"Deploy","startsAt":"2026-11-03T08:00:00Z","endsAt":"2026-11-03T08:15:00Z"}`
)

func init() {
	goldenData["GET /maintenance-windows"] = monList(mwDeployJSON, mwUpgradeJSON)
	goldenData["GET /maintenance-windows/"+mwDeploy] = mwDeployJSON
	goldenData["GET /maintenance-windows/"+mwUpgrade] = mwUpgradeJSON
	goldenData["GET /maintenance-windows/targets"] = mwTargetsJSON
}

// held adds a maintenance hold to a monitor's JSON.
func held(monitorJSON string) string {
	return strings.Replace(monitorJSON, `{"id"`, `{`+mwHold+`,"id"`, 1)
}

// The deploy pattern: stdout of create -o id is the id and nothing else,
// finish needs neither a terminal nor --yes.
func TestMaintenanceDeployPattern(t *testing.T) {
	goldenEnv(t)
	var create, finish recorder
	goldenHandlers(t, map[string]http.HandlerFunc{
		"POST /maintenance-windows":                         create.handler(201, mwDeployJSON),
		"POST /maintenance-windows/" + mwDeploy + "/finish": finish.handler(200, mwDeployJSON),
	})
	before := time.Now().UTC().Add(-time.Second)
	out, _, err := run(t, "monitor", "maintenance", "create", "--monitor", "Shop", "--title", " Deploy ", "--duration", "15m", "-o", "id")
	if err != nil {
		t.Fatal(err)
	}
	if out != mwDeploy+"\n" {
		t.Fatalf("stdout %q, want only the id", out)
	}
	b := create.only(t).body
	if b["title"] != "Deploy" || b["notifyScheduled"] != true || b["notifyStarted"] != true || b["notifyFinished"] != true {
		t.Errorf("body %v", b)
	}
	if _, ok := b["description"]; ok {
		t.Errorf("description sent without the flag: %v", b)
	}
	targets := b["targets"].(map[string]any)
	if ids := targets["monitorIds"].([]any); len(ids) != 1 || ids[0] != monShop {
		t.Errorf("targets %v", targets)
	}
	if len(targets) != 1 {
		t.Errorf("lists nobody named were sent: %v", targets)
	}
	start, err := time.Parse(time.RFC3339, b["startsAt"].(string))
	if err != nil || start.Before(before.Truncate(time.Second)) || start.After(time.Now()) {
		t.Errorf("startsAt %v, %v: want now", b["startsAt"], err)
	}
	end, err := time.Parse(time.RFC3339, b["endsAt"].(string))
	if err != nil || end.Sub(start) != 15*time.Minute {
		t.Errorf("endsAt %v, %v: want 15 minutes after the start", b["endsAt"], err)
	}

	if _, _, err := run(t, "monitor", "maintenance", "finish", strings.TrimSpace(out)); err != nil {
		t.Fatal(err)
	}
	if r := finish.only(t); r.method != http.MethodPost || r.path != "/maintenance-windows/"+mwDeploy+"/finish" {
		t.Errorf("finish sent %+v", r)
	}
}

func TestMaintenanceCreateTargetsAndMail(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /maintenance-windows": rec.handler(201, mwUpgradeJSON)})
	out, _, err := run(t, "monitor", "maintenance", "create", "--title", "Database upgrade", "--description", "Public words",
		"--monitor", monCert, "--monitor", "Shop", "--component", "Support desk", "--component", spSite, "--status-page", "acme",
		"--start", "2026-11-07T14:00:00+01:00", "--end", "2026-11-07T16:00:00Z", "--notify-started=false")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Scheduled maintenance") || !strings.Contains(out, "no published status page") {
		t.Errorf("message %q", out)
	}
	b := rec.only(t).body
	if b["startsAt"] != "2026-11-07T13:00:00Z" || b["endsAt"] != "2026-11-07T16:00:00Z" || b["description"] != "Public words" {
		t.Errorf("body %v", b)
	}
	if b["notifyScheduled"] != true || b["notifyStarted"] != false || b["notifyFinished"] != true {
		t.Errorf("mail switches %v", b)
	}
	targets := b["targets"].(map[string]any)
	if got := targets["monitorIds"].([]any); len(got) != 2 || got[0] != monCert || got[1] != monShop {
		t.Errorf("monitors %v", got)
	}
	if got := targets["componentIds"].([]any); len(got) != 2 || got[0] != spDesk || got[1] != spSite {
		t.Errorf("components %v", got)
	}
	if got := targets["statusPageIds"].([]any); len(got) != 1 || got[0] != spAcme {
		t.Errorf("pages %v", got)
	}
}

func TestMaintenanceCreateNoNotifyAndDurationFromStart(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /maintenance-windows": rec.handler(201, mwDeployJSON)})
	out, _, err := run(t, "monitor", "maintenance", "create", "--title", "Load test", "--monitor", monShop, "--start", "2026-11-07T14:00:00Z", "--duration", "2d", "--no-notify")
	if err != nil {
		t.Fatal(err)
	}
	b := rec.only(t).body
	if b["notifyScheduled"] != false || b["notifyStarted"] != false || b["notifyFinished"] != false || b["endsAt"] != "2026-11-09T14:00:00Z" {
		t.Errorf("body %v", b)
	}
	// The result names the published page it shows on, whatever was asked.
	if !strings.Contains(out, "publicly on Acme status") {
		t.Errorf("message %q", out)
	}
}

func TestMaintenanceComponentNames(t *testing.T) {
	goldenEnv(t)
	base := []string{"monitor", "maintenance", "create", "--title", "x", "--duration", "1h"}
	_, _, err := run(t, append(base, "--component", "Twin")...)
	if !clierr.IsUsage(err) || !strings.Contains(err.Error(), "2 components") {
		t.Errorf("ambiguous name: %v", err)
	}
	_, _, err = run(t, append(base, "--component", "Nothing")...)
	if exitOf(err) != clierr.ExitNotFound {
		t.Errorf("unknown name: %v", err)
	}
}

func TestMaintenanceUsageErrorsBeforeAnyRequest(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{
		"POST /maintenance-windows":              rec.handler(201, mwDeployJSON),
		"PATCH /maintenance-windows/" + mwDeploy: rec.handler(200, mwDeployJSON),
		"GET /maintenance-windows":               rec.handler(200, monList()),
	})
	create := []string{"monitor", "maintenance", "create"}
	full := append(append([]string{}, create...), "--title", "x", "--monitor", monShop)
	update := []string{"monitor", "maintenance", "update", mwDeploy}
	for _, args := range [][]string{
		append(append([]string{}, create...), "--monitor", monShop, "--duration", "1h"),                 // no title
		append(append([]string{}, create...), "--title", " ", "--monitor", monShop, "--duration", "1h"), // blank title
		append(append([]string{}, create...), "--title", "x", "--duration", "1h"),                       // no target
		full, // neither end nor duration
		append(append([]string{}, full...), "--end", "+1h", "--duration", "1h"),                    // both
		append(append([]string{}, full...), "--duration", "soon"),                                  // bad length
		append(append([]string{}, full...), "--duration", "0m"),                                    // no length
		append(append([]string{}, full...), "--duration", "1h", "--start", "2h"),                   // bare span
		append(append([]string{}, full...), "--end", "2h"),                                         // bare span
		append(append([]string{}, full...), "--duration", "1h", "--no-notify", "--notify-started"), // both
		update, // nothing to change
		append(append([]string{}, update...), "--title", " "),
		append(append([]string{}, update...), "--end", "2h"),
		{"monitor", "maintenance", "update", "not-an-id", "--title", "x"},
		{"monitor", "maintenance", "get", "not-an-id"},
		{"monitor", "maintenance", "cancel", "not-an-id", "--yes"},
		{"monitor", "maintenance", "finish", "not-an-id"},
		{"monitor", "maintenance", "list", "--state", "upcoming"},
	} {
		if _, _, err := run(t, args...); exitOf(err) != clierr.ExitUsage {
			t.Errorf("%v: %v (exit %d), want exit %d", args, err, exitOf(err), clierr.ExitUsage)
		}
	}
	if n := rec.count(); n != 0 {
		t.Errorf("%d requests", n)
	}
	// The hint shows the spelling that works.
	_, _, err := run(t, append(append([]string{}, full...), "--duration", "1h", "--start", "2h")...)
	if err == nil || !strings.Contains(err.Error(), "+2h") {
		t.Errorf("no hint: %v", err)
	}
}

// update sends the flags that were given and nothing else.
func TestMaintenanceUpdateSendsOnlyWhatChanged(t *testing.T) {
	goldenEnv(t)
	id := mwDeploy
	for _, c := range []struct {
		args []string
		want map[string]any
	}{
		{[]string{"--title", "New"}, map[string]any{"title": "New"}},
		{[]string{"--description", ""}, map[string]any{"description": ""}},
		{[]string{"--end", "2026-11-07T16:00:00Z"}, map[string]any{"endsAt": "2026-11-07T16:00:00Z"}},
		{[]string{"--start", "2026-11-07T14:00:00Z"}, map[string]any{"startsAt": "2026-11-07T14:00:00Z"}},
		{[]string{"--notify-started=false"}, map[string]any{"notifyStarted": false}},
		{[]string{"--notify-finished"}, map[string]any{"notifyFinished": true}},
		{[]string{"--no-notify"}, map[string]any{"notifyScheduled": false, "notifyStarted": false, "notifyFinished": false}},
		{[]string{"--start", "2026-11-07T14:00:00Z", "--duration", "2h"}, map[string]any{"startsAt": "2026-11-07T14:00:00Z", "endsAt": "2026-11-07T16:00:00Z"}},
		// From the window's current start.
		{[]string{"--duration", "1h"}, map[string]any{"endsAt": "2026-11-03T09:00:00Z"}},
	} {
		var rec recorder
		goldenHandlers(t, map[string]http.HandlerFunc{"PATCH /maintenance-windows/" + id: rec.handler(200, mwDeployJSON)})
		if _, _, err := run(t, append([]string{"monitor", "maintenance", "update", id}, c.args...)...); err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		b := rec.only(t).body
		if len(b) != len(c.want) {
			t.Errorf("%v: sent %v, want %v", c.args, b, c.want)
		}
		for k, v := range c.want {
			if b[k] != v {
				t.Errorf("%v: %s = %v, want %v", c.args, k, b[k], v)
			}
		}
	}
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{"PATCH /maintenance-windows/" + id: rec.handler(200, mwDeployJSON)})
	if _, _, err := run(t, "monitor", "maintenance", "update", id, "--monitor", "Shop"); err != nil {
		t.Fatal(err)
	}
	targets := rec.only(t).body["targets"].(map[string]any)
	if got := targets["monitorIds"].([]any); len(targets) != 1 || got[0] != monShop {
		t.Errorf("targets %v", targets)
	}
}

const mwConflict = `{"title":"Conflict","status":409,"detail":"%s","code":"CONFLICT","reason":"MAINTENANCE_WINDOW_STATE","data":{"state":"%s"}}`

func conflict(detail, state string) string { return fmt.Sprintf(mwConflict, detail, state) }

// A refusal keeps the API's sentence, exits 8 and names the other verb.
func TestMaintenanceConflicts(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{
		"POST /maintenance-windows/" + mwDeploy + "/cancel":  rec.handler(409, conflict("This window has already started: use finish to end it.", "in_progress")),
		"POST /maintenance-windows/" + mwUpgrade + "/finish": rec.handler(409, conflict("This window hasn't started: cancel it instead.", "scheduled")),
		"PATCH /maintenance-windows/" + mwDeploy:             rec.handler(409, conflict("This window has started, so its targets are frozen.", "in_progress")),
	})
	for _, c := range []struct {
		args              []string
		message, hintWant string
	}{
		{[]string{"cancel", mwDeploy, "--yes"}, "use finish", "maintenance finish " + mwDeploy},
		{[]string{"finish", mwUpgrade}, "cancel it instead", "maintenance cancel " + mwUpgrade},
		{[]string{"update", mwDeploy, "--monitor", monShop}, "targets are frozen", ""},
	} {
		_, _, err := run(t, append([]string{"monitor", "maintenance"}, c.args...)...)
		e := Describe(err)
		if e.Code != clierr.CodeConflict || e.ExitCode() != clierr.ExitConflict || !strings.Contains(e.Message, c.message) || !strings.Contains(e.Hint, c.hintWant) {
			t.Errorf("%v: %+v", c.args, e)
		}
	}
}

func TestMaintenanceCancelConfirms(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /maintenance-windows/" + mwUpgrade + "/cancel": rec.handler(200, mwUpgradeJSON)})
	_, _, err := run(t, "monitor", "maintenance", "cancel", mwUpgrade)
	if !clierr.IsUsage(err) || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("no terminal, no --yes: %v", err)
	}
	if rec.count() != 0 {
		t.Fatal("cancelled without confirmation")
	}
	out, _, err := run(t, "monitor", "maintenance", "cancel", mwUpgrade, "--yes", "-o", "text")
	if err != nil {
		t.Fatal(err)
	}
	if out != mwUpgrade+"\tscheduled\n" {
		t.Errorf("text %q", out)
	}
	if r := rec.only(t); r.method != http.MethodPost {
		t.Errorf("%+v", r)
	}
}

// A refused publish names the page, exits 5 and says what would allow it.
func TestMaintenancePublishDenied(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	denied := `{"title":"Forbidden","status":403,"detail":"This window would show publicly on Acme status, and you can't publish there. Showing it needs permission to publish maintenance on that page; a scoped token also needs the monitor.statuspages scope.","code":"FORBIDDEN","reason":"MAINTENANCE_PUBLISH_DENIED","data":{"statusPageIds":"` + spAcme + `"}}`
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /maintenance-windows": rec.handler(403, denied)})
	_, _, err := run(t, "monitor", "maintenance", "create", "--title", "x", "--monitor", monShop, "--duration", "1h")
	e := Describe(err)
	if e.ExitCode() != clierr.ExitAccess || !strings.Contains(e.Message, "Acme status") || !strings.Contains(e.Hint, "monitor.statuspages") {
		t.Errorf("%+v", e)
	}
}

func TestMaintenanceScopeRefusal(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	scope := `{"title":"Forbidden","status":403,"detail":"This token's scopes don't allow this.","code":"FORBIDDEN","reason":"INSUFFICIENT_SCOPE"}`
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /maintenance-windows/" + mwDeploy + "/finish": rec.handler(403, scope)})
	_, _, err := run(t, "monitor", "maintenance", "finish", mwDeploy)
	e := Describe(err)
	if e.Code != clierr.CodeInsufficientScope || e.ExitCode() != clierr.ExitAccess || !strings.Contains(e.Hint, "monitor.maintenance") {
		t.Errorf("%+v", e)
	}
}

func TestMaintenanceListFilters(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{"GET /maintenance-windows": rec.handler(200, monList(mwUpgradeJSON))})
	if _, _, err := run(t, "monitor", "maintenance", "list", "--state", "in_progress", "--monitor", "Shop", "--status-page", "acme", "--limit", "5"); err != nil {
		t.Fatal(err)
	}
	q := rec.only(t).query
	for _, want := range []string{"status=in_progress", "monitorId=" + monShop, "statusPageId=" + spAcme, "limit=5"} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q lacks %s", q, want)
		}
	}
}

func TestMaintenanceListIDs(t *testing.T) {
	goldenEnv(t)
	out, _, err := run(t, "monitor", "maintenance", "list", "-o", "id")
	if err != nil || out != mwDeploy+"\n"+mwUpgrade+"\n" {
		t.Errorf("%q, %v", out, err)
	}
	// Something without an id is a usage error, not an empty line.
	if _, _, err := run(t, "monitor", "incidents", "summary", "-o", "id"); !clierr.IsUsage(err) {
		t.Errorf("no id: %v", err)
	}
}

func TestMaintenanceCompletion(t *testing.T) {
	goldenEnv(t)
	clearCompletionCache(t)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"monitor", "maintenance", "finish", "4d"}, mwDeploy},
		{[]string{"monitor", "maintenance", "get", "5e"}, mwUpgrade},
		{[]string{"monitor", "maintenance", "list", "--state", ""}, "scheduled,in_progress,completed,cancelled"},
		{[]string{"monitor", "maintenance", "create", "--monitor", "2b"}, monCert},
		{[]string{"monitor", "maintenance", "create", "--status-page", "ac"}, "acme"},
	} {
		got, _ := complete(t, c.args...)
		if strings.Join(got, ",") != c.want {
			t.Errorf("%v: %v, want %s", c.args, got, c.want)
		}
	}
}

// The help carries the deploy pattern and the scopes.
func TestMaintenanceHelp(t *testing.T) {
	goldenEnv(t)
	out, _, err := run(t, "monitor", "maintenance", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-o id)", `maintenance finish "$id"`, "monitor.maintenance", "monitor.statuspages"} {
		if !strings.Contains(out, want) {
			t.Errorf("help lacks %q", want)
		}
	}
}

// A window held problem doesn't fail --fail-on: the API leaves it out of the
// open counts, and the report moves its monitor to the maintenance list.
func TestMonitorStatusInMaintenance(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{
		"GET /incidents/summary": rec.handler(200, `{"open":{"total":0,"critical":0,"warning":0,"unacknowledged":0,"inMaintenance":1},"resolvedLast7d":0,"resolvedLast30d":0}`),
		"GET /monitors":          rec.handler(200, monList(held(monShopJSON), monDomainJSON)),
	})
	out, _, err := run(t, "monitor", "status", "--fail-on", "warning", "-o", "json")
	if err != nil {
		t.Fatalf("held incident failed the gate: %v", err)
	}
	var r struct {
		Status      string                        `json:"status"`
		Problems    []map[string]any              `json:"problems"`
		Maintenance []map[string]any              `json:"maintenance"`
		Incidents   struct{ Open map[string]int } `json:"incidents"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if r.Status != "ok" || len(r.Problems) != 0 || len(r.Maintenance) != 1 || r.Incidents.Open["inMaintenance"] != 1 {
		t.Fatalf("%s", out)
	}
	if r.Maintenance[0]["name"] != "Shop" || r.Maintenance[0]["maintenance"].(map[string]any)["windowId"] != mwDeploy {
		t.Errorf("%v", r.Maintenance[0])
	}
	table, _, err := run(t, "monitor", "status", "--no-pager")
	if err != nil || !strings.Contains(table, `"Deploy" until`) || !strings.Contains(table, "Incidents in maintenance:") {
		t.Errorf("%v\n%s", err, table)
	}
}

// What the calendar says stays a problem during a window: an expiring
// certificate on an SSL monitor, and a watched aspect on an HTTP one.
func TestMonitorStatusCalendarProblemsStayLive(t *testing.T) {
	goldenEnv(t)
	aspect := strings.Replace(monShopJSON, `"aspect":"certificate","subject":"shop.example.com","status":"ok"`, `"aspect":"certificate","subject":"shop.example.com","status":"warning"`, 1)
	if aspect == monShopJSON {
		t.Fatal("fixture changed")
	}
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{
		"GET /incidents/summary": rec.handler(200, `{"open":{"total":2,"critical":1,"warning":1,"unacknowledged":2,"inMaintenance":0},"resolvedLast7d":0,"resolvedLast30d":0}`),
		"GET /monitors":          rec.handler(200, monList(held(aspect), held(monCertJSON))),
	})
	out, _, err := run(t, "monitor", "status", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Problems    []map[string]any `json:"problems"`
		Maintenance []map[string]any `json:"maintenance"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Problems) != 2 || len(r.Maintenance) != 0 {
		t.Errorf("%s", out)
	}
}

// monitors run --wait still fails on critical in a window; the result says so.
func TestMonitorRunInMaintenance(t *testing.T) {
	goldenEnv(t)
	result := func(severity string) string {
		return `{"id":9003,"checkedAt":"2026-11-03T08:10:00Z","severity":"` + severity + `","condition":"unexpected_status","region":"eu-central-1","timings":{"dnsMs":1,"connectMs":1,"tlsMs":1,"ttfbMs":1,"totalMs":4}}`
	}
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{
		"GET /monitors/" + monShop:             rec.handler(200, held(monShopJSON)),
		"POST /monitors/" + monShop + "/check": rec.handler(200, result("critical")),
	})
	_, _, err := run(t, "monitor", "monitors", "run", monShop, "--wait", "--fail-on", "critical", "-o", "json")
	e := Describe(err)
	if e.Code != clierr.CodeJobFailed || !strings.Contains(e.Message, "maintenance") {
		t.Fatalf("%+v", e)
	}
	goldenHandlers(t, map[string]http.HandlerFunc{
		"GET /monitors/" + monShop:             rec.handler(200, held(monShopJSON)),
		"POST /monitors/" + monShop + "/check": rec.handler(200, result("ok")),
	})
	out, _, err := run(t, "monitor", "monitors", "run", monShop, "--wait", "-o", "json")
	if err != nil || !strings.Contains(out, `"inMaintenance": true`) || !strings.Contains(out, `"severity": "ok"`) {
		t.Errorf("%v\n%s", err, out)
	}
	out, _, err = run(t, "monitor", "monitors", "run", monShop, "-o", "text")
	if err != nil || !strings.HasSuffix(strings.TrimSpace(out), "yes") {
		t.Errorf("%v\n%q", err, out)
	}
	out, _, err = run(t, "monitor", "monitors", "run", monShop, "--no-pager")
	if err != nil || !strings.Contains(out, "In maintenance:") {
		t.Errorf("%v\n%s", err, out)
	}
}

// Monitor lists show the end of the window holding a monitor, at the end of
// the row, so -o text keeps its earlier columns.
func TestMonitorListShowsMaintenance(t *testing.T) {
	goldenEnv(t)
	var rec recorder
	goldenHandlers(t, map[string]http.HandlerFunc{
		"GET /monitors":            rec.handler(200, monList(held(monShopJSON))),
		"GET /monitors/" + monShop: rec.handler(200, held(monShopJSON)),
	})
	out, _, err := run(t, "monitor", "monitors", "list", "-o", "text")
	if err != nil || !strings.HasSuffix(strings.TrimSpace(out), "\t2026-11-03T08:15:00Z") {
		t.Errorf("%v\n%q", err, out)
	}
	out, _, err = run(t, "monitor", "monitors", "get", monShop, "--no-pager")
	if err != nil || !strings.Contains(out, `In maintenance:`) || !strings.Contains(out, mwDeploy) {
		t.Errorf("%v\n%s", err, out)
	}
}
