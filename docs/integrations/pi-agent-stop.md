# Exact registered-agent stop transport

[Pi feedback diagnostics](pi-diagnostics.md) · [Durable agents](../durable-agents.md)

## Daemon/client infrastructure only

Diagnostics schema 38 adds the finite `agent_stop` maintenance transport. This
is **not a CLI/Pi stop control** or an approval mechanism. Overview advertises
`agent_stop_daemon_client_transport: true`, `agent_stop_previews: false` and
`agent_stop_apply: false`; broad `runtime_controls` remains false. The existing
Pi sections cannot invoke this operation. A future CLI/Pi adapter must obtain
separate fresh interactive exact-target interruption consent, bind the complete
immutable latest preview to this feedback workspace/session, and refuse
headless, denied, changed or expired approval. Preview reads need their own
fresh exact-target approval. A plan ID is freshness, never user consent.

The dedicated typed `diagnostics-maintenance` request uses operation
`agent_stop`, mode `preview` or `apply`, exact retained task `parent_id`, exact
registered `agent_id`, registered `feedback_workspace`, and origin/feedback or
explicit installation scope. Workspace scope resolves an exact registered
anchor; installation scope is still one exact agent, never a sweep. Apply also
requires the latest `plan_id`. No arbitrary command, PID, runtime name, private
registration, native callback, executable plan or client-supplied consent is
accepted. Duplicate (including escaped), case-folded, null, unknown and
cross-operation selector fields fail closed before native preparation/effects.

## Native plan, disclosure and execution

The adapter reuses the existing unique typed stop provider and exact native
capture/shutdown path, not generic agent control or a second executor. Scope
binds the current exact task/agent ref and complete registered feedback and
workspace identities. The original captured native guard rechecks scope,
revision and resource-publication admission before effects. Native
registration/root/instance/socket/owner and runner identity safeguards remain.
These are point-in-time checks, not an atomic collection/filesystem transaction,
OS peer attestation or PID-incarnation proof.

Plans share the existing bounded 16-entry per-task maintenance cache and
supersede cleanup previews for that task. Conservative retention accounting
includes private native and adapter/guard data. Server retention never extends
the original native monotonic 30-second expiry. Apply consumes before further
validation. Drift, expiry, cancellation, refusal or lost replies never restore
an executable plan or authorize automatic replay, retry or silent replan.

The complete allowlisted response is at most 32 KiB. It contains public task,
agent and workspace IDs, freshness, `desired_before`/`running_before` capture
facts, fixed evidence/limitations, and bounded native handler outcomes. No
paths, PIDs, runtime names, checkpoint/token contents or provider errors escape.
The client validates the full shape, required/non-null fields, scope, exact
task/agent, mode, schema and apply plan; private sibling fields also refuse.
It uses only the existing socket, never daemon startup, generic `agentCall`,
automatic retry or an inventory fallback. Update CLI, daemon and separately
installed Pi extension together; old schemas are rejected. Persisted formats
are unchanged: this is not a registry/data migration.

Preview is `not_dispatched` and `not_attempted`. Apply's
`native_stop_callback_entered_not_process_outcome` and
`native_handler_returned_unverified` describe the guarded native handler only.
A current-daemon `agent-stop` managed operation starts inside final admission,
with handler start/return timing and succeeded/failed handler status, not
business success or complete durable history. Failures/cancellation may be
`unverified_may_be_partial`. Resource observations are marked pending once
native dispatch begins, including failed or cancelled returns; independent
recollection is still required.

Stopping sets desired state false and may interrupt agent work. A desired-state
write can precede later shutdown failure; there is no rollback or safe-retry
claim. Handler return does not prove process exit, completed work, checkpoint
success, released leases or resource absence. The stopped registration still
pins its workspace. Data, registration, workspace and sandbox remain; start,
restart, remove and sandbox stop are not part of this operation.
