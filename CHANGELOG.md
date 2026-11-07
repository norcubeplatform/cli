# Changelog

Notable changes, newest first. Release notes on GitHub list every commit;
this file collects what users and scripts need to know.

## Unreleased

### New

- `norcube login` signs in through OAuth 2.0: the browser approves the
  sign-in and hands the CLI a one-time code over `127.0.0.1`, which the CLI
  exchanges with PKCE. The session's tokens no longer pass through the
  dashboard page. `login --device` uses the same OAuth server.
- `norcube login --scopes backup.read,langsync.sync` limits a login to the
  given scopes (the scopes CI tokens use; the default is `full`). The
  login prints the scopes Norcube granted. Commands outside them exit with
  code 5 (`insufficient_scope`) and the hint "This login is limited to …;
  run `norcube login` for full access". A scope this release doesn't
  know yet is still sent; Norcube Auth decides on it.
- `auth sessions list` has a SCOPES column (and `scopes` in JSON);
  `whoami` and `auth status` show the login's scopes; `doctor` shows the
  credential format and scopes and warns about the older format.
- `norcube logout` revokes the login's refresh token on Norcube (RFC 7009),
  which ends its CLI session and removes it from Connected apps. A failure
  on the server is reported, and the local sign-out happens anyway.
- `norcube login --force` ends the session it replaces on Norcube (the way
  `logout` does) before storing the new one. If that fails, it warns and
  signs in anyway.
- An account without an organization can sign in and use `whoami`, `org
  list` and `org create`: Norcube Auth tokens are minted without an
  organization. Product commands still need one.
- Fewer refreshes: a login refreshes one short-lived CLI token and
  exchanges it per service and organization without the profile lock, so
  parallel commands no longer queue behind each other.

### Upgrading

- A session from an older version moves to the new sign-in on its first
  command, once, without signing in again (`auth status` and `doctor` only
  describe the session and leave it as it is). A session made before
  Norcube recorded CLI sessions can't be moved: the CLI asks for one
  `norcube login`.
- After that move, an **older `norcube` binary** on the same machine must
  sign in again: the session it knew was used up by the move.
- An older binary that keeps **its own copy** of the session's refresh
  token (for example a copied credentials file) and presents it after the
  move counts as a replayed token: Norcube then ends the session, which
  signs the upgraded CLI out as well. Upgrade every `norcube` that shares a
  session, or sign each in separately.
- Against a Norcube Auth that doesn't offer the OAuth sign-in yet, the CLI
  keeps using the older sign-in for this release. CI tokens
  (`NORCUBE_TOKEN`) are unchanged.

### Changes for scripts

- The credentials item of a profile has a new format (2): refresh token,
  cached CLI access token, granted scopes and a format marker. Tokens per
  service are no longer stored between commands.
- `login -o json` has `scopes`; `whoami -o json` and `auth status -o json`
  have `scopes`, and `auth status` has `credentialFormat`. The `auth status`
  and `auth sessions list` tables have a SCOPES column.

## 0.25.0 (2026-11-06)

### New

