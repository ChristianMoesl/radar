# Authored task identity and link repair

A linked task may contain **one distinct authored identity**. Multiple observations of that same UUID are valid. An issue, branch, workspace or transitive chain is an association, not permission to merge independent notes.

Linking qualifies the entire connected component, including existing record membership, before merging. Components reaching multiple authored identities do not acquire inferred ownership: new ambiguous contributors stay separate, while established source ownership and authored bindings stay unchanged. This deliberately favors missing associations over an arbitrary note owner. Bound but unavailable work still blocks completion. Normal single-note linking and ordinary non-authored grouping continue to work.

## Before upgrading an existing installation

The SQLite schema, note formats and existing valid task records are unchanged. Inventory the configured database and any adjacent legacy `tasks.json` before rollout. Old invalid groups are **rejected unchanged** at runtime startup, with their record and authored IDs in the error; they are never silently split or reset. A legacy importer preserves grouped observations/history, but that does not make the aggregate safe to serve afterward. Inspect migration warnings and choose the repair handling before installing/starting against affected data. Do not delete the cache, reset Radar, edit Markdown to invent ownership, or weaken review checks.

On a backed-up copy, build the candidate binary and inspect its configured SQLite projection without starting the daemon:

```sh
./radar repair-task-links --json > link-repair.json
```

This default is a read-only SQLite transaction: it does not create state, change permissions, refresh providers or modify notes. A missing database is an error, not a fresh empty installation. It supports existing/migrated SQLite state, not direct editing of legacy JSON. For a legacy-only installation, inspect `migrate-state` warnings on copies of the coordinated notes/cache/registry and qualify the migration and repair there first.

The plan includes a fingerprint, conflicting record IDs, all known authored identities and observation IDs, and the decisions required. `radar state-export` also includes `link_conflicts` alongside the complete authored authority and projection. These files contain private task metadata; retain locally, not in public issues.

## Explicit offline repair

1. Stop Radar and other task-state writers. Back up notes, workspace/agent records and configuration alongside SQLite. Retain the original migration backup/cache if applicable.
2. Generate the plan from the exact database to repair. Complete every resolution from independent evidence, not reference order:
   - `keep_authored_id`: which note retains the original numeric task identity/history. Other notes receive fresh, non-reused task IDs.
   - `acknowledged_authored_ids`: identities to which the old acknowledgement applies. Use an explicit array; an existing acknowledgement cannot be discarded.
   - `pending_review_authored_ids`: identities to retain the old pending review on. Existing pending review cannot be cleared; if provenance is unknown, explicitly retain it on all affected notes until reviewed.
   - `assignments`: an entry for **every non-authored stored reference**, including inactive/informational history. Value is an authored ID or `""` to retain it in a separate unowned record. Existing durable binding ownership cannot be transferred by this operation.
3. If task-number/history, acknowledgement or contributor provenance cannot be recovered, obtain an explicit decision from the owner. The generated plan intentionally supplies no answers. A missing authored task or snapshot-only observation requires recovering its authority/provenance from backups first; the tool refuses to guess it.
4. Apply the reviewed plan offline with a **new** mandatory backup destination:

   ```sh
   ./radar repair-task-links --apply link-repair.json \
     --backup /absolute/private/path/before-link-repair.db --json
   ```

Apply takes the same exclusive lock used by the daemon/migrator, checks the current fingerprint, makes a full SQLite backup, rechecks the projection and current binding ownership inside a transaction, and writes the split plus a journal containing the exact original projection and chosen plan atomically. Existing backups are never replaced. Stale/incomplete plans, unknown choices, active state locks and conflicting ownership abort without changing task data.

The operation preserves authored UUIDs, note bytes/paths/modes, durable bindings/preferences, source observations and their timestamps/active flags. It keeps the chosen original task number, allocates fresh numbers for other identities, and attributes acknowledgement/review state only as explicitly selected. Unattributed contributors are retained separately, not deleted. The original complete aggregate/history remains in the backup and `documents` repair journal.

5. Inspect `state-export`, confirm the plan now has no conflicts, restart Radar and verify fresh collection, independent review persistence and another restart. Shared associations must not recreate the group. Do not call unavailable evidence healthy.

For rollback, stop all writers and restore the coordinated pre-repair database/notes/workspace backup before accepting new state. After new work, reconcile from the retained journal/backup instead of blindly restoring and losing subsequent decisions. Repair does not invoke a live note migration, remote ownership change, reset, release or deployment.
