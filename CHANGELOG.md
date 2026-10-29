# Changelog

Notable changes, newest first. Release notes on GitHub list every commit;
this file collects what users and scripts need to know.

## Unreleased

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
