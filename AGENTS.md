# Instructions for AI coding agents

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
   flag. The docs reference is generated from it.
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
