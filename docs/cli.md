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
radar task title <task-id> <title>
radar task note <task-id> <absolute-note-path>
radar migrate-state [--apply|--resume|--rollback]
radar state-backup <destination.db>
radar state-export
radar status
radar tasks
radar reconcile-workspace --request <json> [--workspace <path>] [--preview]
radar recreate-sandboxes [--workspace <path>] [--preview] [--yes]
radar workspace-context [--workspace <path>] [--registration-only | --resources-only]
radar repository-refs --repo <repo>
radar clone-repository --request <json> [--workspace <path>] [--preview | --plan <id>]
radar open-resource --request <json> [--workspace <path>] [--preview | --plan <id>]
radar agent add <module> --id <id> --workspace <registered-path> [--config <json-file>] [--collection github]
radar agent list
radar agent inspect|start|stop|restart|remove|logs|token <id>
radar agent schedule set <id> <name> --time HH:MM --weekdays mon,tue,wed,thu,fri --timezone <IANA-zone> --payload <json-file>
radar agent schedule pause|resume|remove <id> <name>
radar agent emit <id> --source <source> --event-id <id> # JSON payload on stdin
radar agent message <id> <text> --revision <revision> [--event-id <id>]
radar agent action <id> <action-id> [text] --revision <revision> [--event-id <id>]
radar agent conversation <id> <conversation-id>
radar agent serve [--listen 127.0.0.1:7433]
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

See the [durable-agent guide](durable-agents.md) for installation, required workspace association, SBX execution, event receipts, retention and examples. Agent registrations and data survive `radar reset`. A stopped registration still pins its workspace; `agent remove` releases the association and retains durable data.

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

## Approval-gated host resources

The two host operations require a registered workspace and strict JSON requests:

```sh
radar clone-repository --workspace /work/task --request '{"repository":"https://github.com/acme/app.git","destination":"/sources/app"}' --preview --json
radar open-resource --workspace /work/task --request '{"resource_ids":["github:open:pr:acme/app:123:changes"]}' --preview --json
```

Select a destination under `workspace-context.repository_dirs` and resource IDs from its `openable_resources`. Preview has no clone/browser effects. Without `--preview`, readable CLI mode displays the plan and asks for explicit approval. JSON apply is the trusted Pi adapter transport: after obtaining the required consent, it resubmits the same request with `--plan <accepted-plan-id> --json`. A plan ID detects drift; it is not proof of consent. `--json` or `workspace.auto_confirm` alone never approves either operation.

Initial clones always require consent; verified read-only reuse does not. Every opening batch requires one fresh confirmation showing all URLs. Unknown IDs, changed plans and unsafe destinations fail closed. Do not bypass missing targets or consent through shell/browser fallbacks. Opening uses the host default browser and returns launch status, not page content or proof of page load. Browser traffic is outside SBX policy.

Clone failure can leave a reported `partial_path`. Opening can succeed for some targets and fail for others. Scripts must inspect `ok`, clone `status`/`error` and per-target opening `results`; a returned structured partial/cancelled result is not success. There is no automatic destructive cleanup, rollback, or whole-batch retry.

See [Git cloning](integrations/git.md#host-source-cloning) and [GitHub opening](integrations/github.md#provider-owned-browser-opening).

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

SQLite at `$XDG_STATE_HOME/radar/state.db` (or `~/.local/state/radar/state.db`) owns durable task identity, preferences, lifecycle decisions, note associations, bindings, acknowledgements and review safeguards. Notes contain only content. Override the database path with `RADAR_STATE=/absolute/path/state.db`.

`radar reset` clears collected observations and derived views while preserving durable records. Unknown schema versions and corrupt data fail closed instead of being discarded. Runtime does not read old JSON/frontmatter state.

```sh
radar state-path
radar state-backup /absolute/path/backup.db
radar state-export > radar-state.json
```

Back up notes and the database together. Existing installations [migrate automatically on first startup](sqlite-state-migration.md); explicit migration commands provide read-only preflight and offline recovery. `radar task title` changes display metadata without changing note bytes; `radar task note` explicitly repairs an external note move after workspace cleanup.

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

`workspace-context --resources-only` is the fresh lifecycle projection used by Pi. It returns member instructions/skills and owned sandbox/shared-directory routing without ports, dirty status, repository discovery, opening resources or a reconciliation revision. Use full `workspace-context` before reconciliation.

Completed cleanup and reconciliation responses can contain `resource_refresh_pending: true`. Removal/provisioning has completed; the cached task/resource list is explicitly pending the next local collection. Partial cleanup failures include their successful removal prefix. The dashboard labels pending lists. A complete collection started after the mutation clears the marker; failed or canceled collection keeps it pending.
