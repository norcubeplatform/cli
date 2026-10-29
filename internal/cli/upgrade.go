package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/creativeprojects/go-selfupdate"
	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/buildinfo"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

// releaseSlug identifies the GitHub repository to pull releases from.
// Tags are expected in the form vX.Y.Z to match GoReleaser's defaults.
const (
	releaseOwner = "norcubeplatform"
	releaseRepo  = "cli"
)

func newUpgradeCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Update norcube to the latest release",
		Example: `  norcube upgrade
  norcube upgrade --force`,
		Long: `Checks GitHub for newer releases of norcube, downloads the archive for
this platform, verifies its SHA-256 against the release's checksums.txt
(both fetched over TLS from GitHub), and atomically replaces this binary
with the latest version. The upgrade stops without touching the binary
when the checksum is missing or doesn't match.

checksums.txt is also signed with Sigstore (cosign) by the release
workflow; see the README's "Verify a release" section to check the
signature and build provenance yourself.

Skipped when the binary appears to be managed by a package manager
(Homebrew, Scoop, winget, deb/rpm/apk); upgrade with that tool instead.
Pass --force to override.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			current := "v" + strings.TrimPrefix(buildinfo.Version, "v")
			if reason := managedInstallReason(); reason != "" && !force {
				return cmdutil.Print(cmd, output.Done{
					Message: reason + "\nPass --force to upgrade anyway.",
					Value:   upgradeResult{From: current, To: current, Skipped: reason},
				})
			}
			res, err := runUpgrade(cmd.Context(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			msg := fmt.Sprintf("Already on the latest version (%s).", res.From)
			if res.Upgraded {
				msg = fmt.Sprintf("Upgraded to %s. Run `norcube --version` to verify.", res.To)
			}
			return cmdutil.Print(cmd, output.Done{Message: msg, Value: res})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Upgrade even when the binary appears to be managed by a package manager")
	return cmd
}

// upgradeResult is what `upgrade -o json` prints.
type upgradeResult struct {
	From     string `json:"from" yaml:"from"`
	To       string `json:"to" yaml:"to"`
	Upgraded bool   `json:"upgraded" yaml:"upgraded"`
	Skipped  string `json:"skipped,omitempty" yaml:"skipped,omitempty"`
}

// runUpgrade installs the latest release, writing progress to out.
func runUpgrade(ctx context.Context, out io.Writer) (upgradeResult, error) {
	exe, err := os.Executable()
	if err != nil {
		return upgradeResult{}, fmt.Errorf("locate current binary: %w", err)
	}
	// When invoked via the `nrc` short-name symlink, os.Executable() returns
	// the symlink path ("…/nrc"), but the release tar only contains the
	// canonical `norcube` binary. Resolve to the real file so the updater
	// looks for the right name inside the archive. The `nrc` symlink keeps
	// working after the swap because it points by name, not by inode.
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}

	src, err := selfupdate.NewGitHubSource(selfupdate.GitHubConfig{})
	if err != nil {
		return upgradeResult{}, err
	}
	updater, err := selfupdate.NewUpdater(upgradeConfig(src, runtime.GOOS, runtime.GOARCH))
	if err != nil {
		return upgradeResult{}, err
	}

	repo := selfupdate.NewRepositorySlug(releaseOwner, releaseRepo)
	latest, found, err := updater.DetectLatest(ctx, repo)
	if err != nil {
		return upgradeResult{}, fmt.Errorf("check latest release: %w", err)
	}
	if !found {
		return upgradeResult{}, errors.New("no releases found on GitHub — has v0.1.0 been tagged yet?")
	}

	current := strings.TrimPrefix(buildinfo.Version, "v")
	if latest.LessOrEqual(current) {
		return upgradeResult{From: "v" + current, To: "v" + current}, nil
	}

	fmt.Fprintf(out, "Upgrading from v%s to v%s…\n", current, latest.Version())
	if err := updater.UpdateTo(ctx, latest, exe); err != nil {
		// Permission denied on system-wide install dirs (/usr/local/bin, etc.)
		// — give the user a concrete next step instead of the raw error.
		if errors.Is(err, fs.ErrPermission) || strings.Contains(err.Error(), "permission denied") {
			return upgradeResult{}, fmt.Errorf(`install update: cannot write to %s.

This usually means norcube is installed system-wide (e.g. /usr/local/bin).
Two ways forward:

  1. Run with sudo:
       sudo norcube upgrade

  2. Reinstall to a per-user location (no sudo needed for future upgrades):
       sudo rm %s %s
       curl -fsSL https://github.com/%s/%s/raw/main/install.sh | sh

