# Staged workspace cleanup and safety details

**Unresolved** means Radar found local work that may need preserving, or a
persistent cleanup problem. It does not mean that a task is unfinished or that
a terminal is currently being used. Local-data warnings block conservative
cleanup, but do not block runtime retirement or extend a registered workspace's
seven-day destructive expiry deadline.

The overview has no cleanup-status badge or replacement indicator. Inspect (`i`) shows task details first,
then **Workspace expiry** for a done task with a valid completion time and a
registered workspace, then **Unresolved** with affected resources and reasons,
and **Source refs**. Expiry includes a local date/time with timezone and an
explicit permanent-loss warning. A past deadline reads **Eligible since**;
structural safety conditions can still prevent removal. Rendering is read-only:
it does not inspect the filesystem, preview cleanup, fetch remotes, or remove
resources. Sections without applicable data are omitted. GC feedback stays
counts-only.

## Workspace expiry

The daemon checks GC hourly; deadlines describe eligibility at the next run,
not an exact deletion instant. A registered note-only or multi-member workspace
is one bundle, including its linked tmux session and sandbox.

| Time continuously done | Garbage-collection policy |
| --- | --- |
| Less than 24 hours | Keep the registered workspace and its runtimes available, including during `radar gc` / TUI `X`. |
| 24 hours to less than seven days | Stop SBX and remove the linked tmux session. Preserve the stopped sandbox, sandbox-local state, workspace files, worktrees, branches, shared directory, and registration. Dirty/unpublished work and unavailable publication verification do not block this runtime-only stage. |
| Seven days (168 hours) or more | Registered workspace expiry: GC may discard local data and remove the remaining eligible resources, including the stopped sandbox. Structural safety conditions still apply. |

**Expiry can permanently delete local changes, unpublished commits in deletable
Radar-owned branches, and unknown workspace-root files. There is no recovery
archive.** Move valuable work outside the workspace or publish it before the
deadline. Keeping a local commit inside a deletable branch is not preservation.

Both stages use the latest transition to Done: runtime retirement is `DoneAt`
plus 24 hours, and destructive expiry is `DoneAt` plus seven days, not
workspace creation, file modification times, recent terminal activity, or daemon
startup. Reopening the task cancels expiry; completing it again starts a new
completion-based deadline. A missing or malformed completion time does not grant
destructive expiry eligibility.

Only **registered** Radar workspaces expire: the task must have a workspace-owning
ref (`ProvidesWorkspace`) with a nonempty `WorkspaceID` and workspace path.
Standalone observed worktrees stay conservative and do not expire, even when
linked to the same done task. Their branches remain preserved. Active tasks do
not expire. Agent-result review holds keep tasks active after agents settle.

Expiry never authorises deleting canonical task notes, primary repositories,
protected/shared branches, remote branches/PRs/issues, or external mount targets.
Invalid registrations, unsafe paths, provider inspection failures, and targets
that cannot be reliably identified still block removal. Symlinks are unlinked
without following targets outside the cleanup boundary. Protected data found
inside a proposed deletion boundary remains a blocker, not disposable content.

Execution first resolves every target to exactly one configured cleanup provider.
A missing or duplicate provider rejects the whole bundle before resource leases
or provider admission, preflight or removal callbacks. A supported first target
is not permission to delete it before discovering an unsupported later target.
Valid bundles retain native target order and all ownership/lease/preflight guards;
provider failures after dispatch can still be partial and require a fresh preview.

The shared native cleanup planner owns each provider's returned operations and
safety warnings before invoking the next provider. Later provider planning or
lifecycle callbacks cannot rewrite an earlier target through borrowed maps or
slices, and changing one returned preview cannot change the provider's values or
a separately planned preview. Target order, exact native values and nil/empty
shapes are preserved. This fixes an in-process aliasing boundary; it does not
freeze external resources, change policy, grant consent, replace last-moment
guards or add a diagnostic retirement/expiry apply adapter.

