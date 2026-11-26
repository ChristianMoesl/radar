# Exact registered sandbox recreation

`radar_diagnostics` section `recreation_preview` now supports exact native preview
and one-use apply through the **existing** recreation backend and executor. CLI,
daemon and loaded Pi must match diagnostics **schema 37**; old peers fail closed
without a compatibility, probe, cached or shell fallback. No persisted format
changes. `make install` does not update/reload separately installed Pi sources.

Select `parent_id=<exact registered workspace ID>` and origin (default), feedback
or installation scope. Scoped requests match the registered anchor, not a member
path. Installation still selects one workspace, never all sandboxes. Preview is
`action=preview` (default); apply requires `action=apply` and the latest returned
`plan_id`. No row/state/cursor/limit/subset, cleanup mode, runtime name, native
path, recipe, command or private native confirmation selector is accepted. The
replaced proposal-only `diagnostics-probe` route is refused, not a second workflow.
Ordinary inventories, probes, retirement and cleanup never invoke recreation.

## Separate approval and native reads

Pi requires fresh interactive exact-parent/scope confirmation for **each** preview,
then **SEPARATE fresh destructive confirmation** for apply. Preview approval,
recommendations, guard success and plan IDs are not consent to discard state.
Headless/denied/aborted approval reaches no native callback. Copied selectors and
an immutable complete public disclosure bind the exact feedback workspace,
registered target/scope and Pi session. Context recapture, session/availability or
workspace drift, another request, expiry, denial or cancellation invalidates the
UI binding. Mutation of returned model details cannot change retained disclosure.
Apply refuses blocked/skipped proposals. Each apply attempt consumes the UI
binding before its dialog; no replay, automatic retry or silent replan is offered.

The adapter calls existing `PreviewRecreateSandboxes` planner once. Its internal
capture separates the SAME native confirmation from the public proposal; rejected
observations return neither. The native confirmation is never serialized. The
shipped backend retains it in one cache of at most 16 latest-per-workspace plans,
with random opaque public identity and monotonic 60-second expiry. Public wall
expiry is disclosure only. Registered feedback caller, exact scope/target, private
registration, store revision and pending publication must remain bound. Successful
preview supersedes the earlier target plan. Failed preview admission does not
renew or replace it. Expiry, supersession and daemon restart require a new preview
and fresh separate consent, not an automatic replan.

Apply consumes the daemon identity **before** live binding/expiry/publication
validation, calls only existing `RecreateSandboxes`, and never recovers/replaces
the private confirmation with another planner call. The executor itself replans
to COMPARE the full native recorded-recipe/runtime plan, with native last-moment
registration, recipe, metadata and runtime checks, shared deletion plus exclusive
writer admission, pinned runtime lifetime and restoration guards. Registered
owner counts/deletion-lease observations do not substitute recreation policy or
writer admission. There is no second executor or native admission cache.

The cooperative native budget is 30 seconds; CLI/Pi allow transport margins.
Disconnect is not guaranteed forced cancellation or rollback. Once dispatched,
recreation discards sandbox-local state and interrupts commands/services while
intending to retain workspace files/registration, recorded mounts/ports and prior
running/stopped state. Native uncertainty, refusal, failed restoration, errors,
cancellation, timeout or lost reply may leave partial effects. Plans are never
restored or replayed. No cleanup, ordinary refresh/GC, login, credential setup,
release/deployment or arbitrary command fallback is requested.

Approval covers native managed preflight; SBX runtime/mount/port and
sandbox-secret/policy **metadata** reads using existing authentication; and native
recipe checks for mount directories, local kit existence and recorded env-file
readability. Radar opens that file only to check readability, not to read or
interpret its contents. Native preflight can inspect platform/daemon cache-policy
evidence. SBX read commands can auto-start **its daemon**, not a sandbox/VM.
Preview performs no implicit credential setup, fallback credential, sandbox
creation, recreation or service restart. Apply has only the separately confirmed
native recreation effects. Cancellation is not daemon-auto-start rollback.
Approval also covers two bounded native agent-registration scans and two transient
exclusive probes of the existing workspace deletion-lease inode. Probes briefly
lock/release it without creating coordination files/directories or reading lock
contents. They may contend with another operation; they never reserve admission.

## Evidence, privacy and limits

The shared native planner and confirmed pre-removal guard require unambiguous
secret/policy metadata. Repeated decoded object members or noncanonical case-fold
aliases of safety fields cannot overwrite evidence of secrets or local policies.
The generated kit-policy exception requires explicit nonnull `editable: false`
and recorded provisioning provenance; missing/null evidence is not permission to
delete. Empty inventories, global policies and irrelevant additional metadata
remain supported. Metadata refusal yields the native `blocked` proposal, not a
new eligibility claim or an instruction to export secret values. These checks do
not reserve metadata against later external changes.

