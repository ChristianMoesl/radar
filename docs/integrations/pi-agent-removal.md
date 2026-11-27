# Exact stopped-registration removal: daemon/client contract

Diagnostics **schema 40** adds finite daemon/client `agent_remove` preview/apply
transport only. `agent_remove_daemon_client_transport: true`, but
`agent_remove_previews: false`, `agent_remove_apply: false` and broad
`runtime_controls: false`: there is no feedback CLI section or Pi removal tool.
The existing native `radar agent remove` command is unchanged. Neither transport
nor a plan ID is a consent-token issuer. The trusted adapter boundary is explicit. Trusted callers require **SEPARATE fresh
interactive exact-target registration-removal consent** after an immutable latest
preview before apply. Existing stop consent cannot approve removal; removal never
implicitly stops a running agent or permits cleanup/deletion.

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

Preview performs bounded effect-free native capture of an already stopped exact
registration. Apply reuses the original single native executor: pause retained
schedules, then unlink registration, releasing **only this workspace association**.
Durable data, ownership intent, workspace files, sandbox and canonical note remain.
All other owners/activity/leases and separate cleanup/expiry policy still apply; the
release does not prove cleanup eligibility, resource absence or safety to retry.
Schedule pause may precede unlink failure or cancellation: effects may be partial,
without rollback. Captured counts are not counts of disabled schedules or outcomes. The finite
schedule-disable disclosure is not proof that those effects completed.

Removal uses the same 16-entry per-task maintenance retention and stop/cleanup
supersession. The original monotonic 30-second expiry is never extended; native
and complete adapter/guard context share the conservative memory budget. Apply
consumes before live checks; freshness/approval are separate. Exact task/ref,
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
complete nor durable. Schema-matched Pi may inspect this evidence without gaining
removal controls or requesting consent.

CLI, daemon and loaded Pi must match schema 40; separately install/reload Pi after
updating sources (`make install` alone does not reload it). Persisted formats are
unchanged: not a registry/data migration, no reset or compatibility shim.