The internal native `cleanup.Service.CheckBundle` reuses that exact admission and
whole-bundle preflight path without calling any provider's `Cleanup`. It releases
all path, runtime-name, incarnation and provider-discovered admission before
returning, including on cancellation or error. This is **not a read-only probe**:
checks can create lock files, inspect runtimes and refresh provider metadata.
Success is only point-in-time evidence for these shared guards, not complete
last-moment provider eligibility, a reservation, consent or deletion authority.
It does not regenerate the caller's preview or establish task/activity/retention
eligibility supplied by higher-level planners. Execution reacquires admission
and repeats guards and provider-specific last-moment checks. No CLI, daemon or
Pi check/apply endpoint is added; their advertised capabilities remain unchanged.

Existing completed-note relocation to `Tasks/Archived/<filename>.md` remains
unchanged after the last workspace reference disappears. This preserves the
canonical note; it is **not** a recovery archive for discarded workspace files
or commits.

### Before installing this policy

Already-done registered workspaces use their **existing `DoneAt`**. Those already
24 hours past completion can have runtimes retired; those seven days past
completion can be removed at the next GC run after the new binary starts, or by
`radar gc` / `X`. There is no installation-time or startup
grace period, configuration switch, schema change, or timestamp migration.

Before installing, disclose this behavior and inventory registered workspaces
and their completion times read-only. Review local changes, local-only commits,
and unknown anchor files; preserve what is needed outside deletion boundaries
or reopen the task. Code tests do not replace this rollout review of existing
local data.

## Agent activity and result review

Both automatic stages recheck task completion and reported agent activity
before destructive execution. Waiting for the shared authored-lifecycle admission
is bounded by the caller's context: cancellation or deadline expiry stops a
waiting guard before fresh author/activity checks or its execution callback.
Cancellation is checked again after acquisition; the guard releases only its own
admission. A live caller still waits for the existing owner and then rechecks
fresh lifecycle/activity evidence under the same serialization. This does not
interrupt an already running callback, roll back effects, reserve future safety
or add a diagnostic retirement/expiry adapter.

A retained primary authored work-item completion also requires its own current,
unambiguous task-authoring integration at that guard. Missing or multiple
registered authoring capabilities, or a replacement author with a different
source identity, cannot skip the fresh lifecycle read and authorize cleanup from
cached completion. The guard refuses before activity/provider execution and does
not collect or publish a fallback. Tasks with no relevant primary authored
work-item still do not require an unrelated optional author. This applies to both
runtime retirement and expiry; recovering the capability does not replay refused
work or grant diagnostic preview/apply consent.

At the final execution guard, GC rebuilds the
production candidate plan from one atomic, owned task/reference snapshot using
the original run's policy and decision time. The selected candidate, including
its active linked references, must still exactly match the preview input. A
removed/moved workspace or changed resource bundle is not authorized by an
unchanged Done timestamp. Changes to unrelated tasks or an unchanged selected
task republished at a later revision do not invalidate it. This point-in-time
check is not a reservation against later changes; native ownership, activity,
lease and provider-specific last-moment checks still apply. It adds no diagnostic
retirement/expiry apply adapter or automatic retry.

GC now uses one private per-candidate preparation/execution boundary for both
stages and standalone cleanup. Preparation selects only that candidate's ordered
native bundle, mode and already-missing status outside lifecycle admission.
Execution retains the original policy and decision time, enters the same
authored-lifecycle guard and performs the final candidate/path checks before
calling the existing cleanup Service. It does not rerun provider preview, select
other candidates or switch retirement into expiry. Native error results retain
the returned successful prefix and dispatch observation, not verified outcomes
or safe-retry permission. The public GC command still sweeps all candidates;
this internal sharing is not itself a diagnostic endpoint, consent token or
apply capability.

Cleanup owns the selected bundle at execution/check entry and passes independent
copies to each native admission, whole-bundle preflight and provider dispatch.
A callback cannot redirect later targets after their leases/checks, rewrite a
retained plan's private operations or safety notices, or alter another callback's
inputs through shared maps/slices. Each returned successful target is copied
before later provider and release callbacks, including on partial failure or
cancellation. This ownership isolation is not synchronization for concurrent
mutation, a new executor, or independent verification of provider results.

