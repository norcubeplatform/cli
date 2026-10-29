// Package resolve decides which profile, endpoints, organization,
// credentials and output format one command uses, and remembers where each
// value came from (for `norcube config list`).
//
// Precedence, highest first:
//
//	profile      --profile > NORCUBE_PROFILE > default_profile > "default"
//	organization --org > NORCUBE_ORG > project file (.langsync.json) > profile
//	endpoints    --endpoint-url svc=url (and --auth-url, --web-app)
//	             > NORCUBE_<SERVICE>_URL > profile endpoints > environment
//	credentials  --token-file > NORCUBE_TOKEN > profile credential_store > keyring
//	output       -o > NORCUBE_OUTPUT > profile output > [defaults] output > table
//
// The project file is read by the commands that support it (LangSync);
// this package reports whether --org or NORCUBE_ORG already decided.
package resolve

import (
	"fmt"
	"sort"
	"strings"

	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/config"
)

// Environment variables read here.
const (
	EnvProfile = "NORCUBE_PROFILE"
	EnvOrg     = "NORCUBE_ORG"
	EnvToken   = "NORCUBE_TOKEN" // #nosec G101 -- the variable's name, not a credential
	EnvOutput  = "NORCUBE_OUTPUT"
)

// Value is a resolved setting and where it came from, e.g. "flag --org",
// "env NORCUBE_ORG", "profile work", "environment production", "default".
type Value struct {
	Value  string
	Source string
}

// Input is everything a command was given.
type Input struct {
	File   *config.File
	Getenv func(string) string

	Profile string // --profile
	Org     string // --org
	// Output is -o; OutputSet tells an explicit -o table from the default.
	Output    string
	OutputSet bool
	// Endpoints are --endpoint-url service=url values, plus --auth-url and
	// --web-app mapped to their services.
	Endpoints map[string]string
	TokenFile string // --token-file
}

// Credentials sources beyond the profile's stores.
const (
	CredentialTokenFile = "token-file"
)

// Result is the resolved configuration of one command.
type Result struct {
	Profile Value
	// ProfileDefined is false when the profile isn't in the config file
	// yet (a first login, or the implicit "default").
	ProfileDefined bool
	Environment    Value
	Endpoints      map[string]Value

	// Org is the organization as given (slug or id); empty when none.
	// OrgOverride is true when it came from --org or NORCUBE_ORG, which
	// beat a project file.
	Org         Value
	OrgOverride bool

	// Credentials is keyring, file, env (NORCUBE_TOKEN) or token-file.
	Credentials Value
	// TokenFile is the --token-file path when Credentials is token-file.
	TokenFile string

	Output Value

	profile *config.Profile
}

// StoredProfile returns the profile's stored settings (an empty profile
// when it isn't defined).
func (r *Result) StoredProfile() *config.Profile {
	if r.profile == nil {
		return &config.Profile{}
	}
	return r.profile
}