- Norcube Monitor maintenance windows: `monitor maintenance list | get | create |
  update | cancel | finish`. A window pauses alerts for its monitors, stays out
  of their uptime, shows on every published status page displaying a target and
  mails subscribers when it is scheduled, starts and finishes, unless you pass
  `--notify-scheduled=false` and so on, or `--no-notify` for all three.
  - `--start` and `--end` take an RFC 3339 timestamp, `now` or a span from now
    (`+2h`, `+7d`). A bare `2h` is refused, because `--since` reads that as two
    hours ago. `--duration` takes `15m`, `2h` or `7d`.
  - Wrap a deploy: `id=$(nrc monitor maintenance create --monitor api --title
    "Deploy" --duration 15m -o id)`, deploy, `nrc monitor maintenance finish
    "$id"`. A window ends by itself if the deploy dies first.
  - `cancel` takes a scheduled window and confirms (`--yes`); `finish` takes one
    in progress and doesn't ask. Each one's refusal exits 8 and names the other.
  - CI tokens change windows with the new `monitor.maintenance` scope, which no
    other scope includes, and need `monitor.statuspages` too when a window shows
    on a published page; a refusal names the page. `cancel` and `finish` need
    only `monitor.maintenance`. Any Norcube Monitor scope reads them.
  - The API's delete has no command: it takes only cancelled windows, and `nrc
    api` reaches it, as it does the list of what a window could target.
- `-o id` prints only a result's id, one per line for a list, for capturing it in
  a script. A result without an id is a usage error.

### Changes

- `monitor status` shows monitors whose problem a maintenance window holds under
  "In maintenance" instead of "Problems", and doesn't count their incidents
  towards the verdict or `--fail-on`. A certificate or domain running out isn't
  held and stays a problem.
- `monitors run` says when a maintenance window holds the monitor. `--wait` still
  fails on a critical result: it is there to tell whether the deploy worked.
- `monitors list` and `get` show until when a window holds a monitor, and
  `incidents summary` how many open incidents are held.
- Refreshed the Norcube Monitor client and spec for maintenance windows and the
  summary of each status page in the page list. The summary has no command yet;
  `monitor status-pages list -o json` shows it.

### Changes for scripts

- The `-o` values and `nrc commands` `outputFormats` gain `id`.
- `monitor status` JSON and YAML: new `maintenance` list; a monitor whose problem
  a window holds moves there from `problems`. `incidents.open` gains
  `inMaintenance`, and the API leaves held incidents out of its other counts.
- `monitor status` `-o text`: two new columns at the end, the number of held
  incidents and the monitors in maintenance.
- `monitors list` and `get` `-o text`: a new column at the end, the end of the
  window holding the monitor. JSON and YAML: `maintenance` while one does.
- `monitors run` JSON, YAML and `job_failed` details: new `inMaintenance`.
  `-o text`: a new column at the end, `yes` or `no`.
- `incidents summary` `-o text`: a new column at the end, the held incidents.
- From the API, additively: `monitors stats get`, `monitors availability get` and
  results gain `maintenance` counts and `maintenanceMinutes`, incidents
  `maintenanceHeld`, status changes `maintenanceWindowId`, and incident timelines
  the event type `maintenance_hold`.

## 0.24.0 (2026-11-05)

### New

- Norcube Monitor status page subscribers: `monitor status-pages subscribers
  list | delete`, with pages named by id or exact slug. Addresses are personal
  data, so these commands need the `monitor.statuspages` scope on a CI token,
  even to list. A token without it exits 5 and says so. The API's subscriber
  count and CSV export have no commands yet; `nrc api` reaches them.
- `--notify` on `monitor status-pages incidents create` and `incidents updates
  create` emails the update to the page's subscribers.
- `--auto-post` on `monitor status-pages create` and `update` lets a page post
  confirmed critical monitor incidents by itself. It is optional on `create`
  and off unless you ask for it.
- `monitor status-pages get` shows whether a page posts automatically, and
  `incidents get` shows when a post was written by the system.

### Changes

- Refreshed the generated API clients and specs. No command, flag, JSON field or
  `-o text` column changes. Norcube Monitor's status page domain and logo
  endpoints, DomainRadar's expiry alert settings and Billing's plan features are
  in the clients but have no commands yet; `nrc api` reaches them.
- Refreshed the Norcube Monitor client and spec for status page subscribers and
  automatic posts.

### Changes for scripts

- `monitor status-pages incidents` JSON and YAML: posted incidents gain
  `autoPosted` and `autoManaged`, and their updates `notifySubscribers`.
- `monitor status-pages` page JSON and YAML: `settings` gains `autoPost`.

## 0.23.0 (2026-11-05)

### New

- Norcube Monitor status pages: `monitor status-pages list | get | create |
  update | delete | publish | unpublish | preview-link`, with pages named by
  id or exact slug.
  - `create` takes every setting as a flag and fills in none of them.
  - `layout get | set --file` (`-` reads standard input); `set` prints the
    saved layout, and `get -o json` prints the document `set` takes.
  - `incidents list | get | create | update` and `incidents updates create`
    post incidents and updates. Writes print the public address of the post,
    or say the page isn't published. `--component` takes a component's id or
    exact public name.
  - Changes to a published page and posts on it are public within a minute;
    the help of those commands says so.
  - Completion for page slugs and for a page's posted incidents.
  - CI tokens change status pages only with the new `monitor.statuspages`
    scope, which no other scope includes. Any Norcube Monitor scope reads
    them.

### Fixes

- Norcube Monitor's help no longer mentions a plan: Monitor is metered, so
  `monitors create | update --interval` and `status-pages create` now refer
  to your organization's limit.

### Changes for scripts

- `nrc commands -o json`: `service.name` now holds the short product name
  (`Backup`, `Billing`, `Monitor`, `Auth`) instead of `Norcube Backup`,
  `Norcube Billing`, `Norcube Monitor` and `Norcube Auth`. Match on
  `service.key`, which doesn't change. Error messages name products the same
  way.

## 0.22.0 (2026-11-04)

### New

- `norcube commands` lists every command with its summary, offline and
  without signing in. `-o json` or `-o yaml` prints the whole tree as one
  versioned document (`schemaVersion` 1) for scripts and AI agents: each
  command's path, aliases, help text, examples, arguments and flags (type,
  default, allowed values, required, repeatable), whether it reads, writes
  or changes only local state (`effect`), asks for confirmation (`--yes`),
  takes `--wait` or pages, and which service and OAuth audience it uses.
  The global flags are listed once. `norcube commands backup` narrows the
  tree to one group; `--all` adds hidden commands and the old names of
  renamed ones.
- Shell completion offers the allowed values of `-o`, `auth token
  --audience`, `langsync init --seed` and `backup datasources update
  --region`.
- Norcube Monitor (formerly PulseCheck) commands: `norcube monitor`
  covers every operation of the Norcube Monitor API an organization
  member has.
  - `monitor status` shows open incidents, monitors by status and every
    monitor that needs attention; `--fail-on critical` (or `warning`)
    fails a CI job while such an incident is open.
  - `monitors list | get | create | update | delete | pause | resume`,
    with typed flags per type (`--type http | keyword | ssl | domain`) or
    the API's JSON body with `--from-file`; secret header values only from
    a file or stdin (`--secret-headers-file`). Monitors are named by id or
    exact name.
  - `monitors run <monitor>` runs a check now; with `--wait` a critical or
    inconclusive result (or a warning with `--fail-on warning`) exits 1
    with `job_failed`, for deploy pipelines.
  - `monitors bulk pause | resume | delete` by ids, a file, or the list
    filters, in batches of 100.
  - `monitors results list`, `status-changes list` (alias `history`),
    `stats get`, `availability get`, `certificate get`, `alerts get | set`.
  - `incidents list` (every API filter), `get` (timeline and alerts),
    `acknowledge` (alias `ack`), `summary`.
  - `channels list | get | create | update | delete | test | verify` and
    `channels deliveries list`. A webhook's signing secret is written to
    `--secret-file` and never printed; Slack and Discord URLs are read
    from a file or stdin; `verify` asks for the code on a terminal or
    reads it with `--code-file`.
  - Completion for monitor, channel and incident ids; `nrc api monitor`
    and `auth token --audience monitor`; CI tokens with the
    `monitor.read`, `monitor.check`, `monitor.write` and
    `monitor.channels` scopes (alert channels need the last one;
    `monitor.write` doesn't include it).
- Errors of services that answer RFC 9457 problems (Norcube Monitor) show
  the problem's detail and invalid fields, and `-o json` errors carry its
  `reason`, field `errors` and `data` in `details`.

### Changes for scripts

- The `norcube commands` document is a contract: within `schemaVersion` 1
  fields, values and commands are only ever added. A breaking change
  bumps `schemaVersion`.
- New error code `unhealthy` (exit 1): `monitor status --fail-on`
  found what it was told to fail on; `details` holds the report.
- `--endpoint-url`, `NORCUBE_MONITOR_URL` and the `endpoints` table of
  a profile accept the new service key `monitor`.

## 0.21.4 (2026-10-30)

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