The retirement stage now also uses an exact native `RetirementPlan` entry over
that same boundary. Preparation compares one expected registered GC candidate
with a fresh owned atomic snapshot, refuses ambiguous identities, non-retirement
stages and scope drift before native planning, and retains only its complete
ordered selected bundle. An owned preview copy cannot rewrite retained operations
or safety notices; execution retains the original policy, time and lifecycle
callback and delegates to the unchanged candidate/provider execution guards.
This in-process value is not public transport, fresh registration admission,
user consent, a one-use cache or safe-replay authority. A typed diagnostic caller
must still bind fresh registered feedback/target scope, the existing one-use
plan machinery and separate explicit preview/apply consent. The schema-37 typed
retirement adapter now supplies that separate binding; the native entry alone
never does.

The destructive expiry stage also uses an exact native `ExpiryPlan` over this
same per-candidate boundary, not a second executor or an installation sweep.
`PrepareExpiry` accepts only a complete matching registered candidate at or after
its seven-day deadline, with the original GC retention policy. Manual GC's
retention option cannot turn an earlier stage or standalone worktree into expiry.
The whole ordered workspace bundle, including eligible Git targets, is retained;
private operations and safety notices are owned and returned only as copies.
Expiring-data warnings keep their native expiry semantics while structural
blockers remain authoritative. Execution preserves the captured policy, decision
time and lifecycle callback, rechecks the complete candidate and uses the same
native ownership, lease and provider guards without replanning. Already-missing
paths still require the final lifecycle and absence checks. Returned successful
prefixes, dispatch observations and no-op path evidence are not independent
runtime/registration absence, rollback or safe-retry authority. This is a native
sharing foundation only: typed diagnostic expiry apply, its fresh exact-scope
one-use retention and separate destructive consent remain unsupported.

Already-missing cleanup results pass through the same final lifecycle guard,
including cancellation and authored-mutation admission. GC rechecks path absence
inside that guard before counting the no-op as deleted; a reappearing entry
(including a dangling symlink) or unavailable inspection is skipped, not removed.
No provider cleanup is dispatched for that no-op. This is point-in-time path
absence, not proof that a runtime, registration or unrelated resource is absent.

Busy, waiting, unverifiable activity, and pending handoff writes prevent removal even when an older preview or completion timestamp
would otherwise authorize it. Activity-driven reopening clears the completion
time; the review hold keeps the task unfinished after agents settle. Explicit
Done after reviewing starts both clocks again. Retained registrations keep the
runtime configuration, so the normal workspace open/recovery flow can restart a
stopped sandbox and recreate its missing tmux session. Retirement sends at most
one SBX stop to the inspected runtime ID, never the reusable name; reassignment
of that name cannot redirect the command to a replacement. There is no name
fallback or automatic replay. This does not reserve future external state, and
a failed observation may follow a partial stop of the original lifetime.
A successful SBX stop exit alone does not count as retirement: under retained
path/name leases, a fresh listing must show the same runtime stopped with its
registered read/write mount,
and current registration/filesystem/cancellation checks must still pass. Missing,
replaced, running or unverifiable post-stop runtimes report failure without
removal, automatic command replay or dropping registration. This is a bounded
native SBX observation, not proof of all bundle outcomes or future state.
Likewise, a successful tmux kill exit counts only after fresh native exact
identity/name checks observe absence under retained path/name/incarnation
admission, followed by current registration/filesystem/cancellation checks.
Ignored kills, replacement lifetimes or names, and unverifiable observations
fail without another kill or dropping registration. Initially absent sessions
are unstamped no-ops. These observations do not reserve future external state.
Partial runtime failures retain registration; obtain fresh eligibility before
retrying. Initially missing/stopped runtimes are idempotent no-ops, not new
retirement counts. Identification and inspection errors still block the operation. Terminal attachment alone
is not an activity signal, and unrelated idle sessions do not prevent cleanup.