// Resolve applies the precedence rules.
func Resolve(in Input) (*Result, error) {
	getenv := in.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	f := in.File
	if f == nil {
		f = &config.File{}
	}
	r := &Result{}

	// Profile.
	switch {
	case in.Profile != "":
		r.Profile = Value{in.Profile, "flag --profile"}
	case getenv(EnvProfile) != "":
		r.Profile = Value{getenv(EnvProfile), "env " + EnvProfile}
	case f.DefaultProfile != "":
		r.Profile = Value{f.DefaultProfile, "default_profile"}
	default:
		r.Profile = Value{config.DefaultProfileName, "default"}
	}
	if err := ValidateProfileName(r.Profile.Value); err != nil {
		return nil, clierr.Usage(err)
	}
	p, ok := f.Profiles[r.Profile.Value]
	r.ProfileDefined = ok && p != nil
	if !r.ProfileDefined {
		p = &config.Profile{}
	}
	r.profile = p
	profileSrc := "profile " + r.Profile.Value

	// Environment.
	r.Environment = Value{config.EnvProduction, "default"}
	if p.Environment != "" {
		if !config.IsEnvironment(p.Environment) {
			return nil, fmt.Errorf("profile %s: unknown environment %q (want production or custom; for other endpoints set environment = \"custom\" and list them with `norcube configure set endpoints.<service> <url>`)", r.Profile.Value, p.Environment)
		}
		r.Environment = Value{p.Environment, profileSrc}
	}

	// Endpoints.
	builtIn, ok := config.EnvironmentEndpoints(r.Environment.Value)
	builtInSrc := "environment " + r.Environment.Value
	if !ok {
		// Custom: anything the profile doesn't list falls back to production.
		builtIn, _ = config.EnvironmentEndpoints(config.EnvProduction)
		builtInSrc = "environment production (not set in custom profile)"
	}
	r.Endpoints = map[string]Value{}
	for svc := range in.Endpoints {
		if !config.IsService(svc) {
			return nil, clierr.Usage(fmt.Errorf("--endpoint-url: unknown service %q (one of %s)", svc, strings.Join(config.Services, ", ")))
		}
	}
	for _, svc := range config.Services {
		switch {
		case in.Endpoints[svc] != "":
			r.Endpoints[svc] = Value{strings.TrimRight(in.Endpoints[svc], "/"), "flag"}
		case getenv(config.ServiceEnvVar(svc)) != "":
			r.Endpoints[svc] = Value{strings.TrimRight(getenv(config.ServiceEnvVar(svc)), "/"), "env " + config.ServiceEnvVar(svc)}
		case p.Endpoints[svc] != "":
			r.Endpoints[svc] = Value{strings.TrimRight(p.Endpoints[svc], "/"), profileSrc}
		default:
			r.Endpoints[svc] = Value{builtIn[svc], builtInSrc}
		}
	}

	// Organization.
	switch {
	case strings.TrimSpace(in.Org) != "":
		r.Org = Value{strings.TrimSpace(in.Org), "flag --org"}
		r.OrgOverride = true
	case strings.TrimSpace(getenv(EnvOrg)) != "":
		r.Org = Value{strings.TrimSpace(getenv(EnvOrg)), "env " + EnvOrg}
		r.OrgOverride = true
	case p.Organization != "":
		r.Org = Value{p.Organization, profileSrc}
	}

	// Credentials.
	switch {
	case in.TokenFile != "":
		r.Credentials = Value{CredentialTokenFile, "flag --token-file"}
		r.TokenFile = in.TokenFile
	case getenv(EnvToken) != "":
		r.Credentials = Value{config.CredentialEnv, "env " + EnvToken}
	case p.CredentialStore != "":
		if !config.IsCredentialStore(p.CredentialStore) {
			return nil, fmt.Errorf("profile %s: unknown credential_store %q (want keyring, file or env)", r.Profile.Value, p.CredentialStore)
		}
		r.Credentials = Value{p.CredentialStore, profileSrc}
	default:
		r.Credentials = Value{config.CredentialKeyring, "default"}
	}

	// Output.
	switch {
	case in.OutputSet:
		r.Output = Value{in.Output, "flag --output"}
	case getenv(EnvOutput) != "":
		r.Output = Value{getenv(EnvOutput), "env " + EnvOutput}
	case p.Output != "":
		r.Output = Value{p.Output, profileSrc}
	case f.Defaults.Output != "":
		r.Output = Value{f.Defaults.Output, "defaults"}
	default:
		r.Output = Value{"table", "default"}
	}
	switch r.Output.Value {
	case "table", "json", "yaml", "text":
	default:
		return nil, clierr.Usage(fmt.Errorf("output format %q (from %s): want table, json, yaml or text", r.Output.Value, r.Output.Source))
	}
	return r, nil
}

// Config returns the command's configuration for the existing commands.
func (r *Result) Config() *config.Config {
	p := r.StoredProfile()
	c := &config.Config{
		Profile:     r.Profile.Value,
		Environment: r.Environment.Value,
		User:        p.User,
		ActiveOrg:   p.ActiveOrg(),
	}
	for svc, v := range r.Endpoints {
		c.SetEndpoint(svc, v.Value)
	}
	return c
}

// ValidateProfileName rejects names that can't be a TOML table key, a
// keyring item or a file name on every OS.
func ValidateProfileName(name string) error {
	if name == "" || len(name) > 64 {
		return fmt.Errorf("profile name %q: use 1 to 64 characters", name)
	}
	for _, c := range name {
		if !nameChar(c) {
			return fmt.Errorf("profile name %q: use letters, digits, '-', '_' and '.' only", name)
		}
	}
	return nil
}

// ParseEndpoints turns --endpoint-url values ("service=url") into a map.
func ParseEndpoints(values []string) (map[string]string, error) {
	out := map[string]string{}
	for _, v := range values {
		svc, url, ok := strings.Cut(v, "=")
		svc = strings.TrimSpace(svc)
		if !ok || svc == "" || strings.TrimSpace(url) == "" {
			return nil, clierr.Usage(fmt.Errorf("--endpoint-url %q: want service=url, e.g. langsync=http://localhost:4600", v))
		}
		if !config.IsService(svc) {
			return nil, clierr.Usage(fmt.Errorf("--endpoint-url %q: unknown service %q (one of %s)", v, svc, strings.Join(config.Services, ", ")))
		}
		out[svc] = strings.TrimSpace(url)
	}
	return out, nil
}

// ProfileNames returns the profiles defined in f, sorted.
func ProfileNames(f *config.File) []string {
	names := make([]string, 0, len(f.Profiles))
	for n := range f.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// nameChar reports whether c may appear in a profile name.
func nameChar(c rune) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.'
}
