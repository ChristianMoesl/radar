# Exact stopped-registration removal: daemon/client contract

Diagnostics **schema 42** enables section `agent_remove` through the existing
finite daemon/client transport and single native removal executor:
`agent_remove_daemon_client_transport: true`, `agent_remove_previews: true`,
`agent_remove_apply: true`, broad `runtime_controls: false`.
The native `radar agent remove` command shares the same removal effect sequence.

In the registered feedback workspace, `radar_diagnostics` requires exact retained
task `parent_id` and registered `agent_id`, with origin (default), feedback or
explicit installation scope. Preview requires fresh interactive exact-target
metadata-read approval. Apply requires the **immutable latest preview** in this
feedback workspace and **identified Pi session**, plus **SEPARATE fresh interactive
exact-target registration-removal consent** for its complete disclosure. Stop
consent is never removal consent. Removal never implicitly stops a running agent
or permits cleanup or sandbox deletion. No row/page/subset or generic-control selectors.

Headless, denied, cancelled, expired, superseded, changed-workspace/session/context
or mismatched-target requests refuse before dispatch. Context/session recapture
and any other diagnostics section invalidate local approval freshness; every
attempt consumes it before validation or confirmation. Returned model details
cannot mutate the retained immutable disclosure. The native original one-use
30-second deadline is not extended during confirmation. Concurrent confirmations
invalidate older in-flight attempts. Failure, cancellation and unknown replies
never replan, retry or replay automatically.

The local CLI is a **trusted adapter, not a consent-token issuer**. It does not
prompt or authenticate interactive consent itself; direct callers own that boundary.
Pi always obtains separate fresh approval before invoking this dedicated adapter:

```sh
radar feedback-diagnostics --workspace <registered-feedback-workspace> \
  --section agent_remove --scope origin --parent-id <exact-task> \
  --agent-id <registered-agent> --action preview --json
# Apply only after separate fresh exact-target removal consent:
radar feedback-diagnostics --workspace <registered-feedback-workspace> \
  --section agent_remove --scope origin --parent-id <same-exact-task> \
  --agent-id <same-registered-agent> --action apply --plan-id <latest-plan-id> --json
```

The dedicated `diagnostics-maintenance` RPC accepts only the exact typed envelope:
`diagnostics_schema`, operation `agent_remove`, mode `preview` or `apply`, scope
`origin`, `feedback` or `installation`, exact retained task `parent_id`, registered
`agent_id`, exact registered `feedback_workspace` and resolved registered scope
`workspace` (omitted for installation). Apply alone requires the original `plan_id`.
Duplicate (including escaped), folded, null, unknown/private or cross-operation
members, paged/subset selectors, arbitrary controls and mismatched schemas refuse
before native effects. The bounded client never starts the daemon, falls back to
a generic agent endpoint, silently replans, replays or automatically retries after
cancellation, failure or a lost reply.

The finite response `diagnostic_agent_remove` has exact task/record/agent and
feedback/scope/workspace identities, revision, plan ID, original deadline, fixed
limitations, `schedules_before`/`enabled_schedules_before` **capture counts**, and
explicit dispatch/problem/outcome evidence. No schedule names, payloads, checkpoint
contents, private paths, PIDs, runtime names, tokens or native errors are disclosed.
Complete response validation is canonical, bounded to 32 KiB for the view and
36 KiB for the envelope, with every required/null member checked and the reply
bound to the original exact request. Older peers fail closed, without aliases.

Schema 42 also requires a separate finite `observation` object; handler status is
unchanged. Preview has `evidence: unknown_not_observed`, `registration: unknown`,
`schedules: unknown`, and no `observed_at`. Apply may retain that unknown evidence
even after handler success. Otherwise `evidence: bounded_native_checkpoint_reads`
has a canonical UTC `observed_at` within this apply's original native deadline,
`registration: original_present | absent | unknown`, and
`schedules: paused_or_empty_preserved | original_preserved | unknown`.
These are independently reread exact native checkpoints, not inferred counts or
handler results. A failed handler may follow observed effects. Cancelled/expired
requests acquire no detached observations. Invalid native evidence becomes unknown;
unknown/private/duplicate/null wire members and inconsistent observations fail closed.