Explicit `radar cleanup` / `x` remains a separately confirmed destructive action;
it is not a way to mark a task done and can terminate agents. This feature does
not remove that existing capability. Activity checks cover reported producers,
not arbitrary processes or an atomic guarantee against a new run starting after
the final check.

## Remaining issues and how to resolve them

The first four rows are local-data warnings: they block conservative GC but can
be discarded after registered-workspace expiry. The remaining rows are hard
structural safety conditions and do not expire.

| Issue | Why conservative cleanup stops | Potential resolution activities |
| --- | --- | --- |
| **Uncommitted changes** | Staged changes, modified tracked files, or untracked files could be discarded with the worktree. | Review the affected worktree with `git status` and `git diff`. Commit and publish work you need, stash it for later, or move untracked files outside the workspace. Discard changes only after deciding they are unnecessary. A local commit can still need publication verification. |
| **Local commits not verified as published or merged** | A branch scheduled for deletion has commits that cannot be verified on origin or as the exact head of a merged GitHub PR. A completed task or reused branch name is not sufficient proof. | Inspect the branch history and its PR. Publish any local-only work. Check for commits added **after** the PR merged. If the PR used squash/rebase merging, refresh Radar after the merge and confirm the local tip matches the merged PR's head. On other Git hosts, publish the branch if its original tip is no longer reachable on origin. |
| **Publication or merge verification unavailable** | Fetching origin or obtaining GitHub merge evidence still fails after one retry, or a verification check times out. Before expiry, uncertainty is not permission to delete commits. | Check connectivity and repository access; use `gh auth status` for GitHub API access. Correct invalid remotes or credentials, then refresh Radar. A transient failure that succeeds on retry does not become an issue. |
| **Unknown workspace-root files** | The workspace anchor contains files Radar does not own. A familiar filename or extension is not enough to declare them disposable. | Inspect the exact paths listed. Move valuable scripts, notes, or downloads into an appropriate repository or another directory. Remove only files you recognise as unnecessary, or explicitly configure disposable root entries as described below. Use the workspace's advertised shared directory for disposable screenshots and temporary exchange files. Explicit manual cleanup can instead delete the listed unknown content after a data-loss warning and confirmation. |
| **Unsafe workspace location** | The workspace is outside the configured workspace root, or is the root itself. | Check the configured root and workspace registration. Correct an unintended configuration or use Radar's workspace management to restore a valid location. Do not bypass the boundary by forcing directory deletion. |
| **Invalid workspace registration** | For example, a managed member incorrectly points at a primary repository checkout, or a referenced repository cannot be inspected. | Inspect the workspace membership and repository paths. Correct the member through Radar's workspace management; keep primary repositories outside the removal set. Restore a missing source repository if its worktree metadata must be inspected. |
| **Persistent inspection or resource-identification failure** | Git/filesystem/registry inspection fails, or Radar cannot identify a sandbox or tmux cleanup target. | Read the specific reason in Inspect. Restore permissions/access, unlock a worktree only if it is safe, or reconcile stale workspace/runtime information and refresh Radar. A running sandbox or attached terminal alone is not an error. |

After resolving an issue, refresh Radar (`r`) or wait for the next local
collection. Local checks run on each collection; successful remote verification
observations can be cached for up to two minutes. Actual cleanup checks remote
safety freshly. New local commits change the GitHub proof lookup key, so old
merged-PR evidence cannot authorise deleting a newer local tip.

Done tasks with a current unresolved workspace stay visible beyond the ordinary
three-day display limit. Once issues clear or the workspace disappears, normal
visibility rules apply again. This does not reset task completion dates or
change automatic runtime retirement at 24 hours or destructive expiry at seven days.

## Conditions that no longer need user intervention

- **Remote branch deleted after merge:** ordinary merges remain verifiable from
  origin's commit ancestry. For GitHub squash/rebase merges, Radar also accepts
  a merged PR only when its exact head equals the local branch tip, its target
  repository matches origin, and its merge commit remains reachable on origin.
  Closed-but-unmerged PRs, reused branch names, and later local commits do not
  satisfy this check. Non-GitHub hosts still use origin ancestry verification.
