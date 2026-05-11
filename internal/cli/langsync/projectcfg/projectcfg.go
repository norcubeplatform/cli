// Package projectcfg loads and writes the per-project Langsync config
// (`.langsync.json`). It is intentionally narrow — *only* the on-disk
// shape and IO helpers live here; semantic decisions (what to do with
// a config, what's required for sync, etc.) belong in callers.
//
// This package is scoped to the langsync service. Other services that
// later need their own project-level config should live under their
// own CLI subtree (e.g. `internal/cli/snapdb/projectcfg/`) so each
// service owns its own schema and dotfile name. There is deliberately
// no shared umbrella package — services stay decoupled, and one
// service's schema churn never touches another's file on disk.
package projectcfg

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Filename is the canonical name we read and write. The CLI does not
// support alternate locations — the file lives at the project root,
// alongside go.mod / package.json / similar, and `init` plus `sync`
// always look for exactly this name in the current working directory
// (with a walk-up to find an ancestor copy, see Find).
const Filename = ".langsync.json"

// CurrentVersion is the version field written by `init`. Older configs
// load fine; the version is reserved for forward-incompatible changes
// (rename, schema reshuffle) where we'd want to refuse to load v1 from
// a v2 CLI without an explicit migrate step.
const CurrentVersion = 1

// Format is the on-disk shape of translation files. Only flat-json is
// supported in v1 — that's what every Norcube-internal repo uses today
// (see ytracker-be/i18n). Add a new value (e.g. "i18next-nested",
// "flutter-arb") here when a project actually needs it.
type Format string

const (
	FormatFlatJSON Format = "flat-json"
)

// File is the on-disk root document.
type File struct {
	Version    int         `json:"version"`
	Namespaces []Namespace `json:"namespaces"`
}

// Namespace ties one backend Langsync namespace to one local directory
// of translation files. DefaultLocalLanguage is the local file the
// developer edits (and that the CLI pushes from). It is derived from
// the namespace's backend default language at init time and re-checked
// on every sync so a server-side change doesn't silently desync.
type Namespace struct {
	// Namespace is the URL-slug name of the Langsync namespace.
	Namespace string `json:"namespace"`
	// Dir is the directory containing one <lang-code>.<ext> file per
	// language. Relative paths are resolved against the directory the
	// config file itself lives in (NOT the user's working directory) so
	// the config is reproducible regardless of where `nrc` is invoked.
	Dir string `json:"dir"`
	// Format is how files are encoded. Only "flat-json" exists today.
	Format Format `json:"format"`
	// DefaultLocalLanguage is the code of the language the developer
	// writes marks in. Stored for reference and drift detection; sync
	// fetches the server default at runtime and errors if they diverge.
	DefaultLocalLanguage string `json:"default_local_language"`
}

// Find walks up from start (inclusive) looking for a .langsync.json
// file. Returns its absolute path. Mirrors how git, npm, and similar
// tools find their root marker.
func Find(start string) (string, error) {
	if start == "" {
		var err error
		start, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(abs, Filename)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", fmt.Errorf("no %s found in %s or any parent directory — run `norcube langsync init` to create one", Filename, start)
		}
		abs = parent
	}
}

// Load reads, decodes, and lightly validates a config file. It does
// NOT verify that namespace directories exist or that the backend
// namespace is still configured the same way — that's a sync-time
// concern.
func Load(path string) (*File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := f.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &f, nil
}

// Save writes the config atomically (tmp file + rename) with stable
// two-space indentation, the same format `init` emits.
func Save(path string, f *File) error {
	if err := f.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	// Trailing newline keeps every editor's whitespace linter happy.
	out = append(out, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".langsync.json.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// Validate checks shape-level invariants the rest of the CLI relies on.
// Returns the first error encountered so the user sees a single fixable
// problem at a time.
func (f *File) Validate() error {
	if f.Version <= 0 {
		return fmt.Errorf("version must be a positive integer")
	}
	if f.Version > CurrentVersion {
		return fmt.Errorf("config version %d is newer than this CLI supports (max %d) — upgrade with `norcube upgrade`", f.Version, CurrentVersion)
	}
	if len(f.Namespaces) == 0 {
		return fmt.Errorf("namespaces array must not be empty — rerun `norcube langsync init` to add one")
	}
	seen := map[string]bool{}
	for i, ns := range f.Namespaces {
		if strings.TrimSpace(ns.Namespace) == "" {
			return fmt.Errorf("namespaces[%d].namespace must not be empty", i)
		}
		if seen[ns.Namespace] {
			return fmt.Errorf("namespaces[%d].namespace %q is listed twice", i, ns.Namespace)
		}
		seen[ns.Namespace] = true
		if strings.TrimSpace(ns.Dir) == "" {
			return fmt.Errorf("namespaces[%d] (%q): dir must not be empty", i, ns.Namespace)
		}
		switch ns.Format {
		case FormatFlatJSON:
			// ok
		case "":
			return fmt.Errorf("namespaces[%d] (%q): format must be set (only %q is supported in v1)", i, ns.Namespace, FormatFlatJSON)
		default:
			return fmt.Errorf("namespaces[%d] (%q): unknown format %q (only %q is supported in v1)", i, ns.Namespace, ns.Format, FormatFlatJSON)
		}
		if strings.TrimSpace(ns.DefaultLocalLanguage) == "" {
			return fmt.Errorf("namespaces[%d] (%q): default_local_language must not be empty", i, ns.Namespace)
		}
	}
	return nil
}

// ResolveDir returns the absolute path to ns.Dir, using configPath as
// the anchor for relative paths. Always use this rather than calling
// filepath.Abs on ns.Dir directly — the latter would silently anchor
// to the user's current working directory, which is wrong.
func (f *File) ResolveDir(configPath string, ns Namespace) string {
	if filepath.IsAbs(ns.Dir) {
		return filepath.Clean(ns.Dir)
	}
	return filepath.Clean(filepath.Join(filepath.Dir(configPath), ns.Dir))
}

// FindNamespace returns the namespace entry by name (case-sensitive)
// and a bool for found-ness, mirroring the standard library idiom.
func (f *File) FindNamespace(name string) (Namespace, bool) {
	for _, ns := range f.Namespaces {
		if ns.Namespace == name {
			return ns, true
		}
	}
	return Namespace{}, false
}
