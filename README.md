# norcube CLI

[![release](https://img.shields.io/github/v/release/norcubeplatform/cli)](https://github.com/norcubeplatform/cli/releases/latest)
[![ci](https://github.com/norcubeplatform/cli/actions/workflows/ci.yaml/badge.svg)](https://github.com/norcubeplatform/cli/actions/workflows/ci.yaml)
[![license](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

Command-line interface for the [Norcube platform](https://norcube.com). Manage backups, namespaces, organizations, and more from the terminal.

What it's best at: proving you're never locked in to Norcube Backup. `norcube backup jobs download` streams any backup straight from storage into `pg_restore` or `mongorestore`, and `norcube backup health` shows whether your backups actually restore — restore-tested against real throwaway databases, not just checksummed.

![norcube CLI demo](demo/demo.gif)

<sub>Scripted demo with sanitized names: whoami, Backup health, scripting with `-o json --query`, any endpoint with `norcube api`, and your CLI sessions. The tape and stub live in [`demo/`](demo/).</sub>

> **Status**: pre-1.0. Norcube Backup (data sources, backups on demand and downloads, restores, restore tests and their settings, health, policies), LangSync (namespaces, terms, translations, languages, prompts, glossary, API keys, CSV import, audit log, project sync), DomainRadar (domain checks, bulk checks, saved domains, brand monitoring, name generation), Billing (status, subscriptions, usage, invoices, spending caps), organizations, profiles, and `norcube api` for everything else. See the [changelog](CHANGELOG.md).

## Install

**Homebrew (macOS / Linux):**

```bash
brew install --cask norcubeplatform/tap/norcube
```

Installs `norcube`, the short alias `nrc`, and bash/zsh/fish completions.
Upgrade with `brew upgrade norcube`.

**Debian / Ubuntu, Fedora / RHEL / openSUSE, Alpine:**

Every release has `.deb`, `.rpm` and `.apk` packages for amd64 and arm64.
They install `/usr/bin/norcube`, the `nrc` alias, shell completions and man
pages (`man norcube`).

```bash
VERSION=0.21.0   # the release to install, without the leading v
ARCH=amd64       # or arm64
BASE=https://github.com/norcubeplatform/cli/releases/download/v$VERSION

# Debian / Ubuntu
curl -fsSLO "$BASE/norcube_${VERSION}_linux_${ARCH}.deb"
sudo apt install "./norcube_${VERSION}_linux_${ARCH}.deb"

# Fedora / RHEL / openSUSE
curl -fsSLO "$BASE/norcube_${VERSION}_linux_${ARCH}.rpm"
sudo dnf install "./norcube_${VERSION}_linux_${ARCH}.rpm"   # or: sudo zypper install ...

# Alpine (the package isn't signed with an apk key; verify the download first)
curl -fsSLO "$BASE/norcube_${VERSION}_linux_${ARCH}.apk"
sudo apk add --allow-untrusted "./norcube_${VERSION}_linux_${ARCH}.apk"
```

The packages aren't served from an apt/dnf repository, so upgrade by
installing the next release's package the same way. [Verify a
release](#verify-a-release) shows how to check a download before installing
it.

**One-liner (macOS / Linux):**

```bash
curl -fsSL https://github.com/norcubeplatform/cli/raw/main/install.sh | sh
```

Installs to `~/.norcube/bin/norcube` and creates a short alias `nrc` →
`norcube` in the same directory. A per-user location means `norcube upgrade`
never needs sudo. Override the install directory with
`INSTALL_DIR=/usr/local/bin` (system-wide; later upgrades will need sudo),
or pin a version with `VERSION=v0.2.0`.

If `~/.norcube/bin` isn't on your `PATH` yet, the script prints the export
line to add to your shell rc:

```bash
export PATH="$HOME/.norcube/bin:$PATH"
```

The script verifies the SHA-256 against the release's `checksums.txt`
before writing to disk; it'll abort if the download was tampered with.

**Direct download:**

Download the archive for your platform from the [GitHub Releases
page](https://github.com/norcubeplatform/cli/releases/latest)
(`norcube_<os>_<arch>.tar.gz`, or `norcube_windows_amd64.zip`), extract
`norcube` (`norcube.exe` on Windows), and put it on your `PATH`. The archive
also holds shell completions (`completions/`) and man pages (`manpages/`).

**From source (requires Go ≥ 1.24):**

```bash
git clone git@github.com:norcubeplatform/cli.git norcube-cli
cd norcube-cli
make install   # installs `norcube` to $GOPATH/bin
```

## Upgrade

```bash
norcube upgrade
```

Checks GitHub for newer releases, downloads the archive for your platform,
verifies its SHA-256 against the release's `checksums.txt` (both over TLS
from GitHub), and atomically replaces the running binary. Nothing is
replaced when the checksum is missing or doesn't match. Run
`norcube --version` afterward to confirm.

If `norcube` was installed with Homebrew or a deb/rpm/apk package (or a
Scoop or winget package, once those are published), the upgrade command
detects that and tells you to upgrade with that tool instead; pass
`--force` if you really want to override it.

## Verify a release

Every release is built by the [release
workflow](.github/workflows/release.yaml) from a tag, and ships:

- `checksums.txt`: the SHA-256 of every archive, package and SBOM;
- `checksums.txt.sigstore.json`: a keyless [Sigstore](https://www.sigstore.dev)
  signature of `checksums.txt`, made with the workflow's GitHub identity;
- `<archive>.sbom.json`: an SPDX software bill of materials per archive;
- a [GitHub artifact
  attestation](https://docs.github.com/actions/security-for-github-actions/using-artifact-attestations)
  (SLSA build provenance) for every file in `checksums.txt`.

`norcube upgrade` and `install.sh` check the SHA-256. To also check that
the checksums were signed by this repository's release workflow, install
[cosign](https://docs.sigstore.dev/cosign/system_config/installation/) and
run:

```bash
VERSION=v0.21.0
gh release download "$VERSION" --repo norcubeplatform/cli \
  --pattern checksums.txt --pattern checksums.txt.sigstore.json \
  --pattern norcube_linux_amd64.tar.gz

cosign verify-blob checksums.txt \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github\.com/norcubeplatform/cli/\.github/workflows/release\.yaml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

# then check the download against the verified checksums
sha256sum --ignore-missing -c checksums.txt          # Linux
shasum -a 256 --ignore-missing -c checksums.txt      # macOS
```

To pin the exact release instead of any tag, replace
`--certificate-identity-regexp ...` with
`--certificate-identity "https://github.com/norcubeplatform/cli/.github/workflows/release.yaml@refs/tags/$VERSION"`.

To check the build provenance with the [GitHub CLI](https://cli.github.com):

```bash
gh attestation verify norcube_linux_amd64.tar.gz \
  --repo norcubeplatform/cli \
  --signer-workflow norcubeplatform/cli/.github/workflows/release.yaml
```

The macOS binaries are signed with a Developer ID certificate and notarized
by Apple when the release was built with the signing keys configured;
`codesign -dv --verbose=2 "$(command -v norcube)"` shows the signature.

## Quick start

```bash
norcube login                       # opens your browser, signs you in
norcube whoami                      # who you are and which organization commands target
norcube org use my-org              # target another organization
norcube backup jobs list            # every backup job in the organization
norcube backup health               # which backups actually restore
norcube langsync namespaces list    # LangSync namespaces
norcube domainradar check acme.dev  # is a domain free?
norcube billing usage estimate      # this month's charges so far
norcube doctor                      # something not working? start here
```

`nrc` is a short alias for `norcube` (the install script, the Homebrew cask
and the Linux packages create it). Add `-o json` and `--query` to any command for
scripting, and `--org <slug>` to target another organization for one
command.

## In CI and on servers

Create a CI token for the organization (scopes limit what it can do, and it
always expires), store it as the secret `NORCUBE_TOKEN`, and run commands
as usual:

```bash
norcube auth ci-token create --name "GitHub Actions" --scopes langsync.sync -o text | gh secret set NORCUBE_TOKEN
```

```yaml
- uses: norcubeplatform/setup-norcube@v1
  with:
    token: ${{ secrets.NORCUBE_TOKEN }}
- run: nrc langsync sync
```

On a machine without a browser, `norcube login --device` signs you in with a
code you approve on your phone or laptop. See
[CI and automation](https://docs.norcube.com/platform/cli#ci-and-automation).

## Documentation

- [CLI guide](https://docs.norcube.com/platform/cli): login, profiles,
  organizations, output and scripting, network settings, exit codes,
  troubleshooting.
- [Command reference](https://docs.norcube.com/platform/cli-reference):
  every command and flag, generated from this repository.
- [Backup CLI](https://docs.norcube.com/backup/cli),
  [LangSync CLI](https://docs.norcube.com/langsync/cli), including
  [project sync](https://docs.norcube.com/langsync/cli/project-sync), and
  [DomainRadar CLI](https://docs.norcube.com/domainradar/cli).
- `norcube <command> --help` shows the same reference offline.

## Contributing and security

Contributions are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for the
dev setup and the DCO sign-off we require instead of a CLA, and
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for how the code is organized
and how to add a service or a command.
Security issues go through [private vulnerability
reporting](https://github.com/norcubeplatform/cli/security), not public
issues — details in [SECURITY.md](SECURITY.md).

## Open core

This CLI is open source (Apache 2.0) and always will be: the tool that
proves you can take your data and leave should not itself be a lock-in.
The Norcube platform it talks to — the hosted backup scheduling, restore
testing, alerting, and the rest — is a commercial service. The API
surface between them is documented at
[docs.norcube.com](https://docs.norcube.com), so the CLI is also a
reference client if you'd rather script against the API directly.

## License

Apache License 2.0 — see [LICENSE](LICENSE). Use it, fork it, ship it,
including commercially.

"Norcube" and the Norcube logo are trademarks of Norcube; the license does
not grant trademark rights. A fork is welcome to exist, just not to call
itself Norcube.
