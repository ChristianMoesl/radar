# Exact registered-agent stop

[Pi feedback diagnostics](pi-diagnostics.md) · [Durable agents](../durable-agents.md)

## Exact CLI/Pi interruption consent

Diagnostics schema 44 exposes `radar_diagnostics` section `agent_stop` with exact
retained task `parent_id` and registered `agent_id`. Overview advertises
`agent_stop_daemon_client_transport: true`, `agent_stop_previews: true` and
`agent_stop_apply: true`; broad `runtime_controls` remains false. Start, restart,
remove, sandbox stop and generic agent controls are unsupported.

Preview reads require fresh interactive exact-target approval. Apply requires
**separate fresh interactive exact-target interruption consent** covering the
complete immutable latest preview in this registered feedback workspace and
identified Pi session. A plan ID is freshness, never user consent. The prompt
discloses desired-state persistence, possible interruption of unfinished work,
stopped registration ownership, preservation and unknown/partial outcomes.
Headless, denied, changed, superseded, expired or stale approval fails closed;
every attempt consumes the local preview binding before validation/confirmation.
Context/session recapture or another diagnostics request invalidates it. No
cached approval, subset, automatic retry, replay or silent replan is accepted.
The original native 30-second expiry remains authoritative during confirmation.

The trusted local CLI adapter resolves only registered paths and invokes the
finite maintenance transport; it is not a consent-token issuer or a second
executor. Pi, not a client-supplied approval flag, owns interactive consent:

```sh
radar feedback-diagnostics --workspace <feedback workspace> --section agent_stop --parent-id <retained task> --agent-id <registered agent> --scope origin --action preview --json
# Only after separate exact-target interruption approval of that complete preview:
radar feedback-diagnostics --workspace <feedback workspace> --section agent_stop --parent-id <retained task> --agent-id <registered agent> --scope origin --action apply --plan-id <latest plan ID> --json
```

Only exact stop selectors are accepted, including rejection of explicitly empty
or default-valued unrelated selectors. Other sections reject `agent_id`. The
complete allowlisted response must match the retained immutable disclosure even
if the model mutates returned details; unavailable evidence has no fallback.

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
Native execution uses the earliest of that original expiry, the caller deadline
and its existing 15-second cap, including connected shutdown I/O. Cancellation
or expiry observed after effects cannot produce a successful native return;
already accepted desired-state/shutdown effects are not rolled back.

The complete allowlisted response is at most 32 KiB. It contains public task,
agent and workspace IDs, freshness, `desired_before`/`running_before` capture
facts, fixed evidence/limitations, independent finite observations and bounded
native handler outcomes. No
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

## Independent bounded native stop observations

Schema 44 requires `observation` independently of handler status. Preview uses
`unknown_not_observed`, `registration: unknown`, `runner: unknown`, without a
timestamp. Apply may expose `bounded_native_stop_reads` with canonical UTC
`observed_at` bounded to this execution and the original native expiry:

- `registration`: `unknown`, `matching_stopped_definition` (captured native
  definition with desired false), or `original_preserved` (original raw file).
- `runner`: `unknown`, `matching_listener_present`, or
  `no_listener_and_no_live_observed_owner` (no listener, no live recorded owner,
  and no live captured snapshot PID when one was captured).

Native reads bracket the probe with original directory identities and stable
registration/owner/socket evidence. Unsafe, changed, missing, cancelled or expired
evidence remains unknown; handler success never fills it in. A failed handler
may still have observed stopped intent, and a returned handler may still have a
matching listener. These are point-in-time observations, not attribution, PID
incarnation, completed work, checkpoint success, lease release, cleanup eligibility,
workspace/sandbox absence, rollback or safe retry. Resource refresh remains pending.
Inspection is memory-only and cannot restore consumed admission.

Clients and Pi reject missing/null/private nested fields, duplicate or folded keys,
noncanonical/stale/future timestamps, observations in preview or cancelled results,
and old schemas without fallback. Update CLI, daemon and installed Pi together;
this wire revision changes no persisted data, native executor or consent policy.
