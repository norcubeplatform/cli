# Architecture

How the CLI's code is organized, and how to add a service or a command.
For using the CLI, see the [docs](https://docs.norcube.com/platform/cli).
The principles, contracts and playbooks this code follows, and why, are in
[DESIGN.md](DESIGN.md); read it before changing behaviour.

## The path of one command

```
cmd/norcube            main: signals, exit codes, error rendering
  └─ internal/cli      cobra tree; root.go wires global flags
       ├─ clictx       the invocation's flags, profile, config, HTTP client
       ├─ service      service.New[T]: org + endpoint + token + transport → typed client
       │    ├─ registry      services.yaml: keys, audiences, endpoints, specs
       │    ├─ auth          token exchange, per-profile session, refresh lock
       │    └─ transport     timeouts, retries, User-Agent, --debug trace
       ├─ api/<service>      generated clients (oapi-codegen); api/auth.go by hand
       └─ cmdutil            result → --query → output (table | json | yaml | text | id)
```

A command's `RunE` builds its client with `service.New`, calls the API,
and returns a value through `cmdutil.Run` / `cmdutil.Print`. It never
writes to stdout itself: stdout carries only the result, so `-o json`
always pipes cleanly into other tools. Progress, hints and confirmations
go to stderr. Errors are returned, never `os.Exit`ed; `internal/clierr`
maps them to stable codes and exit codes.

## Packages

| Package | Responsibility |
| --- | --- |
| `internal/registry` | The service manifest (`services.yaml`, embedded): config key, product name, command name and aliases, OAuth audience, production endpoint, spec location, generated package. |
| `internal/service` | `service.New[T]` resolves the target organization (explicit, `--org`/`NORCUBE_ORG`, a command default such as `.langsync.json`, then the profile), the endpoint, a token source and the shared transport, and returns the typed client. |
| `internal/config`, `internal/resolve` | The config file (profiles, schema v2, migration) and which profile, endpoints, organization, credentials and output a command uses, each with its source. |
| `internal/auth`, `internal/credstore` | Sign-in (OAuth browser and device flows, and the older flows for servers without them), the per-profile session (OS keyring or a 0600 file), the per-(audience, organization) token cache, and the refresh lock that keeps parallel commands from racing. See [Sign-in and tokens](#sign-in-and-tokens). |
| `internal/transport` | The one HTTP client: connect and read timeouts, retries with backoff and `Retry-After`, proxy and CA bundle, User-Agent, and the redacting `--debug` trace. |
| `internal/cmdutil`, `internal/output` | The result path: `--query` (JMESPath), the four output formats, pagination flags, `--wait` polling and `JobFailed`, `Confirm` and `Cancelled`, text and item input from files or stdin, safe file saves, renamed-command notes, and the command annotations (`annotations.go`: effect, flag enums, service) that `nrc commands` reads. |
| `internal/clierr` | Error codes, exit codes and the JSON error shape. |
| `internal/completion` | Dynamic shell completion with a one-second budget and a one-minute cache. |
| `internal/orgs` | Organization slug resolution and its cache. |
| `internal/api/<service>` | Generated clients. Never edit `*.gen.go`; run `make codegen`. `internal/api/apierror` turns any error response (the `{type, msg}` shape and RFC 9457 problems) into a `clierr.Error`. |

`internal/cli/commands.go` is `nrc commands`: it walks the cobra tree and
prints the machine-readable command tree (schema in
[DESIGN.md, section 3.8](DESIGN.md#38-nrc-commands-the-discovery-entry-point)).
It reads only the tree, the annotations and the registry, never the
network.

## Sign-in and tokens

The CLI is a public, first-party OAuth 2.0 client of Norcube Auth
(`client_id` `norcube-cli`). Everything it needs is discovered from the
auth URL's RFC 8414 metadata (`/.well-known/oauth-authorization-server`),
cached for a day in the user cache directory.

- **Browser sign-in** (`internal/auth/oauth.go`): authorization code with
  PKCE (S256) and a random `state`, redirected to a loopback listener on
  `127.0.0.1` and a random port (RFC 8252). The callback must carry the
  `state` and the server's issuer (`iss`, RFC 9207) before the code is
  exchanged. The authorization request names the scopes, the device's
  hostname (`device_name`) and the CLI's User-Agent (`client_user_agent`)
  for the consent screen and the session.
- **Device sign-in** (`internal/auth/device.go`): RFC 8628 against the
  metadata's `device_authorization_endpoint`, polled at the token endpoint
  (`authorization_pending`, `slow_down` adds five seconds, `access_denied`,
  `expired_token`).
- **Tokens** (`internal/auth/oauthsession.go`): a sign-in gives a refresh
  token and a 5-minute CLI access token, which no product accepts. Every
  command exchanges it (RFC 8693) for a token per (audience,
  organization; Norcube Auth's own `auth` audience also without one, for
  an account that has none yet); exchanges use nothing up, run in parallel without any
  lock and are cached in memory only. Refreshing rotates the refresh token,
  so it runs under an in-process mutex plus a per-profile lock file, and
  the rotated token is stored before the new access token is used. A
  refused refresh re-reads the stored token once in case another process
  rotated it meanwhile.
- **Credentials** (`internal/auth/keyring.go`): one item per profile.
  Format 2 holds the refresh token, the cached CLI access token, the
  granted scopes and a format marker; it fits the 2.5 KB Windows Credential
  Manager limit, and a store that refuses it gets the blob without the
  access token. Format 1 is the older sign-in (a refresh JWT that is
  refreshed straight into per-service tokens, cached in the item too). It
  is moved to format 2 once, under the lock, by a token exchange of the
  refresh JWT; a token Norcube Auth won't move asks for one `norcube login`.
- **Errors**: a dead refresh token is `login_required` (exit 4),
  `org_access_denied` is `permission_denied` (exit 5), and
  `insufficient_scope` is `insufficient_scope` (exit 5) with a hint naming
  the login's scopes.
- **Sign-out**: `logout` revokes the refresh token (RFC 7009); a format 1
  session uses the older sign-out endpoint. `login --force` ends the
  session it replaces the same way, best effort.
- **Older servers**: when the metadata is missing or doesn't list the token
  exchange grant, `login` uses the older sign-in (the web app's
  `/cli-login` page and `/auth/cli/device/*`) and sessions refresh the
  older way. CI tokens (`NORCUBE_TOKEN`) work as before.

## Adding a service

1. **Manifest.** Add an entry to `internal/registry/services.yaml`: `key`
   (config and `NORCUBE_<KEY>_URL` name; never change it later), `name`,
   `command`, `audience`, the production `endpoint`, and `spec` (where the
   service's Swagger file lives). The service now has an endpoint setting,
   an `--endpoint-url` key and `nrc api <command>` support.
2. **Client.** Set `package`, create `internal/api/<package>/oapi-codegen.yaml`
   (copy an existing one, change `package` and `output`) and run
   `make codegen MONO=<backend checkout>`. Routes that aren't for
   organization members (superadmin tools, webhooks) are left out by tag
   with `output-options.exclude-tags`; codegen drops them, and the schemas
   only they use, from the checked-in spec as well.
3. **Commands.** Create `internal/cli/<command>/` with a `NewCmd()` that
   builds the group, and a constructor on top of `service.New`:

   ```go
   type xContext = service.Client[*x.ClientWithResponses]

   func newXContext(cmd *cobra.Command) (*xContext, error) {
   	return service.New(cmd, config.ServiceX, func(base string, h *http.Client, edit service.RequestEditor) (*x.ClientWithResponses, error) {
   		return x.NewClientWithResponses(base, x.WithHTTPClient(h), x.WithRequestEditorFn(edit))
   	})
   }
   ```

4. **Wire it** into `NewRootCmd` in `internal/cli/root.go`, and register
   dynamic completions for the resource ids it takes.

## Adding a command

- Name it `<service> <resource> <verb>`: plural resources, verbs `list`,
  `get`, `create`, `update`, `delete` plus domain verbs (`run`,
  `download`, `pause`, `resume`, `sync`, `pull`, `cancel`, `export`).
  When renaming, keep the old name working with `cmdutil.Renamed` (or an
  alias) until v1.0.
- Give it a sentence-style `Short` (capitalized, no trailing period), a
  `Long` when there's more to say, at least one `Example`, and a
  description for every flag. Tests enforce this, because the
  [command reference](https://docs.norcube.com/platform/cli-reference) is
  generated from it.
- Declare what it changes in the literal: `Annotations: cmdutil.Reads()`,
  `cmdutil.Writes()` or `cmdutil.Local()` (a test fails without one).
  Flags with a fixed set of values call `cmdutil.FlagEnum`, which also
  wires their completion. `nrc commands` reads both.
- Return the result from `cmdutil.Run`; lists use `cmdutil.Paginate` and
  `output.Table`.
- Commands that delete or change something in bulk ask with
  `cmdutil.Confirm` and take `--yes`; without a terminal they fail with a
  usage error instead of prompting.

## Generated files

| What | Source | Regenerate |
| --- | --- | --- |
| `internal/api/<package>/*.gen.go`, `specs/*.openapi.json` | the services' Swagger specs | `make codegen MONO=…` (CI's drift check runs `make codegen-check`) |
| Docs site command reference | the cobra tree's help text | `make docs DOCS=../docs` (also on every release tag) |
| Shell completions, man pages | the cobra tree | `go run ./tools/gendocs` (release archives and Linux packages) |

## Releases

A `vX.Y.Z` tag runs GoReleaser (`.goreleaser.yaml`) from
`.github/workflows/release.yaml`: archives, deb/rpm/apk packages, SBOMs, a
Sigstore-signed `checksums.txt`, build provenance attestations, and the
Homebrew, Scoop and winget manifests. `norcube upgrade` installs only the
archive for its platform and only after its SHA-256 matches
`checksums.txt`. [RELEASING.md](RELEASING.md) is the runbook, including the
secrets and the one-time setup.

## Tests

- `internal/cli` runs the real command tree in-process against a fake
  backend (`cli_test.go`) with a temporary config, cache and mocked
  keyring. `internal/auth/authtest` is a fake Norcube Auth OAuth server
  (metadata, authorization redirect, code exchange, refresh with rotation
  and reuse detection, token exchange, device flow, session upgrade,
  revocation) for sign-in and token tests; a test "browser" follows the
  authorization redirect to the CLI's loopback callback. Golden files under `internal/cli/testdata/golden` pin every
  output format; update them with `go test ./internal/cli -run TestGolden -update`.
- Contract tests walk the tree: every command has an example, complete
  help text and an effect, nothing writes to stdout outside the result
  path, and no two sibling commands share a name or alias.
  `commands_test.go` pins the `nrc commands` schema (golden
  `commands-billing.*`; `commands-monitor.json` pins a whole product
  group) and checks that it lists every visible command, sorted, offline.
- `go test -race ./...`, `go vet` and golangci-lint (pinned in CI) must
  pass.
