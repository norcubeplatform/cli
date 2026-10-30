# Changelog

Notable changes, newest first. Release notes on GitHub list every commit;
this file collects what users and scripts need to know.

## Unreleased

### Fixes

- Release binaries are built with Go 1.26.10. Earlier releases used Go
  1.26.1, whose crypto/x509 and net/http have known vulnerabilities
  (GO-2026-6809, GO-2026-6617) reachable from every HTTPS call.
  golang.org/x/crypto, x/sys, x/term and x/text are updated as well.

## 0.21.3 (2026-10-29)

### Fixes

- A permission refusal while using a CI token now explains that some
  actions (such as Backup restores and data source or policy changes)
  always need a signed-in person, and points to `norcube auth ci-token
  scopes`. Before, it was a bare "permission denied".

## 0.21.2 (2026-10-29)

### Fixes

- Release publishing: 0.21.1 was published, but its Homebrew cask update
  and build provenance attestations failed. 0.21.2 has the same changes,
  with the cask and attestations.

## 0.21.1 (2026-10-29)

The first published release with the changes below. 0.21.0 was tagged but
never published: its macOS notarization timed out before Apple answered.

## 0.21.0 (not published)

### New

- Releases are verifiable: `checksums.txt` is signed keylessly with
  Sigstore (`checksums.txt.sigstore.json`), every archive, package and SBOM
  has a GitHub build provenance attestation, and each archive ships with an
  SPDX SBOM (`<archive>.sbom.json`). The README's "Verify a release" shows
  the `cosign verify-blob` and `gh attestation verify` commands.
- Linux packages: `.deb`, `.rpm` and `.apk` for amd64 and arm64, with the
  `nrc` alias, shell completions and man pages.
- Scoop and winget manifests are built with every release; publishing them
  is switched on once their repositories and tokens are set up.
- The Homebrew cask installs the `nrc` alias.
- macOS binaries are signed with a Developer ID certificate and notarized
  once the signing keys are configured for the release workflow.

### Changes for scripts

- `norcube upgrade` installs only the release archive for its platform
  (`norcube_<os>_<arch>.tar.gz` or `.zip`) and only after its SHA-256
  matches `checksums.txt`; a release without that entry is refused. The
  background update check uses the same rule.
- `norcube upgrade` recognizes Scoop, winget and Homebrew cask installs on
  Intel Macs, and tells you to upgrade with that tool (`--force`
  overrides). The messages for Homebrew and system packages are reworded.

### Fixes

- `backup policies create | update`: the `--keep-last` help said "keep at
  most"; it is the number of newest backups that are always kept,
  however old. `--retention-days` now says the newest `--keep-last`
  backups are never deleted and 0 keeps every backup.

## 0.20.0 (2026-10-29)

### New

- Backup: `backup jobs run --datasource <id>` (`--policy`, `--wait`) and
  `jobs get`; `datasources create | update` with the connection string
  read from `--connection-uri-file` (a file or stdin); `restores list |
  get | create` (create asks first, `--yes` for scripts, `--wait`);
  `restore-tests run` without a backup id tests the newest successful
  backup, and `--wait` waits for the verdict; `restore-tests list` and
  `restore-tests settings get | update`; `policies create | update |
  attach` and `destinations list`.
- Billing: `norcube billing status`, `subscriptions list`, `usage estimate
  | metrics`, `invoices list | download <id>` (PDF) and `budget get | set`
  (spending caps and alert thresholds; set asks first, `--yes` for
  scripts). A CI token with `billing.read` runs every read command.
- LangSync: `translations get <termId>`, `terms import --wait` and
  `terms import status <import-id>`, `glossary languages attach
  <language>`.

### Changes for scripts

- A command that waits for a job (`--wait`) which then fails exits 1 with
  the new error code `job_failed`, with the job as `details` in the JSON
  error. This includes `domainradar bulk create --wait`, `names generate
  --wait` and `names check --wait`, which exited 0 on a failed or
  cancelled job.
- `backup policies list` without `--datasource` lists the organization's
  backup policies instead of failing.
