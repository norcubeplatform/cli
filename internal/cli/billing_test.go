package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/norcubeplatform/cli/internal/clierr"
)

// Billing answers of the fake backend (see golden_test.go).
func init() {
	for k, v := range map[string]string{
		"GET /billing/status": `{"status":"grace","graceStartedAt":"2026-10-20T08:00:00Z","graceExpiresAt":"2026-11-03T08:00:00Z"}`,
		"GET /billing/services": `[
			{"code":"snapdb","name":"Norcube Backup","billingMode":"plans","subscription":{"id":"sub-1","planCode":"backup-team","planName":"Team","status":"active","currentPeriodStart":"2026-10-01T00:00:00Z","currentPeriodEnd":"2026-11-01T00:00:00Z"}},
			{"code":"domainradar","name":"DomainRadar","billingMode":"metered"}
		]`,
		"GET /billing/estimate": `{"currentUsage":12.3,"estimatedTotal":31.5,"linearEstimate":29.1,"historicalAverage":27.75,"daysElapsed":12,"daysInMonth":31,"periodStart":"2026-10-01","periodEnd":"2026-10-31"}`,
		"GET /billing/free-tier": `{"metrics":[
			{"serviceCode":"domainradar","serviceName":"DomainRadar","metricCode":"domainradar.check.rdap","metricName":"RDAP checks","metricType":"usage","unit":"checks","freeTierUnits":1000,"currentUnits":640,"projectedEndOfMonthUnits":1653.33,"previousMonthUnits":900,"overflowProjected":true},
			{"serviceCode":"snapdb","serviceName":"Norcube Backup","metricCode":"snapdb.storage","metricName":"Backup storage","metricType":"storage","unit":"GB","freeTierUnits":0,"freeTierUnlimited":false,"currentUnits":2.5,"projectedEndOfMonthUnits":2.5,"previousMonthUnits":2}
		]}`,
		"GET /billing/invoices": `{"list":[
			{"id":"1042","organizationId":"org-1","periodStart":"2026-09-01T00:00:00Z","periodEnd":"2026-09-30T23:59:59Z","status":"draft","currency":"usd","subtotal":25,"tax":5.25,"total":30.25,"billingName":"Acme Inc","billingCity":"Prague","billingCountryCode":"CZ","billingPostalCode":"11000","billingState":"","billingStreet":"Main 1","billingType":"company"}
		],"cursors":{},"meta":{}}`,
		"GET /billing/budget": `{"hardCapCents":50000,"alertThresholds":[50,80,100]}`,
	} {
		goldenData[k] = v
	}
}

func TestGoldenBilling(t *testing.T) {
	commands := []struct {
		name string
		args []string
	}{
		{"billing-status", []string{"billing", "status"}},
		{"billing-subscriptions-list", []string{"billing", "subscriptions", "list"}},
		{"billing-usage-estimate", []string{"billing", "usage", "estimate"}},
		{"billing-usage-metrics", []string{"billing", "usage", "metrics"}},
		{"billing-invoices-list", []string{"billing", "invoices", "list"}},
		{"billing-budget-get", []string{"billing", "budget", "get"}},
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

func TestBillingInvoiceDownload(t *testing.T) {
	goldenEnv(t)
	goldenHandlers(t, map[string]http.HandlerFunc{
		"GET /billing/invoices/1042/pdf": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/pdf")
			fmt.Fprint(w, "%PDF-1.7 fake")
		},
		"GET /billing/invoices/7/pdf": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"type":"INTERNAL","msg":"Internal server error."}`)
		},
	})
	dest := filepath.Join(t.TempDir(), "inv.pdf")
	out, stderr, err := run(t, "billing", "invoices", "download", "1042", "--file", dest, "-o", "json")
	if err != nil {
		t.Fatalf("%v %s", err, stderr)
	}
	if b, _ := os.ReadFile(dest); string(b) != "%PDF-1.7 fake" || !strings.Contains(out, `"sha256"`) {
		t.Fatalf("file %q out %s", b, out)
	}
	// Never overwrites; a failed download leaves no file behind.
	if _, _, err := run(t, "billing", "invoices", "download", "1042", "--file", dest); !clierr.IsUsage(err) {
		t.Errorf("existing file: %v", err)
	}
	failed := filepath.Join(t.TempDir(), "x.pdf")
	if _, _, err := run(t, "billing", "invoices", "download", "7", "--file", failed); err == nil {
		t.Error("server error accepted")
	}
	if _, err := os.Stat(failed); !os.IsNotExist(err) {
		t.Errorf("partial file left behind: %v", err)
	}
	if _, _, err := run(t, "billing", "invoices", "download", "abc"); !clierr.IsUsage(err) {
		t.Errorf("bad id: %v", err)
	}
}

func TestBillingBudgetSet(t *testing.T) {
	goldenEnv(t)
	var put map[string]any
	puts := 0
	goldenHandlers(t, map[string]http.HandlerFunc{"PUT /billing/budget": func(w http.ResponseWriter, r *http.Request) {
		puts++
		put = nil
		_ = json.NewDecoder(r.Body).Decode(&put)
		b, _ := json.Marshal(put)
		_, _ = w.Write(b)
	}})

	// Without a terminal and without --yes: a usage error, nothing sent.
	if _, _, err := run(t, "billing", "budget", "set", "--soft-cap", "300"); !clierr.IsUsage(err) || puts != 0 {
		t.Fatalf("got %v, %d PUTs", err, puts)
	}
	out, stderr, err := run(t, "billing", "budget", "set", "--soft-cap", "$300.50", "--yes")
	if err != nil {
		t.Fatalf("%v %s", err, stderr)
	}
	// What it doesn't change is sent as it was: the API replaces the budget.
	if put["softCapCents"] != float64(30050) || put["hardCapCents"] != float64(50000) || fmt.Sprint(put["alertThresholds"]) != "[50 80 100]" {
		t.Errorf("PUT body %v", put)
	}
	if !strings.Contains(out, "$300.50") {
		t.Errorf("out %s", out)
	}
	if _, _, err := run(t, "billing", "budget", "set", "--hard-cap", "none", "--alerts", "90,50", "--yes"); err != nil {
		t.Fatal(err)
	}
	if _, ok := put["hardCapCents"]; ok || fmt.Sprint(put["alertThresholds"]) != "[50 90]" {
		t.Errorf("PUT body %v (hard cap removed, thresholds sorted)", put)
	}
	for _, args := range [][]string{
		{},
		{"--hard-cap", "lots"},
		{"--alerts", "0,50"},
		{"--soft-cap", "600"}, // above the hard cap of $500
	} {
		before := puts
		full := append([]string{"billing", "budget", "set", "--yes"}, args...)
		if _, _, err := run(t, full...); !clierr.IsUsage(err) || puts != before {
			t.Errorf("%v: got %v, want a usage error and no PUT", args, err)
		}
	}
}

func TestBillingReadOnlyTokenExits5(t *testing.T) {
	goldenEnv(t)
	goldenHandlers(t, map[string]http.HandlerFunc{"PUT /billing/budget": func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"type":"INSUFFICIENT_SCOPE","msg":"This token can only read billing information. Make this change in the dashboard."}`)
	}})
	_, _, err := run(t, "billing", "budget", "set", "--alerts", "50", "--yes")
	if e := Describe(err); e.Code != clierr.CodeInsufficientScope || e.ExitCode() != clierr.ExitAccess {
		t.Fatalf("got %v (code %s, exit %d)", err, e.Code, e.ExitCode())
	}
}
