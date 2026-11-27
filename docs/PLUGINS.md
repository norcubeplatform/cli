# Plugins

Some Norcube products ship their command-line engine as a separate
executable, a **plugin**. `norcube <command> …` then runs that executable
with the arguments that follow, so the product's commands sit next to every
other `norcube` command while the engine is built and released on its own.

This document is the contract between the CLI and a plugin. **Protocol
version: 1.**

## Which plugins run

- Only plugins named in the service registry
  (`internal/registry/services.yaml`, field `plugin`) ever run. The CLI never
  discovers executables by a name prefix, so a file called
  `nrc-something` on `PATH` does nothing.
- A plugin's executable is named `nrc-<command>` (`nrc-example` for
  `norcube example`). The registry refuses any other name.
- Lookup order: the plugin directory, then `PATH`. The plugin directory is
  `NORCUBE_PLUGIN_DIR`, else `<user config dir>/norcube/plugins`
  (`~/Library/Application Support/norcube/plugins` on macOS,
  `~/.config/norcube/plugins` on Linux, `%AppData%\norcube\plugins` on
  Windows).
- An executable, or the directory it is in, that other users may write is
  refused. Anyone who could replace it could run code as you.

## Handshake

Before running a plugin the CLI runs `<plugin> __plugin-info` (5-second
limit) and expects one JSON object on stdout:

```json
{"name": "nrc-example", "version": "1.4.0", "protocol": 1}
```

The CLI refuses a plugin whose `name` differs from the registry's, or whose
`protocol` differs from its own, and names the side that needs an update.

## Running

`norcube [global flags] <command> [args…]` runs `<plugin> [args…]`:

- **Arguments.** Everything after the command goes to the plugin
  unchanged, including `--help`. Global flags before the command
  (`--profile`, `--org`, `-o`, `--debug`) are applied by the CLI and passed
  to the plugin in the environment.
- **Streams.** stdin, stdout and stderr are the terminal's. The plugin
  prints its own result and errors; the CLI adds nothing.
- **Exit code.** The CLI exits with the plugin's exit code. Codes 2
  (usage), 4 (login required), 5 (permission denied) and 130 (interrupted)
  mean the same as in the CLI; others are the plugin's own and documented
  by it.
- **Signals.** Ctrl-C cancels the plugin; after 5 seconds it is killed.

## Environment

| Variable | Meaning |
|---|---|
| `NRC_PLUGIN_PROTOCOL` | The protocol version, `1` |
| `NRC_BIN` | Absolute path of the running CLI, for calling it back |
| `NRC_PROFILE` | `--profile`, when given |
| `NRC_ORG` | `--org`, when given |
| `NRC_OUTPUT` | `-o`, when given (`table`, `json`, `yaml`, `text`, `id`) |
| `NRC_DEBUG` | `1` with `--debug` |

The rest of the environment is passed through (`NO_COLOR`, `NORCUBE_*`).

## Credentials

The CLI never hands a plugin a token or a refresh token. A plugin that
calls a Norcube API asks the CLI for a short-lived access token each time:

```sh
"$NRC_BIN" auth token --audience <audience> -o json
```

That works with every way of signing in (browser login, device login, a CI
token in `NORCUBE_TOKEN`), honours the profile, organization and scopes, and
leaves storage and refresh to the CLI. A plugin must not store tokens.

## Compatibility

- A new protocol version is introduced only for a change a plugin must
  react to. Additions (a new environment variable) keep version 1.
- The CLI speaks exactly one protocol version. When it moves to version 2,
  plugins answer the handshake with the version they speak and the CLI
  tells the person which side to update.
