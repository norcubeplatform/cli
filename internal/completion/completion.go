// Package completion serves dynamic shell completions (organization slugs,
// namespaces, data source and job ids, language codes) from the API.
//
// Completion must never get in the way of typing: every lookup has a one
// second budget, results are cached for a minute per profile and
// organization, nothing prompts, and any failure simply offers nothing.
package completion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/clictx"
)

// Budget is how long one lookup may take.
const Budget = time.Second

// TTL is how long a cached answer is reused.
const TTL = time.Minute

// rootFlags are the global flags of the running command tree. Cobra calls
// completion functions without running the root's pre-run hook, so the
// flags aren't attached to the command yet; Bind hands them over.
var rootFlags atomic.Pointer[clictx.Flags]

// Bind registers the root command's flags. NewRootCmd calls it.
func Bind(f *clictx.Flags) { rootFlags.Store(f) }

// Fetcher looks the candidates up. Each is a value, optionally followed by
// a tab and a description ("id\tname").
type Fetcher func(cmd *cobra.Command) ([]cobra.Completion, error)

// Func returns a cobra completion function for positional arguments that
// serves fetch's candidates for the first maxArgs arguments (0: all).
func Func(key string, maxArgs int, fetch Fetcher) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if maxArgs > 0 && len(args) >= maxArgs {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return Lookup(cmd, key, toComplete, fetch), cobra.ShellCompDirectiveNoFileComp
	}
}

// Flag returns a cobra completion function for a flag value.
func Flag(key string, fetch Fetcher) cobra.CompletionFunc {
	return Func(key, 0, fetch)
}

// Lookup returns the cached or freshly fetched candidates that start with
// prefix. It never fails: an error or a timeout yields nothing.
func Lookup(cmd *cobra.Command, key, prefix string, fetch Fetcher) []cobra.Completion {
	flags := rootFlags.Load()
	if flags == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), Budget)
	defer cancel()
	clictx.Attach(cmd, ctx, flags)

	path := cachePath(flags, key)
	items, ok := readCache(path)
	if !ok {
		var err error
		items, err = fetch(cmd)
		if err != nil {
			return nil
		}
		writeCache(path, items)
	}
	return filter(items, prefix)
}

func filter(items []cobra.Completion, prefix string) []cobra.Completion {
	out := make([]cobra.Completion, 0, len(items))
	for _, it := range items {
		value, _, _ := strings.Cut(it, "\t")
		if strings.HasPrefix(value, prefix) {
			out = append(out, it)
		}
	}
	return out
}

// cachePath keys the cache by profile, organization override and lookup,
// so two profiles or organizations never see each other's candidates.
func cachePath(flags *clictx.Flags, key string) string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	profile, org := flags.Profile, flags.OrgOverride
	if res, err := flags.Resolve(); err == nil {
		profile, org = res.Profile.Value, res.Org.Value
	}
	sum := sha256.Sum256([]byte(profile + "\x00" + org + "\x00" + key))
	return filepath.Join(dir, "norcube", "completion", hex.EncodeToString(sum[:12])+".json")
}

type cacheFile struct {
	At    time.Time          `json:"at"`
	Items []cobra.Completion `json:"items"`
}

func readCache(path string) ([]cobra.Completion, bool) {
	if path == "" {
		return nil, false
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- our own cache file
	if err != nil {
		return nil, false
	}
	var c cacheFile
	if json.Unmarshal(raw, &c) != nil || time.Since(c.At) > TTL || c.At.After(time.Now().Add(time.Minute)) {
		return nil, false
	}
	return c.Items, true
}

func writeCache(path string, items []cobra.Completion) {
	if path == "" {
		return
	}
	raw, err := json.Marshal(cacheFile{At: time.Now(), Items: items})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}
