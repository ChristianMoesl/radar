# Exact registered sandbox recreation proposal

`radar_diagnostics` section `recreation_preview` adds an **explicit proposal**, not
recreation execution. CLI, daemon and loaded Pi must match diagnostics **schema
34**; old peers fail closed, without compatibility or shell fallback. No
persisted format changes. `make install` does not update/reload separately
installed Pi sources.

Select `parent_id=<exact registered workspace ID>` and origin (default), feedback
or installation scope. Scoped requests must match the exact registered anchor,
not a nested/member path. Installation scope still selects just one exact
workspace, never every sandbox. A sandbox must be recorded for that workspace.
No row, state, cursor, cleanup, apply, native path/target/command or plan selector
is accepted. A bounded `limit` is accepted, but the complete proposal is always
one row without pagination. Ordinary inventories, existing probes, retirement
and cleanup previews do not silently invoke this planner.

## Separate approval and native reads

Pi requires fresh interactive exact-parent/scope confirmation for **each**
request. Denial, missing UI, cancellation before dispatch and invalid selectors
reach no native callback. Confirmation binds a copied request; editing the
original while its dialog is open does not change the approved target.

The adapter calls the existing native `PreviewRecreateSandboxes` planner once,
with the selected registration's path. It does not duplicate recreation policy
or call `RecreateSandboxes`, Cleanup, ordinary refresh/GC, login, explicit start
or arbitrary commands. The cooperative native budget is 30 seconds; CLI and Pi
allow transport margins. Disconnect is not a guaranteed immediate cancellation
of native work, nor rollback of the effects disclosed below.

Approval covers native managed preflight; SBX runtime/mount/port and
sandbox-secret/policy **metadata** reads using existing authentication; and native
recipe checks for mount directories, local kit existence and recorded env-file
readability. Radar opens that file only to check readability, not to read or
interpret its contents. Native preflight can inspect platform/daemon cache-policy
evidence. SBX read commands can auto-start **its daemon**, not a sandbox/VM.
There is no implicit credential setup, fallback credential, sandbox creation,
recreation or service restart. Cancellation is not daemon-auto-start rollback.
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

Only these proposal fields are returned:

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
unavailable envelope, page, single row, finite nested fields, limitations and
cross-field invariants are allowlisted before Pi text/details delivery; malformed,
private, oversized or mismatched responses fail closed with fixed sanitized
errors. No automatic retry or replay.

The proposed **later** recreation discards sandbox-local state and interrupts
commands/services, while intending to retain workspace files/registration and
restore recorded mounts/ports and prior running/stopped state. This request
performs none of those effects. Registered removal-owner counts and deletion-lease
observations are not runtime ownership, fresh activity, holder identity or the
shared deletion/exclusive writer admission used by native recreation. They do not
invent an execution blocker or establish complete eligibility or safe retry. The display identity is deliberately **not** a native apply plan: it has no
apply route, one-use execution cache, expiry promise or consent semantics.
Recreation apply and runtime controls remain unsupported. Later apply needs its own
fresh exact-target consent and existing native admission/execution safeguards;
no proposal, fingerprint, unknown/partial outcome or successful guard check
supplies that authority.
