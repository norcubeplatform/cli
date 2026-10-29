package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/norcubeplatform/cli/internal/clierr"
)

// Backup answers of the fake backend (see golden_test.go), beyond the
// ones golden_test.go already has.
func init() {
	for k, v := range map[string]string{
		"GET /datasources/ds-1/backups/job-1": `{"datasourceEngine":"postgres","datasourceId":"ds-1","datasourceName":"prod-db","destinationId":"dst-1","destinationIsManaged":true,"destinationRegion":"eu-central-1","jobBytesWritten":1048576,"jobCreatedAt":"2026-10-01T10:00:00Z","jobDurationMs":42000,"jobFinishedAt":"2026-10-01T10:00:42Z","jobId":"job-1","jobScheduledFor":"2026-10-01T10:00:00Z","jobStartedAt":"2026-10-01T10:00:00Z","jobStatus":"success","jobTrigger":"schedule","policyCompression":"gzip","policyEncryption":"sse-kms","policyId":"pol-1","policyMethod":"logical_dump","policyName":"Nightly","policyRetentionDays":30,"verifyPassed":true,"verifyStatus":"success"}`,
		"GET /datasources/ds-1/restores": `{"list":[
			{"id":"rst-1","dataSourceId":"ds-1","dataSourceName":"prod-db","engine":"postgres","mode":"new_database","status":"success","sourceBackupId":"job-1","targetConnectionRef":"postgres://…@db.example.com:5432/app_restored","targetDatabase":"app_restored","createdAt":"2026-10-02T08:00:00Z","startedAt":"2026-10-02T08:00:05Z","finishedAt":"2026-10-02T08:03:05Z","durationMs":180000,"bytesRestored":2097152,"hasLog":true},
			{"id":"rst-2","dataSourceId":"ds-1","dataSourceName":"prod-db","engine":"postgres","mode":"overwrite","status":"failed","sourceBackupId":"job-1","createdAt":"2026-10-01T08:00:00Z","errorSummary":"permission denied for schema public"}
		],"cursors":{"next":"cur-r2"},"meta":{}}`,
		"GET /restores/rst-1":                 `{"id":"rst-1","dataSourceId":"ds-1","dataSourceName":"prod-db","engine":"postgres","mode":"new_database","status":"success","sourceBackupId":"job-1","targetConnectionRef":"postgres://…@db.example.com:5432/app_restored","targetDatabase":"app_restored","createdAt":"2026-10-02T08:00:00Z","startedAt":"2026-10-02T08:00:05Z","finishedAt":"2026-10-02T08:03:05Z","durationMs":180000,"bytesRestored":2097152,"hasLog":true}`,
		"GET /datasources/ds-1/restore-check": `{"enabled":true,"mode":"every_nth","nth":7,"target":"managed_ephemeral","validationLevel":"l2","hasScratchCredentials":false,"lastRunAt":"2026-10-01T11:00:00Z"}`,
		"GET /backup-policies": `{"list":[
			{"id":"pol-1","name":"Nightly","scheduleCron":"0 2 * * *","timezone":"Europe/Prague","retentionDays":30,"keepLastN":30,"method":"logical_dump","compression":"gzip","encryption":"sse-kms","destinationId":"dst-1","organizationId":"org-1","parallelism":1,"pitrEnabled":false}
		],"cursors":{},"meta":{}}`,
		"GET /storage-destinations": `[
			{"id":"dst-1","name":"Norcube storage","type":"s3","isManaged":true,"isActive":true,"region":"eu-central-1"},
			{"id":"dst-2","name":"Own bucket","type":"s3","isManaged":false,"isActive":true,"region":"eu-west-1","bucket":"acme-backups"}
		]`,
	} {
		goldenData[k] = v
	}
}

// Connection strings of the tests (fake hosts and passwords).
const (
	testTargetURI = "postgres://admin:secret@db.example.com:5432/postgres" // #nosec G101 -- a test fixture
	testSourceURI = "postgres://backup:pw@db.example.com/app"              // #nosec G101 -- a test fixture
)