- **Attached tmux session:** attachment is not an unresolved issue and does not
  prevent cleanup of an eligible completed workspace. Reported busy/waiting agent
  activity separately prevents automatic cleanup. Removing the session can
  terminate its shells, agents, or other commands. This does **not** make active
  tasks eligible for GC or shorten the seven-day destructive grace period.
- **Branch used by another worktree:** remove the selected worktree but keep the
  shared branch and the other checkout. Cleanup must honour a preview that said
  the branch would be kept even if that other checkout subsequently disappears.
- **Managed worktree-removal outcomes:** every registered-member removal,
  including a keep-branch preview, uses the native managed remover to check both
  filesystem absence and a fresh, NUL-delimited Git worktree listing. A preview
  that preserves a branch cannot start deleting it after sharing ends. When
  local branch deletion is allowed, the native planner still protects shared
  and repository branches before deleting a local branch. It then independently checks branch absence after
  the expected-old-object-ID deletion. Repository/common-directory identities
  stay pinned and branch admission stays held across these observations. Remaining,
  replaced, malformed, unavailable or cancelled evidence fails closed; the
  caller retains member registration and stops later cleanup targets. Earlier
  effects can remain: no rollback or automatic retry is implied. These are
  point-in-time native observations, not future-state guarantees or diagnostic
  apply authority. Standalone removal and already-missing member paths are not
  covered by this managed-remover observation.
- **Primary repository checkout:** keep it; it is not a removable linked
  worktree. Registering one as a managed workspace member is still a
  configuration problem, not permission to delete the repository.
- **Already-missing worktree:** remove only the stale registration. Preserve its
  local branch and commits, including unpublished commits. A reappearing or
  locked worktree stops this operation; Radar does not run a repository-wide
  forced prune. Refresh removes stale standalone resource observations.
- **Radar-owned artifacts:** registered member directories, the managed
  `notes.md` symlink, and the validated workspace-specific shared temporary
  directory are already recognised. The canonical note is not disposable.
  Other root files block conservative cleanup unless explicitly configured as
  disposable; expiry can discard them within a validated registered anchor.
- **Temporary remote failure:** retry the read-only publication verification
  once before raising an issue. Persistent failures remain visible and block
  conservative GC; registered-workspace expiry can discard unverified commits.
  Filesystem, repository inspection, or target-identification failures remain
  hard safety conditions.

## Disposable workspace-root entries

For disposable caches such as `.pnpm-store`, add an explicit deletion allowlist
in the **user** config (`radar config-path`):

```yaml
workspace:
  cleanup:
    disposable_entries:
      - .pnpm-store
```

The default is `[]`. This setting **authorises deletion**, not just ignoring a
safety warning: when an otherwise eligible workspace is cleaned up, Radar removes
each listed entry, including a directory's contents. It applies to all registered
workspaces, including note-only workspaces, and is not a repository `.radar.yaml`
setting. Existing user configs are not rewritten; no registry or cache migration
is needed.

Names match exact, case-sensitive direct children of the workspace anchor, not
files inside member worktrees. Paths, glob patterns, empty/duplicate names,
control characters and leading/trailing whitespace are rejected. `notes.md` is
reserved (including case variants), and a configured name never overrides managed
member safety checks or permits deleting the canonical note.

Collection, cleanup preview, manual cleanup and automatic GC use the same
configured-entry policy. Configured entries no longer cause **Unresolved**;
unknown siblings still block conservative cleanup of the whole bundle until
registered-workspace expiry. Cleanup previews list the disposable entries under
**REMOVE**, including in the compact TUI view. Manual cleanup also lists unknown
entries with an explicit permanent-loss warning; they do not make the resource
check fail. Execution reloads the configuration and rechecks the anchor before
deleting its entries. Revoked permission or newly appearing entries absent from
the preview stop anchor cleanup; preview again after reviewing the changes. Unknown content blocks conservative cleanup;
explicitly confirmed manual cleanup and expired registered-workspace GC
separately authorise its removal.

