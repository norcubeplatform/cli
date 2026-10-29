// Package config reads and writes the CLI's configuration file.
//
// One TOML file (schema version 2) holds named profiles. A profile picks
// an environment (production, or custom endpoints), an
// organization, and where its credentials live. Secrets never go in this
// file: refresh and access tokens live in the credential store the profile
// names (the OS keyring by default).
//
//	schema_version = 2
//	default_profile = "work"
//
//	[defaults]
//	output = "table"
//
//	[profiles.work]
//	environment = "production"
//	organization = "acme"
//	org_id = "0b9d2b8e-…"
//
//	[profiles.selfhosted]
//	environment = "custom"
//	organization = "acme"
//
// Which profile, organization and endpoints a command uses is decided by
// package resolve, from flags, environment variables and this file.
package config

import (
	"errors"
	"os"
	"path/filepath"
)

// SchemaVersion is the version this CLI writes. Files without a version
// are the flat version 1 format and are migrated on load.
const SchemaVersion = 2

// DefaultProfileName is used when nothing names a profile.
const DefaultProfileName = "default"

// Credential stores a profile can use.
const (
	CredentialKeyring = "keyring"
	CredentialFile    = "file"
	// CredentialEnv reads an access token from NORCUBE_TOKEN and never
	// writes anything.
	CredentialEnv = "env"
)

// IsCredentialStore reports whether v names a credential store.
func IsCredentialStore(v string) bool {
	return v == CredentialKeyring || v == CredentialFile || v == CredentialEnv
}

// File is the on-disk configuration.
type File struct {
	SchemaVersion  int                 `toml:"schema_version"`
	DefaultProfile string              `toml:"default_profile"`
	Defaults       Defaults            `toml:"defaults"`
	Profiles       map[string]*Profile `toml:"profiles"`
}

// Defaults apply to every profile unless the profile, an environment
// variable or a flag says otherwise.
type Defaults struct {
	Output string `toml:"output"`
	// Pager turns automatic paging of long tables on or off; nil means on.
	Pager          *bool  `toml:"pager"`
	MaxAttempts    int    `toml:"max_attempts"`
	ReadTimeout    string `toml:"read_timeout"`
	ConnectTimeout string `toml:"connect_timeout"`
}

// Profile is one named set of environment, organization and credentials.
type Profile struct {
	// Environment is production or custom.
	Environment string `toml:"environment"`
	// Organization is what the user chose (usually the slug); OrgID and
	// OrgName are what the CLI resolved it to.
	Organization string `toml:"organization"`
	OrgID        string `toml:"org_id"`
	OrgName      string `toml:"org_name"`
	// CredentialStore is keyring (default), file or env.
	CredentialStore string `toml:"credential_store"`
	Output          string `toml:"output"`
	// Endpoints override the environment's URL per service key.
	Endpoints map[string]string `toml:"endpoints"`
	// User is who signed in with this profile, recorded at login.
	User User `toml:"user"`
}

// User is the signed-in account.
type User struct {
	ID    string `toml:"id"`
	Name  string `toml:"name"`
	Email string `toml:"email"`
}

// Organization is an organization as the CLI knows it.
type Organization struct {
	ID   string
	Slug string
	Name string
}

// Profile returns the named profile, creating it in f if missing.
func (f *File) Profile(name string) *Profile {
	if f.Profiles == nil {
		f.Profiles = map[string]*Profile{}
	}
	p, ok := f.Profiles[name]
	if !ok || p == nil {
		p = &Profile{}
		f.Profiles[name] = p
	}
	return p
}

// ActiveOrg returns the profile's organization.
func (p *Profile) ActiveOrg() Organization {
	o := Organization{ID: p.OrgID, Slug: p.Organization, Name: p.OrgName}
	if o.Slug == o.ID {
		// An organization given by id: no slug known.
		o.Slug = ""
	}
	return o
}

// SetActiveOrg records the profile's organization.
func (p *Profile) SetActiveOrg(o Organization) {
	p.Organization = o.Slug
	if p.Organization == "" {
		p.Organization = o.ID
	}
	p.OrgID = o.ID
	p.OrgName = o.Name
}

// Config is the configuration one command runs with: the selected profile
// with every endpoint resolved. Package resolve builds it.
type Config struct {
	Profile     string
	Environment string

	Auth        string
	WebApp      string
	SnapDB      string
	Langsync    string
	Domainradar string
	Billing     string
	Prompthub   string

	User      User
	ActiveOrg Organization
}

// Endpoint returns the resolved URL of a service.
func (c *Config) Endpoint(service string) string {
	switch service {
	case ServiceAuth:
		return c.Auth
	case ServiceWebApp:
		return c.WebApp
	case ServiceSnapDB:
		return c.SnapDB
	case ServiceLangsync:
		return c.Langsync
	case ServiceDomainradar:
		return c.Domainradar
	case ServiceBilling:
		return c.Billing
	case ServicePrompthub:
		return c.Prompthub
	}
	return ""
}

// SetEndpoint sets the resolved URL of a service.
func (c *Config) SetEndpoint(service, url string) {
	switch service {
	case ServiceAuth:
		c.Auth = url
	case ServiceWebApp:
		c.WebApp = url
	case ServiceSnapDB:
		c.SnapDB = url
	case ServiceLangsync:
		c.Langsync = url
	case ServiceDomainradar:
		c.Domainradar = url
	case ServiceBilling:
		c.Billing = url
	case ServicePrompthub:
		c.Prompthub = url
	}
}

// EnvConfigFile names the config file explicitly.
const EnvConfigFile = "NORCUBE_CONFIG_FILE"

// Path returns the config file location: NORCUBE_CONFIG_FILE, else
// $XDG_CONFIG_HOME/norcube/config.toml (on every OS), else the OS user
// config directory (~/.config on Linux, ~/Library/Application Support on
// macOS, %AppData% on Windows).
func Path() (string, error) {
	if p := os.Getenv(EnvConfigFile); p != "" {
		return p, nil
	}
	osDir, osErr := os.UserConfigDir()
	osPath := ""
	if osErr == nil {
		osPath = filepath.Join(osDir, "norcube", "config.toml")
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" && filepath.IsAbs(x) {
		xdgPath := filepath.Join(x, "norcube", "config.toml")
		// Older releases ignored XDG_CONFIG_HOME on macOS and Windows: keep
		// using a config that already lives in the OS directory.
		if osPath != "" && osPath != xdgPath && !exists(xdgPath) && exists(osPath) {
			return osPath, nil
		}
		return xdgPath, nil
	}
	if osErr != nil {
		return "", osErr
	}
	return osPath, nil
}

// Dir is the directory holding the config file, its lock files and the
// file credential store.
func Dir() (string, error) {
	p, err := Path()
	if err != nil {
		return "", err
	}
	return filepath.Dir(p), nil
}

func exists(path string) bool {
	_, err := os.Stat(path) // #nosec G703 -- a config path the user controls
	return err == nil || !errors.Is(err, os.ErrNotExist)
}
