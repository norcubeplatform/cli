package cli

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/config"
	"github.com/norcubeplatform/cli/internal/credstore"
	"github.com/norcubeplatform/cli/internal/resolve"
)

// isolatedConfig gives the test its own config file (and with it its own
// lock and credential directories) and clears the endpoint variables
// TestMain set, so profiles decide the endpoints.
func isolatedConfig(t *testing.T, fn func(f *config.File)) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv(config.EnvConfigFile, path)
	t.Setenv("NORCUBE_AUTH_URL", "")
	t.Setenv("NORCUBE_SNAPDB_URL", "")
	if _, err := config.Update(func(f *config.File) error { fn(f); return nil }); err != nil {
		t.Fatal(err)
	}
	return path
}

func newBackend(t *testing.T) *fakeBackend {
	t.Helper()
	b := &fakeBackend{refresh: "refresh-0"}
	b.srv = httptest.NewServer(b)
	t.Cleanup(b.srv.Close)
	return b
}

// customProfile points a profile's auth and Backup endpoints at b.
func customProfile(f *config.File, name string, b *fakeBackend, org config.Organization) {
	p := f.Profile(name)
	p.Environment = config.EnvCustom
	p.Endpoints = map[string]string{config.ServiceAuth: b.srv.URL, config.ServiceSnapDB: b.srv.URL}
	p.User = config.User{ID: "user-" + name, Email: name + "@example.com"}
	p.SetActiveOrg(org)
}

// Two profiles (two accounts with their own endpoints and organizations),
// one process: each command talks to its profile's backend with its
// profile's session.
func TestTwoProfilesSideBySide(t *testing.T) {
	work, client := newBackend(t), newBackend(t)
	isolatedConfig(t, func(f *config.File) {
		f.DefaultProfile = "p-work"
		customProfile(f, "p-work", work, config.Organization{ID: homeOrgID, Slug: "home"})
		customProfile(f, "p-client", client, config.Organization{ID: acmeOrgID, Slug: "acme"})
	})
	for name, b := range map[string]*fakeBackend{"p-work": work, "p-client": client} {
		if err := auth.SaveRefreshToken(name, b.srv.URL, "refresh-0"); err != nil {
			t.Fatal(err)
		}
	}

	if _, stderr, err := run(t, "backup", "list"); err != nil {
		t.Fatalf("default profile: %v\n%s", err, stderr)
	}
	if _, stderr, err := run(t, "backup", "list", "--profile", "p-client"); err != nil {
		t.Fatalf("second profile: %v\n%s", err, stderr)
	}
	t.Setenv(resolve.EnvProfile, "p-client")
	if _, stderr, err := run(t, "backup", "list", "--org", "home"); err != nil {
		t.Fatalf("NORCUBE_PROFILE + --org: %v\n%s", err, stderr)
	}

	if got := work.mintedTokens(); len(got) != 1 || got[0] != auth.AudienceSnapDB+"/"+homeOrgID {
		t.Errorf("default profile's backend minted %v", got)
	}
	want := []string{auth.AudienceSnapDB + "/" + acmeOrgID, auth.AudienceAuth + "/" + acmeOrgID, auth.AudienceSnapDB + "/" + homeOrgID}
	if got := client.mintedTokens(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("second profile's backend minted %v, want %v", got, want)
	}

	// Signing one profile out leaves the other alone.
	t.Setenv(resolve.EnvProfile, "")
	if _, _, err := run(t, "logout", "--profile", "p-client"); err != nil {
		t.Fatal(err)
	}
	if auth.HasCredentials("p-client", client.srv.URL) {
		t.Error("p-client still signed in")
	}
	if !auth.HasCredentials("p-work", work.srv.URL) {
		t.Error("logout of p-client signed p-work out")
	}
	if _, _, err := run(t, "auth", "status"); err != nil {
		t.Errorf("default profile reported signed out: %v", err)
	}
	_, _, err := run(t, "auth", "status", "--profile", "p-client")
	if !errors.Is(err, auth.ErrLoginRequired) {
		t.Errorf("auth status for a signed-out profile: %v", err)
	}
}

func TestLogoutAll(t *testing.T) {
	a, b := newBackend(t), newBackend(t)
	path := isolatedConfig(t, func(f *config.File) {
		customProfile(f, "la-one", a, config.Organization{ID: homeOrgID, Slug: "home"})
		customProfile(f, "la-two", b, config.Organization{ID: acmeOrgID, Slug: "acme"})
	})
	_ = auth.SaveRefreshToken("la-one", a.srv.URL, "refresh-0")
	_ = auth.SaveRefreshToken("la-two", b.srv.URL, "refresh-0")

	if _, _, err := run(t, "logout", "--all"); err != nil {
		t.Fatal(err)
	}
	if auth.HasCredentials("la-one", a.srv.URL) || auth.HasCredentials("la-two", b.srv.URL) {
		t.Fatal("a profile is still signed in")
	}
	cfg, _ := os.ReadFile(path)
	if strings.Contains(string(cfg), "@example.com") {
		t.Fatalf("users kept after logout --all:\n%s", cfg)
	}
	if !strings.Contains(string(cfg), "[profiles.la-two]") {
		t.Fatal("logout removed the profile itself")
	}
}