// restoreTestsList is the restore tests of ds-1 (trigger=verification).
const restoreTestsList = `{"list":[
	{"id":"rt-1","dataSourceId":"ds-1","dataSourceName":"prod-db","engine":"postgres","mode":"new_database","status":"success","sourceBackupId":"job-1","verifyLevel":"l2","verifyPassed":true,"createdAt":"2026-10-01T11:00:00Z","startedAt":"2026-10-01T11:00:00Z","durationMs":95000},
	{"id":"rt-2","dataSourceId":"ds-1","dataSourceName":"prod-db","engine":"postgres","mode":"new_database","status":"success","sourceBackupId":"job-0","verifyLevel":"l2","verifyPassed":true,"verifyWarnings":"index skipped","createdAt":"2026-09-30T11:00:00Z","startedAt":"2026-09-30T11:00:00Z","durationMs":91000},
	{"id":"rt-3","dataSourceId":"ds-1","dataSourceName":"prod-db","engine":"postgres","mode":"new_database","status":"failed","sourceBackupId":"job-x","createdAt":"2026-09-29T11:00:00Z","errorSummary":"archive truncated"}
],"cursors":{},"meta":{}}`

// restoresByTrigger answers the restores list by its trigger filter.
func restoresByTrigger(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("trigger") == "verification" {
		fmt.Fprint(w, restoreTestsList)
		return
	}
	fmt.Fprint(w, goldenData["GET /datasources/ds-1/restores"])
}

func TestGoldenBackup(t *testing.T) {
	commands := []struct {
		name string
		args []string
	}{
		{"backup-jobs-get", []string{"backup", "jobs", "get", "job-1", "--datasource", "ds-1"}},
		{"backup-restores-list", []string{"backup", "restores", "list", "--datasource", "ds-1"}},
		{"backup-restores-get", []string{"backup", "restores", "get", "rst-1"}},
		{"backup-restore-tests-list", []string{"backup", "restore-tests", "list", "--datasource", "ds-1"}},
		{"backup-restore-tests-settings-get", []string{"backup", "restore-tests", "settings", "get", "--datasource", "ds-1"}},
		{"backup-policies-definitions", []string{"backup", "policies", "list"}},
		{"backup-destinations-list", []string{"backup", "destinations", "list"}},
	}
	for _, c := range commands {
		for format, ext := range goldenFormats {
			t.Run(c.name+"/"+format, func(t *testing.T) {
				url := goldenEnv(t)
				goldenHandlers(t, map[string]http.HandlerFunc{"GET /datasources/ds-1/restores": restoresByTrigger})
				out, stderr, err := run(t, append(c.args, "-o", format, "--no-pager")...)
				if err != nil {
					t.Fatalf("%v\n%s", err, stderr)
				}
				checkGolden(t, c.name+"."+ext, out, url)
			})
		}
	}
}

// jobSequence answers GET of a backup job with the next status of
// statuses each time (the last one repeats).
func jobSequence(id string, statuses ...string) http.HandlerFunc {
	var n int32
	return func(w http.ResponseWriter, r *http.Request) {
		i := int(atomic.AddInt32(&n, 1)) - 1
		if i >= len(statuses) {
			i = len(statuses) - 1
		}
		fmt.Fprintf(w, `{"jobId":%q,"datasourceId":"ds-1","datasourceName":"prod-db","jobStatus":%q,"jobTrigger":"manual","jobErrorSummary":"connection refused","jobBytesWritten":0,"jobCreatedAt":"2026-10-28T09:00:00Z","jobScheduledFor":"2026-10-28T09:00:00Z","policyId":"pol-1","policyName":"Nightly"}`,
			id, statuses[i])
	}
}

func TestBackupJobsRunWaits(t *testing.T) {
	goldenEnv(t)
	var body map[string]any
	goldenHandlers(t, map[string]http.HandlerFunc{
		"POST /datasources/ds-1/backups/run-now": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&body)
			fmt.Fprint(w, `{"jobId":"job-9","policyId":"pol-1","destinationId":"dst-1"}`)
		},
		"GET /datasources/ds-1/backups/job-9": jobSequence("job-9", "running", "success"),
	})
	out, stderr, err := run(t, "backup", "jobs", "run", "--datasource", "ds-1", "--policy", "pol-1", "--wait", "-o", "json", "--query", "jobStatus")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if strings.TrimSpace(out) != `"success"` || body["policyId"] != "pol-1" {
		t.Fatalf("out=%s body=%v", out, body)
	}

	// Without --wait it prints the queued job; without --policy it sends none.
	body = nil
	out, _, err = run(t, "backup", "jobs", "run", "-d", "ds-1", "-o", "json", "--query", "jobId")
	if err != nil || strings.TrimSpace(out) != `"job-9"` {
		t.Fatalf("%v %s", err, out)
	}
	if _, ok := body["policyId"]; ok {
		t.Errorf("sent a policy without --policy: %v", body)
	}
}

