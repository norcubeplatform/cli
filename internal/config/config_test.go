package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// useFile points the package at a config file in a temp dir.
func useFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(EnvConfigFile, path)
	return path
}

// A version 1 file as the CLI wrote it before profiles.
const v1Production = `auth = 'https://auth.api.norcube.com'
billing = ''
domainradar = ''
langsync = 'https://langsync.api.norcube.com'
prompthub = ''
snapdb = 'https://snapdb.api.norcube.com/app/v1'
web_app = 'https://app.norcube.com'

[active_org]
id = 'b2a446ea-7686-4cba-9f04-2d6750708d4c'
name = 'Jetlio'
slug = 'jetlio'

[user]
email = 'dev@example.com'
id = '7d7a739e-b9fd-4a4f-85df-880264d3422c'
name = 'Dev'
`

func TestMigrateV1(t *testing.T) {
	tests := []struct {
		name      string
		file      string
		env       string
		endpoints map[string]string
		org       Organization
	}{
		{
			name: "production",
			file: v1Production,
			env:  EnvProduction,
			org:  Organization{ID: "b2a446ea-7686-4cba-9f04-2d6750708d4c", Slug: "jetlio", Name: "Jetlio"},
		},
		{
			name: "non-production URLs become a custom profile",
			file: `auth = 'https://auth.example.test'
web_app = 'https://app.example.test'
langsync = 'https://langsync.example.test'
`,
			env: EnvCustom,
			endpoints: map[string]string{
				ServiceAuth:     "https://auth.example.test",
				ServiceWebApp:   "https://app.example.test",
				ServiceLangsync: "https://langsync.example.test",
			},
		},
		{
			name: "a changed service URL stays as an override",
			file: `auth = 'https://auth.api.norcube.com'
langsync = 'http://localhost:4600'
`,
			env:       EnvProduction,
			endpoints: map[string]string{ServiceLangsync: "http://localhost:4600"},
		},
		{
			name: "unknown auth URL is a custom profile",
			file: `auth = 'http://localhost:4100'
web_app = 'http://localhost:51730'
`,
			env:       EnvCustom,
			endpoints: map[string]string{ServiceAuth: "http://localhost:4100", ServiceWebApp: "http://localhost:51730"},
		},
		{
			name: "org known by id only",
			file: "[active_org]\nid = '0b9d2b8e-1c1f-4c3e-9a51-6d1c2f9a7e01'\n",
			env:  EnvProduction,
			org:  Organization{ID: "0b9d2b8e-1c1f-4c3e-9a51-6d1c2f9a7e01"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := useFile(t, tt.file)
			f, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if f.SchemaVersion != SchemaVersion || f.DefaultProfile != DefaultProfileName {
				t.Fatalf("schema %d, default %q", f.SchemaVersion, f.DefaultProfile)
			}
			p := f.Profiles[DefaultProfileName]
			if p == nil {
				t.Fatal("no default profile")
			}
			if p.Environment != tt.env {
				t.Errorf("environment %q, want %q", p.Environment, tt.env)
			}
			if len(p.Endpoints) != len(tt.endpoints) {
				t.Errorf("endpoints %v, want %v", p.Endpoints, tt.endpoints)
			}
			for k, v := range tt.endpoints {
				if p.Endpoints[k] != v {
					t.Errorf("endpoint %s = %q, want %q", k, p.Endpoints[k], v)
				}
			}
			if got := p.ActiveOrg(); got != tt.org {
				t.Errorf("org %+v, want %+v", got, tt.org)
			}
			// Written back as version 2, with the original kept.
			b, _ := os.ReadFile(path)
			if !strings.Contains(string(b), "schema_version = 2") {
				t.Errorf("not rewritten:\n%s", b)
			}
			backups, _ := filepath.Glob(path + ".bak-*")
			if len(backups) != 1 {
				t.Fatalf("backups %v", backups)
			}
			orig, _ := os.ReadFile(backups[0])
			if string(orig) != tt.file {
				t.Error("backup differs from the original")
			}
		})
	}
}

func TestMigrateV1KeepsUser(t *testing.T) {
	useFile(t, v1Production)
	f, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	u := f.Profiles[DefaultProfileName].User
	if u.Email != "dev@example.com" || u.ID == "" || u.Name != "Dev" {
		t.Fatalf("user %+v", u)
	}
}

// Loading a migrated file again changes nothing and makes no new backup.
func TestMigrationIsIdempotent(t *testing.T) {
	path := useFile(t, v1Production)
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(path)
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Fatalf("second load changed the file:\n%s\n---\n%s", first, second)
	}
	if backups, _ := filepath.Glob(path + ".bak-*"); len(backups) != 1 {
		t.Fatalf("backups %v", backups)
	}
}

