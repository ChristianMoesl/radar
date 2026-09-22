# Obsidian task authoring

Obsidian is Radar's required, always-registered task-authoring provider. Every managed workspace has a canonical note; there is no enable/disable setting. A note owns task identity, title, lifecycle, priority, timestamps, and user content. Radar's task state remains a rebuildable projection.

## Configuration

Add the vault to `radar config-path`:

```json
{
  "obsidian": {
    "vault_path": "~/Documents/Obsidian/Work"
  }
}
```

Radar expands `~/`, requires an absolute vault containing `.obsidian/`, and creates `<vault>/Tasks/`. Obsidian Desktop is needed only for the **Open in Obsidian** action. An unconfigured vault is shown as disabled on the dashboard, with setup guidance; configured but invalid vaults are errors. Tasks and workspace creation still require a valid vault. Radar never guesses or creates a vault.

## Task layout

Radar gives every normal task a private directory:

```text
<Vault>/Tasks/Plan authentication--2c965c99/Plan authentication.md
```

The directory name combines the sanitized creation title with the first eight hexadecimal characters of `radar-id`. It stays in place for the lifetime of an attached workspace. Editing `radar-title` changes the task title without moving the note. Renaming the Markdown file changes only its path and Obsidian URL; task title and identity stay unchanged. A task directory must contain exactly one Markdown task note. Attachments may share the directory.

Completed tasks without a workspace use a flat archive:

```text
<Vault>/Tasks/Archived/Plan authentication.md
```

Collection reads private task directories and direct Markdown files in `Tasks/Archived/`. Titles and IDs remain unique across both locations. State comes from frontmatter, not the directory: a completed task can remain in its private directory while its workspace exists. Other note layouts are invalid.

A new note contains only managed frontmatter and a final newline:

```md
---
radar-id: 2c965c99-6a50-446e-834a-72656fbc056a
radar-title: "Plan authentication"
radar-state: open
radar-priority: normal
radar-created-at: 2026-08-25T10:30:00Z
radar-completed-at:
---
```

Radar does not generate headings or body text. The required `radar-title` YAML string is the display title. Radar writes it as a quoted string so colons, quotes, hashes, and other punctuation round-trip safely. Plain, single-quoted, double-quoted, and block-scalar YAML strings are readable; blank or non-string titles are invalid. State is `open` or `done`; priority is `normal` or `urgent`; timestamps use UTC RFC 3339. Unknown frontmatter and the complete body belong to the user and survive Radar mutations. Markdown checkboxes do not control lifecycle.

Task creation preserves the original title (apart from surrounding whitespace) in `radar-title` and sanitizes only its directory and filename. Filesystem-reserved characters, control characters, and Obsidian link characters `[]#^` become hyphens. Leading and trailing spaces and dots are trimmed, Windows device names get an underscore prefix, and names are capped at 200 UTF-8 bytes without splitting characters. Dot-only names become `Untitled`; empty or whitespace-only titles are rejected. The display title is neither sanitized nor truncated. Duplicate display titles are rejected. Filename collisions are also rejected because the flat archive shares one filename namespace. Existing paths are not renamed by this change.

Mutations re-read and validate the note, modify only managed fields, and replace it atomically. Radar never overwrites malformed notes.

## Source refs and lifecycle

A valid note emits one authoritative `obsidian:task:<radar-id>` ref with:

- lifecycle `work_item` and authority `primary`
- canonical and linking key `obsidian:task:<radar-id>`
- preferred title from `radar-title`
- signal `low_priority`, `immediate`, or `done`
- an `obsidian://open` URL for its current note path
- canonical note and task-directory metadata

Without authoritative remote work, the note owns the projected lifecycle. A successful full refresh reopens a completed note when any authoritative contributor confirms active work, even if another contributor cannot be refreshed. It automatically completes an open note when every linked authoritative contributing work item is confirmed done. At least one contributor is required. Informational refs and Git, tmux, Pi, or SBX resources do not decide completion. These informational refs and local resources cannot reopen a done note.

Automatic completion writes `radar-state: done` and `radar-completed-at` before returning a done observation. A failed write or a note edited since collection leaves the cached lifecycle unchanged and reports an Obsidian source error. Missing unresolved remote refs and incomplete collections block automatic completion; previously confirmed terminal refs remain valid.

Radar maintains optional `radar-completion-baseline` bookkeeping in frontmatter. A manual lifecycle change sets it to `pending`. After reopening, the next successful full refresh replaces that marker with a SHA-256 hash of the sorted completed contributor IDs, joined with newlines, without closing the note. When active work is observed, Radar updates the baseline. Completion becomes eligible again when all contributors are done and their completed set differs from the baseline. Automatic completion also saves the baseline, so directly reopening that note preserves the same protection. Neither restart nor cache reset discards it. Manual completion does not override confirmed active remote work; a full refresh reopens the note.

If a local mutation happens during source collection, Radar publishes the freshly reread note but waits for the next full refresh to reconcile lifecycle; a pre-mutation remote snapshot must not undo the user's action.

The baseline is not a configuration option. It is absent from new notes until a lifecycle mutation needs it, so existing notes require no migration and keep their bodies and unrelated frontmatter unchanged.

## Planning workspaces