Radar unlinks disposable symlinks without following their targets, including
links inside disposable directories. It refuses symlinked anchors and symlinked
parents below the configured workspace root. Conservative cleanup recursively
removes only approved entries; the anchor must then be empty. The allowlist does
not shorten task-completion retention or the seven-day destructive grace period. Expiry can remove unknown entries inside the
validated anchor, without following symlink targets or overriding canonical-note,
primary-repository, or member ownership protections.

## Explicit cleanup versus garbage collection

Use `radar cleanup <task-id>` or `x` in the dashboard. The preview covers every
linked worktree plus shared tmux/SBX resources once. A standalone local-resource
task removes only that resource. Remote branches, PRs/issues and canonical notes
remain untouched.

The dashboard preview shows safety warnings, then **REMOVE** and **KEEP**.
Press `d` for full paths/branches and arrows or Page Up/Down to scroll. Only `y`
confirms; Enter does nothing, and Escape or `n` cancels. Read all warnings before
confirming: manual cleanup can discard local changes and unpublished commits.

The daemon binds the complete native preview to a random, one-use `plan_id` on
this existing CLI/TUI path. Only the latest preview for that task is accepted,
within 60 seconds and at the same cached revision with no pending resource
refresh. Issuance reads publication state before its final revision comparison,
so a revision change during that reader refuses the new native or typed preview
without retaining it or replacing a prior plan. An earlier identity remains
subject to its own apply-time revision check; rejection does not revive it.
Issuance also rechecks caller cancellation/deadline after waiting for admission
and reading publication state. These point-in-time checks neither prevent later
drift/cancellation nor reserve resources or grant execution consent.
Changed targets, private operations or warnings, unissued bundles,
expired/superseded previews and previews from a previous daemon are rejected
before execution. The ID is freshness evidence, **not consent**: confirmation
is still required. Native ownership, activity, alias/path/runtime leases and
preflight checks still run; the ID reserves no resources or future safety.
The daemon rejects duplicate JSON members before routing or consuming a plan,
including escaped names and case-folded aliases of typed fields. Provider-owned
map keys remain case-sensitive; repeated keys in separate targets are distinct.

Plans are consumed before native execution, even on cancellation, guard failure
or partial removal. A lost response must never be automatically replayed; refresh,
inspect actual resources and obtain a new preview and confirmation. A resource
reread clearing pending state does not revive an old plan. At most 16 private
previews of 512 targets / 1 MiB each are retained in current-daemon memory only.
This does not enable diagnostic/Pi cleanup apply or verified resource outcomes;
their existing schema-28 proposals remain non-executable.

If Git cannot inspect a worktree's local changes, manual cleanup shows the
underlying status-check error and affected path as a warning. Only explicit
confirmation permits removal despite that unknown local-data state; automatic
GC, including seven-day expiry, still stops on that inspection failure. This
exception does not bypass resource identity, ownership, registry/path safety,
protected repositories/branches, durable-agent leases, or Git removal errors.

Other failed resource checks open **Cleanup needs attention** with the underlying
provider/resource error. Use `i` to inspect, Escape/`n` to return, or `r`/`x` to
refresh and retry. Retry builds a fresh preview for the same task and requires
`y` again; it never replays the old deletion plan. If removal fails partway,
Radar reports the completed count and remaining targets (including a possibly
partially removed target), preserves remaining registration, and offers the same
retry path. Fix the reported permission/identity/provider problem before retrying;
registration-only removal is not a substitute for deleting owned resources.

Registered SBX deletion requires a fresh identifiable listing proving both the
previewed lifetime and its reusable name absent. A renamed original, replacement
or ambiguous listing fails closed under retained path/name admission; no second
removal is attempted. This bounded observation is not future-state reservation,
safe-retry evidence or diagnostic/Pi outcome attestation.

Ordinary name-only SBX Source cleanup, including targets whose registered owner
is discovered during removal, also pins the currently observed lifetime,
revalidates it before one ID-addressed removal, and independently checks both
name and lifetime absence. Managed targets retain actual-owner/path/name
admission and repeat registration, filesystem, agent and cancellation checks
around those observations, without holding the note lock across SBX commands.
Successful command exits or errors containing `not found` are not absence
proof. An initial complete absent-name listing has no earlier lifetime to attest.
This does not bind a name-only preview to a runtime lifetime or grant safe-retry
consent. Direct low-level cleanup helpers are outside this Source-route contract.

