# Pi feedback diagnostics

[Documentation](../README.md) · [Pi integration](pi.md) · [Workspaces](../workspaces.md)

In an [Improve Radar workspace](../workspaces.md#improve-radar), `pi-radar`
restores feedback-specific guidance on every turn. The exact `linear` MCP server
must expose a reachable issue creation tool in that session; configured-but-not-
connected servers and hidden tools do not qualify. Direct, codemode and deferred
exposure are supported when the corresponding calling mechanism is available.
Pi owns MCP connections, authentication and tool execution; no MCP configuration
or credentials are copied to a feedback workspace.

The restored guidance leads from the initial description to a suggested bug/feature
classification, targeted follow-ups and an explicit readiness announcement. Bug
reports do not require a proven root cause; feature requests develop scope,
acceptance criteria and a grounded plan. Existing conversation and `notes.md`
prevent repeated classification/checklists, and assumptions remain separate from
confirmed decisions. The assistant searches related issues, presents the exact
title/body/destination and proceeds when the user clearly requests submission,
without an Improve Radar-specific MCP write dialog or mandatory “are you sure?”
question. Unclear intent still needs targeted clarification; readiness alone is
not permission to create an issue. This applies to direct, codemode and deferred
calls, without changing Pi/MCP authentication, tool access or approval behavior
outside feedback. Lost MCP availability still blocks feedback calls, and actual
host-command and private-log approvals remain unchanged. Only confirmed creation
yields an identifier/link for the user and `notes.md`; filing does not implement
the request or close the workspace.

`radar_diagnostics` is a typed host tool available only in enabled feedback
sessions. The current delivery is **observation, registration-only retirement previews, explicit native manual/expiry cleanup previews, and explicitly confirmed collection-only refresh**: the overview advertises
its capabilities explicitly. Explicit registered-runtime observations require
fresh interactive approval. Full provider cleanup eligibility, durable operation history, cleanup apply
and runtime controls are not yet exposed; unsupported
requests fail instead of running arbitrary commands or changing state.

The default `overview` returns the installed CLI version, loaded extension
version, cached daemon/provider status and a typed `settings` policy projection.
`feedback_sandbox_registered` separately describes the feedback registration;
it is not runtime presence or an effective sandbox-permission check.

## Explicit installation-wide collection refresh

`section: "refresh", scope: "installation"` defaults to `action: "preview"`.
This is the only maintenance apply currently exposed. Origin/feedback scope,
provider/entity subsets, inventory/log selectors and cleanup selectors are
rejected; installation-wide **inspection never authorizes mutation**. Preview
requires a registered feedback workspace and matching daemon but no provider
calls, locks, collection or mutation. At most 32 unique registered collection
sources are returned as sorted opaque `integration:<hash>` IDs, matching the
integration inventory. Disabled/unavailable sources remain targets, not proof
that they can collect. No raw names, paths, provider payloads or errors are added.

Preview discloses installation-wide effects, unverified target counts,
resource-refresh-pending state and finite limitations. The latest preview in
this daemon replaces any earlier unconsumed preview. Its random `plan_id` expires
after **60 seconds** using the daemon's monotonic clock; store revision or source
identity drift also invalidates apply. The displayed `expires_at` is UTC wall
time. The Pi tool also rejects expired/far-future previews; clock skew can block
consent rather than silently extending it. No on-disk plan, job queue or history
service is introduced. Restart discards plans and results. **A plan ID establishes
freshness, not user consent.** A preview is not a frozen provider configuration or
an attestation of effective permissions; native configuration/access is evaluated
by the existing services at execution.

For a separately requested refresh, use `action: "apply", plan_id: "<latest ID>"`
in the same feedback workspace. The actual tool accepts only a validated preview
it has just shown in that workspace, and requires fresh interactive confirmation
of the exact plan, complete source target set and installation-wide effects.
Headless apply, canceled approval, expired approval, lost MCP availability or
unmatched previews fail closed. CLI/Pi/daemon must match **schema 26**; the typed
maintenance request carries the schema and the daemon rejects mismatches before
admission. Read/probe RPCs cannot apply maintenance. The fixed CLI form is
`feedback-diagnostics --section refresh --scope installation --action apply
--plan-id <ID> --workspace <feedback-workspace> --json`; direct host CLI callers
remain responsible for obtaining human consent. No arbitrary command, SQL,
file, target-path or state-edit parameter exists.

Apply reuses production taskservice local-then-remote collection, authoring
reconciliation, collection-event delivery, publication and native freshness
fences. **It does not call ordinary daemon refresh**, because that callback also
runs hourly workspace GC. This dedicated collection-only adapter never requests
workspace GC, cleanup, login/setup or runtime startup. Native provider reads can
use existing authentication and refresh Git metadata; reconciliation can update
notes, bindings and task lifecycle and deliver collection events. This is a
mutation, not an observational probe or promise that downstream configured event
consumers have no effects. Existing provider/network/cloud permissions still
apply; no implicit setup or credential fallback is added.

Full/local collection admission is nonblocking: busy work is not queued or
coalesced and then misreported as this request's collection. The existing
publication mutex honors cancellation. Native execution has a **30-second
cooperative context deadline**, with a 32-second CLI response deadline and a
35-second Pi budget. Providers must honor context cancellation; this is not a
hard process kill or transaction. Cancellation/disconnect after dispatch can
leave local publication, authoring or event-delivery changes. It is **not
rollback** and never permission to replay. The current-daemon plan is consumed
before calling the adapter, including failures; duplicate RPCs return the same
operation's running/returned evidence without executing again. While running,
a new preview is rejected. Pi consumes its cached preview before dispatch, so
an ambiguous/invalid/oversized response cannot be used to retry automatically.
Obtain a new preview and fresh user request/confirmation before another action.

The response separately reports `status` (`preview`, `running`, `returned`), a
finite `problem` (`none`, `native_error`, `cancelled_or_timed_out`,
`incomplete_or_unverified`), correlation `operation_id` after admission, and exact
per-source attempt outcomes: `complete`, `incomplete`, `skipped_before_collection`,
`cancelled_or_timed_out` or `unknown_not_attempted`. Counts are `complete`,
`incomplete`, `skipped` and `unverified`; canceled/missing attempts remain
unverified, never success or absence. Running/preview counts are all unverified.
A `complete` attempt is not successful reconciliation, remote health or verified
resource outcomes. The resource-refresh-pending marker certifies only its
existing local publication contract, independently of remote completeness.
`problem: none` is not business success; handler timings measure the native
publication interval only. Native `operations` includes kind `refresh` at
installation scope; it has no task/workspace target or fabricated cleanup counts.
No durable audit history, automatic retry, resource verification, cleanup apply
or runtime-control capability is claimed. An unavailable/old daemon has no
fallback. A lost apply reply returns `request_outcome_unknown_do_not_replay`,
not "no host changes made" or an apparently successful empty result.

## Configuration policy and origins

Overview `settings` are **CLI-process global defaults**. With a matching daemon,
`daemon.settings` separately reports **daemon-process global defaults** from the
same native bounded reader executed in the running daemon process. The existing
`diagnostics` RPC's selector-free `overview` returns this typed schema-26
projection; it never uses task visibility filters or provider calls. No new public
tool or general file/configuration endpoint is added.

Both projections reread their process's current fixed configuration location.
They have independent capture times/scopes, and neither is an environment dump,
startup/loaded-settings attestation, repository/workspace-effective configuration,
current provider access or execution permission. Differences help diagnose process
configuration drift; matching allowlisted revisions do not prove the same file,
environment or private configuration. `daemon_environment` and
`workspace_effective_policy` remain false; `daemon_process_configuration` advertises
only this narrow projection. Missing/old/mismatched daemon support is explicitly
unavailable with no legacy summary fallback. A valid daemon response can contain
unavailable policy evidence; it must not be replaced with CLI settings or defaults. The native defaults, YAML decoder and validation are
shared with production. Values and declarations come from the same bounded read
of the configured `config.yaml`; no second file is used to guess origins.

The finite ten-setting allowlist contains:
- `workspace.auto_confirm`: `true`/`false`.
- `sbx.enabled`, `github.enabled`, `jira.enabled`, `datadog.enabled`,
  `terraform.enabled`: `automatic`, `required` or `disabled`. `required` expresses
  configuration intent, not usable tools/authentication or an enabled runtime.
- `workspace.cleanup.disposable_entries` and `sbx.additional_mounts`: counts only,
  not names, paths, accepted deletion targets or permission to delete anything.
- `sbx.env_file` and `sbx.ready_command`: configured presence only; paths,
  command/argument values and referenced file contents are never read or returned.

Each setting identifies `origin` (`compiled_default` or `configuration_file`) and
`declaration` (`absent`, `null` or `value`). Explicit values equal to defaults still
have file provenance; native null/default behavior, aliases and YAML merges are
preserved. `location_origin` identifies only `xdg_config_home` or `user_home`, never
the path or an environment dump. Model/thinking strings and arbitrary old settings
maps are no longer returned. The revision hashes only these allowlisted values
and declarations, not private configuration bytes. `captured_at` is read assembly
time, not evidence that a daemon loaded this configuration. The daemon overview
also carries at most 32 bounded cached provider status rows, with only detail
presence (never detail text), and the current resource-refresh-pending marker.
Cached status is installation-wide, not live access, collection or workspace health.

At most 64 KiB is read from the fixed native configuration file. Leaf symlinks
(including legitimate user-managed configuration symlinks), FIFOs, nonregular,
oversized, invalid, replaced or incomplete reads are unavailable, not valid
policy or authorization. An actually absent file yields compiled defaults without
creating it. Raw parser/validation errors are withheld. A prior workspace
registration lookup still uses its native configuration-loading rules; this
bounded projection does not change normal configuration or startup behavior.
Provider credentials, secret/environment files, tools, network access, repository
configuration, daemon environment and maintenance are never inspected or invoked
by this projection. No persisted configuration or policy was changed.

The originating workspace is projected from its registration only: it does not
run SBX, tmux, Git, remote probes or workspace startup. A registered resource is
not proof that it exists or is running. Missing daemon/origin evidence is
unavailable, not healthy, empty, repaired or enrolled.

## Cached integration evidence

Use `section: "integrations"` for the complete paginated union of registered
integration capabilities, cached installation-wide provider statuses and retained
reference providers (including no-longer-registered ones). `id` selects an opaque
stable integration ID. Installation scope includes
all these rows; origin/feedback scopes select only providers with directly related
retained references, including inactive and dashboard-hidden records. There is
no inference that an unrelated provider is unused, healthy or unavailable for a
workspace. Parent and task-state selectors are rejected rather than ignored.

Capabilities are static registered interfaces, not effective configuration,
tool availability, authorization or permission to execute an operation. Cached
statuses are explicitly installation-wide even in workspace scope; status alone
is not workspace health. Only `ok`, `partial`, `error` and `disabled` are projected;
missing or unrecognized evidence stays `unknown`. Duplicate registration/status
identities have explicit ambiguity evidence and do not invent capabilities or
health. Raw provider errors/details are withheld behind `details_available`.
Installation-reported observation counts are omitted in workspace scope. Native
zero/missing counts are also omitted: the persisted status model does not
distinguish them (including skipped collection), so they cannot prove none.

Active/inactive reference counts and latest valid retained-reference observation
timestamps describe only the selected scope. Invalid/missing timestamps are
counted as unknown. Such timestamps never establish the last collection attempt,
last successful collection, provider cache freshness or authentication. The
separate current-daemon native envelopes below never turn these retained
timestamps into collection history;
no persisted schema is introduced.

Registration and cached status/reference snapshots are projected without calling
provider `Status`, `Collect`, authentication, rate-limit or runtime methods,
looking up/running tools, reading provider credentials/configuration or refreshing any
provider. Native references and statuses share one atomic cache revision; cursors
also bind selected scope, registration/capability changes, returned collection
attempts and pending publication.
IDs hash full native provider identity before clipping display text. CLI, daemon
and extension must all support schema 26; older versions fail closed.

Pi validates the complete integration envelope, page, row and capability allowlist,
not only the collection subobject. Unknown sibling fields and inconsistent scope,
identities, counts, evidence, limits or pagination fail closed before either tool
text or details reach the model. Unavailable responses cannot include cached
fallback or private payloads. Native UTF-8 clipping may expand display text through
replacement/ellipsis; that expansion remains bounded. This validation repair
keeps schema 26 and its native wire shape unchanged. Separately installed Pi
sources require an update/reload; `make install` alone does not replace them.

## Current-daemon collection attempt evidence

`integrations` includes `collection_evidence` and, when available, `collection`:
the latest returned native Status/Collect/ResolveBindings envelope, with UTC
start/finish times, an optional actual collection-start time, and a finite
`complete`, `incomplete`, `skipped_before_collection` or
`cancelled_or_timed_out` outcome. `last_complete_collection_finished_at` retains
the latest complete native collection across later failures/skips. A skipped
precheck is not a collection. `complete` requires the native collection/bound
lookup completeness flag, final status `ok`, and a non-cancelled envelope; it
does not establish reconciliation, publication or business success.

This is bounded latest-per-provider evidence in the **current daemon's memory**,
not a persisted history platform. Restarted/unobserved sources are unknown;
there is no inferred history from retained reference timestamps. Only normal
refresh discovery attempts are recorded, including attempts whose cache
publication was cancelled or superseded by resource mutation. Reconciliation,
authoring, delivery and cache publication may fail independently. Active/queued
attempts are not recorded here. Completion does not prove live authentication,
current access, cache freshness, workspace health or maintenance eligibility.
Workspace scope selects related providers; envelope evidence remains
installation-wide for that provider, not workspace-specific collection success.

Reads use one atomic retained-reference/status/attempt snapshot without provider
calls. Attempt changes invalidate diagnostic cursors but never advance task
revisions, notify task watchers or write the projection database. Raw provider
messages/results, credentials and private payloads are withheld. Collection and
native publication policies, access permissions and persisted formats are unchanged.

## Explicit runtime-retirement target preview

Use `section: "retirement_preview", parent_id: "<registered workspace ID>"`
with the usual origin (default), feedback or explicitly requested installation
scope. Even installation scope selects **one exact registered workspace**, not
a sweep. Row/state/cursor selectors are rejected. Each request requires fresh
interactive confirmation naming that workspace and scope; headless, declined,
aborted or lost-MCP requests never issue the host read. The CLI equivalent is
`radar feedback-diagnostics --workspace <feedback workspace> --section retirement_preview --parent-id <registered workspace ID> --scope origin --json`.

This narrow preview reuses the production cached GC candidate/retention/stage
planner and the native `RuntimeCleanupTargets` registration/path checks. It
requires exactly one matching cached candidate in the **runtime-retirement**
stage (24 hours through seven days done), including UI-hidden retained tasks.
Not-yet-ready, expired, ambiguous, missing/unsafe/shared registration and
unsupported targets fail explicitly; there is no full-cleanup/expiry fallback.
Only the selected anchor is checked, even if its task has other workspaces.

At most three allowlisted proposals describe retaining the workspace,
stopping registered SBX commands/services and removing its tmux session/processes.
The latter may lose terminal processes/contents. Workspace files, worktrees and
branches, the sandbox and its local state, and registration are preserved.
Proposals do **not** prove that any runtime is present, running or needs a stop;
already missing/stopped resources may require no work. Opaque target IDs,
notice/blocker counts and fixed effect labels replace raw provider operations,
names, errors and task/note contents. Registration, target and cache drift, a
stage change or the five-second evidence budget invalidate the result.

Fresh registered-agent removal ownership is checked with the production decoder
and ownership rule, including stopped owners and nested backing workspaces.
`ownership_evidence: fresh_bounded_registration` includes `agent_owner_count`;
a positive count sets `execution_eligibility: blocked_registered_agent`. Zero
registered owners never means runtime ownership, lease safety or permission.
Unsafe, invalid or over-budget registration evidence is `unavailable` with **no
count**, never an apparently complete zero. The scan reads at most 1024 registry
entries, 64 KiB per registration and 1 MiB total per pass, inside the shared
five-second budget. Two private full-registration fingerprints detect drift,
including unchanged-count owner replacement and availability changes. No private
registration commands, owner IDs, tokens or checkpoint contents are returned.

No runtime/Git/network probes, Git fetch/prune, lease checks, locks, refresh,
login or mutations run. In particular this does not invoke the full cleanup
previews, which can fetch Git refs. Runtime ownership, fresh activity/review,
leases and execution preflight remain unverified; pending resource refresh is
reported, not ignored. The snapshot is not an executable plan, freshness token
for apply, execution eligibility or user consent. **Maintenance apply and verified operation outcomes are still unsupported.**
Use the separately approved cleanup preview below for manual or expired native targets.
Schema 19 must match in the CLI, daemon and loaded Pi extension; update the
extension sources and reload separately from `make install`.

## Explicit native manual-cleanup previews

Use `section: "cleanup_preview", parent_id: "<retained task record or numeric ID>"`
only for a requested confirmed-manual cleanup proposal. Origin (default), feedback
or installation scope selects the owning retained task, **not a resource boundary**:
the proposal covers its whole native related-resource bundle, including other
workspaces. Hidden retained tasks are included. Missing/ambiguous parents,
state filters, pending publication and unsupported native providers/targets fail
closed; there is no cached, safe-mode or expiry-mode fallback.

Every exact request/page requires fresh interactive Pi confirmation. The native
`cleanup.Service.Preview(..., CleanupConfirmed)` can perform authenticated
provider reads and **Git fetch/prune reference-metadata changes**. It never calls
cleanup execution, collection, login, startup or operation-lock acquisition.
With the same fresh explicit confirmation it also tests existing native lease
files using nonblocking exclusive locks, briefly held then released. These tests
may transiently contend with a new operation; they never create lease files or
directories, read contents, or reserve a lock for execution. Existing native
registration read locks may be used. Declined, headless, aborted
or lost-MCP requests never reach the host command. Cancellation or failure does
not roll back reference metadata already refreshed by the native planner.

Each page has a 30-second shared native deadline, at most 256 active task refs,
512 native targets and 32 KiB of redacted output (1–50 targets, default 20).
Private native target evidence is capped at 64 KiB each / 1 MiB total; safety
notices are capped at 1024 per target. Registered workspace projections are capped
at 1024 / 1 MiB per pass. Follow `next_cursor`; each page repeats native checks
with fresh approval. Cache, projected registration, registered-agent ownership,
observed lease inode/contention and full private target drift invalidate
snapshots/cursors, including private changes
that preserve public counts. No duplicate network preview is run solely to recheck
one page.

Only opaque target IDs, retained task identity, fixed provider/effect labels,
local-branch preservation/deletion, remote-repository preservation and native
safety kinds/flags/counts are returned. Raw paths, branch/runtime names, messages,
errors and private operations are withheld. Unknown notice kinds remain unknown.
Proposed later cleanup may discard local changes and worktrees/branches, remove
sandbox/local state and terminal processes, remove workspace files/registration
and archive a completed note. This preview does not itself remove those resources.

For each native target with a path, two bounded registry scans reuse the production
registration decoder and removal-owner predicate. `ownership_evidence` is
`fresh_bounded_registration` with `agent_owner_count`, or `unavailable` with **no
count**. Stopped and nested registrations remain owners. Targets with no path
report `not_checked_no_target_path` without a count, matching the native executor's
path-based guard. Each scan handles at most 512 paths, 1024 registry entries,
64 KiB per registration and 1 MiB aggregate; unsafe/invalid/over-budget evidence
makes the whole batch unavailable, never partial known-zero counts. Private owner
IDs, definitions, commands and fingerprints are withheld; no runner, token,
checkpoint, lease or operation lock is read/acquired by this ownership observation.

A positive per-target count sets `execution_eligibility: blocked_registered_agent`
and blocks the **whole native bundle**. Counts are per target, not deduplicated
bundle totals. Separately, `lease_evidence` tests the same path/lock identity used
by the native executor, once per distinct path in each of two bounded passes:
- `contended_at_probe`: the native exclusive lock conflicted with another lock;
  absent a registered-owner block, eligibility is `blocked_lease_contention`.
  Either blocker prevents the whole bundle, not just that target.
- `exclusive_available_at_probe`: the existing inode was exclusively locked and
  immediately released. This is not a reservation, no-active-work proof or
  execution permission; the holder and its shared/exclusive mode are unknown.
- `unknown_no_lock_file`: no file/directory could be opened because it was missing.
  This is **unknown**, not no leases: a process could still hold an unlinked inode.
- `unavailable`: unsafe/symlink/non-regular/multiply-linked files, invalid paths,
  cancellation or failed checks provide no contention evidence.
- `not_checked_no_target_path`: the native target has no path, matching its guard.

No raw lock filename, path, fingerprint, holder identity or file content is
returned. Observed inode/status drift rejects the page, including same-status
inode replacement; transient/ABA changes can escape observation. The two passes
are not an atomic lease snapshot, and `observed_at` is page capture time, not an
exact timestamp for every probe. Default inventories, runtime/agent probes and
registration-only retirement previews never run these lease tests.

Unless an observed owner or lease conflict blocks, eligibility remains
`unknown_fresh_execution_guards_not_run`, never safe or executable. Runtime
ownership, active work, fresh review/activity, actual execution-time leases,
preflight, expiry policy and automatic eligibility still require fresh guards. A native proposal is
not an executable plan, an apply token, publication approval or apply consent.
Native safety flags describe planner evidence, never permission to
weaken a safeguard. Maintenance apply remains unsupported; expired proposals use the explicit mode below.
CLI, daemon and the actual loaded Pi extension must match schema 26 without a
legacy fallback; separately installed extension sources require updating/reload
independently from `make install`. No persisted format or cleanup policy changes.

## Explicit expired-cleanup proposals

The same `cleanup_preview` section accepts `cleanup_mode: "expired"` plus
`workspace_id: "<exact registered workspace ID>"` and the owning retained task
`parent_id`. Manual mode remains the default and rejects `workspace_id`; no
safe-mode fallback, policy override or apply operation is added. The fixed CLI
selectors are `--cleanup-mode expired --workspace-id <ID>`.

Expired mode requires a unique current registration and the production cached GC
candidate/retention/stage gate (`InspectPlan` and `CandidateMode == CleanupExpired`).
The exact candidate must lie in the selected origin/feedback scope, or in explicit
installation scope. A different workspace of the same task cannot bypass that
boundary. Not-done, recent, active/review-held, unregistered, ambiguous, outside-root,
wrong-stage and refresh-pending candidates fail closed before provider checks.

The native expiry planner receives the whole owning task, just as GC does. Its
provider reads may inspect **other workspaces** and refresh Git reference metadata;
fresh per-page consent says so. Only proposed effects selected by the production
`SelectTargetsForCandidate` helper are returned, with a required matching workspace
anchor. An absent anchor fails unavailable, never an apparently empty/safe plan.
The complete pre-filter bundle shares the 512-target / 1 MiB evidence budgets and
private cursor fingerprint, including excluded-resource drift.

Every row identifies `cleanup_mode: "expired"`, the exact registered `workspace_id`
and production `expiry_at`. `automatic_blocker_count` reuses native expired-mode
semantics: explicitly expiring warnings can cease blocking, while other blockers
remain. Original safety flags/counts are preserved. The expiry gate is cached,
not execution eligibility; registered-agent counts still include stopped owners,
and zero/unavailable counts do not establish runtime ownership, leases or consent.
The same explicitly approved existing-inode lease probes and point-in-time
limitations above apply to the selected expiry targets; contention blocks the
bundle even when an expiring warning no longer does. All existing
30-second/output/privacy/drift safeguards apply. The preview never calls cleanup
execution, creates/reserves leases, changes retention policy or removes data.
Maintenance apply remains unsupported. CLI, daemon and loaded Pi extension must
match diagnostics schema 26; there is no legacy fallback.

## Explicit registered-runtime observations

Use `section: "runtime_probe", parent_id: "<registered workspace ID>"` only for
requested live runtime evidence. The usual origin (default), feedback and
installation scopes still apply; even installation scope requires one exact
registered workspace. Task-state filters are rejected. Every request/page needs
fresh interactive confirmation naming its parent, scope and optional runtime ID.
Headless, declined or aborted confirmation never starts a probe. The explicit
CLI invocation is `radar feedback-diagnostics --workspace <feedback workspace>
--section runtime_probe --parent-id <registered workspace ID> --json`; the normal
cached diagnostics RPC rejects probes, with no older-daemon fallback.

The provider-owned adapters run only native `sbx ls --json` and
`tmux list-sessions` (including native SBX/WSL executable selection, environment
and listing path normalization). One request has a shared five-second deadline;
stdout is capped at 20 MiB per command, stderr/provider errors are withheld, and
cancellation terminates command process groups. No collection, pane content,
startup, login, Git/network refresh, provider cleanup preview, ownership/lease
acquisition or maintenance runs. A runtime CLI may contact its own backend;
this is not a cached or offline observation.

Each item reports registered name, provider, bounded presence/state, opaque
runtime-lifetime ID and comparison against active cached native identities for
that exact workspace/name. Full native identities are compared before hashing
or clipping; reuse of a name with a different lifetime is explicit. A complete
identifiable listing may establish absence; malformed, ambiguous, unsupported,
failed or timed-out evidence is **unknown**, never absence. Shared registered
names are not probed. Changed registration/cache evidence rejects the result;
changed observations invalidate pagination cursors, and every page repeats the
bounded listing. The observation timestamp is not a guarantee of future state.

Runtime matches, absence or mismatches establish neither registration ownership,
lease safety, orphan absence nor cleanup eligibility/consent. Unrelated listing
rows, native lifetime IDs, mounts, environment, pane contents and raw errors are
not returned. Unregistered runtimes are not classified. Cached inventories never
start these probes implicitly; use existing retained references for cached
history, not as proof of liveness.

## Explicit registered-agent observation

Use `section: "agent_probe", parent_id: "<agent registration ID>"` only when the
user requests a live observation of that exact registered agent. Origin is the
default scope; feedback and installation remain explicit and distinct. Each call
requires fresh interactive approval naming the exact agent and scope. Headless,
declined, aborted or lost-MCP requests never contact the runner. Approval does
not authorize publication, work execution or maintenance.

The fixed host facade sends only native `hello` and `snapshot` RPCs to that
registration's socket, within one five-second deadline and native 1 MiB response
frame bounds. Registration is a stable, no-follow regular-file read capped at
64 KiB, decoded with the production registration rules and rechecked after the
probe. Invalid/unsafe/out-of-scope registrations fail before socket access;
registration or daemon-state drift invalidates the result. This is one target
and one result, not a paginated runner sweep; row/state/cursor selectors are
rejected. A page-size selector does not widen the target set.

A matching protocol/instance hello and a valid live snapshot are separate
facts. Only definition version, presentation revision, pending count, attention
and problem-presence flags are returned. Missing fields, incompatible identity,
oversized frames, transport failures and timeouts remain unknown. There is no
persisted-snapshot fallback: an unreachable runner is not verified stopped or
absent. A stopped registration still pins its workspace under native ownership
policy. Private presentations, actions, task/event/conversation payloads, PID,
raw errors and tokens are not returned; no token/checkpoint reads, workspace
runtime inspection, lease acquisition, refresh, startup, login or maintenance
occurs. Socket reachability and counters never establish business success,
workspace health, lease state or cleanup eligibility.

The exact CLI adapter is `radar feedback-diagnostics --workspace <registered
feedback workspace> --section agent_probe --parent-id <agent registration ID>
--json`; cached `diagnostics` RPC cannot contact a runner. CLI, daemon and Pi
extension must all support schema 26; there is no legacy fallback. Separately
installed extensions require matching sources/reload.

## Retained inventories (diagnostics schema 26)

Use `section: "tasks"` or `"workspaces"` to inspect records omitted from the
dashboard. `scope` is `"origin"` by default, `"feedback"` for the current feedback
workspace, or `"installation"` for explicitly requested installation-wide
inspection. The response identifies the feedback and origin independently.
An absent/unregistered origin never falls back to installation-wide access.

Inventories support `id` (stable record/workspace ID, or numeric task ID), `state`
(exact task state), `limit` (1–50, default 20), and `cursor`. For example:

```json
{"section":"workspaces","scope":"installation","state":"done","limit":20}
```

The equivalent fixed facade is:

```sh
radar feedback-diagnostics --workspace /registered/feedback --section workspaces --scope installation --state done --limit 20 --json
```

Follow `inventory.next_cursor` with the same selectors until
`pagination_complete` is true; `total` covers all matching top-level records.
A changed daemon revision, registration projection or refresh-pending state
invalidates the cursor. Restart at the first page when
`reason: "snapshot_changed_restart_pagination"` is returned. No long-lived
snapshot is persisted. Items are sorted by stable ID, pages are capped at
256 KiB, nested lists at 16 entries, and text fields at 512 bytes. Inspect each
item's `truncated` and the page's `limitations`; complete pagination is not a
claim of complete nested evidence or current runtime health.

## Current-daemon managed operations

Use `section: "operations"` with origin (default), feedback or explicitly requested
installation scope, `id` for exact managed-operation drill-down, and the usual
`limit`/`cursor` pagination. Task-state and parent selectors are rejected. This is
the existing daemon managed-operation projection, not a new job/history service:
active observations plus only the latest 64 terminal or client-reported entries
survive in memory until daemon restart/reset. Empty results mean no **retained
matching observations**, not no work or a complete audit history. Pages are at
most 32 KiB (including JSON escaping), with at most 50 items.

Scope joins operations to cached retained references atomically at the returned
revision, including dashboard-hidden/inactive task history. An explicit workspace
target takes precedence over a related task identity. Task-only operations can
affect the task's entire resource bundle, including other workspaces. Deleted or
unmatched targets may be visible only with installation scope. These joins are
attribution evidence, **not verified registration or ownership**; client-provided
target identities remain unverified. No registration/provider/runtime methods,
refresh, replay or maintenance are called.

Each row distinguishes `running`, handler-returned `succeeded`/`failed`/`partial`,
and `reported` client changes. The latter are never daemon execution. A handler's
success is not verified resource success or reconciled publication. Terminal
native cleanup observations expose the successful-prefix count and original
target count only; absent counts stay unknown rather than zero. They do not
identify failed/remaining targets or capture possible changes inside a failing
target. Other operation kinds' target counts, running counts, retries and
lock-wait durations remain unknown. GC/runtime/remote operations
outside the existing managed projection are not covered. `observed_at` is the snapshot **read time**, never an
operation start/completion timestamp. Raw errors, paths, messages, payloads and
target details are withheld; only bounded identities and fixed evidence labels
are returned. Stable IDs identify observations, not replay/apply authority, and
are distinct from the log query's opaque hashed correlation IDs.

Native handler timing is separately labelled: `native_handler_start_only` has
only `started_at` for a running operation; `native_handler_monotonic_elapsed`
has `started_at`, `finished_at` and nonnegative `elapsed_milliseconds` for a
returned handler, including failed/partial results and sub-millisecond zero.
These UTC wall timestamps are captured when the store records native begin/finish;
elapsed milliseconds use the in-process monotonic clock. Wall-clock corrections
can make timestamps reverse or disagree with elapsed time; never recompute duration
from them. No continuously changing elapsed estimate is added to running rows.
Client reports and missing/invalid timing have `unknown_not_captured` with no
timestamps or duration, not zero-duration execution. Result payloads cannot supply
timing; duplicate completions do not overwrite the first recorded native result.

This interval covers work and waits between native lifecycle publication points,
not a precise provider-call duration. Pre-start queueing/store-lock waits and
post-finish response delivery/reconciliation are excluded; separate phase
aggregates below do not measure lock waits or complete phase coverage. Timing completion proves neither business success
nor resource outcome, even when status is `succeeded`. Inspection never starts a
timer, changes revisions or probes a resource. Timing remains in the same bounded
volatile operation projection and is lost on restart/reset.

Native cleanup handlers additionally expose `phase_timing_evidence`:
`native_completed_phase_aggregates` includes a sorted, unique `phase_timings`
array with fixed `phase`, `completed_intervals` (1–1,000,000) and nonnegative
`elapsed_milliseconds` (at most 9,223,372,036,854). The sum is of **individually
millisecond-truncated completed intervals**, including measured zero; it is not
a wall-time subtraction. An interval that ends in failure is still measured.
Nested phases overlap: do not add different phases into handler elapsed time,
progress or resource success. Running operations include only phases already
returned, never an active-phase timer. Missing/uncaptured evidence is
`unknown_not_captured`, not zero, no work or complete coverage.
`unavailable_invalid_or_overflow` withholds the whole aggregate after invalid
native duration or a count/sum bound; no plausible partial sum is returned.

The eleven allowed labels reuse native instrumentation: `cleanup_execution`,
`cleanup_bundle_preflight`, `cleanup_resource`, `cleanup_worktree`,
`cleanup_runtime`, `cleanup_anchor`, `cleanup_protected_scan`,
`cleanup_member_scan`, `managed_worktree_removal`, `worktree_removal` and
`branch_publication_verification`. No dynamic command/phase names, paths,
arguments, errors or payloads are exposed. Uninstrumented phases, preview and
response delivery, client-executed workspace/GC work, retries and lock waits
remain unknown. Unsupported labels are ignored, not returned or counted as
known phases. The native cleanup context supplies correlation and samples;
result payloads, client reports and callbacks after terminal completion cannot
inject or overwrite aggregates. No separate telemetry/history service, probe or
persisted-format change is introduced. Snapshots copy the bounded aggregates; completed phase changes
invalidate scoped pagination just like handler timing changes.

Capabilities advertise `current_daemon_managed_operations: true`,
`native_managed_handler_timing: true` and
`native_cleanup_completed_phase_aggregates: true`, but
`durable_operation_history`, `verified_operation_resource_outcomes`,
`cleanup_apply` and `runtime_controls` remain false. Only the collection-only
refresh adapter advertises `maintenance` and `collection_only_refresh` as true. CLI, daemon and separately loaded Pi sources must
match schema 26; `make install` alone does not update/reload installed Pi sources.

## Identity and nested drill-down

Use `section: "source_refs", parent_id: "<task record or numeric ID>"` to page
all retained source refs rather than stopping at the task summary's 16 entries.
Each ref has an opaque stable `id` derived from the complete retained identity
(never a clipped path). It distinguishes that ID from the bounded `provider_id`, identifies its
owning record/numeric task, and keeps inactive history explicitly marked.
Omit `parent_id` for the scoped source-ref inventory, or select an exact retained
ref with `id`. A scoped unowned ref is matched only against itself, never an
unrelated unowned record. `state` still filters the owning task lifecycle.

Use `section: "bindings", parent_id: "<retained source-ref ID>"` to page authored
binding intent. Entries expose provider/kind/target identity, concrete-lifetime
key and work-item intent, never arbitrary metadata or raw provider errors.
Stable binding IDs hash the owning retained ref and the binding's identity;
identical repeated entries have one ID with an `occurrences` count. Neither a
binding nor its presence proves that the remote entity currently exists.
`parent_id` on unrelated sections is rejected, not ignored.

Use `section: "workspace_resources", parent_id: "<registered workspace ID>"`
to page every registered member, mount, port and retained task/registered-agent
owner beyond a workspace summary's 16 entries. Omit `parent_id` for the scoped
inventory or use `id` to select an exact resource relationship. `state` is
rejected for this section, not applied to only some resource kinds. All three
origin/feedback/installation scopes retain the same registered boundaries.

Each `workspace_resource` identifies its workspace, `kind`, evidence and one
allowlisted `member`, `mount`, `port`, `task` or `agent_id`. `task_owner` includes
the recorded state/DoneAt/retention gate and a task `record_id` for further task
drill-down. Stable IDs hash full relationship identities before text clipping;
identical relationships share an ID with an `occurrences` count. Changing a
task's state/retention does not change its relationship ID. Summary
`member_count`, `mount_count`, `port_count`, `task_count` and `agent_owner_count`
cover full lists (including duplicate occurrences), not just shown entries.
Task counts respect a workspace inventory's `state` filter.

Agent ownership uses the exact production removal-owner lookup, including
stopped registrations and backing workspaces inside a group. On lookup failure,
`agent_owner_count` is omitted, summary ownership is `unavailable`, and page
evidence explicitly ends in `agent_ownership_unavailable`; zero returned owner
rows then must not be read as no owners. Members/mounts/ports/agent owners are
fresh registration evidence; task ownership includes cached inactive history.
Neither is a runtime probe or full cleanup eligibility. No commands, secret-file
contents, environment values, note bodies or conversation/event payloads are
read or exposed.

Task `authored_identity_count`, `linking_component_records` and `linking_blocked`
reuse production's active connected components and the exact authored-identity
merge guard. Inactive authored history still protects its numeric identity. A
blocked bridge between distinct authored notes is explained, never merged or
repaired by inspection. Counts reveal related ownership without leaking other
tasks' note contents. A zero component count means no current authoritative
linking evidence, not proven absence of external links.

## Lifecycle explanations

Task `state`, `done_at` and `review` remain recorded evidence. The separate
`lifecycle` object evaluates the **same production transition** on all retained
active authoritative inputs, including entries beyond the summary limit. It
never applies that transition or reconstructs historical manual intent.
`source_state` is the prospective `active`/`done` result, or `unchanged` when
there are no selected work-item inputs; it can differ from recorded state.

The bounded `decision` is `no_work_item_inputs`, `work_item_not_done`,
`activity_or_review_pending`, `durable_review_pending` or
`source_completion_ready`. Counts report selected/done work items, inputs
using cached task attention as their signal fallback, authoritative refs and
activity/review holds. Primary work items take precedence over secondary work
items, exactly as in production. `recorded_review_pending` is the stored hold;
`next_review_pending` includes production's authored-completion/review handoff.
With no work-item inputs, production preserves state and the durable hold;
zero evaluated holds then means no transition was evaluated, not no activity.

Evidence is `production_lifecycle_evaluation_on_cached_inputs`, not a live
provider refresh, provenance of a manual change, cleanup eligibility or user
consent. Free-form reasons, source status text and metadata are withheld.
`source_completion_ready` never updates state or starts maintenance.

## Cleanup-retention and cached GC-plan explanations

Task views and each workspace retention entry include `gc_retention`, using
exactly the production GC gate and its active-ref selection. Its decision is
`task_not_done`, `completion_time_missing_or_invalid`, `retention_pending`,
`agent_work_or_review_pending` or `retention_gate_passed`. `ready_at` is the
normal retention boundary, not a deletion deadline or authorization. The
registered workspace's separate `expiry_at` still uses the existing expiry
policy; inspection changes neither deadline.

Evidence is explicitly `production_gc_retention_gate_only`. A passing gate
never establishes cleanup execution eligibility: location, registered-agent
ownership, provider safety, leases, drift and confirmation checks still apply.
A stopped registered owner can block execution after retention has passed.
Activity/review checks use all production-selected active refs, not just the
16 shown in a task summary. No provider preview, runtime probe, cleanup planner
or executor is run to obtain this evidence.

Use `section: "gc_plan", parent_id: "<task record or numeric ID>"` for the
complete production candidate/skip plan beyond that first gate. Omit `parent_id`
for all scoped retained tasks, including UI-hidden work, or use `id` for an exact
opaque GC-plan entry. `state`, `limit` and `cursor` work as for other inventories.
Scope selects the owning task and its complete related resource plan, including
resources related to that task outside the matching workspace. It does not grant
mutation authority. No task title, free-form reason, provider error, command or
executable operation map is returned.

The plan uses one immutable retained-state snapshot, the workspace manager's
production configured root, and the **same pure planner used by automatic GC**.
It retains active-ref selection, anchor-first registered-workspace deduplication,
normal retention/activity/review gates and lexical root exclusions. A row is a
`candidate`, `outside_workspace_root`, a retention-gate decision, or
`no_workspace_candidates` when the passing record has no planner candidates.
Inactive history may select an owning task's scope but does not create a current
candidate. Duplicate standalone candidate identities retain an `occurrences`
count. Unknown/unavailable workspace configuration fails explicitly, not as an
apparently complete empty plan.

`next_stage` reuses production's `safe_cleanup` (standalone),
`runtime_retirement` (registered workspace before expiry), or `expired_cleanup`
choice. Registered `expiry_at` is the existing production deadline, not consent
or proof of data presence. Candidate IDs hash full record/resource identity
before text clipping; decision, age and next-stage changes preserve identity
while invalidating pagination snapshots. A changed resource-refresh-pending bit
also invalidates cursors; pending state remains visible, not a refresh request.

Evidence is `production_gc_plan_on_cached_inputs`. `execution_eligibility`
remains `unknown_provider_ownership_lease_checks_not_run`, even for a candidate:
provider preview/preflight, actual resource presence, agent ownership, leases,
fresh drift checks and user consent are **not established**. This is not an
executable cleanup preview, a full list of provider deletion targets or evidence
of a completed cleanup/retirement. It performs no filesystem existence checks,
runtime/network probes, provider refreshes, lease acquisitions or mutations.
Explicit provider previews and apply adapters remain separate future operations.

## Durable-agent registration and checkpoint evidence

Use `section: "agents"` with the same origin/feedback/installation scopes and
revision-bound pagination, or select an exact agent registration with `id`.
The scope uses the production workspace-ownership relationship, including agents
whose backing workspace is inside the selected workspace. `state` is a retained
task-lifecycle filter, not a runner-state filter; it and `parent_id` are rejected
for this section rather than silently ignored.

Each item distinguishes registered desired state from observed runner state.
An intentionally stopped registration still owns and protects its workspace;
absence from a scoped inventory is not permission to delete resources. Reads
never contact a runner socket, inspect/start a runtime, acquire a lease or replay
an outbox. `runner_evidence` and `lease_evidence` remain explicitly unknown.

A compatible persisted runner snapshot provides only definition/presentation
revision, pending count, attention and a problem flag. `snapshot_saved_at` is the
snapshot file's persistence time, **not** a fresh runner observation or proof of
liveness. A missing, invalid, oversized or unsupported snapshot is unavailable
evidence, not zero work or a healthy/stopped runner. Snapshot reads are limited
to 1 MiB and reject symlinks and non-regular files.

Production-owned collection/schedule readers expose pending counts, delivery
problem flags, configured calendar metadata, derived next due times and bounded
admission timestamps/task IDs. Admission is not execution or business success.
Schedule due calculations reuse production scheduling; cached checkpoint errors
do not reconstruct in-memory supervision errors. The separate supervision
projection below supplies available current-daemon error-presence evidence,
not a change to checkpoint evidence. At most 16 schedule summaries
are returned per agent with the full `schedule_count` and explicit `truncated`;
follow `section: "agent_schedules", parent_id: "<agent registration ID>"` to
page all persisted schedules with the same scope and selectors. Omit `parent_id`
for the scoped schedule inventory, or use `id` for an exact opaque schedule ID.
Native bounded schedule IDs are separate from selectable opaque IDs, which hash
the complete agent/native schedule identity before text clipping. State and due
time changes do not change that identity. Task `state` filters are rejected.

Schedule inventories and summaries share one projection, production checkpoint
validation and production due calculation. Diagnostic schedule reads are capped
at 1 MiB, no-follow/nonblocking and regular-file checked. Missing schedule files
mean no configured persisted schedules, exactly as in production, not a stopped
or healthy runner. Invalid/oversized/unsafe checkpoints make summaries unavailable
with no schedule count; the schedule inventory fails explicitly, never returning
an apparently complete empty/partial list. Scope and parent filters are applied
before checkpoint reads, so an unrelated unavailable checkpoint can be excluded.

Each `agent_schedule` identifies its owning agent and allowlisted calendar,
checkpoint/admission metadata and delivery-problem flag. Evidence is
`fresh_schedule_checkpoints_runtime_errors_unknown`: in-memory supervision errors,
runner liveness, leases, execution and business outcomes remain unknown. Private
event/payload bodies, admission event IDs and free-form provider errors stay
withheld. Reads never tick a schedule, admit/replay events or write checkpoints.
Changes in shown checkpoint evidence, including beyond summary truncation,
invalidate cursors; complete pagination proves only this scoped projection.
Definition commands, tokens, raw errors, titles, conversation references, task
and event bodies, action payloads and schedule payloads are never returned.

`agents` also includes `supervision_evidence`. When exactly one native manager is
available and its mutex can be read immediately, this is
`current_daemon_retry_and_schedule_errors`; `supervision` contains
`retry_record_evidence` (`current_record` or `no_current_record`) and a
`schedule_delivery_problem` boolean copied from the manager's schedule-error map.
A current retry record additionally exposes `retry.failed_start_attempts`,
`retry.retry_not_before` (UTC) and `retry.start_problem` (presence only).
These reuse the actual supervisor retry/backoff state, not a diagnostic policy.

This evidence is volatile and keyed only by registration ID in this daemon's
memory. It is not complete history, a registration-lifetime attestation, the
last runner observation, a running-loop guarantee or a new socket/runtime probe.
A retry record may remain after desired state changes; desired state and stopped
ownership remain separate. No record means only no current retry record, **not**
health, successful work, zero historical attempts or a stopped/absent runner.
`retry_not_before` is the recorded backoff gate, not guaranteed future execution;
it may be in the past. The schedule flag is only current in-memory delivery-error
presence, not runner health, checkpoint validity or successful event execution.
Private error strings never leave the manager projection.

A busy, missing or ambiguous manager returns `unavailable_not_observed` with no
`supervision` object, not false error flags or zero retries. No inspection waits
behind the supervisor's I/O mutex, calls status/recovery, ticks schedules or writes
checkpoints. Scope and exact registration selection precede memory lookup.
Registration/checkpoint reads and the memory copy are not atomic; `observed_at`
is page capture time, not a retry/runner observation time. Shown supervision
changes anywhere in the scoped inventory (including availability changes)
invalidate pagination. No pre-restart evidence is reconstructed. Overview
capabilities advertise `current_daemon_agent_supervision: true` and
`agent_supervision_history: false`. CLI, daemon and loaded Pi must match schema
24; separately installed Pi sources still require updating/reloading independently
of `make install`.

Task views include retained lifecycle state, `DoneAt`, source observation times,
active/inactive refs, ownership identifiers, acknowledgement/review/activity
facts and binding/problem counts. `lifecycle_input` uses the exact production
selection of active authoritative work items (including primary precedence),
not a second lifecycle policy. Free-form titles/reasons and private payloads are withheld; authored binding
identities are available only through the bounded `bindings` inventory; historical manual intent is not reconstructed.

Workspace views include registration paths/members/source repositories, note
association (never its contents), desired SBX/tmux names, mounts/ports/shared
directory, cached task history, completion-based expiry from production
`workspacegc.ExpiryAt`, and fresh durable-agent ownership registrations. A
stopped agent registration still blocks removal; unavailable owner inventory
is **unknown**, never no owners. Runtime evidence remains `unknown_not_probed`,
and without a cleanup planner result the tool cannot assert eligibility or
propose safe deletion. Historical task ownership does not establish a current
owner. Canonical notes, primary repositories and mount targets are not deletion
recommendations.

`observed_at` is the diagnostic assembly time, not the collection time of cached
facts. `revision`, `snapshot`, `evidence`, source `observed_at`,
`resource_refresh_pending` and `limitations` distinguish fresh registration
reads from cached observations. Reads never start the daemon, refresh providers,
export raw state, read note/conversation contents or serialize credentials,
environment files, startup commands or full configuration. CLI, daemon and
extension must support diagnostics schema 26; version mismatches fail closed
without a legacy command/state-export fallback. Update the CLI and the actual
Pi extension source together, then reload Pi; `make install` alone does not
update a separately installed npm extension.

The `logs` section requires **fresh interactive approval for every exact query/page**.
It scans at most the latest 1 MiB of the single configured installation-wide native
Tint daemon log, and returns typed redacted metadata only: at most 100 entries
(default 20) and 32 KiB per page. It no longer returns a raw log tail. Free-form
messages/errors, arbitrary attributes, source locations, paths, credentials and
raw identities are withheld; unknown messages remain unclassified rather than
being interpreted as success or failure. No log format, collection policy,
persisted schema or telemetry/history service changes.

Use `since`/`until` as inclusive `YYYY-MM-DD HH:MM:SS` **native daemon wall times**,
not UTC/RFC3339 instants: native logs have second precision and no timezone.
No historical timezone or DST disambiguation is inferred. `severity` is an exact
`debug`, `info`, `warn` or `error` match. `provider`, `entity` and `operation`
accept only opaque IDs from returned `identities`, never substrings/names/paths.
Entry IDs bind native file lifetime, absolute byte offset and full line, staying
stable across filters/appends while distinguishing rotated files. Identity IDs
hash the complete native attribute value under its field name: they do not
prove cross-field identity, ownership or operation correlation. Only attributes
following known static native event messages are interpreted; apparent fields
inside free-form messages/errors are never searched. Missing correlation and
unrecognized formats remain unknown.

Pass `limit` (1–100) and `cursor=logs.next_cursor` to page the same bounded window.
Each page repeats the bounded read and fresh query-specific approval. Changed
file contents/size/mtime, filters or page size reject the cursor; restart rather
than combining snapshots. Drift returns `available: false`,
`reason: "snapshot_changed_restart_pagination"`, without an empty log page.
`pagination_complete` only covers matching metadata
inside that window, **not all historical operations**. `window_truncated`,
`partial_tail_withheld`, `unparsed_lines`, `query_unknown_lines`, scanned bounds
and limitations disclose missing evidence. Unlinked identity-filter rows are
conservatively counted as unknown, even if other identities are present.
First partial records and unfinished
last lines are withheld; symlinks, FIFOs and nonregular files fail closed.
Rotated files are not followed. A zero-match result is not absence or health.

Allowlisted phase durations (including lock waits) describe a completed timing
measurement, **never successful business execution**. Every row explicitly marks
outcome `unknown_not_recorded`; even a reported deletion/failure is only a log
event, not independently verified resource state or a complete target outcome.
Failed/remaining targets, retries, live locks and full operation outcomes remain
unknown unless separately observed. Log approval permits local inspection, not
publication or automatic Linear attachment; review proposed evidence separately.
Denial, missing UI, cancellation or MCP loss make no log read. Log filters are
rejected outside `logs`; inventory scope/parent/task-state selectors are rejected
for logs, which remain explicitly installation-wide. The tool accepts no arbitrary
path, executable, SQL or shell input. Source reads and experiments stay inside the
normal workspace boundary; no live state/config/socket mounts are needed.

The dashboard learns MCP creation availability through the existing per-session
loaded-version reports, refreshed every five seconds and removed on shutdown.
Reports contain availability metadata, not MCP configuration, tokens or tool
arguments. The dashboard ignores stale/dead-process reports and other Pi profiles.
The session revalidates against its live tool registry before diagnostics or
Linear writes; missing MCP availability never enables a direct API fallback.
