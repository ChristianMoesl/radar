# Markdown and Obsidian task notes

Radar's always-registered note provider supports ordinary directories and Obsidian vaults. Every managed workspace has a canonical Markdown note. **The entire note belongs to the user and agents. Task state and note associations live in SQLite; there is no required Radar frontmatter or in-note ID.**

## Configuration

```yaml
obsidian:
  vault_path: ~/Documents/Obsidian/Work
```

The configured directory must exist. Radar creates `Tasks/` beneath it. `.obsidian/` is optional: a real vault uses Obsidian deep links, while an ordinary directory opens its Markdown file through the system handler. An unavailable directory reports an error. Creating tasks and workspaces still requires an accessible note directory.

Existing installations [migrate automatically on first daemon startup](../sqlite-state-migration.md). The one-time importer backs up old frontmatter and version 8 JSON, imports their state, then removes all managed note fields, including `radar-id`. Ordinary collection reads SQLite exclusively. Explicit commands remain available for preflight and recovery.

## Task layout and content

```text
<Vault>/Tasks/Plan authentication--2c965c99/Plan authentication.md
<Vault>/Tasks/Archived/Plan authentication.md
```

A normal task has a private directory with exactly one Markdown note and optional attachments. The directory combines the sanitized creation title with a short database-owned UUID. It stays stable while a workspace is attached. The display title is stored in SQLite, preserving punctuation and Unicode; the filename is sanitized for filesystems and Obsidian links. Duplicate display titles in a vault and conflicting filenames are rejected.

A new note is an empty file. Radar generates no headings, body text or frontmatter. Any user YAML, Markdown, checkboxes and comments are content, with no lifecycle effect. Lifecycle, priority, mute, binding and review mutations leave file bytes and permissions unchanged.

```sh
radar task create --title "Plan authentication"
radar task title 7 "Updated title"
radar task priority 7 urgent
radar task done 7
radar task reopen 7
```

Changing a title does not relocate its note. Renaming the sole Markdown file within its registered private directory updates the stored association and workspace symlink. Moving the directory externally or renaming a flat archived note needs explicit reassociation:

```sh
radar task note 7 /absolute/path/to/Tasks/Plan--12345678/Renamed.md
```

The old path must be missing, the new path must be in the managed layout, and the private directory keeps its stable UUID suffix. Clean up referencing workspaces before reassociation. Unregistered copies do not create tasks. Missing or ambiguous files report diagnostics while retaining task identity and state in SQLite, including after reset. Radar never guesses identity from content or titles.

## Muting and durable source bindings

```sh
radar task mute 7
radar task unmute 7
```

Muting means keep tracking the entire linked task without requesting attention. Unfinished work moves into Muted and is excluded from active counts and actionable notifications. It does not change Jira, GitHub or other remote lifecycle, and does not authorize workspace cleanup. Completion retains the preference; reopened work returns to Muted. Unmuting does not reopen completed work.

First mute adopts a source-only task by creating its durable application record and ordinary note, without a workspace, worktree, branch, session or sandbox. Discovery does not create notes for every source task. Binding/preference changes are transactional and do not archive or restore files. Unmuting a never-muted source-only task is a no-op.

Bindings store provider-owned `Source`, `Kind`, `ID`, optional concrete lifetime `Key`, and `WorkItem`. SQLite enforces one owner for each exact binding identity. Git and sandbox lifetime keys prevent replacements at reused paths/names from inheriting preferences. Unprovable lifetimes make explicit binding/muting fail visibly. Informational refs do not become authoritative contributors.

Authoring collection reads durable bindings before other sources. Bound-source resolvers recover tracked work that left normal discovery, including remotely completed PRs after a restart/reset. Bindings express tracking intent rather than remote status. Missing unresolved work, unavailable providers and incomplete lookup results block unsupported automatic completion. Confirmed cached terminal facts remain reusable until a deliberate cache reset.

## Lifecycle and agent review

The provider emits a primary authored ref with a database UUID, preferred display title, note URL/location, lifecycle, priority and typed preferences/bindings. Remote providers remain authoritative for their own facts. With no remote contributors, the authored database record controls lifecycle. On full reconciliation, confirmed active contributing work reopens it; every contributor confirmed done can complete it. Informational refs and the existence of worktrees/sessions/sandboxes do not decide completion.

Completion is committed before returning a done observation. Reconciliation compares the collected note content and database revision; concurrent content edits or state changes reject stale evidence. Failures report source diagnostics. An explicit reopening records a completion baseline, so unchanged historical remote completion cannot immediately close the task again. Active or newly completed work arms a later completion cycle. This safeguard survives restart/reset.

Busy/waiting agents prevent completion. A completion collision or resumed agent activity in a completed task persists an agent-result review hold and known bindings transactionally. Once agents settle, Attention shows **Agent finished — review result**, subject to mute/urgency policy. Only explicit Done clears the hold after checking live activity. Opening a workspace, acknowledgement, priority changes and reopening do not clear it. Failed hold writes remain pending and block automatic cleanup.

## Workspaces, archive and trash

`notes.md` in a workspace is an absolute symlink to the canonical file. The workspace mounts only its private task directory in SBX, preserving sibling-task isolation. Pi, tmux and editors start in the workspace. Opening an already-completed workspace does not reopen its task.

After successful workspace cleanup, completed notes can move flat into `Tasks/Archived/`; reopening restores a private directory before activation. A referenced note stays in place. Notes with accompanying files or relative links stay in their private directory with a diagnostic rather than losing context. No shared archive is mounted into a sandbox.

Explicit task deletion requires a fresh preview and `y` confirmation. Referencing workspaces block it. The provider moves the private directory intact, or only the selected archived note, into a unique recoverable vault trash container; it never recursively deletes note content. File collisions, symlinks in managed roots, changed previews and unsafe paths fail visibly. Archive/restore/trash moves use no-overwrite renames and a SQLite recovery journal under the shared note lock.

A trashed task remains a durable record and stays out of ordinary collection. Moving its original content back to its original path restores the same identity. Database backups and note backups must be kept together; restoring notes alone no longer reconstructs application state.

See [SQLite migration, recovery, backup and export](../sqlite-state-migration.md) for the rollout contract.
