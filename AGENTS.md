# Instructions for AI coding agents

## Engineering standards (non-negotiable)

The full text is `docs/ENGINEERING.md` in the golang-template repository (github.com/jetlio/golang-template; locally `~/Work/golang-template/docs/ENGINEERING.md`). Read it before designing or changing code, and say in the plan how the change meets it. References: golang-template, and in backend-mono `apps/monitor` (layout, wiring, lint-enforced layers, apperr) and `apps/assistant` (huma). **Never copy patterns from older services.**

1. **One source of truth.** Generate from the owner of a fact: OpenAPI → clients, catalogs → constants, migrations → sqlc. Never hand-edit generated code; CI checks for drift.
2. **Clean layers.** handler → service → repo, with dto and mapper beside them, enforced by lint.
3. **Dependency injection.** Use `Deps`/`Options` in one composition root. No global state, no singletons.
4. **Typed vocabularies.** Enums, statuses, events and error codes are typed constants or generated. No magic strings or numbers. Configuration is declared once.
5. **Secure by design.** Deny by default. Guard every route from the permission catalog; scope tenants in the query itself. Hash or seal secrets, wire up validation, send outbound calls through the SSRF-safe client, redact logs, keep an audit trail.
6. **Reliable.** A deadline on every outside call, bounded retries, atomic changes, an outbox for side effects, supervised workers (never a bare `go func()`), graceful shutdown.
7. **Bounded and scalable.** Sizes, pages and concurrency are capped. Nothing assumes a single replica.
8. **Errors.** Domain errors map to RFC 9457 problems with enumerated codes. Internals never leak.
9. **Tested at the boundaries,** including authorization denials. Every security fix gets a regression test.
10. **Process.** Docs ship with the change. Short-lived branches and PRs, commits with explicit paths, pinned tool versions. Spikes are labelled as spikes.


Before changing anything, read [docs/DESIGN.md](docs/DESIGN.md) (the
binding principles, contracts and playbooks) and
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) (the code map). Section 7 of
DESIGN.md is the full do/don't list; these are the hard rules.

## Hard rules

1. **stdout is the result only.** Commands return values through
   `cmdutil.Run` / `cmdutil.Print`; hints and progress go to
   `cmd.ErrOrStderr()`. No `fmt.Print*`, `os.Stdout` or `os.Stderr` in
   command code (`TestNoDirectStdoutWrites` fails otherwise).
2. **Never edit generated code.** `internal/api/<package>/*.gen.go` and
   `specs/` come from `make codegen MONO=../backend-mono`; commit spec and
   client together.
3. **Keep the grammar.** `norcube <service> <resource> <verb>`, plural
   resources, the verbs in DESIGN.md P1. Renames keep the old name with
   `cmdutil.Renamed` or an alias.
4. **Document every command where it is defined.** A `Short` (capitalized,
   no trailing period), at least one `Example`, a description for every
   flag, and what it changes (`Annotations: cmdutil.Reads()`, `Writes()`
   or `Local()`). The docs reference and `nrc commands` are generated
   from it.
5. **Contracts don't break.** Never change an existing error code, exit
   code, JSON field or `-o text` column; new ones are additive and go in
   `CHANGELOG.md` under "Changes for scripts".
6. **Build clients with `service.New`** and use the shared helpers
   (`Paginate`, `Poll` + `JobFailed`, `Confirm` + `--yes`, `SaveStream`,
   `ReadItems`, `clierr.Usage`). No new HTTP clients, no `os.Exit` outside
   `cmd/norcube`.
7. **Safe by default.** No prompt without a terminal; destructive commands
   confirm and take `--yes`; secrets never in flags, arguments or output.
8. **Tests with every change.** Update goldens only for intended changes
   (`go test ./internal/cli -run TestGolden -update`) and read the diff.
9. **Public repository.** No internal hostnames, private repository paths,
   staging URLs or customer data in code, help text, tests or docs.
10. **Commits.** Conventional subject, `git commit -s` (DCO), explicit
    paths, a `CHANGELOG.md` entry under `## Unreleased`, no AI attribution
    (no `Co-Authored-By`, no session links). Never push, tag or release
    unless the owner asks.

## Using the CLI

To learn what the CLI can do, run `nrc commands -o json` (offline, no
sign-in) rather than crawling `--help` pages. It prints every command with
its flags, arguments, examples, `effect` (`read`, `write`, `local`),
`confirm` (needs `--yes` without a terminal), `wait`, `paginated` and the
service and audience it uses. Narrow it with a path (`nrc commands backup
jobs -o json`) or `--query`, for example
`--query "commands[?effect=='read'].command"`. The schema is versioned
(`schemaVersion`) and only grows within a version. For an endpoint
without a command, use `nrc api`.

## Checks

Run before calling a change done (the first six are what CI runs):

```bash
go build ./...
go vet ./...
go test -race ./...
go mod tidy -diff
scripts/govulncheck.sh
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run ./...
make codegen-check MONO=../backend-mono   # when the registry, specs or codegen change
make docs DOCS=../docs                    # when help text changes; commit in the docs repo
```
