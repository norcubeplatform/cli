#!/bin/sh
# install.sh — Norcube CLI installer
#
# Usage (default install to /usr/local/bin):
#   curl -fsSL https://github.com/norcubeplatform/cli/raw/main/install.sh | sh
#
# Override the install directory:
#   curl -fsSL https://github.com/norcubeplatform/cli/raw/main/install.sh | INSTALL_DIR=$HOME/.local/bin sh
#
# Pin a version (default: latest):
#   curl -fsSL https://github.com/norcubeplatform/cli/raw/main/install.sh | VERSION=v0.2.0 sh
#
# This script detects OS+arch, downloads the matching archive from
# GitHub Releases, verifies it against checksums.txt, extracts norcube,
# moves it to $INSTALL_DIR, and creates an `nrc` symlink alongside as a
# short alias.

set -eu

REPO="norcubeplatform/cli"
# Default to a per-user directory so `norcube upgrade` never needs sudo.
# Matches the convention used by bun, flyctl, rustup, pnpm, volta, etc.
# Override with INSTALL_DIR=/usr/local/bin to put the binary on a system path
# (uses sudo when the destination isn't writable).
INSTALL_DIR="${INSTALL_DIR:-$HOME/.norcube/bin}"
VERSION="${VERSION:-latest}"

die() { printf 'error: %s\n' "$1" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"; }

need curl
need tar
need uname
need install

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$OS" in
    linux*)               OS="linux" ;;
    darwin*)              OS="darwin" ;;
    msys*|mingw*|cygwin*) die "Windows is supported via GitHub Releases; this script does not install on Windows. Download the .zip from https://github.com/$REPO/releases" ;;
    *) die "unsupported OS: $OS" ;;
esac

ARCH="$(uname -m)"
case "$ARCH" in
    x86_64|amd64)  ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) die "unsupported architecture: $ARCH" ;;
esac

# Resolve "latest" to a concrete tag. GitHub redirects /releases/latest to
# /releases/tag/vX.Y.Z when at least one release exists, and to /releases
# (no /tag/ segment) when no release has ever been cut. Detect the latter
# and produce a clear error instead of trying to download from a bogus URL.
if [ "$VERSION" = "latest" ]; then
    redirect="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest")"
    case "$redirect" in
        */tag/*)
            VERSION="${redirect##*/tag/}"
            VERSION="${VERSION%%[?#]*}"
            ;;
        *)
            die "no releases published yet for $REPO.
A maintainer must tag and push v0.1.0 first:
    git tag v0.1.0 && git push origin v0.1.0
See https://github.com/$REPO/releases" ;;
    esac
fi
case "$VERSION" in
    v[0-9]*) ;;
    *) die "resolved version $VERSION doesn't look like a semver tag; check https://github.com/$REPO/releases" ;;
esac

ARCHIVE="norcube_${OS}_${ARCH}.tar.gz"
DOWNLOAD_BASE="https://github.com/$REPO/releases/download/$VERSION"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM

printf 'Downloading norcube %s (%s/%s)…\n' "$VERSION" "$OS" "$ARCH"
curl -fsSL --retry 3 "$DOWNLOAD_BASE/$ARCHIVE"      -o "$TMP/$ARCHIVE" || die "download archive failed"
curl -fsSL --retry 3 "$DOWNLOAD_BASE/checksums.txt" -o "$TMP/checksums.txt" || die "download checksums failed"

# Verify SHA-256. Try shasum first (BSD/macOS), fall back to sha256sum (GNU/Linux).
printf 'Verifying checksum…\n'
expected="$(grep " $ARCHIVE\$" "$TMP/checksums.txt" | awk '{print $1}')"
[ -n "$expected" ] || die "no checksum entry for $ARCHIVE in checksums.txt"

if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "$TMP/$ARCHIVE" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "$TMP/$ARCHIVE" | awk '{print $1}')"
else
    die "no sha256sum/shasum available — install one or download from GitHub Releases manually"
fi
[ "$expected" = "$actual" ] || die "checksum mismatch: expected $expected, got $actual"

# Extract.
tar -xzf "$TMP/$ARCHIVE" -C "$TMP" norcube || die "tar extraction failed"

# Install. Decide whether we need sudo by walking up the install path until
# we find an existing ancestor directory, then testing its writability. The
# naive `-w "$INSTALL_DIR"` check fails for a fresh install (nothing exists
# yet) and would incorrectly fall back to sudo even when $HOME is writable.
probe="$INSTALL_DIR"
while [ ! -d "$probe" ]; do
    parent="$(dirname "$probe")"
    # Safety: dirname of "/" returns "/" — stop instead of looping forever.
    [ "$parent" = "$probe" ] && break
    probe="$parent"
done
SUDO=""
if [ ! -w "$probe" ]; then
    if command -v sudo >/dev/null 2>&1; then
        SUDO="sudo"
        printf 'Target %s is not writable; using sudo for install (upgrades will need sudo too).\n' "$probe"
    else
        die "$probe is not writable and sudo is not available"
    fi
fi
$SUDO install -d "$INSTALL_DIR"
$SUDO install -m 0755 "$TMP/norcube" "$INSTALL_DIR/norcube"

