# norcube CLI

Command-line interface for the [Norcube platform](https://norcube.com). Manage backups, namespaces, organizations, and more from the terminal.

> **Status**: v0 — login + organization management, SnapDB browse/pause/resume, full Langsync CRUD, and project-level `langsync init` / `langsync sync` for repo-side translation files.

## Install

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

**Homebrew:** planned — see roadmap.

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
nrc snapdb backup list --all
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

## How login works

`norcube login` uses an OAuth-style loopback flow (the same pattern as `gh auth login`, `flyctl auth login`, `stripe login`):

1. The CLI starts a one-shot HTTP server on a random `127.0.0.1` port and opens your browser to `<web-app>/cli-login?port=<P>&state=<nonce>`.
2. After you authenticate (or if you're already signed in), the web page mints a fresh, CLI-specific session via `POST /auth/cli/exchange` and POSTs the tokens to your loopback server.
3. The CLI verifies the state nonce, stores the refresh token in your OS keyring (Keychain / Secret Service / Windows Credential Manager) and exits.

Your password never touches the CLI. The CLI session is independent of your browser session — logging out of the web app does not log out the CLI (and vice versa).

## Configuration

State lives in two places:

- **Secrets** (refresh + cached access tokens) — your OS keyring under the `norcube` service.
- **Preferences** (active org, API URLs, user info) — `~/.config/norcube/config.toml` (`%APPDATA%\norcube\config.toml` on Windows).

| Env var | Flag | Effect |
|---|---|---|
| `NORCUBE_AUTH_URL` | `--auth-url` | Override the auth service base URL |
| `NORCUBE_SNAPDB_URL` / `NORCUBE_LANGSYNC_URL` / `NORCUBE_DOMAINRADAR_URL` / `NORCUBE_BILLING_URL` / `NORCUBE_PROMPTHUB_URL` | — | Override individual service URLs |
| `NORCUBE_WEB_APP` | `--web-app` | Override the web app URL used during browser login |
| – | `--org` | Run a single command against a specific organization |
| – | `--output {table,json,yaml}` | Output format |

## Commands

| Command | Description |
|---|---|
| `norcube login` | Sign in via your browser |
| `norcube logout` | Forget the locally stored session |
| `norcube whoami` | Show signed-in user + active org |
| `norcube org list` | List organizations you can access |
| `norcube org switch` | Interactive picker (arrow keys / `j`,`k` to navigate, enter to select) |
| `norcube org use <slug-or-id>` | Set the active organization without prompting |
| `norcube org current` | Print the active organization |
| `norcube snapdb datasource list` | List SnapDB data sources in the active org |
| `norcube snapdb datasource get <id>` | Show one data source |
| `norcube snapdb datasource pause [id]` | Halt every policy attached to a data source (master switch). Picker when interactive. |
| `norcube snapdb datasource resume [id]` | Re-enable a previously paused data source. |
| `norcube snapdb policy list --datasource <id>` | List policy attachments on a data source |
| `norcube snapdb policy pause --datasource <id> --policy <id>` | Pause one policy on one data source |
| `norcube snapdb policy resume --datasource <id> --policy <id>` | Re-enable a paused attachment |
| `norcube snapdb policy detach --datasource <id> --policy <id> [--yes]` | Remove an attachment (destructive; confirms unless `--yes`) |
| `norcube snapdb backup list` | List backup jobs across the org, newest first |
| `norcube snapdb backup list --datasource <id>` | Filter the list to one (or more) data sources |
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

> Backup detail / download and restore commands will land once the SnapDB backend ships those endpoints (currently stubbed at 501).

### Langsync in a project (`init` + `sync`)

For repos that ship i18n JSON files alongside source (`i18n/<namespace>/<lang>.json`), `langsync init` creates a `.langsync.json` describing which directories sync against which Langsync namespaces. `langsync sync` then keeps server and disk in step.

```bash
# one-time setup in a project root
norcube langsync init
# (pick namespaces, confirm dirs — wizard fetches each namespace's
#  default language from the server and bakes the code into the file,
#  then PULLS current server state to disk by default)

# use my local JSON files as the seed instead of pulling from server
norcube langsync init --seed push

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

By default sync **waits** for autotranslate to finish before writing the per-language files, so one `sync` run gives you complete on-disk state. The wait is capped by `--wait-timeout` (default 2m); on timeout, sync writes whatever the server has so far and notes the remaining gap in the summary. Pass `--wait=false` to skip the wait entirely (useful for big initial syncs where you'd rather come back later for the translations).

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
3. Build a `internal/cli/langsync/` package mirroring the snapdb structure:
   `cmd.go` builds the typed client + a context struct, individual files per
   resource (`namespace.go`, `term.go`, ...).
4. Wire `langsync.NewCmd()` into `internal/cli/root.go`.

The codebase is a small Go module with three internal packages worth knowing:

- `internal/cli/` — cobra command tree.
- `internal/auth/` — browser handshake (`browser.go`), OS keyring (`keyring.go`), and the per-(audience, org) token cache (`tokens.go`).
- `internal/api/` — typed clients for the Norcube HTTP services (currently only `auth`).

## Roadmap

- v0 (this) — login, whoami, org switching, snapdb data sources + backup listing + policy management.
- v0.1 — Homebrew tap (auto-generated by GoReleaser); `langsync` + `domainradar` commands.
- v0.2 — Personal Access Tokens for CI (paired with a backend `cli_sessions` table for revocation).
- v0.3 — backup download / restore commands once the SnapDB backend ships those endpoints.
- v0.4 — shell completion of dynamic resources (org slugs, datasource ids), background "new version available" nudge.
