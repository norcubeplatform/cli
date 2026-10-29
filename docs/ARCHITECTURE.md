# Architecture

How the CLI's code is organized, and how to add a service or a command.
For using the CLI, see the [docs](https://docs.norcube.com/platform/cli).

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
       └─ cmdutil            result → --query → output (table | json | yaml | text)
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
| `internal/auth`, `internal/credstore` | Browser login, the per-profile session (OS keyring or a 0600 file), the per-(audience, organization) access-token cache, and the refresh lock that keeps parallel commands from racing. |
| `internal/transport` | The one HTTP client: connect and read timeouts, retries with backoff and `Retry-After`, proxy and CA bundle, User-Agent, and the redacting `--debug` trace. |
| `internal/cmdutil`, `internal/output` | The result path: `--query` (JMESPath), the four output formats, pagination flags, `--wait` polling and `JobFailed`, `Confirm` and `Cancelled`, text and item input from files or stdin, safe file saves, renamed-command notes. |
| `internal/clierr` | Error codes, exit codes and the JSON error shape. |
| `internal/completion` | Dynamic shell completion with a one-second budget and a one-minute cache. |
| `internal/orgs` | Organization slug resolution and its cache. |
| `internal/api/<service>` | Generated clients. Never edit `*.gen.go`; run `make codegen`. |

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
  keyring. Golden files under `internal/cli/testdata/golden` pin every
  output format; update them with `go test ./internal/cli -run TestGolden -update`.
- Contract tests walk the tree: every command has an example and complete
  help text, nothing writes to stdout outside the result path, and no two
  sibling commands share a name or alias.
- `go test -race ./...`, `go vet` and golangci-lint (pinned in CI) must
  pass.
