package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/creativeprojects/go-selfupdate"
	"golang.org/x/term"

	"github.com/norcubeplatform/cli/internal/buildinfo"
	"github.com/norcubeplatform/cli/internal/config"
)

// updateCheckTTL bounds how often the CLI asks GitHub for the latest
// release. 24h is fresh enough to surface a new version the same workday
// but never approaches GitHub's anonymous rate limit (60/hr).
const updateCheckTTL = 24 * time.Hour

// updateCheckNetTimeout caps the GitHub call itself. Anything slower
// than this is treated as "no answer" — better to skip a nudge than
// keep the user staring at a spinner.
const updateCheckNetTimeout = 5 * time.Second

// updateCheckWaitBudget is how long main is willing to wait for an
// in-flight check to finish before printing the nudge. Only paid when
// the cache was stale or empty; on a warm cache the wait is zero.
const updateCheckWaitBudget = 2 * time.Second

type updateCacheFile struct {
	CheckedAt     int64  `json:"checked_at"`
	LatestVersion string `json:"latest_version"`
}

var (
	updateCache     atomic.Pointer[updateCacheFile]
	updateCheckDone chan struct{}
)

func updateCachePath() (string, error) {
	cfg, err := config.Path()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(cfg), "update-check.json"), nil
}

func loadUpdateCache() *updateCacheFile {
	path, err := updateCachePath()
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var c updateCacheFile
	if err := json.Unmarshal(b, &c); err != nil {
		return nil
	}
	return &c
}

func saveUpdateCache(c *updateCacheFile) {
	path, err := updateCachePath()
	if err != nil {
		return
	}
	b, err := json.Marshal(c)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.WriteFile(path, b, 0o600)
}

// StartUpdateCheck primes the in-process cache from disk and, if the
// cached answer is stale (or absent), spawns a background goroutine that
// hits the GitHub Releases API. The goroutine writes to the cache file
// so a slow first run still benefits a fast second run. Call
// WaitForUpdateCheck before MaybePrintUpdateNudge in main, so a freshly
// fetched result has a chance to land before we decide whether to nudge.
//
// Skips entirely when:
//   - the running subcommand is `upgrade` (does its own check),
//   - stderr isn't a TTY (piped output, CI),
//   - NORCUBE_NO_UPDATE_CHECK is set,
//   - the binary was built without -ldflags (Version == "dev").
func StartUpdateCheck(ctx context.Context, subCommand string) {
	if shouldSkipUpdateCheck(subCommand) {
		return
	}

	if c := loadUpdateCache(); c != nil {
		updateCache.Store(c)
		if time.Since(time.Unix(c.CheckedAt, 0)) < updateCheckTTL {
			return
		}
	}

	updateCheckDone = make(chan struct{})
	go func() {
		defer close(updateCheckDone)
		fetchLatestRelease(ctx)
	}()
}

// WaitForUpdateCheck blocks for up to updateCheckWaitBudget if a
// background check is in flight. No-op when StartUpdateCheck didn't
// launch one (cache was fresh, or the check was skipped).
func WaitForUpdateCheck() {
	if updateCheckDone == nil {
		return
	}
	select {
	case <-updateCheckDone:
	case <-time.After(updateCheckWaitBudget):
	}
}

// MaybePrintUpdateNudge writes a one-line "new release available" footer
// to `out` when the cache says a newer version exists. Safe no-op when
// the cache is empty or the running binary is already current.
func MaybePrintUpdateNudge(out io.Writer) {
	c := updateCache.Load()
	if c == nil || c.LatestVersion == "" {
		return
	}
	current := strings.TrimPrefix(buildinfo.Version, "v")
	latest := strings.TrimPrefix(c.LatestVersion, "v")
	if !semverGreater(latest, current) {
		return
	}
	fmt.Fprintf(out, "\nA new release of norcube is available: v%s → v%s\n", current, latest)
	fmt.Fprintln(out, "Run `norcube upgrade` to install it.")
}

func shouldSkipUpdateCheck(subCommand string) bool {
	switch subCommand {
	case "upgrade", "help", "version":
		return true
	}
	if os.Getenv("NORCUBE_NO_UPDATE_CHECK") != "" {
		return true
	}
	if !term.IsTerminal(int(os.Stderr.Fd())) {
		return true
	}
	if buildinfo.Version == "dev" {
		return true
	}
	return false
}

func fetchLatestRelease(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, updateCheckNetTimeout)
	defer cancel()
	src, err := selfupdate.NewGitHubSource(selfupdate.GitHubConfig{})
	if err != nil {
		return
	}
	updater, err := selfupdate.NewUpdater(selfupdate.Config{Source: src})
	if err != nil {
		return
	}
	latest, found, err := updater.DetectLatest(
		ctx, selfupdate.NewRepositorySlug(releaseOwner, releaseRepo),
	)
	if err != nil || !found {
		return
	}
	next := &updateCacheFile{
		CheckedAt:     time.Now().Unix(),
		LatestVersion: latest.Version(),
	}
	saveUpdateCache(next)
	updateCache.Store(next)
}

// semverGreater compares two dotted-numeric version strings ("0.12.1")
// and returns true when `a` is strictly newer. Three-component aware;
// non-numeric suffixes on a segment ("-rc1") are stripped before
// comparison, so pre-release tags don't break the nudge logic.
// `norcube upgrade` runs the rigorous LessOrEqual check before swapping,
// so this helper only needs to be right enough to decide whether to
// print the hint.
func semverGreater(a, b string) bool {
	if a == b {
		return false
	}
	ap := strings.SplitN(a, ".", 3)
	bp := strings.SplitN(b, ".", 3)
	for i := 0; i < 3; i++ {
		var av, bv int
		if i < len(ap) {
			av, _ = strconv.Atoi(stripVersionSuffix(ap[i]))
		}
		if i < len(bp) {
			bv, _ = strconv.Atoi(stripVersionSuffix(bp[i]))
		}
		if av != bv {
			return av > bv
		}
	}
	return false
}

func stripVersionSuffix(s string) string {
	for i, c := range s {
		if c < '0' || c > '9' {
			return s[:i]
		}
	}
	return s
}
