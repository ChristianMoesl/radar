# One-time ignore → mute migration

This explicit rollout tool is outside Radar's runtime readers. Run it from the
mute-aware checkout before installing/starting the new binary. It preserves the
existing preference; it never infers it from task lifecycle or remote status and
never resets the cache.

## Read-only preflight (default)

From the repository root, supply both actual configured paths explicitly:

```sh
go run ./scripts/migrate-task-muting \
  --vault '/absolute/notes-vault' \
  --state '/absolute/radar-state/tasks.json'
```

Preflight stages **all** managed private and archived notes in a temporary vault
and validates them together using the production Obsidian reader. The migrated
cache is also staged and loaded with production `state.NewStore`, with `RADAR_STATE`
temporarily set to that staged file and then restored. Loaded record/source-ref
counts must match the input, so incompatible/discarded caches cannot pass simply
because loading returned no error. Malformed production types (not just renamed
preferences) are rejected; unknown JSON fields remain untouched.

It does not write live files, create missing live `Tasks` directories, or create
backups. A missing `Tasks` directory is an empty note inventory; a missing cache
is an error. The daemon may remain running during read-only inventory.

The migration changes only:

- The **top-level** YAML key `radar-ignored` to `radar-muted`. Values (including
  explicit `false`), comments, whitespace/newlines, other fields, note body,
  paths, and file permissions are preserved byte-for-byte. Nested keys and text
  mentioning the old key are untouched. Plain and single-line quoted keys are
  supported; multiline key spellings are rejected rather than reformatted.
- Cache version **7 → 8**, and JSON `ignored` key tokens to `muted` only at
  `task_records[].snapshot`, `task_records[].snapshot.source_refs[]`, and
  `source_refs[].snapshot`. All other JSON bytes remain unchanged, including
  metadata keys named `ignored`, unknown fields, numeric IDs/precision,
  acknowledgments, timestamps, lifecycle/status, and cached `true`/`false` values.

Already migrated notes and a fully migrated version-8 cache are noops. Both old
and new preference keys at the same typed location are an error, even if their
values match. Malformed booleans, duplicate YAML keys (including nested ones),
unsafe layouts/ownership, symlinks, unexpected cache versions, and version-8
caches still containing typed legacy preference keys fail closed. Duplicate JSON
keys at inspected typed objects also fail closed.

## Apply (coordinated cutover only)

**External precondition:** stop Radar yourself, prevent it from restarting, and
keep other note/cache writers (including editors) quiescent until migration and
installation finish. Run apply on the host/in the same PID namespace as the real
daemon, not in a container that cannot see host processes. The script-local guard
combines `process.DaemonPIDs` with an independently successful, parseable `ps`
enumeration before applying, after backups, and before every replacement. Failed,
empty, or malformed enumeration is an error even if a stale PID file made runtime
discovery tolerate the failure. Runtime process-discovery behavior is unchanged.
The tool never stops anything. These checks cannot prevent an external writer
from starting between a check and a rename.

Use canonical absolute paths with no symlink components (for example,
`/private/tmp`, not `/tmp`, on macOS). The backup directory must be **new**, outside
the vault, and its real parent directory must already exist:

```sh
go run ./scripts/migrate-task-muting \
  --vault '/absolute/notes-vault' \
  --state '/absolute/radar-state/tasks.json' \
  --apply \
  --backup '/absolute/backups/task-muting-cutover'
```

Apply repeats preflight, saves and syncs **every** input before the first live
replacement (including already migrated/noop inputs), and compares the original
bytes and permission modes before replacing files atomically. It captures and
rechecks the immediate `Tasks`/private/archive directory inventory before and
after staging, before backups, after backups, before each replacement, and at
completion. Added, removed, renamed, or changed-type entries fail closed, including
notes added while backing up and creation of a previously missing `Tasks` root.
It does not scan unrelated vault folders or nested attachments. Backup layout:

```text
task-muting-cutover/
  Tasks/<private-directory>/<note>.md
  Tasks/Archived/<note>.md
  tasks.json                           # original --state file, whatever its basename
```

No note is moved, archived, created, completed, or reopened. No registry or config
is changed. Keep the daemon stopped until the mute-aware Radar binary is installed;
do not run the old binary against migrated data.

If apply fails, its error reports how many replacements succeeded and the retained
backup path. There is no automatic rollback, backup deletion, or cache reset.
Before any replacement all backups are complete; a failure while backing up can
leave a partial backup with **zero** live replacements. Inspect the error and
current files before continuing. Preserve concurrent user edits; restore originals
manually to the original `--vault`/`--state` paths only after deciding how to handle
them. A later retry must use a different new backup directory.

## Tests (temporary fixtures only)

```sh
go test ./scripts/migrate-task-muting
go test -race ./scripts/migrate-task-muting
```

Tests cover dry-run, temporary production-parser/cache staging, true/false
preservation, backup contents/modes, strict collisions and malformed production
types, typed JSON scope, idempotency, symlinks, concurrent edits/permission changes,
notes added during staging/backups/replacements, environment restoration, stale PID
plus failed process enumeration, and partial apply with retained backups. A generated
production cache roundtrip also verifies records, source facts, bindings, and task
projection. Tests do not access the configured live vault or cache, stop daemons,
or install Radar.
