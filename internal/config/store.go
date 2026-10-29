package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/gofrs/flock"
	"github.com/pelletier/go-toml/v2"
)

// lockTimeout bounds the wait for another norcube process writing the
// config.
const lockTimeout = 10 * time.Second

// Load reads the config file. A missing file is an empty configuration.
// A version 1 file is migrated to version 2 and written back (with a
// backup of the original) before it is returned.
func Load() (*File, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	f, _, legacy, err := read(path)
	if err != nil {
		return nil, err
	}
	if !legacy {
		return f, nil
	}
	return Update(func(*File) error { return nil })
}

// Update changes the config file under its lock: it re-reads the file
// (migrating a version 1 file), lets fn change it, and writes it back
// atomically. Keys this CLI doesn't know are kept.
func Update(fn func(*File) error) (*File, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	unlock, err := lock(path)
	if err != nil {
		return nil, err
	}
	defer unlock()

	f, raw, legacy, err := read(path)
	if err != nil {
		return nil, err
	}
	if legacy {
		if err := backup(path); err != nil {
			return nil, err
		}
		removeV1Keys(raw)
	}
	if err := fn(f); err != nil {
		return nil, err
	}
	f.SchemaVersion = SchemaVersion
	merge(raw, f)
	out, err := toml.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	if err := writeAtomic(path, out); err != nil {
		return nil, err
	}
	return f, nil
}

// read parses path into the typed File and the raw table (which keeps
// unknown keys). legacy is true for a version 1 file, which is returned
// already converted.
func read(path string) (f *File, raw map[string]any, legacy bool, err error) {
	raw = map[string]any{}
	b, err := os.ReadFile(path) // #nosec G304 -- the user's own config file
	if errors.Is(err, os.ErrNotExist) {
		return &File{SchemaVersion: SchemaVersion}, raw, false, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return &File{SchemaVersion: SchemaVersion}, raw, false, nil
	}
	if err := toml.Unmarshal(b, &raw); err != nil {
		return nil, nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	if _, ok := raw["schema_version"]; !ok {
		var v1 fileV1
		if err := toml.Unmarshal(b, &v1); err != nil {
			return nil, nil, false, fmt.Errorf("read %s: %w", path, err)
		}
		return migrateV1(v1), raw, true, nil
	}
	f = &File{}
	if err := toml.Unmarshal(b, f); err != nil {
		return nil, nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	if f.SchemaVersion > SchemaVersion {
		return nil, nil, false, fmt.Errorf("%s was written by a newer norcube (schema version %d); upgrade with `norcube upgrade`", path, f.SchemaVersion)
	}
	return f, raw, false, nil
}

// lock takes the config file's lock (<path>.lock).
func lock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create config directory: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), lockTimeout)
	defer cancel()
	fl := flock.New(path + ".lock")
	ok, err := fl.TryLockContext(ctx, 20*time.Millisecond)
	if err != nil || !ok {
		return nil, fmt.Errorf("timed out waiting for another norcube process to write %s", path)
	}
	return func() { _ = fl.Unlock() }, nil
}

// backup copies a version 1 file to config.toml.bak-<timestamp> before it
// is rewritten.
func backup(path string) error {
	b, err := os.ReadFile(path) // #nosec G304 -- the user's own config file
	if err != nil {
		return err
	}
	dst := path + ".bak-" + time.Now().UTC().Format("20060102T150405Z")
	if err := os.WriteFile(dst, b, 0o600); err != nil { // #nosec G703 -- next to the user's config
		return fmt.Errorf("back up config before migrating it: %w", err)
	}
	return nil
}

// writeAtomic replaces path with data: a temp file in the same directory,
// fsynced, renamed over the target, and the directory fsynced, so a crash
// or a concurrent reader never sees half a file.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.toml")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }() // no-op after the rename
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	if d, err := os.Open(dir); err == nil { // #nosec G304 -- the config directory
		_ = d.Sync() // best effort; not supported everywhere
		_ = d.Close()
	}
	return nil
}

// merge writes the typed configuration into raw, leaving keys the CLI
// doesn't know alone. Known keys with empty values are removed.
func merge(raw map[string]any, f *File) {
	raw["schema_version"] = int64(f.SchemaVersion)
	setString(raw, "default_profile", f.DefaultProfile)

	defaults := table(raw, "defaults")
	setString(defaults, "output", f.Defaults.Output)
	if f.Defaults.Pager != nil {
		defaults["pager"] = *f.Defaults.Pager
	} else {
		delete(defaults, "pager")
	}
	setInt(defaults, "max_attempts", f.Defaults.MaxAttempts)
	setString(defaults, "read_timeout", f.Defaults.ReadTimeout)
	setString(defaults, "connect_timeout", f.Defaults.ConnectTimeout)
	dropEmpty(raw, "defaults")

	profiles := table(raw, "profiles")
	for name := range profiles {
		if _, ok := f.Profiles[name]; !ok {
			delete(profiles, name)
		}
	}
	names := make([]string, 0, len(f.Profiles))
	for name := range f.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := f.Profiles[name]
		if p == nil {
			delete(profiles, name)
			continue
		}
		t := table(profiles, name)
		setString(t, "environment", p.Environment)
		setString(t, "organization", p.Organization)
		setString(t, "org_id", p.OrgID)
		setString(t, "org_name", p.OrgName)
		setString(t, "credential_store", p.CredentialStore)
		setString(t, "output", p.Output)

		endpoints := table(t, "endpoints")
		for _, svc := range Services {
			setString(endpoints, svc, p.Endpoints[svc])
		}
		for svc, url := range p.Endpoints {
			if !IsService(svc) {
				setString(endpoints, svc, url)
			}
		}
		dropEmpty(t, "endpoints")

		user := table(t, "user")
		setString(user, "id", p.User.ID)
		setString(user, "name", p.User.Name)
		setString(user, "email", p.User.Email)
		dropEmpty(t, "user")
		// An empty profile is still a profile: keep its table.
	}
	dropEmpty(raw, "profiles")
}

func table(m map[string]any, key string) map[string]any {
	if t, ok := m[key].(map[string]any); ok {
		return t
	}
	t := map[string]any{}
	m[key] = t
	return t
}

func dropEmpty(m map[string]any, key string) {
	if t, ok := m[key].(map[string]any); ok && len(t) == 0 {
		delete(m, key)
	}
}

func setString(m map[string]any, key, v string) {
	if v == "" {
		delete(m, key)
		return
	}
	m[key] = v
}

func setInt(m map[string]any, key string, v int) {
	if v == 0 {
		delete(m, key)
		return
	}
	m[key] = int64(v)
}