Ordinary tmux Source cleanup, both standalone and discovered registered-owner
routes, also requires a fresh exact identity/name observation after one
ID-addressed kill. Command success alone cannot advance the completed prefix
or later cleanup targets. Original name/incarnation admission stays held;
managed routes additionally retain actual-owner path admission and repeat
registration, filesystem, agent and cancellation checks around the observation.
An ignored kill, renamed original, replacement or unavailable observation fails
without another kill or removing registration. Initially absent sessions remain
no-ops. These are point-in-time native checks, not diagnostic outcome attestation,
preview-to-lifetime binding for a name-only target, future-state reservation or
safe-retry consent. Low-level workspace creation rollback is outside this Source
cleanup contract.

GC results distinguish runtime-retired, deleted and skipped workspaces. `radar gc` also prints paths and skip
reasons; use `radar gc --json` for structured results.

GC acts only on completed, eligible workspaces. For registered workspaces,
`radar gc` and TUI `X` use the same 24-hour runtime and seven-day destructive
deadlines as the hourly daemon; they never delete files or worktrees before day
seven, even when clean. Standalone observed worktrees keep their conservative
cleanup policy and may bypass its initial wait with manual GC. Neither path
bypasses activity, durable-agent ownership/lease, or structural safety checks.

The selected task's `x` / `radar cleanup <task-id>` is a separate, explicitly
confirmed cleanup and can discard local work without waiting for GC retention.
Its preview lists unknown workspace-root files and directories under **REMOVE**,
including their names and the fact that directory contents will be deleted.
**Confirming permanently deletes that previewed content; there is no recovery
archive.** Escape/`n` (or declining the CLI prompt) cancels without removing it.
New root entries absent from the preview require another preview; confirmation
is not blanket permission to remove future entries.

Read its warnings and use it only after deciding what to preserve. Confirmation
is not permission to ignore ownership/path boundaries or delete canonical notes,
primary repositories, protected/shared branches, external mount targets, or
remote resources.

## Cleanup response timing

Explicit cleanup returns after executing the confirmed removal plan, without
waiting for follow-up resource collection. The response retains the current
resource snapshot and marks **Resource list refresh pending**, including after
a failure that may have changed resources. Later collection reconciles those
observations; a pending snapshot cannot authorize another cleanup preview or
execution.

During the cleanup check or execution, dashboard task-row Enter explains
**Cleanup in progress — Retry Enter when finished** rather than silently
ignoring a workspace switch. The existing restriction also covers unrelated
workspaces. Navigation and Inspect remain available. Success or failure clears
the operation lock and the explanation; press Enter again to switch. No switch
is queued or replayed automatically. On the initiating task, a failed cleanup
opens its error/retry screen; Escape returns to the dashboard before switching.
If you have moved to another task, the failure stays attached to its initiating
task without interrupting navigation. Follow-up resource collection does not
keep that switching lock held.

Publication-verification failures remain unresolved issues; before registered
workspace expiry they are not permission for GC to discard local work. Expiry
permits that data loss but not structural safety failures.

Publication/merge verification has one 10-second budget covering Git fetch,
GitHub proof, and retries. This applies to collection, explicit cleanup
previews, and GC, including callers without their own deadline. A shorter
caller deadline is preserved. Automatic GC shares the collection lock, so its
verification can also delay an interactive refresh; it must not wait for an
unreachable remote without a deadline. This is a per-verification budget, not a
global response deadline across all workspaces.

Non-interactive integration commands cancel their process group, including
helpers such as `git-remote-https`, when their context expires. Output-pipe
waiting is additionally bounded to 250 ms so inherited descriptors cannot keep
a timed-out command—and the cleanup response—blocked indefinitely. Interactive
login commands keep their terminal's process group.


### Exact diagnostic runtime retirement

