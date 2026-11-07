# Design principles and commitments

This document is the binding design of the Norcube CLI (`norcube`, alias
`nrc`). It says what the CLI is for, which properties it guarantees, why
it is built the way it is, and how to extend it without breaking those
guarantees. It applies to the maintainers and, without exception, to AI
coding agents working in this repository ([AGENTS.md](../AGENTS.md) is
the short form for them).

Status: in force from v0.21.4 (2026-10-30). Changes to a commitment in
section 2 are design changes: they need the owner's agreement, a
`CHANGELOG.md` entry and an update of this file in the same pull request.

## How this document relates to the others

Each document has one job; this one does not repeat the others.

| Document | Owns | Audience |
| --- | --- | --- |
| **DESIGN.md** (this file) | The why, the contracts, the commitments, the playbooks, the decision log | maintainers, AI agents |
| [ARCHITECTURE.md](ARCHITECTURE.md) | The code map: packages, the path of one command, how to add a service or a command | contributors |
| [CONTRIBUTING.md](../CONTRIBUTING.md) | Dev setup, code conventions, commit style, DCO sign-off | contributors |
| [RELEASING.md](RELEASING.md) | The release runbook, secrets and one-time setup | maintainers |
| [CHANGELOG.md](../CHANGELOG.md) | What changed for users and scripts, per release | users, script authors |
| [README.md](../README.md) | What it is, install, verify a release, quick start | GitHub visitors |
| [CLI guide](https://docs.norcube.com/platform/cli) | How to use it: login, profiles, output, exit codes, troubleshooting | users |
| [Command reference](https://docs.norcube.com/platform/cli-reference) | Every command and flag, generated from the help text | users |

When this file and the code disagree, the code is a bug or this file is
stale; either way, fix one of them in the same change.

---

## 1. Purpose and scope

### 1.1 What the CLI is

One open-source (Apache 2.0) binary that drives every Norcube product
from a terminal, a script or a CI job: Norcube Backup, LangSync,
DomainRadar, Norcube Monitor, Billing and the platform itself (sign-in,
organizations, CLI sessions, CI tokens). It is a client of the products' public HTTP
APIs and nothing else: it holds no business logic the API does not have,
and every typed client in it is generated from the backend's Swagger
specs.

It is also the open-core proof that there is no lock-in: `backup jobs
download` streams any backup into `pg_restore` or `mongorestore`, and the
API it uses is documented, so the CLI doubles as a reference client
([README, Open core](../README.md#open-core)).

### 1.2 Who it is for

| User | What they need from the CLI | Where that is guaranteed |
| --- | --- | --- |
| People at a terminal | Browser sign-in, readable tables, hints that say what to do next, completion, confirmations before anything destructive | P1, P5, P6, section 3.6 |
| Scripts and CI jobs | Data-only stdout, stable JSON shapes, stable error codes and exit codes, no prompts, scoped expiring credentials | P2 to P5, P12 |
| AI agents and tools | Everything scripts need, plus a predictable grammar, one machine-readable description of every command (`nrc commands -o json`), an escape hatch for any endpoint (`nrc api`) | P1, P2, P4, P9, sections 3.5 and 3.8 |

### 1.3 "Like the AWS CLI" made concrete

The owner's brief for the current design (2026-10-28) was: "we need to be
prepared for a cli like aws (many different services, many profiles to use
etc.)". Concretely that means:

| Dimension | What it means here | Mechanism |
| --- | --- | --- |
| Many products | A product is a manifest entry plus a commands package; endpoints, audiences, `nrc api`, codegen and completion follow from the entry | [`services.yaml`](../internal/registry/services.yaml), `service.New[T]` |
| One grammar | `norcube <service> <resource> <verb>` everywhere, same flags, same output | P1, `naming_test.go`, `contract_test.go` |
| Many identities | Named profiles, each with its own session, organization, endpoints, credential store and output default | `internal/config`, `internal/resolve`, `internal/credstore` |
| Many organizations | `--org` (slug or id) on any command without changing the profile; CI tokens bound to their own organization | `internal/orgs`, `service.New` |
| Many environments | `production` built in; anything else is a `custom` profile with its own endpoints | `config/environments.go` |
| One precedence rule | flag > environment variable > profile > defaults, with the source of every value visible in `nrc config list` | `internal/resolve` |
| Scripting | `-o table|json|yaml|text`, `--query` (JMESPath), pagination flags, JSON errors | `internal/cmdutil`, `internal/output`, `internal/clierr` |
| Robustness | Timeouts, retries with backoff and `Retry-After`, proxy and CA bundle, one transport for every request | `internal/transport` |
| Diagnostics | `nrc doctor`, `--debug` with redaction, `nrc config list` with sources | `internal/cli/doctor.go`, `transport/debug.go` |

### 1.4 Non-goals

- **No client telemetry.** The only outbound call that is not a user's
  request is the release check against GitHub (daily cache, skipped when
  stderr is not a terminal, `NORCUBE_NO_UPDATE_CHECK=1` turns it off;
  [`updatecheck.go`](../internal/cli/updatecheck.go)).
- **No routes that are not for organization members.** Superadmin,
  webhook and status operations are excluded from the generated clients
  and the checked-in specs by tag (section 3.7).
- **No public CMS surface.** Norcube CMS has no public API; its HTTP API
  serves the dashboard only, so the CLI has no `cms` group. The CI token
  catalog has `cms.*` scopes, which the CLI lists but does not use.
- **No hand-written API clients.** The one exception is the small Norcube
  Auth client in [`internal/api/auth.go`](../internal/api/auth.go).

---

## 2. Principles and commitments

Each principle states the commitment, why it exists, and what enforces
it. "Enforced" means a test, a CI job or a single code path that every
command goes through; "convention" means review is the only guard.

### P1. One command grammar

**Commitment.** Commands are `norcube <service> <resource> <verb>`.
Resources are plural nouns (`jobs`, `datasources`, `restore-tests`,
`terms`). Standard verbs are `list`, `get`, `create`, `update`, `delete`.
Domain verbs are allowed only where CRUD would mislead, and each keeps one
meaning across the tree:

| Verb | Meaning | Examples |
| --- | --- | --- |
| `add` / `remove` | Attach or detach an existing thing to a container; never deletes the thing | `langsync languages add/remove`, `domainradar saved add/remove` |
| `attach` / `detach` | Link two existing resources | `backup policies attach/detach`, `langsync glossary languages attach` |
| `run` | Start a server-side job now | `backup jobs run`, `backup restore-tests run` |
| `pause` / `resume` | Stop and restart a schedule without deleting it | `backup datasources pause`, `backup policies resume` |
| `download` / `export` | Write server data to a file or stdout | `backup jobs download`, `billing invoices download`, `domainradar bulk export` |
| `sync` / `pull` / `init` | LangSync project files | `langsync sync` |
| `acknowledge` | Mark something as being handled without changing it otherwise | `monitor incidents acknowledge` (alias `ack`) |
| `verify` | Confirm a contact with a code sent to it | `monitor channels verify` |
| `summary` | Counts over a whole resource, not a list | `monitor incidents summary` |
| `publish` / `unpublish` | Make a thing public, or take it offline keeping everything on it | `monitor status-pages publish` |
| `preview-link` | Print a short-lived link to see a thing as the public would | `monitor status-pages preview-link` |
| `finish` | End something in progress now, as opposed to `cancel`, which calls off something that hasn't started | `monitor maintenance finish` |
| `cancel`, `watch`, `results`, `status`, `check`, `generate`, `import`, `set`, `revoke`, `use` | As named | `domainradar bulk watch`, `langsync terms import status` |

`rm` is an alias of `remove` only, never of `delete`.

**Why.** A user (or an agent) who knows one product can guess the others.
The pre-0.17 tree had `backup list` meaning jobs and LangSync's "mark"
meaning a term; v0.17 (375658d) renamed everything to this grammar and
kept every old name working.

**Enforced.**
- [`naming_test.go`](../internal/cli/naming_test.go): renamed commands
  still run, help shows only the new names, `languages rm` resolves to
  `remove`, and no two siblings share a name or alias anywhere in the tree.
- [`root.go`](../internal/cli/root.go) `markUsageErrors`: a typo in a
  subcommand exits 2 with a suggestion instead of printing help with exit 0.
- Plural resources and the verb table above: convention (review).

### P2. stdout is the result, stderr is for people

**Commitment.** A command's stdout carries exactly its result, in the
selected format, and nothing else. Progress, spinners, dashboards,
prompts, hints, rename notes and `nextCursor=` go to stderr. `-o json`
always pipes cleanly into `jq`.

**Why.** Scripts and agents parse stdout. One stray line breaks every
pipeline downstream, silently.

**Enforced.**
- Commands return values through `cmdutil.Run` / `cmdutil.Print`
  ([`cmdutil.go`](../internal/cmdutil/cmdutil.go)); a nil result prints
  nothing.
- [`contract_test.go`](../internal/cli/contract_test.go)
  `TestNoDirectStdoutWrites` greps every non-test file under
  `internal/cli/` for `OutOrStdout()`, `os.Stdout` and `fmt.Print*`; the
  only allowed uses are a terminal check and `config edit` handing the
  terminal to `$EDITOR`.
- Pagination writes the next cursor to stderr
  ([`paginate.go`](../internal/cmdutil/paginate.go)); lists stay plain
  arrays.

### P3. Stable output formats and `--query`

**Commitment.** Every command supports these formats:

| Format | Contract |
| --- | --- |
| `table` | For people. Columns and wording may change in any release. Local time. Pages through `$NORCUBE_PAGER` / `$PAGER` on a terminal. |
| `json` | The API's own resource and field names. Lists are arrays. Changes are additive only. |
| `yaml` | The same data and field names as `json`. |
| `text` | Tab-separated, no header, one record per line, times in RFC 3339 UTC. Column order is part of the contract; a new column is a "change for scripts". |
| `id` | Only the result's `id`, one per line for a list, for `id=$(... -o id)`. `--query` applies first, so it can name another field. A result without an id is a usage error (exit 2) and, for a write, comes after the change was made. Not settable with `configure`: it isn't a sensible default. |

`--query` is a JMESPath expression applied to what `-o json` would print,
before formatting, so it composes with every format. An invalid
expression fails with exit 2 before any request is made.

**Why.** Scripts outlive releases. The API's names are already
documented, so `json` reuses them instead of inventing a CLI dialect.
`--query` was a deliberate AWS-CLI choice; `backup datasources list
--query <text>` was renamed to `--search` in v0.16 so `--query` means one
thing everywhere.

**Enforced.**
- [`golden_test.go`](../internal/cli/golden_test.go): about 54 commands
  and query cases, each pinned in all four formats
  (`testdata/golden/*.{json,yaml,txt,tsv}`) against a fake backend;
  `id` follows from the JSON and is pinned in `output/id_test.go`.
- `--query` is compiled in the root `PersistentPreRunE` and again in
  `cmdutil.Run`, both as usage errors.
- Known exception to fix before v1.0: some write commands return ad-hoc
  maps rather than the API resource, and `backup policies list` returns a
  different shape with `--datasource` (section 8).

### P4. Error codes and exit codes are an API

**Commitment.** Every failure has a stable machine-readable `code` and a
stable exit code. With `-o json` or `-o yaml`, or
`NORCUBE_ERROR_FORMAT=json`, stderr gets one object `{"error": {...}}`
with `code`, `message`, `hint`, `status`, `service`, `method`, `url`,
`type`, `requestId` and `details`. Messages may be reworded; codes and
exit codes may not.

| Exit | Codes | Meaning |
| --- | --- | --- |
| 0 | | Success |
| 1 | `error`, `server_error`, `job_failed`, `unhealthy` | General failure; a waited-for job ended unsuccessfully (`details` holds the job); a `--fail-on` state was found (`monitor status`, `details` holds the report) |
| 2 | `usage_error`, `validation_failed`, `org_not_found` | Fix the command line or input; also any prompt without a terminal |
| 3 | `not_found` | The API answered 404 |
| 4 | `login_required` | Not signed in, session expired or revoked, CI token expired, revoked or unknown |
| 5 | `permission_denied`, `insufficient_scope` | Signed in but not allowed: not a member, missing IAM permission or scope |
| 6 | `rate_limited` | Still 429 after the retries |
| 7 | `timeout` | A request or `--wait-timeout` ran out |
| 8 | `conflict` | The API answered 409 |
| 130 | `interrupted` | SIGINT or SIGTERM; a second Ctrl-C kills at once |

Hints name the exact fix: the login hint names `--profile` when one was
chosen explicitly; a refusal under a CI token explains which actions
always need a person and points to `norcube auth ci-token scopes`
(054fc5f, v0.21.3).

**Why.** "Session expired" when the real problem was "not a member of
that organization" was the first P0 bug of the 2026-10-28 review: it sent
users into pointless re-logins. Scripts must be able to tell "sign in",
"no access", "not found" and "try later" apart without parsing English.

**Enforced.**
- [`clierr`](../internal/clierr/error.go): the code constants,
  `CodeForStatus` (HTTP status and service error type to code) and
  `ExitFor` (code to exit) are the single mapping, covered by
  `clierr_test.go`.
- [`internal/cli/errors.go`](../internal/cli/errors.go) `Describe`
  classifies every non-API error (context cancelled, login required, org
  access, org not found, usage, timeouts) in one place; `main` is the only
  caller of `os.Exit` ([`cmd/norcube/main.go`](../cmd/norcube/main.go)).
- Golden files `error-not-found.*` and `error-org-not-found.*` pin the
  JSON shape; `errors_test.go` pins the classification.
- `cmdutil.JobFailed` is the only way to report a failed `--wait` job.

### P5. No surprises in scripts

**Commitment.**
- Nothing prompts without a terminal on stdin. A command that would ask
  fails with exit 2 and names the flag that answers it (`--yes`, or the
  flag behind an interactive picker).
- No colour or styling when the output is not a terminal, or with
  `NO_COLOR`, `CLICOLOR=0` or `--no-color`. Spinners, progress bars and the
  update notice appear only on a terminal stderr.
- Output is deterministic for the same API answers: the golden tests
  depend on it.
- Behaviour never depends on the binary's name (`norcube` and `nrc` are
  the same program).
- Rename notes are off by default before v1.0
  (`NORCUBE_DEPRECATION_WARNINGS=1` turns them on), so renames add no
  stderr noise to existing jobs.

**Why.** A CI job that hangs on a hidden prompt, or deletes something
because a prompt defaulted to yes, is the worst failure a CLI can have.

**Enforced.** `cmdutil.Confirm` checks `term.StdinIsTerminal()` and returns
a usage error that asks for `--yes`
([`helpers.go`](../internal/cmdutil/helpers.go)); colour decisions go
through [`internal/term`](../internal/term/term.go) only. The rest is
convention plus the golden tests.

### P6. Safe by default

**Commitment.**
- Destructive or costly changes ask first through `cmdutil.Confirm` and
  take `--yes`: deletes, restores (always), spending caps (always), bulk
  operations. Declining is not an error (`cmdutil.Cancelled`).
- Validation happens before the first request, so a bad flag combination
  never leaves half a change behind.
- Files the CLI creates are never overwritten (`O_EXCL`), are created
  `0600`, get a SHA-256 on the way, and a failed or interrupted write
  removes the partial file (`cmdutil.SaveStream`). `-` streams to stdout.
- The config file is written atomically (temp file, fsync, rename, fsync
  of the directory), `0600` in a `0700` directory, under a lock; a schema
  migration first writes a timestamped backup
  ([`config/store.go`](../internal/config/store.go)).
- Secrets never travel in arguments (visible in `ps` and shell history):
  there is no `--token` flag; tokens come from `NORCUBE_TOKEN` or
  `--token-file`, connection strings from `--connection-uri-file` (a file
  or stdin). Secrets the server shows once (CI tokens, LangSync API keys)
  are printed once and never again; hashes are never printed.
- Credentials live in the OS keyring, one item per profile; where no
  keyring exists, in `0600` files in a `0700` directory, with a one-time
  notice ([`credstore`](../internal/credstore/credstore.go)).
- `--debug` traces redact `Authorization` (keeping the scheme), cookies,
  API-key headers, and token, secret, password, key, credential and
  signature fields in URLs and bodies, including presigned download URLs
  ([`transport/debug.go`](../internal/transport/debug.go)).
- `nrc api` refuses absolute URLs, so the profile's token is only ever
  sent to the configured service endpoint.

**Why.** The CLI holds credentials that can restore over a production
database. Defaults must make the safe path the easy one.

**Enforced.** The helpers above are the only implementations (review
rejects local copies); `transport/debug_test.go` pins redaction;
`credstore_test.go` and `config_test.go` pin permissions on POSIX.

### P7. Network robustness

**Commitment.** Every request goes through one transport
([`internal/transport`](../internal/transport/transport.go)):

| Concern | Behaviour | Override |
| --- | --- | --- |
| Connect timeout | 10 s | `--cli-connect-timeout`, `NORCUBE_CONNECT_TIMEOUT` |
| Read timeout | 60 s per attempt, including reading the body | `--cli-read-timeout`, `NORCUBE_READ_TIMEOUT` |
| Streams and downloads | No total limit; fail after 60 s without a byte | `Client.Streaming` |
| Retries | 3 attempts; exponential backoff with full jitter (200 ms base, 20 s cap); on 429, 502, 503, 504 and network errors; only for idempotent methods or a request carrying `Idempotency-Key`; never other 4xx | `--max-attempts`, `NORCUBE_MAX_ATTEMPTS` |
| `Retry-After` | Honoured (seconds or HTTP date) up to 60 s; a longer one returns the response instead of hanging | |
| Proxy, CA | `HTTPS_PROXY` / `NO_PROXY`; extra CA bundle for TLS-inspecting proxies | `NORCUBE_CA_BUNDLE` |
| User-Agent | `norcube/<version> (<os>; <arch>) go/<version>` | |
| Signals | SIGINT/SIGTERM cancel the command's context; in-flight requests stop, temp files are removed | |

**Why.** Before v0.16 a hung server hung the CLI and a 429 failed
immediately. The AWS SDK "standard" retry mode was the model: jitter
spreads a fleet of CI jobs instead of retrying in lockstep, and retrying
only safe requests means a retry can never apply a change twice.

**Enforced.** `transport_test.go` (timeouts, retry counts, `Retry-After`,
no retry of a POST without a key, cancellation). Generated clients get
the shared `*http.Client` from `service.New`; nothing else may construct
an HTTP client for API calls.

### P8. Clients are generated from the backend contract

**Commitment.** Typed clients under `internal/api/<package>/` are
generated, never edited: `make codegen MONO=<backend checkout>` converts
each Swagger 2.0 spec named in `services.yaml` to OpenAPI 3
(`specs/<package>.openapi.json`, committed) and runs oapi-codegen, pinned
at v2.7.0 in [`tools/codegen`](../tools/codegen/main.go). Spec and client
change in the same commit.

**Why.** The backend is the source of truth; a hand-maintained client
drifts (the LangSync client of May 2026 lacked prompts and the AI label
by October). Pinning the generator means every machine produces the same
bytes, so drift is detectable with `git diff`.

**Enforced.** `make codegen-check` regenerates and fails on any diff; the
[`codegen-drift`](../.github/workflows/codegen-drift.yaml) workflow runs
it on pull requests touching the registry, specs or codegen tools, and
weekly opens a pull request with regenerated clients. The workflow needs
a spec source configured in the repository settings and skips without one
(section 8). Lint excludes `*.gen.go`.

### P9. Help text is the documentation source

**Commitment.** Every visible command has a sentence-style `Short`
(capitalized, no trailing period), a `Long` when there is more to say, at
least one `Example`, and a description for every flag. The command
reference on docs.norcube.com, the man pages and the shell completions
are generated from it ([`tools/gendocs`](../tools/gendocs/main.go)); no
flag list is maintained by hand anywhere. Hand-written guides explain
concepts and link into the reference.

**Why.** The owner asked on 2026-10-28 how to document the CLI properly;
the answer adopted was "each place has one job and nothing is written
twice by hand", the model of gh, AWS and Stripe. Before that, the README
and three docs pages had drifted from the code.

The same tree is also the machine-readable description of the CLI:
`nrc commands -o json` (section 3.8) is the discovery entry point for
scripts and AI agents, so they read one document instead of crawling
`--help` pages. Its `schemaVersion` is a commitment: within version 1
changes are additive only (new fields, new values of an enumerated field
such as `effect`, new commands); removing or renaming a field or changing
its type or meaning needs a new `schemaVersion` and a "Changes for
scripts" entry. What the help text cannot say (whether a command changes
anything, the values a flag accepts, the service of a platform command)
is declared where the command is defined, with the annotations of P13.

**Enforced.** `TestEveryCommandHasAnExample`, `TestHelpTextIsComplete`
and `TestEveryCommandDeclaresAnEffect` in
[`contract_test.go`](../internal/cli/contract_test.go); the schema golden
`commands-billing.*` (and `commands-monitor.json` for a whole product
group) and the tree tests in
[`commands_test.go`](../internal/cli/commands_test.go) (every visible
command listed, hidden ones only with `--all`, sorted output, offline);
the [`docs-reference`](../.github/workflows/docs-reference.yaml) workflow
regenerates the reference on every release tag and opens a pull request
on the docs repository.

### P10. Backwards compatibility and deprecation

**Commitment (pre-1.0, now).**
- Every command path, flag and environment variable that has shipped
  keeps working. Renames keep the old name as a hidden command
  (`cmdutil.Renamed`) or an alias; the old name is left out of help and
  the reference.
- Behaviour a script can observe (exit codes, JSON shapes, `-o text`
  columns, flag semantics) changes only with a "Changes for scripts"
  entry in `CHANGELOG.md`. Moving a failure from exit 1 to a more specific
  code is allowed and listed there.
- Config files migrate forward automatically, once, with a backup
  (schema v1 to v2 in v0.16); they are never migrated back.

**At 1.0** (planned once the foundation phases are complete; the 2026-10-28
plan sets this bar):
- Rename notes become always on.
- An old name is removed no earlier than two minor releases after its
  note became always on, and every removal is listed in `CHANGELOG.md`.
- JSON shapes stay additive; a breaking shape needs a new field or an
  explicit opt-in.

The rename note today reads "the old name keeps working until v1.0"
([`deprecated.go`](../internal/cmdutil/deprecated.go)); reword it when the
1.0 policy is applied so it does not promise removal at 1.0.

**Why.** The v0.17 rename touched almost every Backup and LangSync
command; it shipped without breaking a single script because of this
rule.

**Enforced.** `naming_test.go` (old names keep working, quietly by
default); CHANGELOG review.

### P11. Supply chain

**Commitment.** Every release is built by the
[release workflow](../.github/workflows/release.yaml) from a tag, never
from a laptop, and ships:
- `checksums.txt` with the SHA-256 of every archive, package and SBOM,
  signed keylessly with Sigstore (`checksums.txt.sigstore.json`, bound to
  the workflow's GitHub OIDC identity);
- an SPDX SBOM per archive (syft);
- a GitHub artifact attestation (SLSA build provenance) for every file in
  `checksums.txt`;
- macOS binaries signed with a Developer ID certificate and submitted for
  notarization;
- `.deb`, `.rpm`, `.apk` packages, a Homebrew cask, and Scoop and winget
  manifests (each publisher only when its token is set).

`norcube upgrade` and `install.sh` install only the archive for their
platform and only after its SHA-256 matches `checksums.txt`; a release
without that entry is refused. Package-managed installs are detected and
deferred to the package manager (`--force` overrides).

The build itself is pinned: actions by commit SHA, GoReleaser v2.18.3,
syft v1.54.1, golangci-lint v2.12.2, oapi-codegen v2.7.0, govulncheck
v1.9.0, and the Go toolchain via the `toolchain` line in `go.mod`. CI
fails when `go.mod` is not tidy (GoReleaser tidies before building, so an
untidy module would make the release differ from the tagged commit) and
when govulncheck finds a reachable advisory that is not accepted, with a
reason, in [`scripts/govulncheck.sh`](../scripts/govulncheck.sh).
Workflows run with least-privilege permissions (`permissions: {}` at the
top of the release workflow).

**Why.** A CLI that people pipe into `sh` and that holds production
credentials is a supply-chain target. Users can verify a download
themselves ([README, Verify a release](../README.md#verify-a-release)).

**Enforced.** [`.goreleaser.yaml`](../.goreleaser.yaml), the workflows,
`upgrade_test.go` (archive pattern, managed-install detection).

### P12. Authentication security model

**Commitment.**
- **People sign in in the browser.** `nrc login` is a first-party OAuth
  2.0 client: the browser approves the sign-in and hands a one-time code
  to a loopback listener on `127.0.0.1`, and the CLI exchanges it with
  PKCE. The callback checks `state` and `iss`; a request with another
  `state` is refused and the login keeps waiting. Metadata endpoints must
  share the issuer's origin. `--no-browser` prints the URL; `--device`
  uses the device authorization flow (a short code approved on any
  device). `--scopes` limits a login. Nothing is pasted.
- **Sessions are visible and revocable.** Each login is a named CLI
  session (`nrc auth sessions list | revoke`, and under Connected apps in
  the dashboard); `logout` revokes it on Norcube Auth, `logout --all` for
  every profile.
- **One refresh token per profile, short-lived access tokens per
  (audience, organization).** Access tokens are minted per service and
  cached with an expiry computed from `expires_in` at receipt (immune to
  local clock skew), refreshed 30 s before they expire.
- **Refresh is serialized.** Norcube Auth rotates the refresh token on
  every exchange and rejects the old one, so the CLI refreshes under an
  in-process semaphore plus a per-profile file lock (30 s timeout), reads
  the refresh token only under the lock, re-checks the cache after
  acquiring it, and persists a rotated token before using it.
- **CI tokens are organization principals, not people.** `nrc_ci_` plus
  32 base62 characters plus a CRC32 checksum (the GitHub scheme, so
  scanners recognize leaks and typos fail locally). They always expire,
  carry scopes, are exchanged for 5-minute per-service tokens kept in
  memory only, and work only in their own organization: naming another
  one with `--org`, `NORCUBE_ORG` or a project file is a usage error.
  Commands that act as a person (`whoami`, `org …`, `auth sessions …`) say
  they need `norcube login`.
- **Least privilege for automation.** CI scopes cover what pipelines
  need; restores, data source and policy changes and downloads of
  production data always need a person (decision of 2026-10-29,
  section 5).
- **Secrets are never printed** except when the user asks for one
  (`auth token` warns when stdout is a terminal; `ci-token create` shows
  the secret once, `-o text` prints only the secret for piping into a
  secret store).

**Why.** Parallel commands used to log users out (two refreshes racing on
one rotating token; `langsync sync` even raced with itself across
namespaces). A refresh token that can mint a token for every service must
never be exposed to a log, an argument or a CI runner; CI gets a narrower,
expiring, revocable credential instead.

**Enforced.** `refresh_race_test.go` (multi-process refresh against a
server that revokes on reuse), `keyring_concurrency_test.go`,
`citoken_test.go`, `device_test.go`, `browser_test.go`,
`session_check_test.go`, `logout_test.go`.

### P13. One way to do each thing

**Commitment.** Cross-cutting behaviour has exactly one implementation,
and product packages use it instead of their own:

| Concern | The one implementation |
| --- | --- |
| Building a client | `service.New[T]` |
| Printing a result | `cmdutil.Run` / `cmdutil.Print` |
| Lists | `cmdutil.AddPageFlags` + `cmdutil.Paginate` |
| Waiting for a job | `cmdutil.AddWaitFlags` + `cmdutil.Poll` + `cmdutil.JobFailed` |
| Confirming | `cmdutil.Confirm` + `--yes` + `cmdutil.Cancelled` |
| Reading items or text from args, a file or stdin | `cmdutil.ReadItems`, `cmdutil.ReadText` |
| Writing a file | `cmdutil.SaveStream` |
| Time arguments (`--since`) | `cmdutil.ParseTimeArg` |
| Times ahead and lengths (`--start`, `--duration`) | `cmdutil.ParseFutureTimeArg`, `cmdutil.ParseDurationArg` |
| Server-sent event streams with reconnect | `internal/sse` |
| Usage errors | `clierr.Usage` |
| What a command changes, a flag's allowed values, a platform command's service | `cmdutil.Reads` / `Writes` / `Local`, `cmdutil.FlagEnum`, `cmdutil.AnnotationService` (read by `nrc commands`) |
| Terminal and colour detection | `internal/term` |
| HTTP | `internal/transport` |

Product packages take their collaborators through a small `deps` struct
(Backup, DomainRadar, Billing) so tests can inject the client, the clock
and `sleep`.

**Why.** The owner's brief for the coverage phase (2026-10-29) asked for
code that is "robust, future proof, well documented, end to end, ...
correct DI, secure, unified, reused anything that should be reused".
Duplicated helpers drift: the 2026-10-30 review found LangSync's own poll
loop exiting 0 on a timeout where every shared `--wait` exits 7.

**Enforced.** Convention and review. LangSync predates several helpers and
is the known outlier (section 8); new code in it uses the shared helpers.

---

## 3. How the unification works end to end

### 3.1 The path of one command

```
cmd/norcube/main.go     signal context; the only os.Exit; error rendering
  internal/cli/root.go  global flags; PersistentPreRunE: --query, preferences,
                        profile, CI-token detection, clictx.Attach
    <product>/<cmd>.go  RunE = cmdutil.Run(func) (any, error)
      service.New[T]    registry entry -> config + profile -> organization
                        -> endpoint -> TokenSource(audience, org) -> transport
                        -> generated client
      API call          internal/transport: timeouts, retries, --debug trace
      return value  ->  cmdutil.Print: --query -> table | json | yaml | text -> stdout
      return error  ->  clierr / Describe -> code + exit code -> stderr
```

[ARCHITECTURE.md](ARCHITECTURE.md) describes each package; this section
explains how they make the CLI one product instead of five.

### 3.2 The registry is the single source of truth

[`internal/registry/services.yaml`](../internal/registry/services.yaml) is
embedded in the binary and validated when it is loaded and by
`registry_test.go` (unique names, `https` endpoints, a spec for every
generated package). Each entry gives:

| Field | Drives |
| --- | --- |
| `key` | Config `[profiles.<name>.endpoints]` key, `--endpoint-url key=url`, `NORCUBE_<KEY>_URL`. Never changed once shipped: it is in users' config files. |
| `name` | The product name in messages and `doctor` |
| `command`, `aliases` | The command group and the names `nrc api` accepts |
| `audience` | The OAuth audience `service.New` mints tokens for; services without one are not callable |
| `endpoint` | The production base URL, which is the built-in `production` environment |
| `spec`, `package` | `make codegen` input and output |

Today the registry holds Auth, the web app, Backup (`snapdb`), LangSync,
DomainRadar, Billing, Norcube Monitor (`monitor`) and PromptHub (callable
with `nrc api`, no command group yet).

Not yet derived from the registry, and therefore also edited by hand when
a service is added: the per-service fields and `switch` statements of
`config.Config`, the v1 migration struct, the audience constants in
`internal/auth/tokens.go`, and the audience lists in `internal/cli/authcmd.go`
(section 6.1 lists them; section 8 tracks removing them).

### 3.3 `service.New[T]`: one constructor for every product

[`service.New`](../internal/service/service.go) is the only way a product
command builds a client. It:

1. looks the service up in the registry and refuses one without an
   audience;
2. loads the resolved config of the selected profile;
3. picks the organization: an explicit `ForOrg`, then `--org` /
   `NORCUBE_ORG`, then a command default such as `.langsync.json`
   (`WithOrgDefault`), then the profile's organization; slugs are resolved
   to ids through `internal/orgs` (cached 24 h); under a CI token the
   organization is the token's, and any other source naming a different
   one is a usage error;
4. builds a `TokenSource` for the service's audience and the
   organization, from the selected credential store;
5. hands the shared transport and a bearer injector to the generated
   client's constructor.

A product's constructor is therefore a few lines (see ARCHITECTURE,
"Adding a service"), and timeouts, retries, `--debug`, profiles, CI tokens
and organization rules apply to it without any code in the product
package.

### 3.4 Shared flags and settings

Global flags are declared once on the root
([`root.go`](../internal/cli/root.go)) and inherited by every command:
`--profile`, `--org`, `-o`, `--query`, `--endpoint-url`, `--token-file`,
`--auth-url`, `--web-app`, `--cli-read-timeout`, `--cli-connect-timeout`,
`--max-attempts`, `--debug`, `--no-color`, `--no-pager`. Each has an
environment variable, and settings that make sense per profile can live in
the config file.

Precedence is one rule, implemented and table-tested in
[`internal/resolve`](../internal/resolve/resolve.go): flag, then
environment variable, then the profile, then `[defaults]`, then the
built-in default. Every resolved value carries its source, which
`nrc config list` prints. The full table is in the
[CLI guide](https://docs.norcube.com/platform/cli#precedence).

Profiles are opt-in. A person who never types `--profile` uses `default`,
which is production; profiles exist for a second account, a client's
organization, CI, or custom endpoints. Norcube's internal staging is not a
built-in environment; it is configured as a `custom` profile (decision of
2026-10-28, section 5).

### 3.5 `nrc api`: the escape hatch

`nrc api <service> [METHOD] <path>` ([`api.go`](../internal/cli/api.go))
sends any request with the invocation's profile, organization,
credentials, timeouts, retries and `--debug`, and prints the JSON through
the normal result path, so `-o` and `--query` work. Fields (`-f`, `-F`),
headers (`-H`), a body (`--input`, `-` for stdin) and `--paginate` follow
`gh api`. Paths must be relative.

It exists so that a missing command is never a blocker, for people or
agents: an endpoint becomes usable the day it ships in the backend, and a
dedicated command follows when it is worth it. Every registry service with
an audience and a command name is callable, including ones without a
command group (PromptHub today).

### 3.6 Completion and diagnostics

- **Completion** ([`internal/completion`](../internal/completion/completion.go)):
  commands and flags from cobra; dynamic values (organization slugs,
  profiles, data source and job ids, namespaces, language codes, and more
  per product) from the API with a one-second budget, cached a minute per
  profile and organization, never prompting, offering nothing on any
  failure. Completion must never slow typing down.
- **`nrc doctor`** ([`doctor.go`](../internal/cli/doctor.go)): version and
  available upgrade, config file readability and permissions, which
  profile and organization apply and why, the credential store, proxy and
  CA settings, DNS, TLS and latency per endpoint, clock skew against
  Norcube Auth, the session, and whether a token can be minted for every
  service. `-o json` output contains no secrets and is meant for support
  tickets; `--offline` skips the network.
- **`--debug`** prints each attempt (method, URL, headers, small bodies,
  status, timing, request id) with the redaction of P6.

### 3.7 Code generation in detail

`make codegen` ([`tools/codegen`](../tools/codegen/main.go)) loops over
the registry entries that have a `package`:

1. reads `apps/<app>/docs/<file>` from the backend checkout (`MONO`,
   default `../backend-mono`) or from any directory with the same layout,
   such as a published spec archive;
2. converts Swagger 2.0 to OpenAPI 3 with
   [`tools/swagger2openapi`](../tools/swagger2openapi/main.go), dropping
   operations whose tags are listed under `output-options.exclude-tags` in
   the package's `oapi-codegen.yaml`, and the schemas only they used, so
   admin and webhook routes never appear in this public repository;
3. writes `specs/<package>.openapi.json` and runs the pinned oapi-codegen.

The backend must give every operation an explicit swagger `@Id`, so
generated method names are clean and stable; a renamed `@Id` is a breaking
change for this repository.

### 3.8 `nrc commands`: the discovery entry point

`nrc commands` ([`commands.go`](../internal/cli/commands.go)) walks the
cobra tree and prints it through the normal result path, so `-o` and
`--query` work as everywhere. It needs no sign-in, makes no request and
skips the release check. A table lists command paths and summaries;
`-o json` / `-o yaml` print one document:

| Field | Source |
| --- | --- |
| `schemaVersion`, `cliVersion`, `outputFormats` | The schema version (1), the build version, the `-o` values of P3 |
| `globalFlags` | The root's persistent flags, listed once instead of on every command |
| `commands[]` `command`, `path`, `name`, `aliases`, `short`, `long`, `usage`, `args`, `example`, `group` | The help text |
| `hidden`, `renamedTo`, `deprecated` | cobra's `Hidden` / `Deprecated`, `cmdutil.Renamed`; such commands appear only with `--all` |
| `flags`, `inheritedFlags` | Local flags, and flags inherited from a group other than the root: name, shorthand, type, default, description, `required` (`MarkFlagRequired`), `repeatable` (array and slice types), `enum` (`cmdutil.FlagEnum`) |
| `effect` | `cmdutil.Reads()` (changes nothing), `Writes()` (changes state on a Norcube service), `Local()` (changes only config, credentials, project files or the binary); absent for groups and for `api`, whose effect depends on the method |
| `confirm`, `wait`, `paginated` | The command has `--yes`; `--wait`; `--cursor` and `--all-pages` (the shared helpers of P13 register them) |
| `outputFormats` | The P3 formats; absent for groups, cobra's completion scripts and commands marked `cmdutil.AnnotationNoResult` |
| `service` | Registry key, product name and OAuth audience: the group's registry entry, or `cmdutil.AnnotationService` for platform commands |

Commands are sorted by path and flags by name, so the same binary always
prints the same bytes. `nrc commands backup jobs` narrows the document to
one subtree (names or aliases). A property the tree cannot know is never
guessed: it is declared with an annotation where the command is defined,
or the field is left out. The stability rule is in P9.

---

## 4. Feature inventory (v0.21.4, plus Unreleased)

What exists, per command group. The [command
reference](https://docs.norcube.com/platform/cli-reference) has every
flag; [CHANGELOG.md](../CHANGELOG.md) says when each arrived.

| Group | Resources and verbs |
| --- | --- |
| Platform | `login` (`--device`, `--no-browser`, `--force`), `logout` (`--all`), `whoami`; `auth status`, `auth token`, `auth sessions list | revoke`, `auth ci-token create | list | revoke | scopes`; `configure` (`get | set | unset`); `config list | profiles | path | edit | reset-urls`; `org list | current | use | switch | create`; `api`; `commands`; `doctor`; `upgrade`; `completion` |
| `backup` | `datasources list | get | create | update | pause | resume`; `jobs list | get | run | download`; `restores list | get | create`; `restore-tests list | run`, `restore-tests settings get | update`; `policies list | create | update | attach | detach | pause | resume`; `destinations list`; `health` |
| `langsync` | `init`, `sync`, `pull`; `namespaces list | create | update | delete`; `terms list | add | update | delete | import`, `terms import status`; `translations get | set | suggest | translate-missing`; `languages list | add | remove | create | delete`; `prompts list | get | create | update | delete`; `glossary get`, `glossary items list | get | create | translate | delete`, `glossary languages attach`; `api-keys list | create | revoke`; `audit list` |
| `domainradar` | `check`; `bulk create | list | get | results | watch | export | cancel | delete`; `saved list | add | remove`; `watchers list | get | create | update | delete`; `hits list | get`; `names generate | list | get | results | check | delete` |
| `billing` | `status`; `subscriptions list`; `usage estimate | metrics`; `invoices list | download`; `budget get | set` |
| `monitor` | `status` (`--fail-on`); `monitors list | get | create | update | delete | pause | resume | run` (`--wait`), `monitors bulk pause | resume | delete`, `monitors results list`, `monitors status-changes list`, `monitors stats get`, `monitors availability get`, `monitors certificate get`, `monitors alerts get | set`; `incidents list | get | acknowledge | summary`; `channels list | get | create | update | delete | test | verify`, `channels deliveries list`; `status-pages list | get | create | update | delete | publish | unpublish | preview-link`, `status-pages layout get | set`, `status-pages incidents list | get | create | update`, `status-pages incidents updates create`, `status-pages subscribers list | delete`; `maintenance list | get | create | update | cancel | finish` |
| PromptHub | No command group; `nrc api prompthub ...` |
| CMS | None (CMS has no public API) |

CI token scopes, as defined by Norcube Auth and listed by
`nrc auth ci-token scopes`: `backup.read`, `backup.write` (run backups
now), `backup.verify` (restore tests), `domainradar.check`,
`langsync.read`, `langsync.sync`, `langsync.write`, `billing.read`,
`cms.read`, `cms.write`, `monitor.read`, `monitor.check` (run a
monitor's check now), `monitor.write` (manage monitors, alert routing,
acknowledge incidents), `monitor.channels` (manage alert channels, test
sends and verification codes; not included in `monitor.write`, because
channels hold credentials and sends cost money), `monitor.statuspages` (manage status pages, post
incidents on them and manage their subscribers; not included in any other
scope, because what it changes is public and subscribers' addresses are
personal data), `monitor.maintenance` (manage maintenance windows; not included
in any other scope, because a window silences alerts, and one that shows on a
published page also takes `monitor.statuspages`).

Installation channels: Homebrew cask (with `nrc`), `.deb` / `.rpm` /
`.apk`, `install.sh`, release archives (darwin and linux amd64/arm64,
windows amd64), and from source (`make install`); Scoop and winget are configured and switched
off until their tokens exist. The GitHub Action
`norcubeplatform/setup-norcube` installs the CLI in workflows.

---

## 5. Decision log

Key decisions, newest last. Sources are commits in this repository, the
changelog, and the owner's decisions in the planning sessions of
2026-10-26 to 2026-10-31.

| Date | Decision | Why, and alternatives rejected | Source |
| --- | --- | --- | --- |
| 2026-05-10 | Ship one Go binary with browser login and self-update | | c5e6fd7 |
| 2026-05-14 | `--retranslate-on-source-change` is opt-in | Defaulting it on would overwrite hand-curated translations on a casual sync | 644cb69 |
| 2026-05-21 | Persist a rotated refresh token before using it; a failure to persist is an error | Silently keeping the old token leads to a confusing 401 on the next call | df5d494 |
| 2026-05-30 | Release check: 24 h cache, background, skipped off a terminal, opt-out env | Zero added latency, no noise in CI | 531e16a |
| 2026-08-12 | Apache 2.0 for the CLI | Permissive with a patent grant; the tool that proves no lock-in must not be a lock-in | 4c6f9de |
| 2026-08-12 | Command groups are named after the product (`backup`), generated clients after the service (`snapdb`) | Users know products; the client mirrors the backend it talks to. `snapdb` stays an alias | 2624a1e |
| 2026-08-12 | gh conventions: exit 4 means "sign in", atomic config writes | Audit against clig.dev, cobra and gh | f65c0ec |
| 2026-08-12 | DCO instead of a CLA; golangci-lint with gosec; downloads `0600` | Low-friction contributions with provenance | 8cfefeb |
| 2026-08-12 | Homebrew cask rather than formula | Formula publishing is deprecated in GoReleaser v2.10+ | ae35782 |
| 2026-08-13 | The README demo shows the whole CLI, not one product | The README is the platform CLI's front door | ad22ab2 |
| 2026-10-28 | Design for many services and many profiles, "like aws" | Owner's brief; the review that day found the CLI not yet ready (fake "session expired" on `--org` slugs, refresh races, no headless path, no profiles, no timeouts) | owner, review |
| 2026-10-28 | Seven non-negotiable principles: no breaking changes without deprecation; scriptable by default; never hang or lose a session; never prompt without a TTY; least privilege, no secret leaks; adding a service touches no core code; everything tested | They are P1 to P13 here | plan |
| 2026-10-28 | Registry and shared client (phase 4) before CI tokens (phase 3) | CI tokens are easier once every service goes through one client and one credential store | plan |
| 2026-10-28 | Docs: help text is the source; reference generated; guides hand-written; README short | Nothing written twice by hand (gh, AWS, Stripe model); the hand-written pages had drifted | owner, 375658d |
| 2026-10-28 | Profiles are opt-in; `default` is production; staging is not a built-in environment | A normal user never types `--profile`; Norcube's staging is internal (owner: "hide staging") | owner |
| 2026-10-29 | Exit codes 3 to 8 and 130; retries only for idempotent requests or `Idempotency-Key`; refresh lock across processes; `--org` accepts slugs | Honest errors, no double-applied changes, no lost sessions | 976cbd4 (v0.16) |
| 2026-10-29 | `<service> <resource> <verb>` with plural resources; old names hidden, notes opt-in until 1.0; `rm` never deletes | One grammar without breaking scripts | 375658d (v0.17) |
| 2026-10-29 | `nrc api` (gh-style, relative paths only), `nrc doctor`, redacting `--debug`, dynamic completion with a budget | Never blocked on a missing command; self-service diagnosis | 375658d |
| 2026-10-29 | CI tokens: checksummed format, short-lived in-memory exchange, organization fixed by the token, mismatch is a usage error; device sign-in | Safe automation and headless sign-in without sharing a person's session | 5631930 (v0.18) |
| 2026-10-29 | Shared helpers for item input, waiting, safe downloads, event streams; one process-wide credential store lock | One implementation per concern | b7d9b5f (v0.19) |
| 2026-10-29 | A failed waited-for job exits 1 with `job_failed`; connection strings only from a file or stdin; codegen drops admin and webhook operations | Scripts can tell "job failed" from "CLI failed"; no secrets in `ps` | 4b859e4 (v0.20) |
| 2026-10-29 | Signed checksums, provenance, SBOMs, Linux packages, notarization; actions pinned by SHA; optional release steps switch themselves off | Verifiable releases without blocking on any one secret | d05f003 (v0.21) |
| 2026-10-29 | Submit macOS notarization without waiting | GoReleaser waits at most 20 minutes; a new team's first submissions take hours; a bare binary cannot be stapled anyway | d8c5836 |
| 2026-10-29 | CI token Backup scopes stay narrow: run backups and restore tests only; restores, data source and policy changes and downloads need a person. Fix the error message instead (v0.21.3) | Least privilege for credentials that live in CI. Separate `backup.download` and `backup.restore` (new database only) scopes deferred until customers ask | 054fc5f, owner |
| 2026-10-29 | The CLI becomes a first-party OAuth client of Norcube Auth (PKCE, scoped logins, Connected apps, revocation) | Owner: "i want the proper way". Rejected: scopes on today's CLI sessions, which keeps the refresh token passing through dashboard JavaScript and three token models | owner |
| 2026-10-30 | Pin the Go toolchain, check `go mod tidy`, run govulncheck in CI with documented acceptances | Releases had been built with a Go patch with reachable stdlib advisories | 3b2e964 (v0.21.4) |
| 2026-10-30 | Engineering reviews are written down as reports and fixed in separate, deliberate changes, not by automatic fix agents | Owner's instruction | owner |
| 2026-11-04 | Norcube Monitor in the CLI. Monitors and channels are named by id or exact name (a name matching several is a usage error, never a guess); sub-resources of a monitor are nested resources with standard verbs (`monitors results list`, `monitors stats get`) rather than new verbs; `monitors run --wait` makes the synchronous check's verdict the exit code (`job_failed` on critical or inconclusive, `--fail-on warning`); `monitor status --fail-on` exits 1 with the new code `unhealthy` and prints the report either way (default `none`); bulk actions are their own subgroup (`monitors bulk ...`) so one command's output shape never depends on how many ids it got; a webhook's signing secret is written to `--secret-file` and never printed, Slack and Discord URLs and secret headers come from files or stdin; `output.Object.Text` keeps `-o text` columns fixed for objects whose fields depend on the resource; RFC 9457 problems (`code`, `reason`, `errors`) are parsed by the shared error formatter. No short alias: no product has an abbreviation, and P13 asks for one name. Alert channels get their own scope (`monitor.channels`), granted explicitly and not implied by `monitor.write` (owner: "everything should be well scoped") | Owner's brief: every Norcube Monitor operation, robust, scriptable. Rejected: `--wait` polling a check that is already synchronous; printing the webhook secret once like a CI token (the owner asked that webhook secrets never be printed); `monitors pause a b c` switching to the bulk endpoint and its shape with two or more ids | owner |
| 2026-11-04 | Status pages in the CLI. Pages are named by id or exact slug; a page's posted incidents are a nested resource (`status-pages incidents`), and an update on one is `status-pages incidents updates create` rather than a new verb, by the rule of the Norcube Monitor entry above. `create` marks every setting required and fills in none: a page is public communication, and the backend has no defaults either. `layout set` refuses a document without both lists, since it replaces the layout as a whole. `unpublish` confirms (old addresses are given up); `publish` and posting don't, so an incident pipeline stays one command, and their help says they go public. `preview-link` is a `read`: it changes nothing, though the API asks for `monitor.statuspages` | Owner's brief. Rejected: `incidents post`, a synonym for creating an update; the dashboard's defaults on `create`, as `monitors create` has, because a required setting on a public page should be a choice | owner |
| 2026-11-04 | `nrc commands`: the whole command tree as one versioned, offline document; what the tree cannot reveal is declared with annotations (`effect`, flag enums, service), never inferred from verbs | Agents should not crawl dozens of `--help` pages. Prior art: `kubectl api-resources` / `explain` (verbs per resource, offline schema), `aws ... --generate-cli-skeleton` (machine-readable input), cobra's doc generation (`tools/gendocs` already walks the tree). Global flags listed once rather than on every command, to keep the document small; a verb-based guess of the effect was rejected because domain verbs like `check`, `suggest` or `init` don't say whether they change anything | owner |
| 2026-11-04 | PulseCheck is renamed Norcube Monitor before its first release, every technical name included: command group `monitor` (`nrc monitor monitors list`), registry key, OAuth audience and generated package `monitor`, scopes `monitor.read`, `monitor.check`, `monitor.write`, `monitor.channels`, endpoint `https://monitor.api.norcube.com/app/v1`, `NORCUBE_MONITOR_URL`. No `pulsecheck` alias or `cmdutil.Renamed` entry | The old name was never in a release, so no script, config file or token depends on it and P10 has nothing to keep; the backend renamed its service, audience and scopes in the same change. Rejected: keeping `pulsecheck` as a hidden alias, which would be a second name for one thing (P13) with no user to serve | owner |
| 2026-11-05 | Status page subscribers in the CLI: `status-pages subscribers list` and `delete`. A subscriber is named by id only. `delete <page> <subscriber>` completes the page but not the subscriber, because completion candidates are cached on disk and addresses have no place there. The CLI words the scope refusal itself: the API's sentence is shared with the page's other writes and speaks of changing pages, which misleads on a list | Owner's brief. The API's `count` and `export` have no commands yet; `nrc api` reaches them | owner |
| 2026-11-06 | Maintenance windows in the CLI: `monitor maintenance`. The resource is a mass noun, an exception to the plural rule of P1, because "maintenances" isn't a word and "maintenance windows" is long to type for something used in deploy scripts; `maintenance-windows` is an alias. A window is named by id only: titles repeat by design. `--state` filters on the field the result calls `state` (the API's query parameter is `status`), with the API's values. There is no `delete`: the API deletes only a cancelled window, a completed one explains a gap in uptime and goes by itself after two years, and `cancel` already takes a window out of everything people see; `nrc api` reaches the endpoint. `finish` is a new verb, because `cancel` would say the work didn't happen. `cancel` confirms, since it mails whoever was told; `finish` doesn't and has no `--yes`, because ending a silence is the safe direction and it must run unattended at the end of a deploy. Notifications are explicit in the request, because the API takes no defaults: the flags default to on, as the owner decided, and `--no-notify` turns off all three. `monitor status` keeps a monitor among `problems` when what is wrong is a calendar condition or a watched aspect, because a window doesn't hold those | Owner's brief. Rejected: a bare `2h` meaning two hours ahead, since `ParseTimeArg` reads the same spelling as two hours ago | owner |
| 2026-11-06 | `-o id` prints only the result's id. Deploy scripts capture what they create with `id=$(... -o id)`, and `-o text` prints the state as well, so it would need `cut`. It is a global output value rather than a flag of one command, so it means the same everywhere (P1, P13) | Named in the owner's maintenance windows plan. Rejected: `--query id -o text` as the documented form, which still works but is too long for the one thing every script needs | owner |
| 2026-11-05 | `--auto-post` is the one page setting `create` doesn't require: the flag came after 0.23.0, so requiring it would break scripts that create pages, and the API keeps it off unless asked. `--notify` is sent only when given, and exists only on writes that add an update | Compatibility rule P10; owner's brief | owner |

---

## 6. Playbooks

### 6.1 Adding a product end to end

A product is "in the CLI" when a person, a CI job and an AI agent can use
it with the same guarantees as every other product. The steps, in order:

**Backend (in the product and Norcube Auth)**

1. **Audience.** Norcube Auth mints tokens for the product's audience
   from the dashboard token endpoint, which is also what a full CLI
   sign-in can be exchanged for.
2. **Scopes.** Add the product's scopes to Norcube Auth's CI token scope
   catalog (and so to the CLI sign-in scope catalog), each with its
   audience, label and description, and decide which actions stay
   person-only. A product without scopes refuses every scoped token, so
   it is unusable from CI.
3. **Scope enforcement in the product.** A route table that maps each
   authenticated route to the scopes allowed to call it, where unlisted
   routes are refused for scoped tokens and a test fails until every route
   is listed (DomainRadar's `scopes.go` is the reference). Tokens without a
   `scope` claim (dashboard, full CLI sign-in) are governed by IAM alone.
4. **Contract.** Swagger 2.0 for the routes organization members use,
   every operation with an explicit `@Id` and a tag, admin and webhook
   routes under tags the CLI can exclude; JSON in camelCase; errors with a
   stable `type` (`NOT_FOUND`, ...) so `CodeForStatus` can map them;
   `Retry-After` on 429; cursor pagination in one of the shapes `nrc api
   --paginate` understands (`nextCursor`, `cursors.next`).

**CLI (this repository)**

5. **Registry.** Add the entry to `services.yaml` (`key`, `name`,
   `command`, `audience`, production `endpoint`, `spec`, `package`).
   Choose `key` carefully: it is permanent. From here `nrc api <command>`
   already works.
6. **Hand-kept tables** until they are derived from the registry: a field
   and two `case` lines in `config.Config` (`Endpoint`, `SetEndpoint`), the
   service key constant in `config/environments.go`, the audience constant
   in `internal/auth/tokens.go`, the `auth token --audience` map and, if
   CI tokens apply, `ciAudiences` in `internal/cli/authcmd.go`. The v1
   migration struct is frozen; do not add new services to it.
7. **Client.** `internal/api/<package>/oapi-codegen.yaml` (copy one, set
   `package`, `output` and `exclude-tags`), then `make codegen
   MONO=../backend-mono`; commit the spec and the client together.
8. **Commands.** `internal/cli/<command>/` with `NewCmd()`, a `deps`
   struct, a constructor on `service.New`, commands per P1, results through
   `cmdutil`, the shared helpers of P13; wire it into `NewRootCmd`.
9. **Completion** for every resource id a command takes
   (`completion.Func` / `completion.Flag`).
10. **Tests.** In-process command tests against the fake backend in
    `internal/cli` (a `<product>_test.go`), golden files for every read
    command in all four formats, error mapping tests for the product's
    error types. The contract and naming tests then cover the new tree
    automatically.

**Everything around it**

11. **MCP parity.** The Norcube MCP server exposes the same product to AI
    clients through per-product tools gated by the same scopes; add or
    extend its tools so agents get the same capabilities as the CLI.
12. **Docs.** Run `make docs DOCS=../docs` for the reference (the release
    workflow also does it); write or update the product's CLI guide page
    and link it from the platform CLI guide.
13. **Website.** The CLI page and its demo show every product, not one;
    add the product there.
14. **Changelog** under `## Unreleased`, then release (section 6.6).

**Worked example: Norcube Monitor (state on 2026-11-04).**

| Step | State |
| --- | --- |
| 1 Audience | `monitor` exists in Norcube Auth (sign-in audience list, CI token exchange) |
| 2 Scopes | `monitor.read`, `monitor.check`, `monitor.write`, `monitor.channels`, `monitor.statuspages`, `monitor.maintenance` in the CI token catalog (and so the CLI sign-in catalog), with IAM actions; channel changes and sends only with `monitor.channels`, status page changes, posts and subscribers only with `monitor.statuspages`, neither of which `monitor.write` includes; not on the MCP consent screen until the MCP redesign |
| 3 Enforcement | Default-closed route table (`internal/routers/app/scopes.go`) after the token middleware, coverage and matrix tests, and IAM refusing a scoped request the table never checked |
| 4 Contract | Every operation has a verb-noun `@Id`, `@Security Bearer`, 401/403 problems and `x-norcube-scopes`; the status route is tagged `Status` and excluded here |
| 5 to 10 CLI | Registry entry, hand-kept tables, client, `nrc monitor` commands, completion, command and golden tests |
| 11 MCP | No tools yet |
| 12 to 14 | Changelog entry; the product's CLI guide page and the website mention are still to write |

### 6.2 Adding a command

1. Place it in the grammar (P1): existing resource, plural, a standard
   verb unless a domain verb from the table fits. Do not add a synonym for
   an existing verb.
2. `RunE: cmdutil.Run(...)` returning the API resource (or a named struct
   in the package, never an ad-hoc map for a new command).
3. Lists: `cmdutil.AddPageFlags` + `Paginate` + an `output.Table`.
   Long-running jobs: `AddWaitFlags` + `Poll` + `JobFailed`. Destructive or
   costly: `Confirm` + `--yes`. Secrets: from a file or stdin only.
4. Validate flags and arguments before the first request; return
   `clierr.Usage` for invocation mistakes.
5. `Short`, `Long` when useful, `Example`, every flag described.
   Declare what it changes with `Annotations: cmdutil.Reads()`,
   `Writes()` or `Local()`; flags with a fixed set of values use
   `cmdutil.FlagEnum` (completion and `nrc commands` both read it).
6. Completion for id arguments and flags.
7. Golden files for read commands (`go test ./internal/cli -run TestGolden
   -update`, then review the diff line by line); a command test for writes,
   including the no-terminal and `--yes` paths.
8. `CHANGELOG.md` under `## Unreleased` ("New", and "Changes for scripts"
   if anything observable changed).

### 6.3 Adding a global flag

Global flags are rare: each one is inherited by every command and appears
on every reference page.

1. It must apply to every command (otherwise it is a command flag).
2. Declare it in `NewRootCmd` with a description that names its
   environment variable; store it on `clictx.Flags`.
3. If it is a setting, resolve it in `internal/resolve` with a source, so
   `nrc config list` shows it, and decide whether the config file may set
   it (`[defaults]` or per profile). Table-test the precedence.
4. Mention it in the CLI guide's global behaviour section and in the
   changelog. Never add a global flag that carries a secret.

### 6.4 Changing output, error codes or exit codes

- **JSON / YAML:** additive only. Removing or renaming a field, changing a
  type, or changing a list into an object is a breaking change: before 1.0
  it needs the owner's agreement and a "Changes for scripts" entry; after
  1.0 a new field or an opt-in.
- **`-o text`:** new columns go at the end where possible; any change in
  position is a "Changes for scripts" entry (v0.19 did this for the
  LangSync AI column).
- **Tables:** free to improve; update the goldens.
- **Error codes:** add new codes to `clierr` with a meaning, an exit code
  in `ExitFor`, a test, and the CLI guide's code list. Never repurpose a
  code or move one to another exit code. Moving a failure from exit 1 to a
  more specific code is allowed and listed in the changelog.
- **Exit codes:** the table in P4 is closed; a new exit code is a design
  change.

### 6.5 Renaming or deprecating

1. Add the new command; keep the old one with `cmdutil.Renamed(old,
   "norcube <new path>")` or as an alias if the shape is identical.
2. Add the old name to `TestRenamedCommandsKeepWorking` if it is a
   distinct path.
3. Update examples, guides and the website to the new name; the old one
   disappears from help and the reference automatically.
4. Changelog: "Changes for scripts", naming both. Removal follows P10.

### 6.6 Releasing

Follow [RELEASING.md](RELEASING.md): the version bump moves `##
Unreleased` under `## X.Y.Z (date)`, the merge commit on `main` is tagged
`vX.Y.Z`, and the tag runs the release and docs-reference workflows. Check
the release page and the workflow summary's notices for skipped optional
steps. A release that fails after tagging is fixed by a new patch version,
never by re-tagging (0.21.0 to 0.21.2).

### 6.7 Regenerating clients after a backend change

1. `make codegen MONO=../backend-mono` with the backend checkout on the
   branch whose API you target (production behaviour means its default
   branch).
2. Review the spec diff for removed or renamed operations: a renamed
   `@Id` breaks compilation here, a removed field may break a JSON shape
   the CLI prints (P3).
3. `go build ./... && go test -race ./...`, fix callers, add commands for
   new endpoints where they are worth it.
4. Commit `specs/` and `internal/api/` together.

---

## 7. Rules for AI agents

Imperative, short, no exceptions. [AGENTS.md](../AGENTS.md) carries the
same list.

**Do**
- Read this file and [ARCHITECTURE.md](ARCHITECTURE.md) before changing
  code.
- Build clients with `service.New`; return results through `cmdutil.Run`
  / `cmdutil.Print`; write hints and progress to `cmd.ErrOrStderr()`.
- Use the shared helpers of P13; inject collaborators through the
  package's `deps` struct.
- Keep the grammar `<service> <resource> <verb>`, plural resources, the
  verb table of P1.
- Give every visible command a `Short` (capitalized, no trailing period),
  an `Example`, a description for every flag, and an effect annotation
  (`cmdutil.Reads`, `Writes` or `Local`).
- To find out what the CLI can do, run `nrc commands -o json` (or narrow
  it: `nrc commands backup -o json`) instead of reading many `--help`
  pages; check `effect` and `confirm` before running a command.
- Return errors; use `clierr.Usage` for invocation mistakes; let
  `clierr` and `Describe` pick the code.
- Add tests with every behaviour change; regenerate goldens with
  `-update` only for intended changes and read the diff.
- Add a `CHANGELOG.md` entry under `## Unreleased`; anything a script can
  observe goes under "Changes for scripts".
- Update the docs guide pages in the docs repository when user-visible
  behaviour changes.
- Run the checks in [AGENTS.md](../AGENTS.md) before calling a change
  done.

**Don't**
- Don't write to stdout outside `cmdutil` / `internal/output`, and don't
  use `os.Stdout`, `os.Stderr` or `fmt.Print*` in command code.
- Don't edit `*.gen.go` or `specs/`; run `make codegen MONO=../backend-mono`.
- Don't call `os.Exit` outside `cmd/norcube`; don't create HTTP clients
  outside `internal/transport`.
- Don't change an existing error code, exit code, JSON field or `-o
  text` column position; don't remove or rename a shipped command, flag or
  environment variable (use `cmdutil.Renamed` or an alias).
- Don't prompt without checking for a terminal (use `cmdutil.Confirm`);
  don't add a destructive command without `--yes`.
- Don't accept secrets in flags or arguments; don't print secrets,
  tokens or hashes; don't log request bodies outside the redacting debug
  transport.
- Don't change a registry `key`.
- Don't put internal hostnames, private repository paths, staging URLs or
  customer data in code, help text, tests or docs: this repository is
  public.
- Don't add AI attribution to commits, pull requests, tags or releases (no
  `Co-Authored-By`, no session links). Use conventional commit subjects,
  `git commit -s` for the DCO, and stage explicit paths.
- Don't push, tag or release unless the owner asks in the current
  conversation.

---

## 8. Known gaps and roadmap

Honest state on 2026-11-03. Items marked "before 1.0" are changes to an
observable contract and will be announced in the changelog.

**Commitments not yet fully kept**
- **Registry is not the only place a service is named.** `config.Config`
  fields and switches, auth audience constants and the CLI's audience lists
  are hand-kept (section 6.1 step 6). Goal: derive them from the registry
  and make a forgotten entry a test failure.
- **LangSync is the outlier of P13.** No `deps` struct; its own poll loop;
  `sync` and `pull` define their own `--wait` (default on) and, when
  `--wait-timeout` passes, return success with the last job instead of exit
  7; a failed sync job is a plain error rather than `job_failed`; some
  usage errors exit 1; several local "Cancelled." implementations; direct
  `os.Stderr` writes; vocabulary mixes "mark" and "term" (`terms add` says
  "Add a new mark"). `sync`, `pull`, `init` and the project file code have
  no tests yet. Exit-code fixes here are "Changes for scripts".
- **JSON shapes (before 1.0).** Several write commands return ad-hoc maps
  instead of the API resource; `backup policies list` returns attachments
  instead of definitions with `--datasource`.
- **Flag drift.** `-y` exists only in DomainRadar; the output path flag is
  `--to` in `domainradar bulk export` and `--file` elsewhere;
  `restore-tests run` takes the backup id as an argument while `restores
  create` takes `--backup`. To be aligned with aliases for the old
  spellings.
- **`backup health` and some pickers read one page only.**
- **Codegen drift check is not running.** The workflow skips until a spec
  source (`SPEC_ARCHIVE_URL`, or a read-only backend deploy key) is
  configured in the repository settings; until then drift is caught only by
  running `make codegen-check` locally.
- **Self-update trusts the release it downloads.** It checks the SHA-256
  against `checksums.txt` but not the Sigstore signature that the release
  already produces; the self-update library also pulls several unused
  hosting SDKs into the binary. Plan: an in-house updater that verifies
  the signature against the pinned workflow identity.
- **Testing breadth.** CI runs on Linux only (file locks, permissions,
  config paths and keyrings are untested on macOS and Windows); no
  coverage gate; invocation state in package-level variables prevents
  `t.Parallel()`; no dependency update automation.
- **Small items.** The update check is not skipped on `CI=true` with a
  pseudo-terminal; `--debug` header redaction is an exact-name list; a
  read-only command may write the config file to remember an
  organization's id.

**Roadmap**
- **First-party OAuth sign-in** (P12): in development; scoped logins
  (`nrc login --scopes`), Connected apps, RFC 7009 revocation; the current
  exchange endpoint retires two releases after it ships.
- **Norcube Monitor**: a docs guide page (`/monitor/cli`), the website
  demo, MCP tools (section 6.1 table).
- **Coverage still missing in the CLI:** organization members and IAM
  (`org members ...`), Backup destinations beyond `list`, notifications
  and audit, resumable downloads, PromptHub commands.
- **Distribution:** Scoop and winget publishing once their tokens exist;
  a Windows arm64 build; a container image for CI.
- **1.0:** when the gaps marked "before 1.0" are closed and the OAuth
  sign-in has shipped; then P10's 1.0 rules apply.
