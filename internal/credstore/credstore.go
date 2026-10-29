// Package credstore holds the CLI's secrets: one item per key in the OS
// keyring, or one 0600 file per key when no keyring is available.
package credstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/zalando/go-keyring"
)

// Store is a small secret key-value store.
type Store interface {
	Get(key string) (string, error) // ErrNotFound when absent
	Set(key, value string) error
	Delete(key string) error // nil when absent
}

// ErrNotFound is returned by Store.Get for a missing key.
var ErrNotFound = errors.New("secret not found")

// ErrTooBig is returned by Set when the value exceeds what the store can
// hold (the Windows Credential Manager caps an item at 2.5 KB).
var ErrTooBig = errors.New("secret too big for the credential store")

// KeyringService is the service name of every keyring item. Single word so
// Keychain Access shows "norcube"; changing it orphans users' sessions.
const KeyringService = "norcube"

// Keyring is the OS keyring (macOS Keychain, Secret Service, Windows
// Credential Manager).
type Keyring struct{}

func (Keyring) Get(key string) (string, error) {
	v, err := keyring.Get(KeyringService, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return v, err
}

func (Keyring) Set(key, value string) error {
	err := keyring.Set(KeyringService, key, value)
	if errors.Is(err, keyring.ErrSetDataTooBig) {
		return ErrTooBig
	}
	return err
}

func (Keyring) Delete(key string) error {
	err := keyring.Delete(KeyringService, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// File stores each key in its own 0600 file under Dir (0700).
type File struct{ Dir string }

// path maps a key to a file name: readable when the key is a plain name
// (profile:work → profile-work.json), hashed otherwise.
func (f File) path(key string) string {
	name := strings.ReplaceAll(key, ":", "-")
	for _, c := range name {
		if !nameChar(c) {
			sum := sha256.Sum256([]byte(key))
			name = hex.EncodeToString(sum[:12])
			break
		}
	}
	return filepath.Join(f.Dir, name+".json")
}

func (f File) Get(key string) (string, error) {
	b, err := os.ReadFile(f.path(key)) // #nosec G304 -- inside the user's credential directory
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (f File) Set(key, value string) error {
	if err := os.MkdirAll(f.Dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(f.Dir, ".cred-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(value); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, f.path(key))
}

func (f File) Delete(key string) error {
	err := os.Remove(f.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Available reports whether the OS keyring can be used here. A Linux box
// without a Secret Service (containers, CI, SSH sessions without D-Bus)
// can't.
func Available() bool {
	availableOnce.Do(func() {
		_, err := keyring.Get(KeyringService, "norcube-probe")
		available = err == nil || errors.Is(err, keyring.ErrNotFound)
	})
	return available
}

var (
	availableOnce sync.Once
	available     bool
)

// Open returns the store a profile asked for. "keyring" falls back to the
// file store, with a one-time warning on warn, when no keyring is
// available. dir is the file store's directory.
func Open(kind, dir string, warn io.Writer) (Store, string, error) {
	switch kind {
	case "", "keyring":
		if Available() {
			return Keyring{}, "keyring", nil
		}
		warnOnce(dir, warn)
		return File{Dir: dir}, "file", nil
	case "file":
		return File{Dir: dir}, "file", nil
	}
	return nil, "", fmt.Errorf("credential store %q can't hold a session (want keyring or file)", kind)
}

// warnOnce prints the keyring fallback notice the first time it happens on
// this machine (a marker file in dir remembers it).
func warnOnce(dir string, warn io.Writer) {
	marker := filepath.Join(dir, ".file-store-notice")
	if _, err := os.Stat(marker); err == nil {
		return
	}
	if warn != nil {
		fmt.Fprintf(warn, "norcube: no OS keyring available; storing credentials in %s (files readable only by you). Set credential_store = \"file\" in the profile to silence this.\n", dir)
	}
	if err := os.MkdirAll(dir, 0o700); err == nil {
		_ = os.WriteFile(marker, nil, 0o600) // #nosec G703 -- the user's credential directory
	}
}

// nameChar reports whether c can be used as is in a file name.
func nameChar(c rune) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.'
}