Pressing `Enter` on an Obsidian-only task reuses its note in a workspace draft. Pressing `Enter` in the draft validates and creates the stable anchor without a confirmation dialog. Repositories are optional. Workspace creation from other sources or from scratch automatically prepares a note, with no note selection control. It persists the canonical note association before provisioning worktrees or the sandbox.

```text
<workspace_root>/plan-authentication/
└── notes.md -> <Vault>/Tasks/Plan authentication--2c965c99/Plan authentication.md
```

`notes.md` is an absolute symlink to the canonical note. Pi, tmux, and nvim start in the workspace directory without an automatic prompt. The same Pi session remains active when Git worktrees are later added as child directories through workspace reconciliation.

When SBX is enabled, Radar mounts the workspace and only the task's private directory. The sandbox can edit `notes.md` without seeing sibling task directories or the rest of the vault. A note rename repairs the symlink during local workspace refresh.

Opening an existing completed workspace does not reopen its task or alter its note. Explicit task reopening changes lifecycle and records the completion baseline.

Cleanup removes the tmux session, sandbox, managed worktrees, `notes.md`, and the empty workspace anchor. Only after successful workspace removal does Radar archive its completed note. Removing a member worktree or closing a session does not archive the note. Incomplete notes stay in their private directories.

## Archiving and reopening

Manual and automatic completion mark the note done immediately. If the workspace registry still references its ID or path, Radar leaves the note in place. Otherwise it moves the note to `Tasks/Archived/<filename>.md` and removes the empty private directory. A shared filesystem lock serializes note mutations, workspace creation, attachment, and anchor removal so an archive move cannot race a new workspace link.

Reopening an archived task restores a private directory using its sanitized current title and stable ID before changing its state to open. The title, ID, body, unknown frontmatter, and file permissions survive relocation. Direct workspace creation or attachment to an archived note is rejected with a request to reopen it first. Radar never mounts the shared archive or `Tasks/` to make an archived note accessible.

Moves use the operating system's atomic no-replace rename on Linux and macOS. Existing destination files, directories, or symlinks are never overwritten. If archiving fails, the completed note remains available at its original path and Radar reports the error. If removal of the former empty directory fails after a successful move, the error reports the new note path instead. There is no recursive deletion or relocation journal.

Notes with accompanying files or detected relative links remain in their private directory with an error rather than separating attachments or rewriting user Markdown. Resolve the reported obstacle, then retry `radar task done <task-id>` after refreshing. Vault-relative wikilinks and absolute URLs do not need rewriting. Radar does not repair arbitrary external symlinks or incoming path-based links; these require explicit handling before relocation.

## Rollout

Mandatory workspace notes do not change the registry, note, or configuration schema. Existing note-less workspaces need a one-time local association before opening them with this version; there is no automatic migration or legacy configuration handling. Inventory the registry, canonical notes, and existing `notes.md` entries before applying those associations. Never overwrite user files or replace existing note identities. Adding private note mounts to existing SBX workspaces requires reconciliation and can interrupt sandbox processes.


`radar-title` is a required frontmatter field. There is no filename fallback and collection does not migrate notes automatically. Before installing this version, migrate **both private and archived notes** with the explicit one-time tool:

```sh
# Read-only preflight: stage candidate notes in a temporary vault and validate
# them with the production reader, including title/identity collisions.
go run ./scripts/migrate-note-titles -vault /absolute/path/to/vault

# Apply only after reviewing the preflight. Use the configured workspace root.
# A new backup directory outside the vault is mandatory.
go run ./scripts/migrate-note-titles \
  -vault /absolute/path/to/vault \
  -workspace-root /absolute/path/to/workspaces \
  -apply -backup /absolute/path/to/new-backup \
  -archive-done
```

The tool inserts only `radar-title`, preserving all existing bytes, paths, and file permissions. It guesses a colon for a word-ending hyphen followed by whitespace (`Setup- screenshots` → `Setup: screenshots`), leaving ticket keys, hyphenated words, arrows, and spaced dash separators alone. Existing title metadata is never replaced. Guesses are approximate; edit the frontmatter if needed. Malformed notes or colliding guesses abort preflight before live writes. Apply backs up every note, checks for concurrent edits, and uses the shared note lock and atomic replacement. Close note editors and pause task creation during rollout, then install and refresh Radar. If the old binary creates more notes before installation, rerun preflight/apply with a new backup directory.

`-archive-done` optionally archives completed private notes **after** title migration using the normal no-overwrite and workspace-reference checks. Referenced notes and unsafe relocations remain in place and are reported. It does not clean up workspaces or rewrite links. The backup preserves notes at their pre-migration/pre-archive paths.

Configuration, workspace-registry, and cache schemas are unchanged. Creation plans now carry `title` when `create` is true; recreate any pending pre-upgrade plans instead of inferring titles from paths. Attachments still use path and linking key only. Cached titles refresh from collection; workspace identities, paths, and links stay stable.

Existing nested notes remain supported as the normal task layout. Collection alone does not move existing completed notes or notes manually marked done in Obsidian. They archive on a subsequent Radar completion operation or workspace cleanup. A manually reopened archived note must still go through `radar task reopen` to restore its private directory before activation.

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