func TestBackupJobsRunWaitFailsWhenTheBackupFails(t *testing.T) {
	goldenEnv(t)
	goldenHandlers(t, map[string]http.HandlerFunc{
		"POST /datasources/ds-1/backups/run-now": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"jobId":"job-8"}`)
		},
		"GET /datasources/ds-1/backups/job-8": jobSequence("job-8", "failed"),
	})
	_, _, err := run(t, "backup", "jobs", "run", "-d", "ds-1", "--wait")
	e := Describe(err)
	if e.Code != clierr.CodeJobFailed || e.ExitCode() != clierr.ExitError || !strings.Contains(e.Message, "connection refused") {
		t.Fatalf("got %v (code %s, exit %d)", err, e.Code, e.ExitCode())
	}
}

func TestBackupRunNeedsPaymentIsReported(t *testing.T) {
	goldenEnv(t)
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /datasources/ds-1/backups/run-now": func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		fmt.Fprint(w, `{"type":"ORG_BLOCKED","msg":"Your organization has unpaid invoices."}`)
	}})
	_, _, err := run(t, "backup", "jobs", "run", "-d", "ds-1")
	if err == nil || !strings.Contains(err.Error(), "unpaid invoices") {
		t.Fatalf("got %v", err)
	}
}

func TestBackupRestoreCreateConfirmsAndValidates(t *testing.T) {
	goldenEnv(t)
	var mu sync.Mutex
	var bodies []map[string]any
	goldenHandlers(t, map[string]http.HandlerFunc{
		"POST /restores": func(w http.ResponseWriter, r *http.Request) {
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			mu.Lock()
			bodies = append(bodies, b)
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"rst-9"}`)
		},
		"GET /restores/rst-9": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"id":"rst-9","dataSourceId":"ds-1","dataSourceName":"prod-db","engine":"postgres","mode":"new_database","status":"success","createdAt":"2026-10-28T09:00:00Z"}`)
		},
	})
	uriFile := filepath.Join(t.TempDir(), "target.uri")
	if err := os.WriteFile(uriFile, []byte(testTargetURI+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Invalid combinations and a missing --yes without a terminal are
	// usage errors before anything is sent.
	for _, args := range [][]string{
		{"--mode", "sideways"},
		{"--mode", "new_database", "--target-uri-file", uriFile},
		{"--mode", "new_server"},
		{"--mode", "overwrite", "--target-database", "x"},
		{"--mode", "overwrite"},
	} {
		full := append([]string{"backup", "restores", "create", "-d", "ds-1", "--backup", "job-1"}, args...)
		if _, _, err := run(t, full...); !clierr.IsUsage(err) {
			t.Errorf("%v: got %v, want a usage error", args, err)
		}
	}
	if len(bodies) != 0 {
		t.Fatalf("%d restores started by invalid invocations", len(bodies))
	}

	out, stderr, err := run(t, "backup", "restores", "create", "-d", "ds-1", "--backup", "job-1", "--mode", "new_database",
		"--target-uri-file", uriFile, "--target-database", "app_restored", "--yes", "--wait", "-o", "json", "--query", "status")
	if err != nil || strings.TrimSpace(out) != `"success"` {
		t.Fatalf("%v %s %s", err, out, stderr)
	}
	if _, _, err := run(t, "backup", "restores", "create", "-d", "ds-1", "--backup", "job-1", "--mode", "overwrite", "--yes"); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 {
		t.Fatalf("bodies %v", bodies)
	}
	first, second := bodies[0], bodies[1]
	if first["targetUri"] != testTargetURI || first["targetDatabase"] != "app_restored" || first["confirmOverwrite"] != nil {
		t.Errorf("new_database body %v", first)
	}
	if second["mode"] != "overwrite" || second["confirmOverwrite"] != true || second["targetUri"] != nil {
		t.Errorf("overwrite body %v", second)
	}
}

func TestBackupRestoreTestRunPicksTheNewestSuccessfulBackup(t *testing.T) {
	goldenEnv(t)
	var verified string
	goldenHandlers(t, map[string]http.HandlerFunc{
		"GET /datasources/ds-1/backups": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("cursor") == "" {
				fmt.Fprint(w, `{"list":[{"jobId":"job-run","jobStatus":"running"},{"jobId":"job-gone","jobStatus":"success","jobDeletedAt":"2026-10-20T00:00:00Z"}],"cursors":{"next":"p2"},"meta":{}}`)
				return
			}
			fmt.Fprint(w, `{"list":[{"jobId":"job-ok","jobStatus":"success"}],"cursors":{},"meta":{}}`)
		},
		"POST /datasources/ds-1/backups/job-ok/verify": func(w http.ResponseWriter, r *http.Request) {
			verified = r.URL.Path
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"restoreJobId":"rt-9"}`)
		},
		"GET /restores/rt-9": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"id":"rt-9","dataSourceId":"ds-1","dataSourceName":"prod-db","engine":"postgres","mode":"new_database","status":"success","verifyPassed":false,"verifyLevel":"l2","createdAt":"2026-10-28T09:00:00Z"}`)
		},
	})
	_, stderr, err := run(t, "backup", "restore-tests", "run", "--datasource", "ds-1", "--wait")
	if verified == "" {
		t.Fatalf("no verify request (%v, %s)", err, stderr)
	}
	// The restore finished but the data didn't validate: a failed test.
	if e := Describe(err); e.Code != clierr.CodeJobFailed || !strings.Contains(e.Message, "rt-9 ended failed") {
		t.Fatalf("got %v (code %s)", err, e.Code)
	}

	// No successful backup at all: not found.
	goldenHandlers(t, map[string]http.HandlerFunc{"GET /datasources/ds-1/backups": func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"list":[{"jobId":"job-f","jobStatus":"failed"}],"cursors":{},"meta":{}}`)
	}})
	if _, _, err := run(t, "backup", "restore-tests", "run", "-d", "ds-1"); Describe(err).ExitCode() != clierr.ExitNotFound {
		t.Fatalf("got %v", err)
	}
}