The native reader binds ORIGINAL captured root/instance identities around bounded
no-follow reads and rechecks both leaves afterward. Schedule evidence compares a
private fingerprint of the complete decoded native state, including pending payloads
and admission metadata, with only enabled flags cleared. Missing/empty schedules must
remain preserved; no new file or history is created. `original_preserved` observes
the original unpaused checkpoint, never a successful pause. No private hashes,
checkpoint bytes, paths or payloads cross the wire. The timestamp is point-in-time,
not atomicity or later freshness. Registration absence does not prove runner,
workspace or sandbox absence, other-owner release, cleanup eligibility or safe retry.
The observation accessor is immutable and memory-only; no extra execution, implicit
probe, replay, replan or retry is introduced. Pi validates the complete nested object
before both text and model-details delivery, separately from unchanged handler status.

Preview performs bounded effect-free native capture of an already stopped exact
registration. Apply reuses the original single native executor: pause retained
schedules, then unlink registration, releasing **only this workspace association**.
Durable data, ownership intent, workspace files, sandbox and canonical note remain.
All other owners/activity/leases and separate cleanup/expiry policy still apply; the
release does not prove cleanup eligibility, resource absence or safety to retry.
Schedule pause may precede unlink failure or cancellation: effects may be partial,
without rollback. Captured counts are not counts of disabled schedules or outcomes. The finite
schedule-disable disclosure is not proof that those effects completed.

The single shared native executor checks cooperative cancellation immediately
before schedule pause and registration unlink, then again after unlink returns.
Cancellation after a persisted pause prevents the later unlink when observed at
that boundary: schedules stay disabled and the retained registration still holds
ownership. Cancellation during unlink may follow both effects; the cancelled
return never restores schedules, registration or one-use admission. These checks
are not atomic filesystem cancellation, verified outcomes or safe-retry permission.
Absolute caller deadlines apply at these effect boundaries even before context
timer notification. An expired request cannot start pause; expiry during pause
withholds the later unlink, and expiry during unlink cannot certify success.
Accepted effects are not rolled back. Expired postcondition capture stops further
checkpoint reads and withholds the entire observation, not just its latest field.

Removal uses the same 16-entry per-task maintenance retention and stop/cleanup
supersession. The original monotonic 30-second expiry is never extended; native
and complete adapter/guard context share the conservative memory budget. Apply
consumes before live checks; freshness/approval are separate. Execution uses the
earliest original expiry, caller deadline and existing five-second cap. Final
normalization precedes the call's own deferred cancellation. Exact task/ref,
registered feedback/scope, revision, resource publication and native identity are
rechecked INSIDE the retained final guard spanning both effects. Active/unknown
owners, desired/running agents, drift, pending collection, cancellation and denied
admission refuse. These are point-in-time guards, not an atomic filesystem/process
transaction or OS-peer/PID-incarnation attestation.

A volatile current-daemon `agent-remove` managed operation starts only inside final
admission. `native_remove_callback_entered_not_resource_outcome`,
`native_handler_returned_unverified` and `unverified_may_be_partial` report callback
entry/handler return, not independently verified outcomes, released leases or
resource absence. `succeeded` means handler return only; failed/cancelled returns
are failed handler evidence even if partial effects occurred. Resource observations
become pending after guard entry even on later failure/cancellation; independently
recollect rather than replay. No operation is created for preview or pre-dispatch
refusal, no cleanup target counts/phases are synthesized, and history is neither
complete nor durable. Schema-matched Pi validates this evidence before model output; neither evidence
nor handler success authorizes another removal or cleanup.

CLI, daemon and loaded Pi must match schema 42; separately install/reload Pi after
updating sources (`make install` alone does not reload it). Persisted formats are
unchanged: not a registry/data migration, no reset or compatibility shim.
