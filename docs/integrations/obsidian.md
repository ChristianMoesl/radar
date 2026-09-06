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

Radar gives every normal task a private directory:

```text
<Vault>/Tasks/Plan authentication--2c965c99/Plan authentication.md
```

The directory name combines the creation title with the first eight hexadecimal characters of `radar-id`. It stays in place for the lifetime of an attached workspace. Renaming the Markdown file changes the task title but leaves task and workspace identity unchanged. A task directory must contain exactly one Markdown task note. Attachments may share the directory.

Completed tasks without a workspace use a flat archive:

```text
<Vault>/Tasks/Archived/Plan authentication.md
```

Collection reads private task directories and direct Markdown files in `Tasks/Archived/`. Titles and IDs remain unique across both locations. State comes from frontmatter, not the directory: a completed task can remain in its private directory while its workspace exists. Other note layouts are invalid.

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

Task creation sanitizes the title before using it for the directory and filename. Filesystem-reserved characters, control characters, and Obsidian link characters `[]#^` become hyphens. Leading and trailing spaces and dots are trimmed, Windows device names get an underscore prefix, and names are capped at 200 UTF-8 bytes without splitting characters. Dot-only names become `Untitled`; empty or whitespace-only titles are rejected. The sanitized filename becomes the task title. Duplicate titles are rejected after sanitization, so creation never overwrites another task. Existing notes are not renamed and remain readable and editable.

Mutations re-read and validate the note, modify only managed fields, and replace it atomically. Radar never overwrites malformed notes.

## Source refs and lifecycle

A valid note emits one authoritative `obsidian:task:<radar-id>` ref with:

- lifecycle `work_item` and authority `primary`
- canonical and linking key `obsidian:task:<radar-id>`
- preferred title from the current filename
- signal `low_priority`, `immediate`, or `done`
- an `obsidian://open` URL for its current note path
- canonical note and task-directory metadata

The note owns the projected lifecycle. A successful full refresh automatically completes an open note when every linked authoritative contributing work item is confirmed done. At least one contributor is required. Informational refs and Git, tmux, Pi, or SBX resources do not decide completion. They can promote an open task's attention, but cannot reopen a done note.

Automatic completion writes `radar-state: done` and `radar-completed-at` before returning a done observation. A failed write or a note edited since collection leaves the cached task open and reports an Obsidian source error. Missing unresolved remote refs and incomplete collections block automatic completion; previously confirmed terminal refs remain valid.

Radar maintains optional `radar-completion-baseline` bookkeeping in frontmatter. A manual lifecycle change sets it to `pending`. After reopening, the next successful full refresh replaces that marker with a SHA-256 hash of the sorted completed contributor IDs, joined with newlines, without closing the note. When active work is observed, Radar updates the baseline. Completion becomes eligible again when all contributors are done and their completed set differs from the baseline. Automatic completion also saves the baseline, so directly reopening that note preserves the same protection. Neither restart nor cache reset discards it. Manual completion remains terminal.

The baseline is not a configuration option. It is absent from new notes until a lifecycle mutation needs it, so existing notes require no migration and keep their bodies and unrelated frontmatter unchanged.

## Planning workspaces

Pressing `Enter` on an Obsidian-only task creates or reopens a stable Radar workspace immediately. Repository selection is not part of this step.

```text
<workspace_root>/plan-authentication/
└── notes.md -> <Vault>/Tasks/Plan authentication--2c965c99/Plan authentication.md
```

`notes.md` is an absolute symlink to the canonical note. Pi, tmux, and nvim start in the workspace directory without an automatic prompt. The same Pi session remains active when Git worktrees are later added as child directories through workspace reconciliation.

When SBX is enabled, Radar mounts the workspace and only the task's private directory. The sandbox can edit `notes.md` without seeing sibling task directories or the rest of the vault. A note rename repairs the symlink during local workspace refresh.

Cleanup removes the tmux session, sandbox, managed worktrees, `notes.md`, and the empty workspace anchor. Only after successful workspace removal does Radar archive its completed note. Removing a member worktree or closing a session does not archive the note. Incomplete notes stay in their private directories.

## Archiving and reopening

Manual and automatic completion mark the note done immediately. If the workspace registry still references its ID or path, Radar leaves the note in place. Otherwise it moves the note to `Tasks/Archived/<filename>.md` and removes the empty private directory. A shared filesystem lock serializes note mutations, workspace creation, attachment, and anchor removal so an archive move cannot race a new workspace link.

Reopening an archived task restores a private directory using its current title and stable ID before changing its state to open. The title, ID, body, unknown frontmatter, and file permissions survive relocation. Direct workspace creation or attachment to an archived note is rejected with a request to reopen it first. Radar never mounts the shared archive or `Tasks/` to make an archived note accessible.

Moves use the operating system's atomic no-replace rename on Linux and macOS. Existing destination files, directories, or symlinks are never overwritten. If archiving fails, the completed note remains available at its original path and Radar reports the error. If removal of the former empty directory fails after a successful move, the error reports the new note path instead. There is no recursive deletion or relocation journal.

Notes with accompanying files or detected relative links remain in their private directory with an error rather than separating attachments or rewriting user Markdown. Resolve the reported obstacle, then retry `radar task done <task-id>` after refreshing. Vault-relative wikilinks and absolute URLs do not need rewriting. Radar does not repair arbitrary external symlinks or incoming path-based links; these require explicit handling before relocation.

## Rollout

No frontmatter, configuration, workspace-registry, or cache schema changes are required. Existing nested notes remain supported as the normal task layout. Collection alone does not move existing completed notes or notes manually marked done in Obsidian. They archive on a subsequent Radar completion operation or workspace cleanup. A manually reopened archived note must still go through `radar task reopen` to restore its private directory before activation.

Before installation or bulk archival, inventory the configured `Tasks/` tree and workspace registry, check live `notes.md` links, and inspect destination collisions, accompanying files, and path-based links. Do not move referenced notes. Existing completed notes can be archived explicitly with `radar task done` after reviewing those checks. There is no automatic bulk migration. Cache paths refresh from collection; workspace paths are never retargeted for archiving.

## Collection failures

Source status is:

- `ok` when every discovered task directory and note is valid
- `partial` when a task directory is malformed, duplicated, unreadable, or missing its note
- `error` when configuration, the vault, or `Tasks/` cannot be used, or automatic lifecycle persistence fails

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
go test ./internal/integration/obsidian ./internal/integration/workspace/... ./internal/tui
make test
```
