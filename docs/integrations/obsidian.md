# Obsidian task authoring

Obsidian is Radar's only task-authoring provider. Each authored task is a Markdown note and dedicated working directory in one configured Obsidian vault. Radar state is only a rebuildable projection/cache; the note owns identity, title, lifecycle, priority, timestamps, working notes, and outcome links.

## Configuration

Add the vault to `radar config-path`:

```json
{
  "obsidian": {
    "vault_path": "~/Documents/Obsidian/Work"
  }
}
```

The path is required before authoring or collection can run. Radar expands a leading `~/`, requires the result to be absolute, requires the vault and its `.obsidian/` directory to exist, and creates the fixed task root `Tasks/`.

Obsidian Desktop is required only for **Open in Obsidian**. Collection, mutations, and agent sessions use the filesystem directly.

## Capabilities

The integration implements:

- `Source`
- `StatusReporter`
- `LocalSource`
- `ActionProvider`
- `TaskAuthoringProvider`

It scans exactly one directory level under `<vault>/Tasks/` for `task.md` every local refresh. It does not scan the rest of the vault or task artifact contents.

## Task layout and schema

A created task has this layout:

```text
<Vault>/Tasks/<title-slug>--<short-id>/
├── task.md
└── artifacts/
```

`task.md` starts with:

```md
---
radar-id: <UUID>
radar-title: Refine the authentication epic
radar-state: open
radar-priority: normal
radar-created-at: 2025-08-04T10:30:00Z
radar-completed-at:
---
```

The body contains Intent, Desired outcome, Context, Working notes, and Outcome sections. `radar-id` is immutable. State is exactly `open` or `done`; priority is exactly `normal` or `urgent`; timestamps are UTC RFC 3339. Unknown frontmatter and the entire Markdown body are user-owned and survive Radar mutations. Checkboxes do not control lifecycle.

Mutations re-read and validate the note, change only managed fields, sync a temporary file in the same directory, and atomically rename it over `task.md`. Radar never overwrites a malformed note and never deletes or archives task directories.

## Source refs

A valid note emits two authoritative refs sharing `EntityID = obsidian:task:<radar-id>`:

- `obsidian:task:<radar-id>`
  - kind `task`
  - lifecycle `work_item`
  - lifecycle authority `primary`
  - canonical and linking identity `obsidian:task:<radar-id>`
  - preferred title from `radar-title`
  - signal `low_priority`, `immediate`, or `done`
  - URL `obsidian://open?...` for the current vault-relative note path
- `obsidian:workspace:<radar-id>`
  - kind `task_workspace`
  - lifecycle `workspace`
  - lifecycle authority `none`
  - path set to the task directory
  - linking keys for the task identity and `workspace:<absolute-directory>`
  - signal `none`

The workspace ref does not make an idle task active. It lets tmux and SBX refs join by path.

Obsidian's primary lifecycle is terminal for its authored task. Supporting Jira/GitHub completion cannot complete an open note, and supporting activity cannot reopen a done note. While the note is open, live source signals can still promote it to `in_progress` or `attention`; urgent note priority promotes it to `immediate`.

## Collection and failures

Notes are deduplicated by `radar-id` and emitted in stable-ID order. Moving or renaming a task directory keeps identity. Deleting a note removes its refs on the next complete local refresh, so a task without another authoritative ref disappears. Done notes remain collected and use `radar-completed-at` for Radar's normal three-day done display.

Source status is:

- `ok`: vault/root readable and all discovered notes valid
- `partial`: one or more notes malformed, duplicated, or unreadable; valid notes are collected and previous observations for known failed notes are preserved
- `error`: configuration, vault, or task root cannot be used safely

Status details include paths and validation reasons, never note bodies.

## Commands and TUI

```sh
radar task create --title <title>
radar task done <task-id>
radar task reopen <task-id>
radar task priority <task-id> urgent|normal
```

The daemon validates the protocol, delegates to the sole authoring provider, performs an immediate local refresh, and returns the projected task. These user mutations do not run actionable-transition notifications.

In the TUI, `n` creates a note, `d` toggles open/done, `p` toggles normal/urgent, and `o` offers **Open in Obsidian**. Lifecycle and priority keys reject tasks not owned by the authoring provider.

## Agent workspace

`Enter` switches to an already linked tmux session or creates a deterministic one in the task directory. Pi receives a deterministic session ID based on the Obsidian UUID, `RADAR_TASK_ID`, `RADAR_TASK_NOTE`, and an initial instruction to read `task.md` and maintain Working notes and Outcome links. Existing model, thinking, tmux layout, SBX kit, and additional-mount settings apply. When SBX is enabled, the task directory is the primary workspace mount.

Workspace garbage collection only removes Git worktrees; it never removes an Obsidian task directory or its artifacts.

## Validation

Run the integration and full suites with:

```sh
go test ./internal/integration/obsidian ./internal/state ./internal/server ./internal/tui
make test
```
