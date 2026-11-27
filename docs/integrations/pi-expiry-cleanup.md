# Exact registered-workspace expiry

[Pi feedback diagnostics](pi-diagnostics.md) · [Workspace cleanup](../workspace-cleanup.md)

## Exact registered-workspace expiry preview and apply

Use the existing `cleanup_preview` section with `cleanup_mode: "expired"` and
`workspace_id: "<exact registered workspace ID>"`. Manual mode remains the default
and requires its retained-task `parent_id`; **expired mode rejects `parent_id`**
because native GC selects and binds the owning task. No row, cursor, limit, state
or subset selectors are accepted. This replaces the previous paged proposal,
without an alias, compatibility fallback, second native cache or executor.

```text
radar feedback-diagnostics --workspace <feedback workspace> --section cleanup_preview --cleanup-mode expired --workspace-id <registered workspace ID> --json
```

Origin (default), feedback or explicit installation scope bounds this one exact
registered workspace, never an installation sweep. The unique candidate must pass
the production cached seven-day expiry gate; wrong-stage, active/review-held,
recent, unregistered, ambiguous, out-of-scope and refresh-pending targets refuse.
Native provider reads inspect the whole owning-task bundle, including other
workspaces, and may refresh Git reference metadata. Preview requires fresh
interactive exact-target/scope metadata-read approval; it never dispatches
provider Cleanup or holds execution admission, a lease or lifecycle reservation.

The finite `expiry` response contains the **complete ordered selected bundle**,
opaque target IDs, fixed effects, local-branch effects, safety counts, native
`expiry_at`, explicit `data_loss`, preserved remote repositories and an unexpired
`plan_id`. Every registered member is covered by worktree removal or targeted
missing-member registration cleanup, with runtimes and the final workspace
anchor. The whole disclosure must fit 32 KiB, or issuance refuses; never approve
individual pages or a subset. Paths, names, branches, messages, native operations
and raw errors are withheld. Stopped registered owners block issuance; unknown
ownership refuses. Zero automatic blockers and expired data warnings do not
establish fresh execution eligibility or override structural guards.

`action: "apply"` requires the latest complete unexpired preview returned in this
feedback workspace, exact scope/target and identified Pi session, then **SEPARATE
fresh interactive whole-plan destructive data-loss approval** of the immutable
full disclosure. Preview approval, headless/cached approval, successful checks,
recommendations and plan identity are never deletion consent. Decline, abort,
expiry, context/session drift, supersession or any apply attempt consume the UI
binding. Native matching apply consumes its 60-second monotonic one-use plan
before fresh admission, never restores it and never replans. The CLI is a trusted
transport, not a standalone interactive-consent UI; callers must enforce consent.

**Permanent loss** can include local changes and unknown files, worktrees,
sandbox-local state, terminal processes/contents and eligible local branches
**including unpublished commits**. Missing-member cleanup retains its branch;
remote repositories and native protected/shared branches remain protected.
Registration is removed and a completed canonical note may be archived. The
original production ExpiryPlan, candidate, seven-day policy/time and fresh
authored-lifecycle callback remain authoritative, with native ownership, activity,
alias, lease and last-moment provider guards. No manual substitution or escalation.

Apply repeats the immutable disclosure with finite outcome/problem, operation ID,
completed prefix, optional Cleanup-call dispatch boundary and
`resource_refresh_pending: true`. Returned success/dispatch/invalidation is **not
independent resource absence**, rollback or safe retry. Errors, cooperative
30-second timeout, cancellation or lost transport can leave partial changes.
Never replay, automatically retry or silently replan. Unavailable evidence is
unknown with no paged/native-old fallback. CLI, daemon and separately loaded Pi
must match schema40; reload Pi separately from `make install`. No persisted data,
native expiry deadline or policy changes in this public adapter.
