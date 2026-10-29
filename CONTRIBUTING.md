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

`make run ARGS="backup jobs list"` runs the CLI from source. The binary
talks to the production Norcube API by default; a profile with
`environment = "custom"` or `NORCUBE_<SERVICE>_URL` points it elsewhere
(see [configuration](https://docs.norcube.com/platform/cli#configuration)).
`norcube doctor` and `--debug` help when something doesn't connect.

[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) explains how the code is
organized and walks through adding a service or a command.

### Generated API clients

`internal/api/<package>/` packages are generated with a pinned
oapi-codegen from the services listed in `internal/registry/services.yaml`
(converted specs are committed under `specs/`). Don't edit `*.gen.go` by
hand. Regenerating them (`make codegen`) needs the services' specs, which
live in a private repository, so for most contributions treat the
generated clients and specs as read-only inputs; if an endpoint you need
is missing, open an issue (or call it with `norcube api` meanwhile).

### Documentation

The [command reference](https://docs.norcube.com/platform/cli-reference)
is generated from the commands' help text (`make docs DOCS=../docs`), so
document a command where it's defined: a sentence-style `Short`, a `Long`
when useful, at least one `Example`, and a description for every flag.
Tests fail when one is missing.

## Code conventions

- Commands live in `internal/cli/<command>/`, named after the
  user-facing command (`backup`, `langsync`), and are built with cobra.
  Names follow `<service> <resource> <verb>` with plural resources.
  API client packages are named after the backend service they talk to,
  and clients are built with `service.New`.
- Use `RunE` and return errors; never `os.Exit` inside a command.
  User-facing errors should say what to do next, not just what failed.
- Commands return their result through `cmdutil.Run`; anything written
  to stdout must stay machine-consumable (`-o json` pipes into `jq`), so
  hints and progress go to stderr.
- Add tests for behavior that could regress silently — pure formatting
  and error-mapping logic especially. `go test ./...` must pass.
- Run `gofmt`, `go vet` and `go test -race ./...` before pushing; CI
  enforces them plus golangci-lint.

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
