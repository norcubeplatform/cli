package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/creativeprojects/go-selfupdate"
	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/buildinfo"
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
		Long: `Checks GitHub for newer releases of norcube, verifies the SHA-256
checksum against the release's checksums.txt, and atomically replaces this
binary with the latest version.

Skipped automatically when the binary appears to be managed by a package
manager (Homebrew, apt, rpm) — use the package manager to upgrade instead.
Pass --force to override.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if reason := managedInstallReason(); reason != "" && !force {
				fmt.Fprintf(out, "%s\nPass --force to upgrade anyway.\n", reason)
				return nil
			}
			return runUpgrade(cmd.Context(), out)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Upgrade even when the binary appears to be managed by a package manager")
	return cmd
}

func runUpgrade(ctx context.Context, out io.Writer) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate current binary: %w", err)
	}

	// Verify uploaded archives against checksums.txt before swap so a
	// tampered release can't replace the live binary.
	src, err := selfupdate.NewGitHubSource(selfupdate.GitHubConfig{})
	if err != nil {
		return err
	}
	updater, err := selfupdate.NewUpdater(selfupdate.Config{
		Source:    src,
		Validator: &selfupdate.ChecksumValidator{UniqueFilename: "checksums.txt"},
	})
	if err != nil {
		return err
	}

	repo := selfupdate.NewRepositorySlug(releaseOwner, releaseRepo)
	latest, found, err := updater.DetectLatest(ctx, repo)
	if err != nil {
		return fmt.Errorf("check latest release: %w", err)
	}
	if !found {
		return errors.New("no releases found on GitHub — has v0.1.0 been tagged yet?")
	}

	current := strings.TrimPrefix(buildinfo.Version, "v")
	if latest.LessOrEqual(current) {
		fmt.Fprintf(out, "Already on the latest version (v%s).\n", current)
		return nil
	}

	fmt.Fprintf(out, "Upgrading from v%s to v%s…\n", current, latest.Version())
	if err := updater.UpdateTo(ctx, latest, exe); err != nil {
		return fmt.Errorf("install update: %w", err)
	}
	fmt.Fprintf(out, "Upgraded to v%s. Run `norcube --version` to verify.\n", latest.Version())
	return nil
}

// managedInstallReason returns a non-empty explanation when the running
// binary lives under a path typically owned by a package manager, so the
// upgrade command can refuse to clobber it. Empty string means it's safe
// for `norcube upgrade` to take over.
//
// Detection is path-based and deliberately conservative — false positives
// (refusing to update something we could have) annoy the user once with
// a clear message; false negatives (clobbering Homebrew's file) corrupt
// the package manager's state. We err toward the former.
func managedInstallReason() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return ""
	}
	exe = filepath.Clean(exe)

	type rule struct {
		prefix string
		msg    string
	}
	rules := []rule{
		{"/opt/homebrew/", "Installed via Homebrew — run `brew upgrade norcube` instead."},
		{"/usr/local/Cellar/", "Installed via Homebrew — run `brew upgrade norcube` instead."},
		{"/home/linuxbrew/.linuxbrew/", "Installed via Linuxbrew — run `brew upgrade norcube` instead."},
		{"/usr/bin/", "Installed via the system package manager — use apt/rpm/etc to upgrade."},
		{"/var/lib/snapd/snap/", "Installed via snap — run `snap refresh norcube` instead."},
		{"/var/lib/flatpak/", "Installed via flatpak — run `flatpak update` instead."},
	}
	for _, r := range rules {
		if strings.HasPrefix(exe, r.prefix) {
			return r.msg
		}
	}

	// Windows can ignore the unix-only rules above; nothing to do here yet.
	_ = runtime.GOOS
	return ""
}