- `backup restore-tests run` takes the backup job id as an optional
  argument.
- LangSync glossary item type `n/a` is shown as `other` in tables and
  `-o text`; JSON keeps the API's `n/a`. `--type other` (the default) and
  `--type n/a` both send `n/a`.
- `langsync prompts get` asks for the one prompt instead of listing them
  all; a prompt you can't see is still not found (exit 3).
- `langsync terms import` points to `terms import status` instead of
  `terms list` to follow an import.
- `domainradar hits list --watcher` sends the filter as `watcherId`, the
  query parameter the API reads.

## 0.19.0 (2026-10-29)

### New

- DomainRadar: `norcube domainradar check <domain>...` (args, `--file` or
  stdin; `--mode`, `--max-age`), `bulk create | list | get | results |
  watch | export | cancel | delete` (`--wait` on create, a live stream with
  `watch`, CSV or JSON with `export`), `saved list | add | remove`,
  `watchers` and `hits` for brand monitoring, and `names generate | list |
  get | results | check`.
- LangSync: `terms update`, `terms import <file.csv>` (columns mapped by
  header, `--column` for the rest, `--dry-run`), `translations set |
  suggest | translate-missing`, `prompts list | get | create | update |
  delete`, `namespaces update --default-prompt <id|none>`, `glossary get`
  and `glossary items list | get | create | translate | delete`,
  `api-keys list | create | revoke` and `audit list`.
- `langsync terms list` shows an AI column: translations still exactly as
  AI produced them (`aiGenerated` in JSON).

### Changes for scripts

- `langsync namespaces update` with nothing to change, and an invalid term
  id in `langsync terms delete`, are usage errors (exit 2) instead of 1.
- `langsync terms list` has a new AI column before CREATED; `-o text`
  consumers that read CREATED by position need the next field.
- Tables print a line break inside a value as a space, so every row stays
  on one line.

## 0.18.0 (2026-10-29)

### New

- CI tokens: `NORCUBE_TOKEN` (or `--token-file`) accepts an organization's
  CI token (`nrc_ci_…`). The CLI checks its checksum locally, exchanges it
  for a short-lived token per service and keeps those in memory only.
  `norcube auth ci-token create | list | revoke | scopes` manages them;
  `norcube auth status` shows the token's organization, scopes and expiry.
- `norcube login --device`: approve the sign-in on another device (a phone
  works) with a short code, for SSH and headless machines.

### Changes for scripts

- With a CI token, `--org` / `NORCUBE_ORG` / `.langsync.json` must name the
  token's own organization (a usage error otherwise), and commands that act
  as a person (`whoami`, `org …`, `auth sessions …`) explain that they need
  `norcube login`.

## 0.16.0 and 0.17.0 (2026-10-29)

### Changes for scripts

- `backup datasources list --query <text>` is now `--search <text>`:
  `--query` is the JMESPath filter on every command.
- Commands that printed plain text (`whoami`, `org …`, `config path`,
  `configure get`, `auth token`, actions like `namespaces create`) follow
  `-o` now; use `-o text` for the bare value.
- `-o yaml` uses the same field names as `-o json` (it used lowercased Go
  names such as `isactive`).
- Failures that exited 1 now exit 3 (not found), 6 (rate limited),
  7 (timeout) or 8 (conflict) when the reason is known; 2 is a usage
  error, 4 means sign in, 5 means permission denied.
- Commands follow `norcube <service> <resource> <verb>` with plural
  resources (`backup jobs list`, `backup datasources`, `langsync terms`).
  Every old name keeps working; `NORCUBE_DEPRECATION_WARNINGS=1` prints a
  note when one is used.
- `langsync languages rm` only means `remove` (detach from a namespace);
  it no longer can resolve to `delete`.

### New

- Profiles (`--profile`, `norcube configure`), with the existing config
  migrated automatically into the `default` profile. The session moves
  into the profile the first time a command uses it; an older CLI on the
  same machine then asks you to sign in again.
- `norcube api` for any endpoint, `norcube doctor`, `--debug`, `-o text`,
  `--query`, dynamic shell completion, CLI sessions you can list and revoke.