func TestMissingFileIsEmpty(t *testing.T) {
	path := useFile(t, "")
	f, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Profiles) != 0 {
		t.Fatalf("profiles %v", f.Profiles)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("loading created the file")
	}
}

// Keys this CLI doesn't know survive a write.
func TestUnknownKeysPreserved(t *testing.T) {
	path := useFile(t, `schema_version = 2
default_profile = "work"
future_setting = "keep me"

[defaults]
output = "json"
color = "never"

[profiles.work]
environment = "production"
organization = "acme"
telemetry = false

[profiles.work.endpoints]
cms = "https://cms.example"
`)
	if _, err := Update(func(f *File) error {
		f.Profile("work").Organization = "other"
		f.Profile("selfhosted").Environment = EnvCustom
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	var raw map[string]any
	if err := toml.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["future_setting"] != "keep me" {
		t.Errorf("top-level key lost:\n%s", b)
	}
	defaults := raw["defaults"].(map[string]any)
	if defaults["color"] != "never" || defaults["output"] != "json" {
		t.Errorf("defaults %v", defaults)
	}
	work := raw["profiles"].(map[string]any)["work"].(map[string]any)
	if work["telemetry"] != false || work["organization"] != "other" {
		t.Errorf("work %v", work)
	}
	if work["endpoints"].(map[string]any)["cms"] != "https://cms.example" {
		t.Errorf("unknown endpoint lost: %v", work["endpoints"])
	}
	if raw["profiles"].(map[string]any)["selfhosted"] == nil {
		t.Error("new profile not written")
	}
}

func TestRemovedProfileAndEmptyValuesAreDropped(t *testing.T) {
	path := useFile(t, `schema_version = 2
[profiles.old]
environment = "custom"
[profiles.work]
environment = "production"
organization = "acme"
[profiles.work.user]
email = "a@example.com"
`)
	if _, err := Update(func(f *File) error {
		delete(f.Profiles, "old")
		f.Profile("work").User = User{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	if strings.Contains(s, "old") || strings.Contains(s, "a@example.com") || strings.Contains(s, "[profiles.work.user]") {
		t.Fatalf("stale values kept:\n%s", s)
	}
}

func TestNewerSchemaIsRefused(t *testing.T) {
	useFile(t, "schema_version = 3\n")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "newer norcube") {
		t.Fatalf("want a newer-version error, got %v", err)
	}
}

func TestWrittenFileIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	path := useFile(t, "")
	if _, err := Update(func(f *File) error { f.Profile("work"); return nil }); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
}

func TestPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvConfigFile, "")
	t.Setenv("HOME", dir)
	t.Setenv("AppData", dir)

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	got, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "xdg", "norcube", "config.toml"); got != want {
		t.Errorf("XDG: %q, want %q", got, want)
	}

	// A config that predates XDG support on macOS and Windows keeps
	// being used from the OS directory.
	if runtime.GOOS != "linux" {
		osDir, _ := os.UserConfigDir()
		legacy := filepath.Join(osDir, "norcube", "config.toml")
		if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(legacy, []byte("schema_version = 2\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, _ := Path(); got != legacy {
			t.Errorf("existing OS-directory config: %q, want %q", got, legacy)
		}
	}

	t.Setenv(EnvConfigFile, filepath.Join(dir, "explicit.toml"))
	if got, _ := Path(); got != filepath.Join(dir, "explicit.toml") {
		t.Errorf("NORCUBE_CONFIG_FILE: %q", got)
	}
}

func TestEnvironmentFor(t *testing.T) {
	prod, _ := EnvironmentEndpoints(EnvProduction)
	env, overrides := EnvironmentFor(prod)
	if env != EnvProduction || len(overrides) != 0 {
		t.Fatalf("production: %s %v", env, overrides)
	}
	other := map[string]string{ServiceAuth: "https://auth.example.test", ServiceSnapDB: "https://backup.example.test"}
	env, overrides = EnvironmentFor(other)
	if env != EnvCustom || len(overrides) != 2 || overrides[ServiceAuth] != other[ServiceAuth] {
		t.Fatalf("custom: %s %v", env, overrides)
	}
}

func TestEveryEnvironmentListsEveryService(t *testing.T) {
	for _, env := range []string{EnvProduction} {
		e, _ := EnvironmentEndpoints(env)
		for _, svc := range Services {
			if !strings.HasPrefix(e[svc], "https://") {
				t.Errorf("%s has no %s endpoint", env, svc)
			}
		}
	}
}
