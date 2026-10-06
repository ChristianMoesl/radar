# Obsidian task authoring

Radar's always-registered Markdown task-authoring provider supports both ordinary directories and Obsidian vaults. Every managed workspace has a canonical note; there is no enable/disable setting. A note owns task identity, title, lifecycle, priority, timestamps, the muted preference, durable source bindings, and user content. Radar's task state remains a rebuildable projection.

## Configuration

Add the vault to `radar config-path`:

```json
{
  "obsidian": {
    "vault_path": "~/Documents/Obsidian/Work"
  }
}
```

Radar expands `~/`, requires an existing absolute directory, and creates `<directory>/Tasks/`. First-run setup can create the selected directory after confirmation. `.obsidian/` is not required or created. For a real Obsidian vault, the open action uses an Obsidian deep link; otherwise it opens the Markdown file with the system handler. An unconfigured notes directory is shown as disabled with setup guidance; a configured but unavailable directory is an error. Task creation, mute/unmute persistence, and workspace creation still require a valid notes directory. The existing `obsidian.vault_path` setting, task identities, and note layout are unchanged.

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

## Muted preference and durable source bindings

Per-task muting is a durable preference to keep tracking the whole aggregated Radar task without requesting your attention. It is independent of `radar-state: open|done` and `radar-priority: normal|urgent`: unfinished work remains visible in the Muted section. Repository/user `mute` filters remain distinct and still hide entire matching tasks from the view and all counts, including Muted and Done. The optional managed fields are:

```yaml
radar-muted: true
radar-source-refs:
  - source: jira
    kind: issue
    id: jira:issue:ABC-123
    work_item: true
  - source: github
    kind: pull_request
    id: github:pr:acme/app:7
    work_item: true
```

`radar-muted` must be a YAML boolean (`true` or `false`), not a quoted string, null, or another lifecycle value. An absent field means false. `radar-source-refs` must be a structured YAML sequence of explicit mappings, or `[]`; an absent field leaves an ordinary note without explicit bindings. Ordinary new notes do not need either field. First mute records the preference and binding intent, including an explicit empty sequence for a note-only task.