// A profile with credential_store = "file" keeps its session in
// credentials/<profile>.json next to the config, not in the keyring.
func TestFileCredentialStore(t *testing.T) {
	b := newBackend(t)
	path := isolatedConfig(t, func(f *config.File) {
		customProfile(f, "fs", b, config.Organization{ID: homeOrgID, Slug: "home"})
		f.Profile("fs").CredentialStore = config.CredentialFile
	})
	credDir := filepath.Join(filepath.Dir(path), "credentials")
	auth.UseStore("fs", credstore.File{Dir: credDir})
	if err := auth.SaveRefreshToken("fs", b.srv.URL, "refresh-0"); err != nil {
		t.Fatal(err)
	}
	auth.UseStore("fs", nil)
	t.Cleanup(func() { auth.UseStore("fs", nil) })

	if _, stderr, err := run(t, "backup", "list", "--profile", "fs"); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	blob, err := os.ReadFile(filepath.Join(credDir, "profile-fs.json"))
	if err != nil {
		t.Fatal(err)
	}
	// The exchange rotated the refresh token, and the file has the new one.
	if !strings.Contains(string(blob), `"refresh_token":"refresh-1"`) {
		t.Fatalf("rotated token not stored in the file: %s", blob)
	}
	if _, err := (credstore.Keyring{}).Get("profile:fs"); !errors.Is(err, credstore.ErrNotFound) {
		t.Fatalf("session also written to the keyring: %v", err)
	}

	// Other profiles keep using the keyring in the same process.
	other := newBackend(t)
	if _, err := config.Update(func(f *config.File) error {
		customProfile(f, "kr", other, config.Organization{ID: homeOrgID, Slug: "home"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := auth.SaveRefreshToken("kr", other.srv.URL, "refresh-0"); err != nil {
		t.Fatal(err)
	}
	out, _, err := run(t, "auth", "status", "--all-profiles", "--profile", "fs", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []authStatus
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if !r.SignedIn {
			t.Errorf("profile %s (%s store) reported signed out", r.Profile, r.CredentialStore)
		}
	}
	if _, err := (credstore.Keyring{}).Get("profile:kr"); err != nil {
		t.Errorf("keyring profile not in the keyring: %v", err)
	}
}

// NORCUBE_TOKEN is sent as is; nothing is exchanged or stored.
func TestEnvToken(t *testing.T) {
	b := newBackend(t)
	isolatedConfig(t, func(f *config.File) {
		customProfile(f, "ci", b, config.Organization{ID: homeOrgID, Slug: "home"})
	})
	t.Setenv(resolve.EnvToken, "static-token")
	if _, stderr, err := run(t, "backup", "list", "--profile", "ci"); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if got := b.mintedTokens(); len(got) != 0 {
		t.Fatalf("exchanged tokens with NORCUBE_TOKEN set: %v", got)
	}
	if auth.HasCredentials("ci", b.srv.URL) {
		t.Fatal("stored credentials")
	}
}

func TestConfigListSources(t *testing.T) {
	b := newBackend(t)
	isolatedConfig(t, func(f *config.File) {
		f.DefaultProfile = "cl"
		customProfile(f, "cl", b, config.Organization{ID: homeOrgID, Slug: "home"})
	})
	t.Setenv("NORCUBE_LANGSYNC_URL", "http://env-langsync")
	stdout, _, err := run(t, "config", "list", "-o", "json", "--org", "acme")
	if err != nil {
		t.Fatal(err)
	}
	var rows []setting
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("%v\n%s", err, stdout)
	}
	got := map[string]setting{}
	for _, r := range rows {
		got[r.Name] = r
	}
	checks := map[string][2]string{
		"profile":           {"cl", "default_profile"},
		"environment":       {"custom", "profile cl"},
		"endpoint.auth":     {b.srv.URL, "profile cl"},
		"endpoint.langsync": {"http://env-langsync", "env NORCUBE_LANGSYNC_URL"},
		"organization":      {"acme", "flag --org"},
		"output":            {"json", "flag --output"},
		"credential_store":  {"keyring", "default"},
	}
	for name, want := range checks {
		if g := got[name]; g.Value != want[0] || g.Source != want[1] {
			t.Errorf("%s = %+v, want %v", name, g, want)
		}
	}
}

func TestConfigureSetGetUnset(t *testing.T) {
	path := isolatedConfig(t, func(*config.File) {})
	steps := [][]string{
		{"configure", "set", "environment", "custom", "--profile", "cs"},
		{"configure", "set", "endpoints.langsync", "http://localhost:4600/", "--profile", "cs"},
		{"configure", "set", "output", "json", "--profile", "cs"},
		{"configure", "set", "default_profile", "cs"},
		{"configure", "set", "defaults.max_attempts", "5"},
		{"configure", "set", "organization", "acme"},
	}
	for _, args := range steps {
		if _, stderr, err := run(t, args...); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, stderr)
		}
	}
	get := func(args ...string) string {
		// The cs profile prints JSON by default; -o text is the plain value.
		out, _, err := run(t, append([]string{"configure", "get", "-o", "text"}, args...)...)
		if err != nil {
			t.Fatalf("get %v: %v", args, err)
		}
		return strings.TrimSpace(out)
	}
	if v := get("environment"); v != "custom" {
		t.Errorf("environment %q (default_profile should select cs)", v)
	}
	if v := get("endpoints.langsync"); v != "http://localhost:4600" {
		t.Errorf("endpoint %q", v)
	}
	if v := get("organization"); v != "acme" {
		t.Errorf("organization %q", v)
	}
	if v := get("defaults.max_attempts"); v != "5" {
		t.Errorf("max_attempts %q", v)
	}
	if _, _, err := run(t, "configure", "unset", "endpoints.langsync"); err != nil {
		t.Fatal(err)
	}
	if v := get("endpoints.langsync"); v != "" {
		t.Errorf("unset left %q", v)
	}
	for _, bad := range [][]string{
		{"configure", "set", "environment", "prod"},
		{"configure", "set", "environment", "retired"},
		{"configure", "set", "nope", "x"},
		{"configure", "set", "endpoints.cms", "http://x"},
		{"configure", "set", "defaults.pager", "maybe"},
		{"configure", "set", "default_profile", "a/b"},
	} {
		if _, _, err := run(t, bad...); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "default_profile = 'cs'") && !strings.Contains(string(b), `default_profile = "cs"`) {
		t.Errorf("config:\n%s", b)
	}
}

func TestConfigProfiles(t *testing.T) {
	b := newBackend(t)
	isolatedConfig(t, func(f *config.File) {
		f.DefaultProfile = "pa"
		customProfile(f, "pa", b, config.Organization{ID: homeOrgID, Slug: "home"})
		f.Profile("pb").Environment = config.EnvProduction
	})
	out, _, err := run(t, "config", "profiles", "--profile", "pb", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []profileRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || !rows[0].Default || rows[0].Selected || !rows[1].Selected || rows[1].Environment != "production" {
		t.Fatalf("%+v", rows)
	}
}

// Logging an existing profile in to another environment's URL would
// replace its session; it's refused with a pointer to a separate profile.
func TestLoginRefusesForeignEnvironment(t *testing.T) {
	f := &config.File{Profiles: map[string]*config.Profile{"default": {Environment: config.EnvProduction}}}
	res, err := resolve.Resolve(resolve.Input{
		File: f,
		Getenv: func(k string) string {
			return map[string]string{"NORCUBE_AUTH_URL": "https://auth.example.test"}[k]
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := checkLoginTarget(res); err == nil || !strings.Contains(err.Error(), "separate profile") {
		t.Fatalf("want a refusal, got %v", err)
	}
	// A new profile may be created for any endpoints.
	res, _ = resolve.Resolve(resolve.Input{File: f, Profile: "selfhosted", Getenv: func(k string) string {
		return map[string]string{"NORCUBE_AUTH_URL": "https://auth.example.test"}[k]
	}})
	if err := checkLoginTarget(res); err != nil {
		t.Fatalf("new profile: %v", err)
	}
	// It is recorded as a custom profile listing only the endpoint that was
	// named; the other services fall back to production.
	env, endpoints := newProfileEnvironment(res)
	if env != config.EnvCustom || len(endpoints) != 1 || endpoints[config.ServiceAuth] != "https://auth.example.test" {
		t.Fatalf("new profile recorded as %s %v", env, endpoints)
	}
	res, _ = resolve.Resolve(resolve.Input{File: f, Profile: "fresh"})
	if env, endpoints := newProfileEnvironment(res); env != config.EnvProduction || len(endpoints) != 0 {
		t.Fatalf("plain login: %s %v", env, endpoints)
	}
}

func TestInvalidOutputIsUsageError(t *testing.T) {
	isolatedConfig(t, func(*config.File) {})
	_, _, err := run(t, "config", "path", "-o", "xml")
	if err == nil {
		t.Fatal("-o xml accepted")
	}
}
