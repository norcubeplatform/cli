package resolve

import (
	"strings"
	"testing"

	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/config"
)

func file() *config.File {
	return &config.File{
		SchemaVersion:  2,
		DefaultProfile: "work",
		Defaults:       config.Defaults{Output: "yaml"},
		Profiles: map[string]*config.Profile{
			"work": {
				Environment:  config.EnvProduction,
				Organization: "acme",
				OrgID:        "org-acme",
				Endpoints:    map[string]string{config.ServiceLangsync: "http://localhost:4600"},
			},
			"client": {
				Environment:     config.EnvProduction,
				Organization:    "client-acme",
				CredentialStore: config.CredentialFile,
				Output:          "json",
			},
			"local": {
				Environment: config.EnvCustom,
				Endpoints:   map[string]string{config.ServiceAuth: "http://localhost:4100"},
			},
		},
	}
}

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestProfilePrecedence(t *testing.T) {
	tests := []struct {
		name    string
		flag    string
		env     map[string]string
		noDef   bool
		want    string
		source  string
		defined bool
	}{
		{"flag wins", "client", map[string]string{EnvProfile: "local"}, false, "client", "flag --profile", true},
		{"env over default_profile", "", map[string]string{EnvProfile: "local"}, false, "local", "env NORCUBE_PROFILE", true},
		{"default_profile", "", nil, false, "work", "default_profile", true},
		{"implicit default", "", nil, true, "default", "default", false},
		{"unknown profile resolves, undefined", "nope", nil, false, "nope", "flag --profile", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := file()
			if tt.noDef {
				f.DefaultProfile = ""
			}
			r, err := Resolve(Input{File: f, Getenv: env(tt.env), Profile: tt.flag})
			if err != nil {
				t.Fatal(err)
			}
			if r.Profile.Value != tt.want || r.Profile.Source != tt.source || r.ProfileDefined != tt.defined {
				t.Fatalf("got %+v defined=%v", r.Profile, r.ProfileDefined)
			}
		})
	}
}

func TestInvalidProfileName(t *testing.T) {
	_, err := Resolve(Input{File: file(), Profile: "../etc"})
	if !clierr.IsUsage(err) {
		t.Fatalf("want usage error, got %v", err)
	}
}

func TestOrgPrecedence(t *testing.T) {
	tests := []struct {
		name     string
		flag     string
		env      map[string]string
		profile  string
		want     string
		source   string
		override bool
	}{
		{"flag wins over env", "flag-org", map[string]string{EnvOrg: "env-org"}, "work", "flag-org", "flag --org", true},
		{"env over profile", "", map[string]string{EnvOrg: "env-org"}, "work", "env-org", "env NORCUBE_ORG", true},
		{"profile", "", nil, "work", "acme", "profile work", false},
		{"none", "", nil, "local", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := Resolve(Input{File: file(), Getenv: env(tt.env), Profile: tt.profile, Org: tt.flag})
			if err != nil {
				t.Fatal(err)
			}
			if r.Org.Value != tt.want || r.Org.Source != tt.source || r.OrgOverride != tt.override {
				t.Fatalf("got %+v override=%v", r.Org, r.OrgOverride)
			}
		})
	}
}

func TestEndpointPrecedence(t *testing.T) {
	prod, _ := config.EnvironmentEndpoints(config.EnvProduction)
	tests := []struct {
		name    string
		profile string
		flags   map[string]string
		env     map[string]string
		svc     string
		want    string
		source  string
	}{
		{"flag beats env", "work", map[string]string{"langsync": "http://flag/"}, map[string]string{"NORCUBE_LANGSYNC_URL": "http://env"}, "langsync", "http://flag", "flag"},
		{"env beats profile", "work", nil, map[string]string{"NORCUBE_LANGSYNC_URL": "http://env"}, "langsync", "http://env", "env NORCUBE_LANGSYNC_URL"},
		{"profile override", "work", nil, nil, "langsync", "http://localhost:4600", "profile work"},
		{"environment default", "work", nil, nil, "snapdb", prod["snapdb"], "environment production"},
		{"second profile, same environment", "client", nil, nil, "auth", prod["auth"], "environment production"},
		{"web app env var", "client", nil, map[string]string{"NORCUBE_WEB_APP": "http://web"}, "web_app", "http://web", "env NORCUBE_WEB_APP"},
		{"custom lists its own", "local", nil, nil, "auth", "http://localhost:4100", "profile local"},
		{"custom falls back to production", "local", nil, nil, "billing", prod["billing"], "environment production (not set in custom profile)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := Resolve(Input{File: file(), Getenv: env(tt.env), Profile: tt.profile, Endpoints: tt.flags})
			if err != nil {
				t.Fatal(err)
			}
			got := r.Endpoints[tt.svc]
			if got.Value != tt.want || got.Source != tt.source {
				t.Fatalf("got %+v, want %q from %q", got, tt.want, tt.source)
			}
		})
	}
}

