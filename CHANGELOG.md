# Changelog

Notable changes, newest first. Release notes on GitHub list every commit;
this file collects what users and scripts need to know.

## Unreleased

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