# Create the `nrc` short alias as a relative symlink alongside norcube.
# Using a relative target keeps the alias working when the directory is
# moved (e.g. a portable install copied to a USB stick).
$SUDO ln -sf norcube "$INSTALL_DIR/nrc"

printf '\nInstalled norcube %s to %s/norcube\n' "$VERSION" "$INSTALL_DIR"
printf 'Short alias: %s/nrc\n' "$INSTALL_DIR"

# Add INSTALL_DIR to the user's shell PATH so `norcube` / `nrc` work
# in new terminals without manual setup. Skipped when:
#   - INSTALL_DIR is already on $PATH (typical for /usr/local/bin),
#   - NORCUBE_NO_MODIFY_PATH=1 is set in the environment.
#
# The append is idempotent: re-running the installer detects the
# sentinel comment and leaves the rc file alone. Removing the block
# manually (or running `nrc uninstall`, if available) cleanly undoes
# the change.
case ":$PATH:" in
    *":$INSTALL_DIR:"*)
        printf 'Verify with: norcube --version\n'
        exit 0
        ;;
esac

if [ "${NORCUBE_NO_MODIFY_PATH:-0}" = "1" ]; then
    printf '\nNote: %s is not on PATH and NORCUBE_NO_MODIFY_PATH=1.\n' "$INSTALL_DIR"
    printf 'Add this to your shell rc manually:\n    export PATH="%s:$PATH"\n' "$INSTALL_DIR"
    printf 'Then verify with: norcube --version\n'
    exit 0
fi

SENTINEL="# >>> norcube CLI initialize >>>"
add_path_to_rc() {
    rc="$1"
    if [ -f "$rc" ] && grep -qF "$SENTINEL" "$rc"; then
        return 1 # already there
    fi
    mkdir -p "$(dirname "$rc")"
    {
        printf '\n%s\n' "$SENTINEL"
        printf '# Added by the norcube installer (https://github.com/%s).\n' "$REPO"
        printf '# Remove this block to take norcube off your PATH.\n'
        printf 'export PATH="%s:$PATH"\n' "$INSTALL_DIR"
        printf '# <<< norcube CLI initialize <<<\n'
    } >> "$rc"
    return 0
}

add_path_to_fish() {
    # Fish has its own syntax for PATH and prefers conf.d snippets
    # over editing the main config.fish, so a self-contained file
    # is the cleanest idiom.
    rc="$HOME/.config/fish/conf.d/norcube.fish"
    if [ -f "$rc" ] && grep -qF "$SENTINEL" "$rc"; then
        return 1
    fi
    mkdir -p "$(dirname "$rc")"
    {
        printf '%s\n' "$SENTINEL"
        printf '# Added by the norcube installer (https://github.com/%s).\n' "$REPO"
        printf '# Remove this file to take norcube off your PATH.\n'
        printf 'set -gx PATH "%s" $PATH\n' "$INSTALL_DIR"
    } > "$rc"
    return 0
}

# Detect the user's login shell. We only need the basename — full
# path varies across distributions.
SHELL_NAME="${SHELL##*/}"
RC_MODIFIED=""

case "$SHELL_NAME" in
    zsh)
        # ZDOTDIR honored if set; otherwise ~/.zshrc.
        rc_dir="${ZDOTDIR:-$HOME}"
        if add_path_to_rc "$rc_dir/.zshrc"; then
            RC_MODIFIED="$rc_dir/.zshrc"
        fi
        ;;
    bash)
        # macOS bash users get ~/.bash_profile (login shell convention);
        # Linux bash users get ~/.bashrc.
        if [ "$OS" = "darwin" ]; then
            if add_path_to_rc "$HOME/.bash_profile"; then
                RC_MODIFIED="$HOME/.bash_profile"
            fi
        else
            if add_path_to_rc "$HOME/.bashrc"; then
                RC_MODIFIED="$HOME/.bashrc"
            fi
        fi
        ;;
    fish)
        if add_path_to_fish; then
            RC_MODIFIED="$HOME/.config/fish/conf.d/norcube.fish"
        fi
        ;;
    *)
        # Unknown shell. Fall back to ~/.profile, sourced by most
        # POSIX login shells. Print extra guidance so the user knows
        # to source it.
        if add_path_to_rc "$HOME/.profile"; then
            RC_MODIFIED="$HOME/.profile"
            printf '\nUnknown shell %s — added the PATH line to ~/.profile.\n' "$SHELL_NAME"
            printf 'If your shell uses a different rc file, copy this line into it:\n    export PATH="%s:$PATH"\n' "$INSTALL_DIR"
        fi
        ;;
esac

if [ -n "$RC_MODIFIED" ]; then
    printf '\nAdded %s to your PATH (via %s).\n' "$INSTALL_DIR" "$RC_MODIFIED"
    printf 'Restart your terminal or run:\n'
    case "$SHELL_NAME" in
        fish) printf '    source %s\n' "$RC_MODIFIED" ;;
        *)    printf '    . %s\n' "$RC_MODIFIED" ;;
    esac
    printf 'Then verify with: norcube --version\n'
else
    # add_path_to_rc returned 1 — sentinel already present, nothing
    # to do. Could happen on a re-install after the user's PATH was
    # set up previously but a new shell hasn't yet picked it up.
    printf '\nPATH already configured (sentinel present in shell rc).\n'
    printf 'If `norcube --version` does not work, open a new terminal.\n'
fi
