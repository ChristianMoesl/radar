# Obsidian task authoring

Obsidian is Radar's task-authoring provider. A note owns task identity, title, lifecycle, priority, timestamps, and user content. Radar's task state remains a rebuildable projection.

## Configuration

Add the vault to `radar config-path`:

```json
{
  "obsidian": {
    "vault_path": "~/Documents/Obsidian/Work"
  }
}
```

Radar expands `~/`, requires an absolute vault containing `.obsidian/`, and creates `<vault>/Tasks/`. Obsidian Desktop is needed only for the **Open in Obsidian** action.

## Task layout

Radar gives every task a private stable directory:

```text
<Vault>/Tasks/Plan authentication--2c965c99/Plan authentication.md
```

The directory name combines the creation title with the first eight hexadecimal characters of `radar-id`. It never changes. Renaming the Markdown file changes the task title but leaves task and workspace identity unchanged. A task directory must contain exactly one Markdown task note. Attachments may share the directory.

Collection scans one directory level below `Tasks/`. Titles remain unique across all task directories. Moving a note out of its stable directory is invalid.

A new note contains only managed frontmatter and a final newline:

```md
---
radar-id: 2c965c99-6a50-446e-834a-72656fbc056a
radar-state: open
radar-priority: normal
radar-created-at: 2026-08-25T10:30:00Z
radar-completed-at:
---
```

Radar does not generate headings or body text. The filename without `.md` is the title. State is `open` or `done`; priority is `normal` or `urgent`; timestamps use UTC RFC 3339. Unknown frontmatter and the complete body belong to the user and survive Radar mutations. Markdown checkboxes do not control lifecycle.

Task creation rejects multiline titles, path separators, and duplicate titles. Mutations re-read and validate the note, modify only managed fields, and replace it atomically. Radar never overwrites malformed notes.

## Source refs and lifecycle

A valid note emits one authoritative `obsidian:task:<radar-id>` ref with:

- lifecycle `work_item` and authority `primary`
- canonical and linking key `obsidian:task:<radar-id>`
- preferred title from the current filename
- signal `low_priority`, `immediate`, or `done`
- an `obsidian://open` URL for the nested note path
- canonical note and task-directory metadata

The note owns task completion. Supporting GitHub, Git, tmux, Pi, or SBX activity can promote an open task's attention, but cannot complete it or reopen a done note.

## Planning workspaces

Pressing `Enter` on an Obsidian-only task creates or reopens a stable Radar workspace immediately. Repository selection is not part of this step.

```text
<workspace_root>/plan-authentication/
└── note.md -> <Vault>/Tasks/Plan authentication--2c965c99/Plan authentication.md
```

`note.md` is an absolute symlink to the canonical note. Pi, tmux, and nvim start in the workspace directory without an automatic prompt. The same Pi session remains active when Git worktrees are later added as child directories through workspace reconciliation.

When SBX is enabled, Radar mounts the workspace and only the task's private directory. The sandbox can edit `note.md` without seeing sibling task directories or the rest of the vault. A note rename repairs the symlink during local workspace refresh.

Completing or cleaning the workspace never deletes the canonical task directory or note. Cleanup removes the tmux session, sandbox, managed worktrees, `note.md`, and the empty workspace anchor.

## Collection failures

Source status is:

- `ok` when every discovered task directory and note is valid
- `partial` when a task directory is malformed, duplicated, unreadable, or missing its note
- `error` when configuration, the vault, or `Tasks/` cannot be used

Valid tasks remain available during partial collection. Radar preserves previous observations for known failed notes. Status details include paths and validation reasons, never note contents.

## Commands and TUI

```sh
radar task create --title <title>
radar task done <task-id>
radar task reopen <task-id>
radar task priority <task-id> urgent|normal
```

In the TUI, `n` creates a note, `Enter` opens its planning workspace, `d` changes lifecycle, `p` changes priority, and `o` opens the canonical note in Obsidian.

## Validation

```sh
go test ./internal/integration/obsidian ./internal/workspacegroup ./internal/workspace ./internal/tui
make test
```
