package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/knadh/koanf/parsers/toml/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// Config is the on-disk user configuration. It deliberately holds only
// non-secret state — refresh and access tokens live in the OS keyring.
type Config struct {
	// Auth is the base URL of the auth service (e.g. https://auth.api.norcube.com).
	// This is the source of identity for the CLI; every per-service token
	// is minted from a refresh token issued here.
	Auth string `koanf:"auth"`
	// WebApp is the base URL of the Norcube web app, used to open /cli-login during browser auth.
	WebApp string `koanf:"web_app"`
	// SnapDB / Langsync / Domainradar / Billing / Prompthub base URLs are optional overrides.
	SnapDB      string `koanf:"snapdb"`
	Langsync    string `koanf:"langsync"`
	Domainradar string `koanf:"domainradar"`
	Billing     string `koanf:"billing"`
	Prompthub   string `koanf:"prompthub"`

	User      User         `koanf:"user"`
	ActiveOrg Organization `koanf:"active_org"`
}

type User struct {
	ID    string `koanf:"id"`
	Name  string `koanf:"name"`
	Email string `koanf:"email"`
}

type Organization struct {
	ID   string `koanf:"id"`
	Slug string `koanf:"slug"`
	Name string `koanf:"name"`
}

// Defaults applied when no config file exists yet. Override via env or flags.
//
// These point at the production Norcube environment. To target a non-prod
// environment, pass --auth-url / --web-app on `norcube login` and the
// values are persisted to the user's config file, or set the
// NORCUBE_AUTH_URL / NORCUBE_WEB_APP / NORCUBE_*_URL env vars per command.
const (
	DefaultAuth        = "https://auth.api.norcube.com"
	DefaultWebApp      = "https://app.norcube.com"
	DefaultSnapDB      = "https://snapdb.api.norcube.com/app/v1"
	DefaultLangsync    = "https://langsync.api.norcube.com"
	DefaultDomainradar = "https://domainradar.api.norcube.com/app/v1"
	DefaultBilling     = "https://billing.api.norcube.com/app/v1"
	DefaultPrompthub   = "https://prompthub.api.norcube.com"
)

// ResetServiceURLs overwrites every per-service URL with the current defaults
// while leaving the auth API URL, web-app URL, user, and active-org alone.
// Used by `norcube config reset-urls` to refresh installs whose persisted
// values predate a default change.
func (c *Config) ResetServiceURLs() {
	c.SnapDB = DefaultSnapDB
	c.Langsync = DefaultLangsync
	c.Domainradar = DefaultDomainradar
	c.Billing = DefaultBilling
	c.Prompthub = DefaultPrompthub
}

var (
	loadOnce sync.Once
	loaded   *Config
	loadErr  error
)

// Load reads (or initializes) the user config from $XDG_CONFIG_HOME/norcube/config.toml.
func Load() (*Config, error) {
	loadOnce.Do(func() {
		path, err := Path()
		if err != nil {
			loadErr = err
			return
		}

		k := koanf.New(".")

		if _, err := os.Stat(path); err == nil {
			if err := k.Load(file.Provider(path), toml.Parser()); err != nil {
				loadErr = fmt.Errorf("read %s: %w", path, err)
				return
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			loadErr = err
			return
		}

		cfg := &Config{
			Auth:     DefaultAuth,
			WebApp:   DefaultWebApp,
			SnapDB:   DefaultSnapDB,
			Langsync: DefaultLangsync,
		}
		if err := k.Unmarshal("", cfg); err != nil {
			loadErr = fmt.Errorf("decode config: %w", err)
			return
		}

		// Allow env overrides for every URL the CLI talks to. Useful when
		// pointing the CLI at a non-prod environment (local dev, staging)
		// without permanently rewriting the config file.
		applyEnvURL(&cfg.Auth, "NORCUBE_AUTH_URL")
		applyEnvURL(&cfg.WebApp, "NORCUBE_WEB_APP")
		applyEnvURL(&cfg.SnapDB, "NORCUBE_SNAPDB_URL")
		applyEnvURL(&cfg.Langsync, "NORCUBE_LANGSYNC_URL")
		applyEnvURL(&cfg.Domainradar, "NORCUBE_DOMAINRADAR_URL")
		applyEnvURL(&cfg.Billing, "NORCUBE_BILLING_URL")
		applyEnvURL(&cfg.Prompthub, "NORCUBE_PROMPTHUB_URL")

		loaded = cfg
	})
	return loaded, loadErr
}

// Save persists the current config back to disk, creating the directory if needed.
func Save(cfg *Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	k := koanf.New(".")
	if err := k.Load(structProvider(cfg), nil); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	out, err := k.Marshal(toml.Parser())
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return os.WriteFile(path, out, 0o600)
}

// applyEnvURL overwrites *target with the value of envvar when the latter is
// non-empty. Pulled out as a helper so the URL-override block stays readable.
func applyEnvURL(target *string, envvar string) {
	if v := os.Getenv(envvar); v != "" {
		*target = v
	}
}

// Path returns the canonical config file location. Honors XDG_CONFIG_HOME.
func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "norcube", "config.toml"), nil
}

// structProvider lets us round-trip a Config through koanf without an
// intermediate TOML string. It is a minimal Provider implementation.
type structProviderImpl struct{ cfg *Config }

func structProvider(cfg *Config) *structProviderImpl { return &structProviderImpl{cfg: cfg} }

func (s *structProviderImpl) ReadBytes() ([]byte, error) { return nil, errors.New("not used") }

func (s *structProviderImpl) Read() (map[string]any, error) {
	return map[string]any{
		"auth":        s.cfg.Auth,
		"web_app":     s.cfg.WebApp,
		"snapdb":      s.cfg.SnapDB,
		"langsync":    s.cfg.Langsync,
		"domainradar": s.cfg.Domainradar,
		"billing":     s.cfg.Billing,
		"prompthub":   s.cfg.Prompthub,
		"user": map[string]any{
			"id":    s.cfg.User.ID,
			"name":  s.cfg.User.Name,
			"email": s.cfg.User.Email,
		},
		"active_org": map[string]any{
			"id":   s.cfg.ActiveOrg.ID,
			"slug": s.cfg.ActiveOrg.Slug,
			"name": s.cfg.ActiveOrg.Name,
		},
	}, nil
}
