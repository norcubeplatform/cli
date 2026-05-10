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
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
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

# Install. Use sudo only when the destination isn't writable, like rustup does.
SUDO=""
if [ ! -w "$INSTALL_DIR" ] && [ ! -w "$(dirname "$INSTALL_DIR")" ]; then
    if command -v sudo >/dev/null 2>&1; then
        SUDO="sudo"
    else
        die "$INSTALL_DIR is not writable and sudo is not available"
    fi
fi
$SUDO install -d "$INSTALL_DIR"
$SUDO install -m 0755 "$TMP/norcube" "$INSTALL_DIR/norcube"

# Create the `nrc` short alias as a relative symlink alongside norcube.
# Using a relative target keeps the alias working when the directory is
# moved (e.g. a portable install copied to a USB stick).
$SUDO ln -sf norcube "$INSTALL_DIR/nrc"

# PATH guidance: only print the hint when the install dir isn't already
# on $PATH, to avoid noise.
case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *) printf '\nWarning: %s is not on your PATH.\nAdd this to your shell rc:\n    export PATH="%s:$PATH"\n' "$INSTALL_DIR" "$INSTALL_DIR" ;;
esac

printf '\nInstalled norcube %s to %s/norcube\n' "$VERSION" "$INSTALL_DIR"
printf 'Short alias: %s/nrc\n' "$INSTALL_DIR"
printf 'Verify with: norcube --version\n'
