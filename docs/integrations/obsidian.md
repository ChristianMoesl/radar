# Obsidian task authoring

Obsidian is Radar's only task-authoring provider. Each authored task is one Markdown note in one configured Obsidian vault. Radar state is only a rebuildable projection/cache; the note owns identity, lifecycle, priority, timestamps, working notes, and outcome links, while its filename owns the title.

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

Obsidian Desktop is required only for **Open in Obsidian**. Collection and mutations use the filesystem directly.

## Capabilities

The integration implements:

- `Source`
- `StatusReporter`
- `LocalSource`
- `ActionProvider`
- `TaskAuthoringProvider`

It scans direct Markdown children of `<vault>/Tasks/` on every local refresh. It does not scan nested directories or the rest of the vault.

## Task layout and schema

Each task is one file named after its title:

```text
<Vault>/Tasks/Refine the authentication epic.md
```

The note starts with:

```md
---
radar-id: <UUID>
radar-state: open
radar-priority: normal
radar-created-at: 2025-08-04T10:30:00Z
radar-completed-at:
---
```

The body contains Intent, Desired outcome, Context, Working notes, and Outcome sections. The filename without `.md` is the task title, so renaming the note renames the task. `radar-id` is immutable. State is exactly `open` or `done`; priority is exactly `normal` or `urgent`; timestamps are UTC RFC 3339. Unknown frontmatter and the entire Markdown body are user-owned and survive Radar mutations. Checkboxes do not control lifecycle.

Task titles must be valid single-file names and unique within `Tasks/`. Radar rejects titles containing path separators and never overwrites an existing note.

Mutations re-read and validate the note, change only managed fields, sync a temporary file beside the note, and atomically rename it over the original. Radar never overwrites a malformed note and never deletes or archives task notes.

## Source refs

A valid note emits one authoritative ref:

- `obsidian:task:<radar-id>`
  - entity ID `obsidian:task:<radar-id>`
  - kind `task`
  - lifecycle `work_item`
  - lifecycle authority `primary`
  - canonical and linking identity `obsidian:task:<radar-id>`
  - preferred title from the note filename
  - signal `low_priority`, `immediate`, or `done`
  - URL `obsidian://open?...` for the current vault-relative note path

An Obsidian note is a task record, not a workspace. It does not provide a filesystem workspace or workspace path. Its title is the default workspace name, so activating an Obsidian-only task can start Radar's repository and branch selection flow. The resulting workspace group stores the note's stable task linking key locally, and Git emits that key to associate the worktree with the task. Git worktrees remain Radar's engineering workspaces, while tmux sessions and SBX sandboxes remain resources associated with their own workspace paths.

Obsidian's primary lifecycle is terminal for its authored task. Supporting Jira/GitHub completion cannot complete an open note, and supporting activity cannot reopen a done note. While the note is open, linked source signals can still promote it to `in_progress` or `attention`; urgent note priority promotes it to `immediate`.

## Collection and failures

Notes are deduplicated by `radar-id` and emitted in stable-ID order. Renaming a note keeps identity while updating its title, note metadata, and URL. Deleting a note removes its ref on the next complete local refresh, so a task without another authoritative ref disappears. Done notes remain collected and use `radar-completed-at` for Radar's normal three-day done display.

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

Because the note is not a workspace, `Enter` does not create a note-specific Pi session. It continues to switch to or create sessions for actual linked tmux and Git workspace resources when those are present.

## Validation

Run the integration and full suites with:

```sh
go test ./internal/integration/obsidian ./internal/state ./internal/server ./internal/tui
make test
```
