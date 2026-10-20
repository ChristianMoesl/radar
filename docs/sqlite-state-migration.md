# Migrating task state to SQLite

Tracking epic: [#43](https://github.com/ChristianMoesl/radar/issues/43), with storage #44, note authority #45, importer #46 and qualification #47.

Radar now keeps task authority in a local SQLite database. Notes contain only user/agent content: there is no required frontmatter, including no `radar-id`. This is an explicit one-time cutover, not an automatic startup migration or a runtime compatibility reader.

## Ownership and formats

| Existing data | Handling |
| --- | --- |
| Private and archived task frontmatter | Import identity, title, lifecycle, priority, timestamps, mute/review preferences, bindings and completion safeguards; remove known Radar fields |
| Version 8 `tasks.json` | Import task/numeric IDs, acknowledgements, pending review intent, source ownership/history and collected observations; retain the original file |
| Safe cache-only associations | Promote to durable bindings for their existing authored task |
| Unprovable resource lifetimes | Retain observations; report them without inventing durable selectors |
| Unknown frontmatter, note bodies, permissions, attachments and paths | Preserve |
| Workspace registry, note symlinks, agent registrations/stores, configuration and credentials | Preserve their existing formats; validate workspace note associations |
| Malformed, ambiguous, symlinked or unsupported inputs | Reject before modifying notes |

The default database is `$XDG_STATE_HOME/radar/state.db`, falling back to `~/.local/state/radar/state.db`. `RADAR_STATE` selects a database path. Change an old override that points at `tasks.json` to the adjacent `state.db` before running the migration. The importer reads the adjacent `tasks.json`; moving the old cache elsewhere first would omit its cache-only decisions.

Only version 8 JSON is accepted. An older ignore-era rollout must first be completed with its matching old tool/binary on a backup. The new runtime has no legacy aliases. A missing JSON cache is allowed; frontmatter state is preserved, but absent acknowledgement/history data cannot be recovered from notes.

## Plan and execution order

1. Inventory configured note directories, private/archive notes, JSON state and workspace note references without opening or creating a database.
2. Validate note identity/title/binding ownership, cache identities and versions, regular-file paths and workspace references. Establish the exact import and byte-preserving removal plan.
3. Stop the daemon and prevent migration/daemon overlap with an exclusive state lock. Keep editors and external agents from changing these notes during the cutover.
4. Write an owner-only, fsynced backup manifest containing complete original note/cache/registry bytes and permissions.
5. Transactionally import task authority, bindings, projection records and a recovery journal into SQLite. Notes are still unchanged at this point.
6. Atomically replace each note with its planned content-only bytes. Preserve unknown YAML, comments outside removed fields, line endings, bodies, permissions and paths.
7. Mark the cutover complete only after all replacements succeed. Keep the original JSON and backup manifest; the runtime reads SQLite exclusively.
8. Start the new daemon, inspect tasks and source diagnostics, exercise a mutation, then verify restart/reset behavior. Update the Pi extension too: its note-editing guidance now describes content-only notes.

Build the new binary without installing it over the active one, then use that binary for preflight and cutover:

```sh
make build-radar
./radar migrate-state
radar stop
./radar migrate-state --apply
```

The first command defaults to a read-only preflight. `--json` is supported for inspection. `--apply` requires the daemon to be stopped. Only install/start the incompatible binary after the chosen data handling has completed. Keep the previous binary available for rollback.

## Interrupted migration and rollback

```sh
./radar migrate-state --resume
```

Resume uses the saved plan, including a backup written before the import committed. Every input must match its exact original or planned bytes and permissions. It refuses to overwrite a concurrent edit. Restore/reconcile that file first; it never guesses which content to keep. Startup refuses a pending cutover. Do not start an older daemon against partially stripped notes.

Before accepting new application state, an offline rollback is available:

```sh
./radar migrate-state --rollback
```

It validates that imported task authority and projection data have not changed, restores original note bytes/modes and removes the new database. The original JSON and backup remain. A rollback after task changes would lose data, so it is rejected; restore a coordinated database-and-notes backup instead. Retain the backup after either direction. A rolled-back attempt requires a fresh backup location before repeating the import.

## Runtime storage and reset

`authored_tasks` stores stable task identity, presentation, lifecycle, preferences, note locations and completion safeguards. `bindings` enforces one owner for each exact provider identity. Database revisions reject stale writes. `task_records` retains numeric identity, acknowledgements and pending review decisions; `observed_refs` holds collected source observations. `documents` stores projection metadata and migration bookkeeping. `file_moves` journals archive/restore/trash moves so startup can recover an interrupted rename under the shared note lock.

`radar reset` clears source observations/statuses and derived snapshots while retaining authored tasks, bindings, IDs, acknowledgements and pending review intent. Unknown durable database or projection versions and corruption fail closed. They are never silently discarded. Existing agent SQLite stores and workspace registries retain their independent lifecycles.

The embedded SQLite driver runs without CGO, retaining Linux amd64/arm64 and macOS arm64 builds. Transactions use SQLite's local locking and full synchronous durability; the database belongs on local storage outside the synced vault.

## Backup and export

```sh
radar state-path
radar state-backup /absolute/path/to/radar-backup.db
radar state-export > radar-state.json
```

Backup uses SQLite's consistent snapshot mechanism rather than copying an open database. Export is inspectable JSON; it is not a second runtime persistence format or a replacement for database backups. Back up note content and workspace/agent records alongside the database. For a coordinated restore, stop Radar and restore the matching files together; notes alone no longer reconstruct task state.

## Note identity and moves

The database associates a task UUID with its note location. New notes are empty Markdown files. Their private directories retain the existing sanitized-title and short-UUID layout so sandbox isolation and workspace symlinks remain stable. Renaming the sole Markdown file within its registered private directory preserves identity; arbitrary copied notes do not create tasks.

```sh
radar task title 7 "Updated display title"
radar task note 7 /absolute/path/to/Tasks/Plan--12345678/Renamed.md
```

Changing the title does not rewrite or relocate content. An external directory move or archived-file rename requires explicit note reassociation. The old path must be missing, the new path must fit the managed layout, and a private directory retains its stable UUID suffix. Referencing workspaces must be cleaned up first. Missing notes remain durable task records with diagnostics, including after reset; Radar does not recover identity by matching titles or content.

## Qualification and real-data rollout

Automated qualification covers importing realistic frontmatter/cache fixtures, byte preservation, cold runtime collection, restart/reset, acknowledgements/review holds, transaction rollback, binding uniqueness, stale revisions, backup and interrupted import/file-move recovery. Existing task/lifecycle/mute/workspace tests continue to exercise the production providers.

The development cloud workspace initially contained no configured vault, `tasks.json`, database or workspace registry. Fixture qualification and an isolated CLI cutover can be executed there. The Mac vault shown in the discussion is not attached to that workspace; its migration remains a separate live rollout in #47 until its configured files are accessible and preflight/apply evidence is recorded.