**Upgrading from the prior ignore version:** follow the [explicit offline migration commands](#migrating-task-muting) before starting the new daemon. The runtime does not read `radar-ignored`; ordinary notes without that old field need no note edits.

Each entry maps to `protocol.SourceBinding`:

| Field | Meaning |
| --- | --- |
| `source` | Required source-owned integration name. |
| `kind` | Required source-owned ref kind. |
| `id` | Required exact provider-owned identity or locator, never a Radar numeric task ID or task title. |
| `key` | Optional opaque provider-owned identity that distinguishes the concrete resource lifetime when its locator/name/path can be reused. |
| `work_item` | Optional YAML boolean, false when absent; true identifies an authoritative contributing work item that must be resolved before automatic completion. |

String fields must be nonempty exact YAML strings without surrounding whitespace or control characters. `source` and `kind` cannot contain colons. Unsupported or duplicate entry fields, duplicate binding identities, non-sequence values, and YAML aliases in these managed preference fields are invalid. Bindings contain tracking intent only: no remote status snapshots, source payloads, credentials, resource activity, or informational completion blockers. Providers own the meaning and validation of identities and lifetime keys; do not derive them from display names or build them by parsing another provider's IDs.

`radar task mute <task-id>` reuses the unique associated canonical note, including an archived or renamed note. If a source-only task has no note, Radar creates one normal private task note on demand with its meaningful current title, bindings, and muted preference together. This creates no managed workspace, worktree, branch, tmux/Pi session, sandbox, mount, or port. Discovery does not create notes for every source task. A newly adopted completed task stays completed rather than being resurrected by note creation.

Muting an existing note preserves its identity, title, lifecycle, priority, timestamps, completion baseline, body, unknown frontmatter, and permissions. Bindings merge with existing associations rather than replacing them. Repeated no-op requests do not rewrite unchanged notes. Ambiguous authored-note ownership, conflicting bindings, malformed notes, unsafe paths, and title/filename collisions report errors instead of silently selecting a note or overwriting data. If a provider cannot establish a safe concrete resource lifetime, its `SourceRef.BindingError` makes explicit mute fail clearly; the resource remains normally collectable and inspectable.

`radar task unmute <task-id>` clears only the preference. The note and its bindings remain; it does not delete, detach, recreate, reopen, or relocate the note. Unmute of a never-muted source-only task does not create a note unnecessarily. Neither operation mutates Jira, GitHub, Datadog, or other external state. Note writes use the shared lock and atomic writer, and mutation publication is revision-fenced against older in-flight collection.

Source refs expose the preference through typed `Muted` and the sequence through typed `Bindings`, rather than requiring core to interpret Obsidian metadata. The effective group is actual done → Done, otherwise muted → Muted, otherwise current attention. A muted unfinished task remains in Muted and is excluded from active counts and actionable notifications. All contributors done can still complete it under normal lifecycle rules, retaining `radar-muted: true` and the bindings. Reopened work returns to Muted. Unmute of a done task leaves it done. New comments, urgent signals, runtime activity, source errors, and refreshes do not clear the preference.

### Cold collection and completion safety

Collection reads authored notes and bindings before parallel collection of the other providers, including after a cache reset. `BoundSourceResolver` providers resolve bound items that no longer appear in normal active discovery, such as a PR merged while Radar was stopped. The note's UUID and exact provider-owned bindings reconnect the same aggregate across restarts, cache resets, note renames, archival, and workspace cleanup; titles and cache-local numeric task IDs are not durable association keys.

Previously confirmed terminal source facts can be reused without revalidating every historical completed PR. A binding alone never fabricates an active or done observation. Missing unresolved bound work items, unavailable/failed providers, and incomplete lookup results block unsupported automatic completion. The preference remains effective while those failures are shown as source diagnostics. Supporting local resources remain non-contributing: their disappearance does not complete or delete the authored note, or unmute it.

For explicitly adopted notes, `TaskBindingProvider` reconciliation persists newly established authoritative associations, including after unmute. It retains unresolved bindings when collection misses an item and avoids rewriting unchanged notes. Binding persistence does not override the lifecycle or the existing completion-baseline safeguards described below.

## Source refs and lifecycle

A valid note emits one authoritative `obsidian:task:<radar-id>` ref with:

- lifecycle `work_item` and authority `primary`
- canonical and linking key `obsidian:task:<radar-id>`
- preferred title from `radar-title`
- signal `low_priority`, `immediate`, or `done`
- an `obsidian://open` URL in an Obsidian vault, otherwise a `file://` URL for its current note path
- canonical note and task-directory metadata
- typed `Muted` and `Bindings` values from the optional managed preference fields, without changing the actual source signal or lifecycle

Without authoritative remote work, the note owns the projected lifecycle. A successful full refresh reopens a completed note when any authoritative contributor confirms active work, even if another contributor cannot be refreshed. It automatically completes an open note when every linked authoritative contributing work item is confirmed done. At least one contributor is required. Informational refs and Git, tmux, Pi, or SBX resources do not decide completion. These informational refs and local resources cannot reopen a done note.

Automatic completion writes `radar-state: done` and `radar-completed-at` before returning a done observation. A failed write or a note edited since collection leaves the cached lifecycle unchanged and reports an Obsidian source error. Missing unresolved remote refs and incomplete collections block automatic completion; previously confirmed terminal refs remain valid.

Radar maintains optional `radar-completion-baseline` bookkeeping in frontmatter. A manual lifecycle change sets it to `pending`. After reopening, the next successful full refresh replaces that marker with a SHA-256 hash of the sorted completed contributor IDs, joined with newlines, without closing the note. When active work is observed, Radar updates the baseline. Completion becomes eligible again when all contributors are done and their completed set differs from the baseline. Automatic completion also saves the baseline, so directly reopening that note preserves the same protection. Neither restart nor cache reset discards it. Manual completion does not override confirmed active remote work; a full refresh reopens the note.

If a local mutation happens during source collection, Radar publishes the freshly reread note but waits for the next full refresh to reconcile lifecycle; a pre-mutation remote snapshot must not undo the user's action.

The baseline is not a configuration option. It is absent from new notes until a lifecycle mutation needs it, so the baseline itself requires no note migration and leaves bodies and unrelated frontmatter unchanged.

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

## Deleting authored tasks

`radar task delete <task-id>` and the TUI's `D` key use the same daemon preview/apply operation. Confirmation is explicit (`y` only); workspace auto-confirm does not apply. The preview names the authored task, source identity, exact file/directory, and vault trash directory. It does not create trash or move files.

The Obsidian provider revalidates the note identity, content, private-directory entries, and complete preview under the shared workspace note lock immediately before applying it. A changed or malformed note, ambiguous task containing multiple authored notes, invalid registry, or any workspace reference by identity or path blocks deletion. Clean up referencing workspaces first using the existing cleanup flow. Symlinked task roots, note parents, notes, or trash roots are rejected. Accompanying symlinks inside a private task directory move as symlinks; their targets are not touched.

Deletion creates a unique `<vault>/.trash/radar-<unique>/` container and atomically moves the original path inside it using no-replace rename. A private task directory moves as a unit, preserving attachments, relative layout, note bytes, and file permissions. An archived task moves only its Markdown note; other archive contents remain untouched. A failed move leaves the original in place; there is no recursive delete, cross-filesystem copy fallback, or trash-emptying operation. Relative links are not rewritten, and arbitrary incoming links or external symlinks are not repaired.

The daemon returns a `task_deletion_result` with `task_id`, `source_ref_id`, `original_path`, and `trash_path`, plus the updated task list (possibly empty). There need not be a surviving `task` in this response. Only Obsidian is refreshed. Revision fencing prevents an in-flight collection from restoring the deleted observation, and failed collections cannot reuse it as fallback. Linked remote items and local resources stay unchanged and may still project a task. This is authored-task deletion, not persistent dismissal of arbitrary source-backed rows.

Recovery is manual: move the payload at `trash_path` back to `original_path` without replacing anything, then refresh. Restored notes retain their source identity and lifecycle. The cache's numeric task ID is not a durable recovery identity. After restoring an archived note, use the normal reopen operation if it needs an active workspace. Vault trash may be emptied by Obsidian or other software; it is not a backup.

Deletion itself requires no note, workspace-registry, configuration, or cache schema changes. Existing private and archived notes remain in place until explicitly deleted, and `.trash/` is outside collection. No migration or reset is needed. Before installing, check the configured vault's existing `.trash` path: it must be absent or a real directory, not a file or symlink. Existing trash contents are left untouched.

## Rollout

### Migrating task muting

Users of the prior per-task ignore version **must run an explicit migration before starting the new daemon**. The runtime has no legacy command aliases, old-field readers, or automatic note migration. The cache version changes from 7 to 8 for the renamed typed snapshot property; starting with an unmigrated version 7 cache would discard it as incompatible. Resetting the cache does not rename authored note preferences.

Keep the daemon stopped throughout preflight and apply, and close note editors or other processes that could write the affected notes or state. From a repository checkout, use the one-time offline tool with the configured vault and state-file paths:

```sh
# Read-only preflight; dry run is the default.
go run ./scripts/migrate-task-muting \
  --vault /absolute/path/to/vault \
  --state /absolute/path/to/tasks.json

# Apply only after reviewing preflight, with a new absolute backup directory.
go run ./scripts/migrate-task-muting \
  --vault /absolute/path/to/vault \
  --state /absolute/path/to/tasks.json \
  --apply --backup /absolute/path/to/new-task-muting-backup
```

The tool renames `radar-ignored` to `radar-muted` in both private and archived task notes, renames `ignored` to `muted` only in typed task and source-ref snapshots, and updates cache version 7 to 8. It preserves true/false preference values, bindings, lifecycle, identity, timestamps, note bodies, unrelated frontmatter, and other cache data. Ordinary notes without `radar-ignored` need no note edits. This does not change repository/user filter configuration, workspace registrations, task layout, or remote records.

Inventory both note locations and the configured state file during read-only preflight, and resolve malformed fields or conflicting ownership before apply. Do not install over incompatible or ambiguous authored data until its handling is agreed. Apply requires both `--apply` and `--backup` naming a new absolute directory; retain the backup for recovery. Start the new daemon only after successful migration. Validate cold reconstruction with isolated copies, not live task mutations; a later cache reset must preserve authored muting preferences and bindings.

### Existing workspace and title rollouts

Mandatory workspace notes do not change the registry, note, or configuration schema. Existing note-less workspaces need a one-time local association before opening them with this version; there is no automatic migration or legacy configuration handling. Inventory the registry, canonical notes, and existing `notes.md` entries before applying those associations. Never overwrite user files or replace existing note identities. Adding private note mounts to existing SBX workspaces requires reconciliation and can interrupt sandbox processes.


`radar-title` is a required frontmatter field. There is no filename fallback and collection does not migrate notes automatically. If older notes still lack `radar-title`, migrate notes in **both private and archived locations** with the explicit one-time tool before installation:

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

The title change itself leaves configuration, workspace-registry, and cache schemas unchanged; the separate task-muting rename raises the cache version to 8 as described above. Creation plans now carry `title` when `create` is true; recreate any pending pre-upgrade plans instead of inferring titles from paths. Attachments still use path and linking key only. Cached titles refresh from collection; workspace identities, paths, and links stay stable.

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
radar task mute <task-id>
radar task unmute <task-id>
radar task delete <task-id>
radar task priority <task-id> urgent|normal
```

In the TUI, `n` creates a note, `Enter` on a task opens its planning workspace or existing session, `d` changes lifecycle, `m` toggles Mute / Unmute, `D` previews task deletion, `p` changes priority, and `o` opens its source links, including the canonical note in Obsidian. `i` remains read-only Inspect; it shows the stored muted preference alongside actual attention, source statuses, activity, and cleanup warnings.

The overview places Muted and Done after the existing active groups, both collapsed by default. Their nonempty headers show a disclosure marker and eligible-task count. Normal navigation selects a header, and Enter expands/collapses only that section without a daemon mutation. The independent expansion choices survive refreshes, mutations, and temporary section emptiness for the current TUI session only. Active headers remain unselectable. Collapsed children and their refs are absent from navigation and layout; task-specific keys do nothing on headers. Completing/muting selected active work stays among remaining active tasks or on an available header without expanding history; unmute follows unfinished work back to active categories. Done's existing ordering, retention, and unresolved-workspace exception remain unchanged.

Deletion confirmation is modal: Enter does nothing, `y` confirms, and `Esc` or `n` cancels.

## Validation

```sh
go test ./internal/integration/obsidian ./internal/integration/workspace/... ./internal/tui
make test
```
