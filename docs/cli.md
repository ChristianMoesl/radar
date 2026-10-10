# Command-line reference

[Documentation](README.md) · [Dashboard shortcuts](dashboard.md#keyboard-shortcuts)

## Commands

```sh
radar                         # Dashboard
radar setup                   # Configure or reconfigure
radar setup notifications     # macOS launch/notification approval
radar update                  # Confirm a managed macOS release
radar version
radar documentation [--topic <source>] [--json]
radar create                  # Interactive workspace creation
radar fork
radar task create --title <title>
radar task done <task-id>
radar task reopen <task-id>
radar task mute <task-id>
radar task unmute <task-id>
radar task delete <task-id>
radar task priority <task-id> urgent|normal
radar status
radar tasks
radar reconcile-workspace --request <json> [--workspace <path>] [--preview]
radar recreate-sandboxes [--workspace <path>] [--preview] [--yes]
radar workspace-context [--workspace <path>]
radar repository-refs --repo <repo>
radar cleanup <task-id>
radar gc
radar refresh
radar reset
radar stop
radar restart
radar config-path
radar state-path
radar log-path
```

## Offline documentation

Use **`man radar`** for this command reference and **`man radar-config`** for
configuration guidance. Both are generated from the same Markdown documentation
as the installed CLI's embedded manual. See [manual installation and discovery](installation.md#manual-pages).

The Pi extension reads the embedded Markdown through a non-interactive transport:

```sh
radar documentation --json
radar documentation --topic docs/configuration.md --json
```

Without a topic, the result contains `version`, `commit` and a `topics` list of
canonical `source` paths and `title` values. With a topic, it contains `version`,
`commit` and `document` (`source`, `title`, `content`). Only listed public docs
can be read; arbitrary host paths are rejected. Relative Markdown links resolve
against the source path; omit the `#anchor` when retrieving a document. Without
`--json`, the command prints the index or Markdown directly. It never starts a
daemon, prompts for setup, reads credentials or accesses the network.

## Output for humans and scripts

Commands print readable tables, labeled details, or action summaries by default. The format does not change when stdout is piped. Add **`--json`** for machine-readable output, either before the command or after its arguments:

```sh
radar tasks
radar tasks --json | jq '.tasks // [] | .[] | {id, title}'
radar --json status | jq '.summary'
radar task done 42 --json
radar workspace-context --json | jq '.desired'
radar repository-refs --repo /path/to/repository --json
```

JSON retains the full result and its existing field names, without colors, table formatting, or progress text. In particular, `tasks` and `status` return the daemon response object; task mutations return the task itself. Human presentation is not a parsing contract.

- Results go to stdout; warnings, errors, authentication messages, and confirmation prompts go to stderr. JSON results retain structured warning fields. Runtime errors with `--json` are JSON objects on stderr; usage errors and help remain text.
- Exit status is `0` for a completed command, `1` for runtime errors, and `2` for invalid arguments. Reconciliation can return an incomplete/retryable plan result: scripts must also inspect `ok`, `retryable`, and `reconfirm_required` rather than treating a returned plan as successful convergence.
- `config-path`, `state-path`, and `log-path` keep their convenient bare-path default; with `--json` they return `{ "path": "..." }`. `version`, `stop`, `restart`, `rate-limit`, and `activity` also accept `--json`. Activity publication remains silent by default.
- `--json` selects output only: it does not approve destructive actions. Cleanup and deletion still read confirmation from stdin; cancellation produces no result. Authentication may still require interaction. Complete `radar setup` before JSON-mode creation; JSON mode never launches the setup wizard.
- The dashboard, interactive `create`, `fork`, `setup` (including `setup notifications`), `update`, and foreground `daemon` do not produce JSON results. Use `create --name <name> --json` for a creation result.

Radar's Pi extension and editor integrations must explicitly pass `--json` when decoding results. The CLI and Pi extension should be updated together.

## Local state

The daemon stores rebuildable task records and source-ref observations locally. Task records provide cache-local numeric IDs, lifecycle projection, source-ref ownership, and acknowledgements. Obsidian notes—not this cache—own authored task content and lifecycle.

Radar groups work by linking mark, source-owned identity, and workspace keys. A primary lifecycle ref controls the projected lifecycle when present. Full refreshes reconcile authoritative remote work back to the note through its source provider: active work reopens it, and all-completed work closes it unless its explicit reopen baseline applies. Without a primary, contributing work-item refs retain their combined lifecycle behavior.

Use `radar reset` to discard collected observations and rebuild them from integrations. Acknowledgements may be retained. An incompatible state version is intentionally discarded and recollected; malformed state still fails closed. The task-muting rename raises the cache version from 7 to 8. Users of the prior ignore version must [migrate notes and the cache explicitly](integrations/obsidian.md#migrating-task-muting) before starting the new daemon; a cache reset is not a substitute for renaming the persisted note preference.

```sh
radar state-path
```

By default this is `$XDG_STATE_HOME/radar/tasks.json` or `~/.local/state/radar/tasks.json`.

Override it with `RADAR_STATE=/path/to/tasks.json`.

Radar writes state atomically through a single serialized writer. A malformed state file prevents startup. An incompatible `stateVersion` is treated as a disposable cache and rebuilt from source integrations.

## Logs

The daemon writes logs to:

```sh
radar log-path
```

By default this is `$XDG_STATE_HOME/radar/radar.log` or `~/.local/state/radar/radar.log`.

Follow logs with:

```sh
tail -f "$(radar log-path)"
```

Override the log path with `RADAR_LOG=/path/to/radar.log`.

Set log level with:

```sh
RADAR_LOG_LEVEL=debug radar daemon
```

Supported levels: `debug`, `info`, `warn`, `error`. Default is `info`.