func TestBackupRestoreTestSettingsUpdateKeepsWhatItDoesNotChange(t *testing.T) {
	goldenEnv(t)
	var put map[string]any
	goldenHandlers(t, map[string]http.HandlerFunc{"PUT /datasources/ds-1/restore-check": func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&put)
		fmt.Fprint(w, `{"enabled":true,"mode":"schedule","scheduleCron":"0 4 * * 0","scheduleTimezone":"Europe/Prague","target":"managed_ephemeral","validationLevel":"l2"}`)
	}})
	out, stderr, err := run(t, "backup", "restore-tests", "settings", "update", "-d", "ds-1", "--schedule", "0 4 * * 0", "--timezone", "Europe/Prague")
	if err != nil {
		t.Fatalf("%v %s", err, stderr)
	}
	if put["mode"] != "schedule" || put["scheduleCron"] != "0 4 * * 0" || put["target"] != "managed_ephemeral" || put["validationLevel"] != "l2" || put["enabled"] != true || put["nth"] != float64(7) {
		t.Errorf("PUT body %v", put)
	}
	if !strings.Contains(out, `on schedule "0 4 * * 0" (Europe/Prague)`) {
		t.Errorf("out %s", out)
	}
	for _, args := range [][]string{
		{},
		{"--every", "0"},
		{"--every", "3", "--sample", "10%"},
		{"--sample", "150%"},
		{"--level", "l9"},
		{"--scratch-uri-file", "x.uri"},
	} {
		put = nil
		full := append([]string{"backup", "restore-tests", "settings", "update", "-d", "ds-1"}, args...)
		if _, _, err := run(t, full...); !clierr.IsUsage(err) || put != nil {
			t.Errorf("%v: got %v (sent %v), want a usage error", args, err, put)
		}
	}
}

