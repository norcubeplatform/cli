package orgs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/norcubeplatform/cli/internal/api"
)

// Cache is a small JSON file of the organizations each signed-in user
// belongs to. It holds no secrets (ids, slugs and names only). Every
// failure to read or write it is ignored: it only saves a round trip.
type Cache struct{ Path string }

// DefaultCache lives in the user cache directory (~/Library/Caches,
// $XDG_CACHE_HOME, %LocalAppData%).
func DefaultCache() *Cache {
	dir, err := os.UserCacheDir()
	if err != nil {
		return nil
	}
	return &Cache{Path: filepath.Join(dir, "norcube", "orgs.json")}
}

type cacheFile struct {
	// Scopes maps "<profile>|<auth url>|<user id>" (ScopeKey) to that
	// user's organizations.
	Scopes map[string]cacheScope `json:"scopes"`
}

type cacheScope struct {
	FetchedAt time.Time          `json:"fetched_at"`
	Orgs      []api.Organization `json:"orgs"`
}

func (c *Cache) read() cacheFile {
	var f cacheFile
	if c == nil || c.Path == "" {
		return f
	}
	b, err := os.ReadFile(c.Path)
	if err != nil {
		return f
	}
	_ = json.Unmarshal(b, &f)
	return f
}

func (c *Cache) lookup(scope, value string, now time.Time) (api.Organization, bool) {
	s, ok := c.read().Scopes[scope]
	if !ok || now.Sub(s.FetchedAt) > CacheTTL || now.Before(s.FetchedAt) {
		return api.Organization{}, false
	}
	return Find(s.Orgs, value)
}

func (c *Cache) store(scope string, list []api.Organization, now time.Time) {
	if c == nil || c.Path == "" || strings.Contains(scope, "||") || strings.HasSuffix(scope, "|") {
		return
	}
	f := c.read()
	if f.Scopes == nil {
		f.Scopes = map[string]cacheScope{}
	}
	f.Scopes[scope] = cacheScope{FetchedAt: now.UTC(), Orgs: list}
	_ = c.write(f)
}

// ClearProfile forgets the organizations cached for one profile. Used on
// logout.
func (c *Cache) ClearProfile(profile string) error {
	if c == nil || c.Path == "" {
		return nil
	}
	f := c.read()
	changed := false
	for k := range f.Scopes {
		if strings.HasPrefix(k, profile+"|") {
			delete(f.Scopes, k)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return c.write(f)
}

// Clear removes every cached organization. Used on logout --all.
func (c *Cache) Clear() error {
	if c == nil || c.Path == "" {
		return nil
	}
	if err := os.Remove(c.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (c *Cache) write(f cacheFile) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	dir := filepath.Dir(c.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".orgs-*.json")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), c.Path)
}
