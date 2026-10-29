# norcube CLI

[![release](https://img.shields.io/github/v/release/norcubeplatform/cli)](https://github.com/norcubeplatform/cli/releases/latest)
[![ci](https://github.com/norcubeplatform/cli/actions/workflows/ci.yaml/badge.svg)](https://github.com/norcubeplatform/cli/actions/workflows/ci.yaml)
[![license](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

Command-line interface for the [Norcube platform](https://norcube.com). Manage backups, namespaces, organizations, and more from the terminal.

Its flagship job: proving you're never locked in to Norcube Backup. `norcube backup download` streams any backup straight from storage into `pg_restore` or `mongorestore`, and `norcube backup health` shows whether your backups actually restore — restore-tested against real throwaway databases, not just checksummed.

![norcube CLI demo](demo/demo.gif)

<sub>Scripted demo with sanitized names — the tape and stub live in [`demo/`](demo/).</sub>

> **Status**: v0 — login + organization management, Norcube Backup (browse, pause/resume, download, restore tests, health), full Langsync CRUD, and project-level `langsync init` / `langsync sync` for repo-side translation files.

## Install

**Homebrew (macOS / Linux):**

```bash
brew install --cask norcubeplatform/tap/norcube
```

Installs the binary plus bash/zsh/fish completions. The cask prints how to
add the short `nrc` alias if you want it.

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

**Manual install:**

Download the matching archive from the [GitHub Releases page](https://github.com/norcubeplatform/cli/releases/latest), extract `norcube`, and put it on your `$PATH`.

**Windows:**

Download the `.zip` from GitHub Releases. No installer yet.

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

Checks GitHub for newer releases, verifies the checksum, and atomically
replaces the running binary. Run `norcube --version` afterward to confirm.

If `norcube` was installed via Homebrew / apt / rpm, the upgrade command
detects that and tells you to use the package manager instead — pass
`--force` if you really want to override it.

## Short alias

Both `norcube` and `nrc` are installed pointing to the same binary, so:

```bash
nrc backup list --all-pages
nrc whoami
nrc upgrade
```

Documentation uses `norcube` everywhere because that's the canonical
name; substitute `nrc` in your shell when you want fewer keystrokes.

## Quick start

```bash
norcube login            # opens your browser, signs you in
norcube whoami           # prints your user + active org
norcube org list         # all orgs you belong to
norcube org use my-org   # switch the active org
norcube logout
```

Override the active org for a single command without switching:

```bash
norcube --org my-other-org whoami
```

## Profiles

Most people never need one: `norcube login` signs you in to the profile
`default`, and every command uses it. A profile is a named organization and
session, like AWS CLI profiles, for keeping several accounts or
organizations side by side (an agency working in a client's account, a CI
job with its own sign-in):

```bash
norcube login                                # profile "default"
norcube configure --profile client-acme      # pick the organization for a second profile
norcube login --profile client-acme          # a separate session for it
norcube backup list --profile client-acme
NORCUBE_PROFILE=client-acme norcube whoami
norcube config profiles                      # list profiles; * marks the selected one
norcube auth status --all-profiles           # who is signed in where
norcube logout --profile client-acme         # or: logout --all
```

The selected profile is `--profile`, else `NORCUBE_PROFILE`, else
`default_profile` in the config file, else `default`. Logging in to a
profile that doesn't exist yet creates it. A profile holds one session for
one set of endpoints, so `login` refuses to replace a profile's session with
one from other endpoints (for example when `NORCUBE_AUTH_URL` points
elsewhere); use a separate profile instead.

What each command uses, highest precedence first:

| Setting | Order |
|---|---|
| Profile | `--profile` › `NORCUBE_PROFILE` › `default_profile` › `default` |
| Organization | `--org` › `NORCUBE_ORG` › project file (`.langsync.json`) › profile `organization` |
| Endpoints | `--endpoint-url service=url` (and `--auth-url`, `--web-app`) › `NORCUBE_<SERVICE>_URL` › profile `endpoints` › the profile's environment |
| Credentials | `--token-file` › `NORCUBE_TOKEN` › profile `credential_store` › OS keyring |
| Output | `-o` › `NORCUBE_OUTPUT` › profile `output` › `[defaults] output` › `table` |

`norcube config list` shows every effective value and where it came from.
Change settings with `norcube configure set|get|unset <key> [value]`
(`norcube configure --help` lists the keys) or edit the file with
`norcube config edit`.

The config file:

```toml
schema_version = 2
default_profile = "work"

[defaults]
output = "table"
max_attempts = 3
read_timeout = "60s"

[profiles.work]
environment = "production"     # production (default) | custom
organization = "acme"          # slug or id; the CLI records org_id after resolving it
credential_store = "keyring"   # keyring | file | env

[profiles.client-acme]
organization = "client-acme"

[profiles.selfhosted]
environment = "custom"         # services not listed fall back to production
[profiles.selfhosted.endpoints]
auth = "https://auth.norcube.internal.example"
web_app = "https://app.norcube.internal.example"
langsync = "https://langsync.norcube.internal.example"

[profiles.ci]
organization = "acme"
credential_store = "env"       # takes an access token from NORCUBE_TOKEN; never stores anything
```

The first run of this version converts an older flat `config.toml` into a
`default` profile (keeping a `config.toml.bak-<time>` copy) and copies the
stored session into it, so nothing changes for you. Keys the CLI doesn't
know are kept when it writes the file.

`norcube auth token [--audience langsync] [--org acme]` prints a
short-lived access token for curl and other tools. `norcube auth status`
asks Norcube Auth whether the stored session still works and shows
SIGNED IN as `yes`, `no`, `revoked/expired` (ended on the server) or
`unknown` (Norcube Auth unreachable); in JSON that's
`session: active | none | ended | unknown`. It exits with code 4 when the
profile can't be used (`no` or `revoked/expired`). A cached access token
that hasn't expired counts as active without a request, since it was minted
from the session within the last few minutes. `--offline` skips the check
and only looks at the stored credentials.

### CLI sessions

Every `norcube login` creates a CLI session for your account, labeled with
the machine's hostname and the CLI version. `norcube auth sessions list`
shows them (the same list as Account › CLI sessions in the dashboard) and
`norcube auth sessions revoke <id>` or `--others` ends them: the refresh
token stops working at once, and access tokens it already issued expire
within minutes. `norcube logout` ends the profile's own session on the
server before forgetting it locally; if the server can't be reached it
still signs out locally and warns you. Sessions you don't use for 30 days
end on their own. If a session was ended elsewhere (revoked in the
dashboard, for example), the next `norcube login` notices, forgets it and
signs you in again; no `--force` needed.

## How login works

`norcube login` uses an OAuth-style loopback flow (the same pattern as `gh auth login`, `flyctl auth login`, `stripe login`):

1. The CLI starts a one-shot HTTP server on a random `127.0.0.1` port and opens your browser to `<web-app>/cli-login?port=<P>&state=<nonce>&host=<hostname>&client=<user-agent>`. The page shows the device before you authorize it, and the session is labeled with it.
2. After you authenticate (or if you're already signed in), the web page mints a fresh, CLI-specific session via `POST /auth/cli/exchange` and POSTs the tokens to your loopback server.
3. The CLI verifies the state nonce, stores the refresh token for the selected profile in your OS keyring (Keychain / Secret Service / Windows Credential Manager) and exits.

No browser on hand to open automatically (a remote desktop, a locked-down shell)? `norcube login --no-browser` prints the URL instead; open it in a browser on the same machine. Only the configured web app's origin may call the loopback server.

Your password never touches the CLI. The CLI session is independent of your browser session — logging out of the web app does not log out the CLI (and vice versa).

## Configuration

State lives in two places:

- **Secrets** (refresh + cached access tokens) — one item per profile (`profile:<name>`) in your OS keyring under the `norcube` service. Without a keyring (containers, CI, SSH without D-Bus) the CLI says so once and uses `credentials/<profile>.json` next to the config file instead (0600 files in a 0700 directory); `credential_store = "file"` picks that store explicitly.
- **Preferences** (profiles: environment, organization, endpoints, user) — `config.toml` in `$XDG_CONFIG_HOME/norcube/` when `XDG_CONFIG_HOME` is set (on every OS), else your user config directory: `~/.config/norcube/` on Linux, `~/Library/Application Support/norcube/` on macOS, `%APPDATA%\norcube\` on Windows. `NORCUBE_CONFIG_FILE` names the file directly. Refresh lock files live next to it in `locks/`.
- **Cache** (slug → id of your organizations, kept 24 hours, per profile) — `norcube/orgs.json` in your user cache directory. No secrets; cleared by `norcube logout`.

| Env var | Flag | Effect |
|---|---|---|
| `NORCUBE_PROFILE` | `--profile` | Profile to use (see Profiles) |
| `NORCUBE_ORG` | `--org` | Run against a specific organization (slug or id) |
| `NORCUBE_OUTPUT` | `-o, --output {table,json,yaml,text}` | Output format (see Output) |
| – | `--query <JMESPath>` | Filter or reshape the result before it is printed (see Output) |
| `NORCUBE_ERROR_FORMAT=json` | — | Write every error as JSON on stderr, whatever the output format |
| `NO_COLOR`, `CLICOLOR=0` | `--no-color` | No color or styling (also off whenever the stream isn't a terminal) |
| `NORCUBE_TOKEN` | `--token-file` | Use this access token instead of the profile's session (the flag reads it from a file) |
| `NORCUBE_CONFIG_FILE` | — | Path of the config file |
| – | `--endpoint-url service=url` | Override one service's endpoint (repeatable; `auth`, `web_app`, `snapdb`, `langsync`, `domainradar`, `billing`, `prompthub`) |
| `NORCUBE_AUTH_URL` | `--auth-url` | Override the auth service base URL |
| `NORCUBE_SNAPDB_URL` / `NORCUBE_LANGSYNC_URL` / `NORCUBE_DOMAINRADAR_URL` / `NORCUBE_BILLING_URL` / `NORCUBE_PROMPTHUB_URL` | — | Override individual service URLs |
| `NORCUBE_WEB_APP` | `--web-app` | Override the web app URL used during browser login |
| `NORCUBE_READ_TIMEOUT` | `--cli-read-timeout` | Seconds to wait for each API response, 0 for no limit (default 60). Downloads instead fail after 60 seconds without data |
| `NORCUBE_CONNECT_TIMEOUT` | `--cli-connect-timeout` | Seconds to wait for a connection, 0 for no limit (default 10) |
| `NORCUBE_MAX_ATTEMPTS` | `--max-attempts` | Attempts for requests that hit throttling (429), 502/503/504 or a network error (default 3, 1 disables retries). Only requests that are safe to repeat are retried |
| `NORCUBE_CA_BUNDLE` | — | PEM file of extra CA certificates to trust (TLS-inspecting proxies). `HTTPS_PROXY` / `NO_PROXY` are honored too |

Flags win over environment variables, which win over the config file (`[defaults]` holds `output`, `pager`, `max_attempts`, `read_timeout`, `connect_timeout`). Requests carry a `User-Agent` of the form `norcube/<version> (<os>; <arch>) go/<version>`.

## Output

Every command prints its result in the format `-o` picks:

| Format | What you get |
|---|---|
| `table` (default) | Aligned columns or a short sentence for people; pages through `$PAGER` on a terminal (`--no-pager` to stop it) |
| `json` | The resource itself: lists are arrays, field names are the API's own (camelCase); actions return the affected resource |
| `yaml` | The same data and field names as `json` |
| `text` | Tab-separated values, no header, one record per line, for `cut`, `awk` and shell loops |

Tables show times in your local time zone; `-o text` prints them as RFC 3339
in UTC, and `json`/`yaml` keep the API's own values.

`--query` takes a [JMESPath](https://jmespath.org) expression, applied to what `-o json` would print before formatting, so it works with every format:

```bash
norcube backup list -o json --query "[?jobStatus=='failed'].jobId"
norcube backup datasource list -o text --query "[?!isActive].id"
norcube whoami -o text --query user.email
norcube org list --query "[].{slug: slug, active: active}"     # a table of just those columns
for ns in $(norcube langsync namespace list -o text --query "[].name"); do norcube langsync mark list -n "$ns" -o json; done
```

An invalid expression is a usage error (exit 2) and fails before any request is made.

**stdout is data, stderr is everything else.** Progress bars, the `langsync sync` dashboard, prompts, confirmations, hints and `Cancelled.` go to stderr, so `-o json | jq` always sees clean JSON. `sync`, `pull` and `init` print one summary per namespace with `-o json|yaml|text`; with `-o table` the dashboard is the output.

**Lists.** Paginated lists take `--page-size` (alias `--limit`), `--cursor`, `--all-pages` and `--max-items`. They print as plain arrays; when more results exist, the next cursor goes to stderr: a sentence with `-o table`, `nextCursor=<cursor>` otherwise, so a script can read it with `2>`.

**Errors.** On a terminal an error is the message, then a hint on its own line. With `-o json` or `-o yaml` (or `NORCUBE_ERROR_FORMAT=json`) stderr gets one JSON object instead:

```json
{
  "error": {
    "code": "not_found",
    "message": "data source not found",
    "status": 404,
    "service": "backup",
    "method": "GET",
    "url": "https://snapdb.api.norcube.com/app/v1/datasources/9f8e…",
    "type": "NOT_FOUND",
    "requestId": "…"
  }
}
```

`code` is stable: `usage_error`, `validation_failed`, `org_not_found`, `not_found`, `login_required`, `permission_denied`, `insufficient_scope`, `rate_limited`, `timeout`, `conflict`, `server_error`, `interrupted`, `error`. Include `requestId` when you report a problem.

### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Error (network, server, a failed operation) |
| 2 | Usage or validation error: unknown command or flag, wrong arguments, invalid `--query`, unknown `--org` slug, a prompt needed without a terminal (pass the value or `--yes`) |
| 3 | Not found (the API answered 404) |
| 4 | Not signed in or the session expired: run `norcube login` (with `--profile <name>` for a named profile) |
| 5 | Permission denied: not a member of the organization, or missing a permission or OAuth scope |
| 6 | Still rate limited after the retries |
| 7 | Timed out |
| 8 | Conflict with the current state (the API answered 409) |
| 130 | Interrupted (Ctrl-C or SIGTERM) |

### Changes for scripts in this release

- `backup datasource list --query <text>` is now `--search <text>`: `--query` is the JMESPath filter on every command.
- Commands that printed plain text (`whoami`, `org …`, `config path`, `configure get`, `auth token`, actions like `namespace create`) follow `-o` now; use `-o text` for the bare value.
- `-o yaml` uses the same field names as `-o json` (it used lowercased Go names such as `isactive`).
- Failures that exited 1 now exit 3, 6, 7 or 8 when the reason is known (see Exit codes).

## Commands

| Command | Description |
|---|---|
| `norcube login` | Sign in via your browser |
| `norcube logout [--all]` | End the profile's CLI session on Norcube and forget it locally (or every profile's) |
| `norcube whoami` | Show signed-in user, active org and profile |
| `norcube configure` | Create or change a profile interactively |
| `norcube configure set\|get\|unset <key> [value]` | Change one setting (`environment`, `organization`, `credential_store`, `output`, `endpoints.<service>`, `default_profile`, `defaults.*`) |
| `norcube config list` | Effective settings and where each comes from |
| `norcube config profiles` | List profiles |
| `norcube config edit` / `config path` | Open / locate the config file |
| `norcube auth status [--all-profiles] [--offline]` | Sign-in state per profile, checked with Norcube Auth (exit 4 when signed out or the session ended) |
| `norcube auth token [--audience <service>]` | Print a short-lived access token for scripts |
| `norcube auth sessions list` | Your account's CLI sessions (every `norcube login`); `*` marks this one |
| `norcube auth sessions revoke <id> \| --others` | End one session, or every session but this one |
| `norcube org list` | List organizations you can access |
| `norcube org switch` | Interactive picker (arrow keys / `j`,`k` to navigate, enter to select) |
| `norcube org use <slug-or-id>` | Set the active organization without prompting |
| `norcube org current` | Print the active organization |
| `norcube backup list` | List backup jobs across the org, newest first (incl. restore-test verdicts) |
| `norcube backup list --datasource <id>` | Filter the list to one (or more) data sources |
| `norcube backup download <job-id> -d <id>` | Download a backup artifact (or `--file -` to pipe into a restore) |
| `norcube backup health` | Restore-test health per datasource |
| `norcube backup restore-test run <job-id> -d <id>` | Restore-test one backup into a throwaway database, now |
| `norcube backup datasource list [--search <text>]` | List data sources in the active org |
| `norcube backup datasource get <id>` | Show one data source |
| `norcube backup datasource pause [id]` | Halt every policy attached to a data source (master switch). Picker when interactive. |
| `norcube backup datasource resume [id]` | Re-enable a previously paused data source. |
| `norcube backup policy list --datasource <id>` | List policy attachments on a data source |
| `norcube backup policy pause --datasource <id> --policy <id>` | Pause one policy on one data source |
| `norcube backup policy resume --datasource <id> --policy <id>` | Re-enable a paused attachment |
| `norcube backup policy detach --datasource <id> --policy <id> [--yes]` | Remove an attachment (destructive; confirms unless `--yes`) |
| `norcube langsync namespace list` | List Langsync namespaces in the active organization |
| `norcube langsync namespace create <name> --default-language <code>` | Create a namespace |
| `norcube langsync namespace update <name> [--rename …] [--default-language …]` | Edit a namespace |
| `norcube langsync mark add ["mark"] -n <ns> [--default-value …] [--auto-translate]` | Add a source string to a namespace |
| `norcube langsync mark list -n <ns> [--search …]` | List source strings (cursor-paginated) |
| `norcube langsync lang list [-n <ns>]` | List languages — org-wide by default, namespace-scoped with `-n` |
| `norcube langsync lang add <code-or-id> -n <ns>` | Attach a language to a namespace |
| `norcube langsync lang create <code> <name>` | Create a custom (org-scoped) language |
| `norcube langsync init` | Set up `.langsync.json` in a project (see below) |
| `norcube langsync sync [--dry-run] [-n <ns>] [--strategy local\|server]` | Sync local translation files with Langsync |

> `snapdb` still works as an alias of `backup` (it's the internal service name behind the product).

### Langsync in a project (`init` + `sync`)

For repos that ship i18n JSON files alongside source (`i18n/<namespace>/<lang>.json`), `langsync init` creates a `.langsync.json` describing which directories sync against which Langsync namespaces. `langsync sync` then keeps server and disk in step.

![norcube langsync sync demo](demo/demo-langsync.gif)

```bash
# one-time setup in a project root
norcube langsync init
# (pick namespaces, confirm dirs — wizard fetches each namespace's
#  default language from the server and bakes the code into the file,
#  then PULLS current server state to disk by default)

# use my local JSON files as the seed instead of pulling from server
norcube langsync init --seed push-all

# push only the default-language file and let AI translate the rest
norcube langsync init --seed push-default

# write config only, no pull or push
norcube langsync init --seed none

# preview a sync without touching the server or disk
norcube langsync sync --dry-run

# real sync (server-driven): the CLI submits one job per namespace,
# the backend computes the diff, pushes creates/updates/deletes,
# triggers autotranslate, and returns the per-language result. The
# CLI polls and renders progress as the backend works. Resumable
# across backend restarts (jobs live in Postgres, autotranslate
# never re-fires).
norcube langsync sync

# pull-only refresh (no push, no autotranslate request)
norcube langsync sync --strategy server

# resolve every default-lang conflict by hand
# (keep local / keep server / apply choice to all remaining)
norcube langsync sync --strategy interactive

# also delete server marks that are no longer in the local file
norcube langsync sync --prune

# fire the autotranslate but don't wait — pull translations in a later sync
norcube langsync sync --wait=false
```

The default conflict policy is **local-wins** — when the same key has different values locally and on the server, the local one is pushed. `--strategy server` flips that into a pull-only refresh. `--strategy interactive` walks every conflict via a TUI prompt with "apply to all" shortcuts.

By default, server marks missing from the local default-language file are left alone (the safe default — sync is conservative about destructive actions). Pass `--prune` to delete them.

By default sync **waits** for autotranslate to finish before writing the per-language files, so one `sync` run gives you complete on-disk state. The wait is capped by `--wait-timeout` (default 5m); on timeout, sync writes whatever the server has so far and notes the remaining gap in the summary. Pass `--wait=false` to skip the wait entirely (useful for big initial syncs where you'd rather come back later for the translations).

**Your local source-of-truth language can differ from the namespace's default.** Set `default_local_language` in `.langsync.json` to whatever language you write in (e.g. `cs` for a Czech-speaking dev) — the namespace can stay configured with its team-wide default (e.g. `en-US`). When the two differ, sync uploads from your local file and the LLM translates **from your language** into every attached language including the namespace default. Each dev on a team can run with their own source lang in their own `.langsync.json` without affecting anyone else. Full design notes in the backend repo at [`apps/langsync/docs/sync-source-language.md`](../norcube-platform-backend-mono/apps/langsync/docs/sync-source-language.md).

### Pause vs detach

- **`datasource pause`** — flips the data source's `isActive` flag to `false`. Halts *every* attached policy. One stroke, reversible.
- **`policy pause`** — flips one attachment's `enabled` flag to `false`. Halts *one* policy on *one* data source. Other policies on the same data source keep running.
- **`policy detach`** — removes the attachment row entirely. Use when the attachment was a mistake or is permanently obsolete; otherwise prefer `pause`.

The backend scheduler enforces both gates at the SQL level (`is_active = TRUE AND enabled = TRUE`), so the action is instant — the next minute's tick will already skip the affected rows.

## Development

```bash
make build              # builds bin/norcube
make test               # runs unit tests
make vet                # go vet
make tidy               # go mod tidy
make codegen            # regenerate every service client (see below)
make codegen-snapdb     # regenerate just the snapdb client
ARGS="login" make run   # runs the CLI from source
```

### Adding or regenerating a service client

The Norcube backend services emit Swagger 2.0 via `swag`. The codegen pipeline
is two steps:

1. `tools/swagger2openapi/` — converts Swagger 2.0 → OpenAPI 3.0 and patches
   two known issues (Fiber-style `:param` paths → `{param}`, and stripping
   inconsistent operation-level security blocks).
2. `oapi-codegen` consumes the cleaned OpenAPI 3 spec and emits a typed Go
   client into `internal/api/<service>/<service>.gen.go`.

By default the Makefile expects the backend monorepo at
`../norcube-platform-backend-mono`. Override with `make codegen MONO=...`.

To add a new service (e.g. langsync):

1. Create `internal/api/langsync/oapi-codegen.yaml` (copy from snapdb's).
2. Add a `codegen-langsync` target to the Makefile that runs the converter
   against `apps/langsync/docs/<spec>.json` and feeds it to oapi-codegen.
3. Build a `internal/cli/langsync/` package mirroring `internal/cli/backup/`:
   `cmd.go` builds the typed client + a context struct, individual files per
   resource (`namespace.go`, `term.go`, ...).
4. Wire `langsync.NewCmd()` into `internal/cli/root.go`.

The codebase is a small Go module with three internal packages worth knowing:

- `internal/cli/` — cobra command tree.
- `internal/config/` — the config file (profiles, schema v2, migration from the flat v1 file); `internal/resolve/` — which profile, endpoints, org, credentials and output a command uses, with sources.
- `internal/auth/` — browser handshake (`browser.go`), per-profile credentials (`keyring.go`, on top of `internal/credstore/`: OS keyring or files), and the per-(audience, org) token cache with the refresh lock (`tokens.go`).
- `internal/api/` — typed clients for the Norcube HTTP services (`auth`, `snapdb`, `langsync`), generated by oapi-codegen. Client packages are named after the backend service they talk to (the Backup product's service is `snapdb`), while `internal/cli/` packages are named after the user-facing command.

## Roadmap

- v0 (this) — login, whoami, org switching, Norcube Backup (datasources, job history, downloads, restore tests, health, policies), full Langsync, Homebrew tap.
- v0.1 — `domainradar` commands.
- v0.2 — Personal Access Tokens for CI (paired with a backend `cli_sessions` table for revocation).
- v0.3 — shell completion of dynamic resources (org slugs, datasource ids).

## Contributing and security

Contributions are welcome — see [CONTRIBUTING.md](CONTRIBUTING.md) for the
dev setup, conventions, and the DCO sign-off we require instead of a CLA.
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
