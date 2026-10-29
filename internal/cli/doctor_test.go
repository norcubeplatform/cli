package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/norcubeplatform/cli/internal/config"
	"github.com/norcubeplatform/cli/internal/registry"
)

// pointEveryServiceAtBackend keeps doctor's probes on the fake backend.
func pointEveryServiceAtBackend(t *testing.T) {
	t.Helper()
	for _, key := range registry.Keys() {
		t.Setenv(config.ServiceEnvVar(key), backend.srv.URL)
	}
}

func doctorReport(t *testing.T, args ...string) (map[string]doctorCheck, error) {
	t.Helper()
	out, _, err := run(t, append([]string{"doctor", "-o", "json"}, args...)...)
	var checks []doctorCheck
	if jerr := json.Unmarshal([]byte(out), &checks); jerr != nil {
		t.Fatalf("doctor output %q: %v", out, jerr)
	}
	byName := map[string]doctorCheck{}
	for _, c := range checks {
		byName[c.Check] = c
	}
	return byName, err
}

func TestDoctorHealthyProfile(t *testing.T) {
	pointEveryServiceAtBackend(t)
	signIn(t)
	checks, err := doctorReport(t)
	if err != nil {
		t.Fatalf("doctor failed on a healthy setup: %v (%v)", err, checks)
	}
	for _, name := range []string{"cli version", "config file", "profile", "organization", "credentials", "clock", "session", "endpoint auth", "endpoint langsync", "token backup", "token langsync", "token domainradar"} {
		c, ok := checks[name]
		if !ok {
			t.Errorf("no %q check", name)
			continue
		}
		if c.Status != checkOK {
			t.Errorf("%s: %s (%s)", name, c.Status, c.Detail)
		}
	}
}

func TestDoctorReportsADeadSession(t *testing.T) {
	pointEveryServiceAtBackend(t)
	signIn(t)
	backend.mu.Lock()
	backend.refresh = ""
	backend.mu.Unlock()
	checks, err := doctorReport(t)
	if err == nil {
		t.Fatal("doctor passed with a revoked session")
	}
	if c := checks["session"]; c.Status != checkFail || !strings.Contains(c.Detail, "login") {
		t.Errorf("session check: %+v", c)
	}
}

func TestDoctorOffline(t *testing.T) {
	signIn(t)
	checks, err := doctorReport(t, "--offline")
	if err != nil {
		t.Fatal(err)
	}
	if checks["network"].Status != checkSkip {
		t.Errorf("--offline still probed: %v", checks)
	}
	for name := range checks {
		if strings.HasPrefix(name, "endpoint ") || strings.HasPrefix(name, "token ") {
			t.Errorf("--offline ran %q", name)
		}
	}
}
