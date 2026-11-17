# Managed changes and prompt local reconciliation

Radar keeps the existing Unix revision-watch transport and recovery polls (15s
local, two minutes full). Managed changes no longer need the next poll:

| Entry point | Publication and reconciliation |
| --- | --- |
| Daemon task create/edit/lifecycle/priority/mute/delete, from CLI, TUI or Pi | Existing author-only publication remains immediate. The daemon additionally publishes operation start and result; an error requests local reconciliation because a partial write is possible. |
| Confirmed cleanup | Publishes daemon execution and native result counts. Both success and partial/error outcomes invalidate resource observations and enqueue a local read before returning. The response retains successful prefix removals and the provider error. |
| Automatic/manual GC | Publishes execution/result for each task whose guarded cleanup is attempted. The existing run result retains skips/errors. Completion invalidates and requests a local read, including when only partial work was possible. No retention, ownership, lease or confirmation policy changes. |
| Workspace creation/reconciliation via CLI/Pi or TUI | Reports a task/workspace-scoped client change through `resources-mutated`, marking observations pending and scheduling a read. This is explicitly **reported**, not daemon-confirmed execution. TUI publication belongs to the accepted action, including creation that outlives a popup. Errors conservatively invalidate too; a rejected plan requiring reconfirmation is not replayed. |
| `radar activity` and background agent changes | Reuse their native publication paths, then invalidate and enqueue local reconciliation. Activity producers do not wait for collection. |

`refresh-local` remains synchronous for callers that explicitly need a fresh
read, including foreground authentication and cleanup retry/preview. It is not
the asynchronous mutation notification path. A failed mutation notification
does not certify cached observations; the TUI retains pending/error feedback and
periodic collection remains the recovery path. An identity unavailable after a
failed creation permits invalidation only, not proof of what was changed.

## Operation evidence

Snapshots include current-daemon `operations`: an opaque random daemon namespace
with an increasing sequence, kind, initiating task/workspace identity, and status.
Daemon-executed requests publish `running` followed by `succeeded`, `failed`, or
cleanup `partial`; cleanup carries native completed/total target counts. Running
includes request validation and is not evidence that provider deletion started.
Failed providers can have changed a target even when no target returned success.
Client workspace notifications publish `reported`, never `succeeded`.

TUI rows/Inspect distinguish daemon evidence from local request animation and
show operation identity. Watch updates cannot finish a local request or move it
to the selected row. A running operation is preferred to newer terminal status;
the most recent terminal status is shown otherwise. Detailed native errors and
removed-target lists remain on the initiating request's response. Snapshots
retain all active operations and the latest 64 terminal/reported entries. Old
terminal entries expire; this is not an audit log. Restart/reset clears evidence
and no commands are replayed. There is no persisted schema or configuration change.

## Bounded requests and freshness

A single worker uses a capacity-one request channel and a fixed 100ms window
starting at the first consumed request. A continuous burst cannot reset that
deadline. Duplicates received before collection coalesce; events received during
collection retain one follow-up pass. There is never a goroutine or collection
queued for each event. Cancellation drops queued work and joins the worker.
The window bounds idle request scheduling, **not** provider execution, existing
local-lock wait, or when a busy collector finishes. Deterministic fake-clock
tests cover continuous bursts, blocked collection, follow-up and cancellation.

Only existing local collectors are requested: remote API collection is not
triggered. Resource-epoch and authoring-revision fences remain in force. An old,
cancelled or incomplete read cannot clear pending or overwrite newer state. A
complete local reread clears pending only for its current epoch and publishes a
new revision even if observations are identical. Local publication can converge
while a remote collector is stalled; remote facts and health remain independent.
No filesystem watcher, synthesized deletion, per-file collector or live mutation
authorization is added here.
