# Exact registered sandbox recreation proposal

`radar_diagnostics` section `recreation_preview` adds an **explicit proposal**, not
recreation execution. CLI, daemon and loaded Pi must match diagnostics **schema
32**; old peers fail closed, without compatibility or shell fallback. No
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

## Evidence, privacy and limits

The native wrapper resolves exact registration/scope **before** any runner call,
then brackets the planner with full private registration/recipe snapshots.
Registration drift (including private fields and other registered workspaces),
cancellation, malformed/unsupported registration and invalid target coverage fail
closed. The diagnostic adapter also rejects pending resource publication before
and after the planner, revision drift and unsupported/ambiguous capabilities.
Full private registration and native plan are each limited to 1 MiB for this
projection; native registration parsing and individual native provider reads
retain their existing implementation limits. These are cooperative, point-in-time
checks, not atomic exclusion against external SBX changes or ABA detection.

Only these proposal fields are returned:

- `id`: exact registered workspace identity;
- `preview_id`: opaque `recreation-preview:<sha256>` display fingerprint binding
  full private registration/recipe and the returned native plan;
- `status`: the native planner's `recreate`, `blocked` or `skipped` decision;
- `previous_state`: previously listed `running`, `stopped` or `unknown`;
- `runtime_identity_observed`: whether the native proposal contained a runtime
  identity, **not** continued identity, ownership, provenance or health;
- `execution_eligibility`: always `unknown_fresh_execution_guards_not_run`.

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
performs none of those effects. Native preview metadata checks do not establish
fresh agent ownership, activity, leases, complete execution eligibility or safe
retry. The display identity is deliberately **not** a native apply plan: it has no
apply route, one-use execution cache, expiry promise or consent semantics.
Cleanup apply and runtime controls remain unsupported. Later apply needs its own
fresh exact-target consent and existing native admission/execution safeguards;
no proposal, fingerprint, unknown/partial outcome or successful guard check
supplies that authority.