func TestBackupDatasourceCreateStoresTheConnectionString(t *testing.T) {
	goldenEnv(t)
	var created, creds map[string]any
	goldenHandlers(t, map[string]http.HandlerFunc{
		"POST /datasources": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&created)
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"ds-9"}`)
		},
		"PUT /datasources/ds-9/credentials": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&creds)
			fmt.Fprint(w, `{}`)
		},
	})
	out, stderr, err := runIn(t, testSourceURI+"\n",
		"backup", "datasources", "create", "--name", "app", "--engine", "postgres", "--environment", "prod", "--connection-uri-file", "-", "-o", "json")
	if err != nil {
		t.Fatalf("%v %s", err, stderr)
	}
	if created["engine"] != "postgres" || created["region"] != "eu-central-1" || creds["mode"] != "connection_uri" || creds["uri"] != testSourceURI {
		t.Fatalf("created %v creds %v", created, creds)
	}
	if strings.Contains(out, "pw@") || !strings.Contains(out, `"hasCredentials": true`) {
		t.Errorf("result %s", out)
	}
	if _, _, err := run(t, "backup", "datasources", "create", "--name", "x", "--engine", "oracle", "--environment", "prod"); !clierr.IsUsage(err) {
		t.Errorf("unknown engine: %v", err)
	}
	if _, _, err := run(t, "backup", "datasources", "update", "ds-9"); !clierr.IsUsage(err) {
		t.Errorf("empty update: %v", err)
	}
}

func TestBackupPolicyWrites(t *testing.T) {
	goldenEnv(t)
	var created, patched, attached map[string]any
	goldenHandlers(t, map[string]http.HandlerFunc{
		"POST /backup-policies": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&created)
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"pol-9"}`)
		},
		"PATCH /backup-policies/pol-9": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&patched)
			w.WriteHeader(http.StatusNoContent)
		},
		"POST /datasources/ds-1/policies": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&attached)
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"att-9"}`)
		},
	})
	if _, stderr, err := run(t, "backup", "policies", "create", "--name", "Nightly", "--schedule", "0 2 * * *", "--timezone", "UTC",
		"--retention-days", "30", "--keep-last", "10", "--destination", "dst-1", "--compression", "zstd"); err != nil {
		t.Fatalf("%v %s", err, stderr)
	}
	if created["scheduleCron"] != "0 2 * * *" || created["keepLastN"] != float64(10) || created["compression"] != "zstd" || created["method"] != nil {
		t.Errorf("create body %v", created)
	}
	if _, _, err := run(t, "backup", "policies", "update", "pol-9", "--retention-days", "90"); err != nil {
		t.Fatal(err)
	}
	if len(patched) != 1 || patched["retentionDays"] != float64(90) {
		t.Errorf("update body %v (only the changed setting is sent)", patched)
	}
	if _, _, err := run(t, "backup", "policies", "attach", "--datasource", "ds-1", "--policy", "pol-9", "--paused"); err != nil {
		t.Fatal(err)
	}
	if attached["policyId"] != "pol-9" || attached["enabled"] != false || attached["priority"] != nil {
		t.Errorf("attach body %v", attached)
	}
	for _, args := range [][]string{
		{"backup", "policies", "update", "pol-9"},
		{"backup", "policies", "update", "pol-9", "--method", "rsync"},
		{"backup", "policies", "create", "--name", "x"},
	} {
		if _, _, err := run(t, args...); !clierr.IsUsage(err) {
			t.Errorf("%v: got %v, want a usage error", args, err)
		}
	}
}

func TestBackupMissingScopeExits5(t *testing.T) {
	goldenEnv(t)
	goldenHandlers(t, map[string]http.HandlerFunc{"POST /datasources/ds-1/backups/job-1/verify": func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"type":"INSUFFICIENT_SCOPE","msg":"This connection can't run restore checks."}`)
	}})
	_, _, err := run(t, "backup", "restore-tests", "run", "job-1", "-d", "ds-1")
	if e := Describe(err); e.Code != clierr.CodeInsufficientScope || e.ExitCode() != clierr.ExitAccess {
		t.Fatalf("got %v (code %s, exit %d)", err, e.Code, e.ExitCode())
	}
}