func TestCredentialPrecedence(t *testing.T) {
	tests := []struct {
		name      string
		profile   string
		tokenFile string
		env       map[string]string
		want      string
		source    string
	}{
		{"token file beats env", "work", "/tmp/tok", map[string]string{EnvToken: "t"}, CredentialTokenFile, "flag --token-file"},
		{"env token beats profile", "client", "", map[string]string{EnvToken: "t"}, config.CredentialEnv, "env NORCUBE_TOKEN"},
		{"profile store", "client", "", nil, config.CredentialFile, "profile client"},
		{"keyring by default", "work", "", nil, config.CredentialKeyring, "default"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := Resolve(Input{File: file(), Getenv: env(tt.env), Profile: tt.profile, TokenFile: tt.tokenFile})
			if err != nil {
				t.Fatal(err)
			}
			if r.Credentials.Value != tt.want || r.Credentials.Source != tt.source {
				t.Fatalf("got %+v", r.Credentials)
			}
		})
	}
}

func TestOutputPrecedence(t *testing.T) {
	tests := []struct {
		name    string
		profile string
		flag    string
		flagSet bool
		env     map[string]string
		noDef   bool
		want    string
		source  string
	}{
		{"explicit flag, even table", "client", "table", true, map[string]string{EnvOutput: "json"}, false, "table", "flag --output"},
		{"env beats profile", "client", "table", false, map[string]string{EnvOutput: "yaml"}, false, "yaml", "env NORCUBE_OUTPUT"},
		{"profile beats defaults", "client", "table", false, nil, false, "json", "profile client"},
		{"defaults", "work", "table", false, nil, false, "yaml", "defaults"},
		{"built-in", "work", "table", false, nil, true, "table", "default"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := file()
			if tt.noDef {
				f.Defaults.Output = ""
			}
			r, err := Resolve(Input{File: f, Getenv: env(tt.env), Profile: tt.profile, Output: tt.flag, OutputSet: tt.flagSet})
			if err != nil {
				t.Fatal(err)
			}
			if r.Output.Value != tt.want || r.Output.Source != tt.source {
				t.Fatalf("got %+v", r.Output)
			}
		})
	}
}

func TestInvalidValues(t *testing.T) {
	bad := func(mut func(f *config.File)) error {
		f := file()
		mut(f)
		_, err := Resolve(Input{File: f, Profile: "work"})
		return err
	}
	if bad(func(f *config.File) { f.Profiles["work"].Environment = "prod" }) == nil {
		t.Error("unknown environment accepted")
	}
	if bad(func(f *config.File) { f.Profiles["work"].CredentialStore = "vault" }) == nil {
		t.Error("unknown credential store accepted")
	}
	if bad(func(f *config.File) { f.Defaults.Output = "xml" }) == nil {
		t.Error("unknown output accepted")
	}
	if _, err := Resolve(Input{File: file(), Endpoints: map[string]string{"cms": "x"}}); !clierr.IsUsage(err) {
		t.Errorf("unknown --endpoint-url service: %v", err)
	}
}

func TestParseEndpoints(t *testing.T) {
	got, err := ParseEndpoints([]string{"langsync=http://localhost:4600", "auth = http://a"})
	if err != nil {
		t.Fatal(err)
	}
	if got["langsync"] != "http://localhost:4600" || got["auth"] != "http://a" {
		t.Fatalf("%v", got)
	}
	for _, v := range []string{"langsync", "=http://x", "nope=http://x", "langsync="} {
		if _, err := ParseEndpoints([]string{v}); !clierr.IsUsage(err) {
			t.Errorf("%q: want usage error, got %v", v, err)
		}
	}
}

func TestConfigView(t *testing.T) {
	r, err := Resolve(Input{File: file(), Profile: "work"})
	if err != nil {
		t.Fatal(err)
	}
	c := r.Config()
	if c.Profile != "work" || c.Langsync != "http://localhost:4600" || c.ActiveOrg.ID != "org-acme" || c.ActiveOrg.Slug != "acme" {
		t.Fatalf("%+v", c)
	}
}

// An environment the CLI doesn't know (such as one removed in a later
// release) is an error that names the valid ones and the way out, not a
// crash or a silent fallback to production.
func TestUnknownEnvironmentIsAClearError(t *testing.T) {
	f := file()
	f.Profiles["old"] = &config.Profile{Environment: "retired"}
	_, err := Resolve(Input{File: f, Getenv: env(nil), Profile: "old"})
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{`"retired"`, "production or custom", "endpoints.<service>"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}