The native wrapper resolves exact registration/scope **before** any runner call,
then brackets the planner with full private registration/recipe snapshots and
bounded registered-owner/lease observations. The owner scan uses the native
removal-owner predicate, counts owners including stopped registrations, and reads no runner,
checkpoint, conversation, token or schedule payload. Missing/unsafe/over-budget
registration evidence remains unavailable, never a zero-owner claim.
Registration drift (including private fields and other registered workspaces),
cancellation, malformed/unsupported registration and invalid target coverage fail
closed. The diagnostic adapter also rejects pending resource publication before
and after the planner, revision drift and unsupported/ambiguous capabilities.
After metadata reads, one independent native runtime listing must still show the
same name, lifetime and state, plus recorded mounts for a `recreate` proposal.
An absent-runtime proposal must still observe absence; it cannot adopt a newly
appeared runtime. Replacement, rename, disappearance, ambiguous/unavailable
listing or cancellation invalidates the proposal without replanning or replay.
Existing private registration/owner/lease checks follow that observation, so
changes during the final read also invalidate their evidence. Ports, secrets and
policies are not re-read; this does not attest their continued freshness, exclude
intervening ABA changes, or reserve safety after the final observation.
Full private registration and native plan are each limited to 1 MiB for this
projection; native registration parsing and individual native provider reads
retain their existing implementation limits. These are cooperative, point-in-time
checks, not atomic exclusion against external SBX changes or ABA detection.
Changed owner availability/private fingerprint or lease status/inode fingerprint
rejects the proposal, including same-count owner changes and same-status inode
replacement. Consistently unavailable observations cannot prove no intervening
change. Owner scans retain their existing 1,024-entry/1 MiB aggregate/64 KiB file
budgets. Lease probes use the same canonical identity as native deletion admission;
missing files are unknown, not free (an unlinked held inode may still exist).

The nested public `proposal` contains only these fields:

- `id`: exact registered workspace identity;
- `preview_id`: opaque `recreation-preview:<sha256>` display fingerprint binding
  full private registration/recipe, returned native plan and private observed
  ownership/lease fingerprints;
- `status`: the native planner's `recreate`, `blocked` or `skipped` decision;
- `previous_state`: previously listed `running`, `stopped` or `unknown`;
- `runtime_identity_observed`: whether the native proposal contained a runtime
  identity, **not** continued identity, ownership, provenance or health;
- `ownership_evidence`: `fresh_bounded_registration` or `unavailable`;
- `agent_owner_count`: a nonnegative count up to 1,024, present only with fresh
  bounded registration evidence; this is not runtime ownership or holder identity;
- `lease_evidence`: `unknown_no_lock_file`, `exclusive_available_at_probe`,
  `contended_at_probe` or `unavailable`, never a lock path or holder payload;
- `execution_eligibility`: always `unknown_fresh_execution_guards_not_run`, even
  with a registered owner, contention or an available deletion-lease probe.

Native plan IDs, runtime names/IDs, paths, recipe values/commands, native reason
text, secret/policy payloads and errors are withheld. `blocked` does not invent a
specific refusal reason. `skipped` is the native proposal's skip decision, not an
independent absence/cleanup outcome or recovery/retry permission. Unavailable
responses contain no proposal or cached fallback. The complete available and
unavailable envelope, finite nested proposal, limitations and cross-field
invariants are allowlisted before Pi text/details. Bounded JSON decoding rejects
extra/missing/null, duplicate (including escaped), case-folded, private or
mismatched members; errors are sanitized. No automatic retry or replay.

## Finite version-matched result and trustworthy outcomes

The existing CLI section returns `recreation`, not an inventory page. The finite
16 KiB view contains schema/operation/mode/scope, registered feedback and scoped
workspace IDs, the proposal above, random opaque public `plan_id`, wall
`expires_at`, store `revision`, fixed limitations, `problem`, `outcome`, optional
`operation_id` and `resource_refresh_pending`. The public display `preview_id` is
**not** a native apply plan and is never sent as ExpectedPlanID. Only the random
opaque public identity selects the private one-use retained confirmation.

Preview has `outcome=not_attempted`, `problem=none`, no operation and no pending
publication. Apply returns only the exact immutable preview disclosure with:
- `native_restoration_checks_returned` / `none` for a complete single-target
  successful native return. This is NOT new independent live-resource verification.
- `unverified_may_be_partial` with `native_error`,
  `invalid_or_incomplete_native_result` or `cancelled_or_timed_out` otherwise.
  Missing/inconsistent targets or counters never become success or safe retry.

Native errors, target names/IDs, paths, recipes, reasons and provider payloads stay
private. A current-daemon exact-workspace managed operation/timing is recorded;
resource reconciliation is requested even after uncertain failure. These are
handler-return observations and invalidation, not complete durable history,
rollback or verified resource outcomes. No collection refresh or GC is requested.
Unknown/unavailable results have no cached fallback. Old-schema peers fail closed
before native dispatch. Other runtime controls remain unsupported.