Underlying error: %w`,
				filepath.Dir(exe),
				exe, filepath.Join(filepath.Dir(exe), "nrc"),
				releaseOwner, releaseRepo, err)
		}
		return upgradeResult{}, fmt.Errorf("install update: %w", err)
	}
	return upgradeResult{From: "v" + current, To: "v" + latest.Version(), Upgraded: true}, nil
}

// checksumsAsset is the release asset GoReleaser writes the SHA-256 of
// every other asset into.
const checksumsAsset = "checksums.txt"

// upgradeConfig is the updater setup shared by `norcube upgrade` and the
// background update check.
//
// The validator makes every download fail closed: the archive's SHA-256
// must appear in checksums.txt, or nothing is replaced. The filter pins
// the asset to the release archive for this platform, so the packages,
// SBOMs and signatures that sit next to it in a release are never picked
// up by the updater's looser name matching.
func upgradeConfig(src selfupdate.Source, goos, goarch string) selfupdate.Config {
	return selfupdate.Config{
		Source:    src,
		Validator: &selfupdate.ChecksumValidator{UniqueFilename: checksumsAsset},
		Filters:   []string{releaseArchivePattern(goos, goarch)},
		OS:        goos,
		Arch:      goarch,
	}
}

// releaseArchivePattern matches the archive name GoReleaser publishes for
// one platform (see archives.name_template in .goreleaser.yaml):
// norcube_<os>_<arch>.tar.gz, or .zip on Windows.
func releaseArchivePattern(goos, goarch string) string {
	ext := `\.tar\.gz`
	if goos == "windows" {
		ext = `\.zip`
	}
	return "^" + regexp.QuoteMeta("norcube_"+goos+"_"+goarch) + ext + "$"
}

// managedInstallReason returns a non-empty explanation when the running
// binary lives under a path typically owned by a package manager, so the
// upgrade command can refuse to clobber it. Empty string means it's safe
// for `norcube upgrade` to take over.
func managedInstallReason() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return ""
	}
	return managedInstallReasonFor(filepath.Clean(exe), runtime.GOOS)
}

// managedInstallReasonFor is managedInstallReason for a resolved binary
// path on goos.
//
// Detection is path-based and deliberately conservative: false positives
// (refusing to update something we could have) annoy the user once with
// a clear message; false negatives (clobbering a package manager's file)
// corrupt the package manager's state. We err toward the former.
func managedInstallReasonFor(exe, goos string) string {
	const (
		brew   = "Installed via Homebrew. Run `brew upgrade norcube` instead."
		system = "Installed from a system package (deb, rpm or apk). Upgrade it with your package manager (apt, dnf, zypper or apk) instead."
	)
	type rule struct {
		match string
		msg   string
	}

	if goos == "windows" {
		// Windows paths are case-insensitive; compare a lower-cased,
		// forward-slashed copy so one rule covers C:\ and \\?\ forms.
		p := strings.ReplaceAll(strings.ToLower(exe), `\`, "/")
		rules := []rule{
			// %USERPROFILE%\scoop\apps\norcube\<version>\norcube.exe, or
			// %ProgramData%\scoop\apps\... for a global install.
			{"/scoop/apps/", "Installed via Scoop. Run `scoop update norcube` instead."},
			// Portable winget packages live under WinGet\Packages (user or
			// machine scope) with a link in WinGet\Links.
			{"/winget/packages/", "Installed via winget. Run `winget upgrade Norcube.CLI` instead."},
			{"/winget/links/", "Installed via winget. Run `winget upgrade Norcube.CLI` instead."},
		}
		for _, r := range rules {
			if strings.Contains(p, r.match) {
				return r.msg
			}
		}
		return ""
	}

	rules := []rule{
		// Homebrew on Apple silicon, Intel macOS (Cellar for formulae,
		// Caskroom for casks) and Linux.
		{"/opt/homebrew/", brew},
		{"/usr/local/Cellar/", brew},
		{"/usr/local/Caskroom/", brew},
		{"/home/linuxbrew/.linuxbrew/", brew},
		// The deb, rpm and apk packages install to /usr/bin.
		{"/usr/bin/", system},
		{"/var/lib/snapd/snap/", "Installed via snap. Run `snap refresh norcube` instead."},
		{"/var/lib/flatpak/", "Installed via flatpak. Run `flatpak update` instead."},
	}
	for _, r := range rules {
		if strings.HasPrefix(exe, r.match) {
			return r.msg
		}
	}
	return ""
}