Schema37 `feedback-diagnostics --section retirement_preview --parent-id <registered workspace ID>`
uses the same native exact preparation/execution boundary as GC, not its sweep.
`--action preview` returns one complete ordered selected runtime bundle.
`--action apply --plan-id <latest ID>` consumes that original per-task plan once;
no client native targets, subset, manual-cleanup substitution, expiry escalation,
replay or silent replanning is accepted. The raw registration-only diagnostic
probe remains inspection, never an executable plan.

Pi requires fresh interactive preview approval for native provider metadata reads,
then a SEPARATE fresh exact-target whole-plan destructive confirmation for apply,
matching the immutable disclosure, feedback workspace and Pi session. Plan IDs
are freshness, not consent. Sandbox stops interrupt commands/services; terminal
removal loses processes/contents. Workspace files, worktrees/branches, sandbox
local state and registration are preserved. Safety counts are not private messages
or full eligibility. Unknown ownership refuses; stopped registered owners block.
Native fresh authored lifecycle, collected-resource, candidate and provider
ownership/activity/lease guards remain authoritative. No lease is reserved by
preview. Cancellation/disconnect is cooperative, not rollback; returned prefix,
dispatch, operation and invalidation evidence is not independent resource
verification or safe retry. Other typed reconciliation/runtime controls
remain unsupported. No persisted schema or deployment change is required.

### Exact diagnostic expiry whole-plan consent

Schema37 also defines a bounded daemon/client `expire` preview/apply transport
for one exact registered seven-day production GC candidate. The existing public
CLI/Pi `cleanup_preview --cleanup-mode expired --workspace-id <registered ID>`
now uses this complete plan flow, not a paged proposal. Expired mode rejects a
task parent, page or subset; native GC binds the owning task.
`expiry_cleanup_apply: true` advertises separately confirmed expiry apply.

The daemon retains the original production `ExpiryPlan`, candidate, policy,
time, authored-lifecycle callback and complete ordered native bundle behind the
existing latest-per-task, 60-second, one-use cleanup identity. It neither sweeps
nor replans at apply, and accepts no client native targets/candidates, subsets,
manual mode, stage escalation or executable payload. Each registered member must
be covered exactly once by worktree removal or targeted missing-member cleanup,
with the registered runtime targets and workspace anchor. Incomplete, unknown,
empty/no-op-only, over-budget or structurally blocked disclosures refuse rather
than returning a partial plan. Complete public disclosure must fit 32 KiB; private
candidate, bundle and registration context count against the existing 1 MiB plan
budget. There is no second native cache or executor.

Expiry can discard workspace-local changes and unknown files, sandbox-local
state, terminal processes and eligible local branches **including unpublished
commits**. Missing-member cleanup retains its local branch; protected/shared
branches and remote repositories remain protected by native policy. Workspace
registration is removed and a completed canonical note may be archived. Expiring
data warnings do not bypass structural, fresh authored-lifecycle, ownership,
activity, alias, lease or provider guards. Preview ownership includes stopped
registered owners; unknown ownership refuses. No lease or lifecycle reservation
is held by preview. A matching apply consumes the latest plan before fresh
admission checks, including observed cancellation, and is never restored.

The finite response exposes only fixed effects, branch-effect labels, safety
counts, deadline and opaque target identities; paths, names, branches, messages
and native operation maps are withheld. The public Pi adapter requires
**separate fresh interactive exact-target whole-plan destructive consent** bound
to the immutable disclosure, feedback workspace, exact scope/target and Pi
session before dispatching apply. Preview metadata-read approval is separate;
headless, declined, aborted, expired, superseded or context-drifted approval
never dispatches apply. Any apply attempt consumes the UI binding. The CLI is
a trusted transport; it does not itself obtain interactive consent. Plan
freshness and successful preview are not consent. Returned prefix,
Cleanup-call boundaries, current-daemon operation and resource-invalidation
observations are not independent removal/absence verification, rollback or safe
retry. A cooperative deadline or lost reply may leave partial effects: never
replay, automatically retry or silently replan. No persisted-format change is
required; all existing CLI/daemon/loaded-Pi diagnostics must match schema37.
