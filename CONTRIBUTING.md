# Contributing to the Norcube CLI

Thanks for considering a contribution! This document covers the
practicalities: getting a build running, the conventions the codebase
follows, and the sign-off we need on commits.

## Development setup

You need Go ≥ 1.24. Then:

```bash
git clone https://github.com/norcubeplatform/cli
cd cli
make build      # → bin/norcube
make test       # go test ./...
make vet        # go vet ./...
```

`make run ARGS="backup list"` runs the CLI from source. The binary talks
to the production Norcube API by default; `NORCUBE_AUTH_URL` and friends
override the endpoints (see the README's Configuration section).

### Generated API clients

`internal/api/<service>/` packages are generated with oapi-codegen from
the backend services' OpenAPI specs (committed under `specs/`). Don't
edit `*.gen.go` by hand. Regenerating them (`make codegen`) requires a
checkout of the private backend repository, so for most contributions
you should treat the generated clients and specs as read-only inputs —
if an endpoint you need is missing, open an issue instead.

## Code conventions

- Commands live in `internal/cli/<command>/`, named after the
  user-facing command (`backup`, `langsync`), and are built with cobra.
  API client packages are named after the backend service they talk to.
- Use `RunE` and return errors; never `os.Exit` inside a command.
  User-facing errors should say what to do next, not just what failed.
- Tables go through `internal/output`; anything written to stdout must
  stay machine-consumable (`-o json` pipes into `jq`), so hints and
  progress go to stderr.
- Add tests for behavior that could regress silently — pure formatting
  and error-mapping logic especially. `go test ./...` must pass.
- Run `gofmt` and `go vet` before pushing; CI enforces both plus
  golangci-lint.

## Commit messages

Follow the existing style: a `type: summary` first line
(`feat:`, `fix:`, `docs:`, `build:`, `chore:`), imperative mood, with a
body explaining *why* when the change isn't self-evident.

## Developer Certificate of Origin

We use the [DCO](https://developercertificate.org/) instead of a CLA:
by signing off a commit you certify that you have the right to submit
the work under this repository's license (Apache 2.0).

Add a `Signed-off-by` line to each commit — `git commit -s` does it for
you:

```
Signed-off-by: Your Name <you@example.com>
```

Pull requests with unsigned commits can't be merged; `git rebase
--signoff` fixes up an existing branch.

## Pull requests

Keep them focused: one logical change per PR. If you're planning
something larger (a new command group, a new dependency), open an issue
first so we can agree on the direction before you invest the time.
