# Workspace cleanup and Unresolved issues

**Unresolved** means Radar found local work that may need preserving, or a
persistent cleanup problem. It does not mean that a task is unfinished or that
a terminal is currently being used. Local-data warnings block conservative
cleanup, but do not extend a registered workspace's eight-day expiry deadline.

The overview has one `unresolved` badge. Inspect (`i`) shows task details first,
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
| Less than 24 hours | Automatic GC waits. `radar gc` / TUI `X` may collect a safe workspace earlier. |
| 24 hours to less than eight days | Conservative GC: local changes, unpublished commits, unavailable publication verification, or unknown anchor files block the bundle. |
| Eight days (192 hours) or more | Registered workspace expiry: GC may discard that local data despite those warnings. Structural safety conditions still apply. |

**Expiry can permanently delete local changes, unpublished commits in deletable
Radar-owned branches, and unknown workspace-root files. There is no recovery
archive.** Move valuable work outside the workspace or publish it before the
deadline. Keeping a local commit inside a deletable branch is not preservation.

The deadline is the task's completion time (`DoneAt`) plus eight days, not
workspace creation, file modification times, recent terminal activity, or daemon
startup. Reopening the task cancels expiry; completing it again starts a new
completion-based deadline. A missing or malformed completion time does not grant
destructive expiry eligibility.

Only **registered** Radar workspaces expire: the task must have a workspace-owning
ref (`ProvidesWorkspace`) with a nonempty `WorkspaceID` and workspace path.
Standalone observed worktrees stay conservative and do not expire, even when
linked to the same done task. Their branches remain preserved. Active tasks do
not expire.

Expiry never authorises deleting canonical task notes, primary repositories,
protected/shared branches, remote branches/PRs/issues, or external mount targets.
Invalid registrations, unsafe paths, provider inspection failures, and targets
that cannot be reliably identified still block removal. Symlinks are unlinked
without following targets outside the cleanup boundary. Protected data found
inside a proposed deletion boundary remains a blocker, not disposable content.

Existing completed-note relocation to `Tasks/Archived/<filename>.md` remains
unchanged after the last workspace reference disappears. This preserves the
canonical note; it is **not** a recovery archive for discarded workspace files
or commits.

### Before installing this policy

Already-done registered workspaces use their **existing `DoneAt`**. Those already
eight days past completion can be removed at the next GC run after the new
binary starts, or by `radar gc` / `X`. There is no installation-time or startup
grace period, configuration switch, schema change, or timestamp migration.

Before installing, disclose this behavior and inventory registered workspaces
and their completion times read-only. Review local changes, local-only commits,
and unknown anchor files; preserve what is needed outside deletion boundaries
or reopen the task. Code tests do not replace this rollout review of existing
local data.

## Remaining issues and how to resolve them

The first four rows are local-data warnings: they block conservative GC but can
be discarded after registered-workspace expiry. The remaining rows are hard
structural safety conditions and do not expire.

| Issue | Why conservative cleanup stops | Potential resolution activities |
| --- | --- | --- |
| **Uncommitted changes** | Staged changes, modified tracked files, or untracked files could be discarded with the worktree. | Review the affected worktree with `git status` and `git diff`. Commit and publish work you need, stash it for later, or move untracked files outside the workspace. Discard changes only after deciding they are unnecessary. A local commit can still need publication verification. |
| **Local commits not verified as published or merged** | A branch scheduled for deletion has commits that cannot be verified on origin or as the exact head of a merged GitHub PR. A completed task or reused branch name is not sufficient proof. | Inspect the branch history and its PR. Publish any local-only work. Check for commits added **after** the PR merged. If the PR used squash/rebase merging, refresh Radar after the merge and confirm the local tip matches the merged PR's head. On other Git hosts, publish the branch if its original tip is no longer reachable on origin. |
| **Publication or merge verification unavailable** | Fetching origin or obtaining GitHub merge evidence still fails after one retry, or a verification check times out. Before expiry, uncertainty is not permission to delete commits. | Check connectivity and repository access; use `gh auth status` for GitHub API access. Correct invalid remotes or credentials, then refresh Radar. A transient failure that succeeds on retry does not become an issue. |
| **Unknown workspace-root files** | The workspace anchor contains files Radar does not own. A familiar filename or extension is not enough to declare them disposable. | Inspect the exact paths listed. Move valuable scripts, notes, or downloads into an appropriate repository or another directory. Remove only files you recognise as unnecessary, or explicitly configure disposable root entries as described below. Use the workspace's advertised shared directory for disposable screenshots and temporary exchange files. |
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
change automatic GC's 24-hour retention period or eight-day expiry deadline.

## Conditions that no longer need user intervention

- **Remote branch deleted after merge:** ordinary merges remain verifiable from
  origin's commit ancestry. For GitHub squash/rebase merges, Radar also accepts
  a merged PR only when its exact head equals the local branch tip, its target
  repository matches origin, and its merge commit remains reachable on origin.
  Closed-but-unmerged PRs, reused branch names, and later local commits do not
  satisfy this check. Non-GitHub hosts still use origin ancestry verification.
- **Attached tmux session:** attachment is not an unresolved issue and does not
  prevent cleanup of an eligible completed workspace. Removing the session can
  terminate its shells, agents, or other commands. This does **not** make active
  tasks eligible for GC or shorten the eight-day destructive grace period.
- **Branch used by another worktree:** remove the selected worktree but keep the
  shared branch and the other checkout. Cleanup must honour a preview that said
  the branch would be kept even if that other checkout subsequently disappears.
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

```json
{
  "workspace": {
    "cleanup": {
      "disposable_entries": [".pnpm-store"]
    }
  }
}
```

The default is `[]`. This setting **authorises deletion**, not just ignoring a
safety warning: when an otherwise eligible workspace is cleaned up, Radar removes
each listed entry, including a directory's contents. It applies to all registered
workspaces, including note-only workspaces, and is not a repository `.radar.json`
setting. Existing user configs are not rewritten; no registry or cache migration
is needed.

Names match exact, case-sensitive direct children of the workspace anchor, not
files inside member worktrees. Paths, glob patterns, empty/duplicate names,
control characters and leading/trailing whitespace are rejected. `notes.md` is
reserved (including case variants), and a configured name never overrides managed
member safety checks or permits deleting the canonical note.

Collection, cleanup preview, manual cleanup and automatic GC use the same policy.
Configured entries no longer cause **Unresolved**; unknown siblings still block
conservative cleanup of the whole bundle until registered-workspace expiry.
Cleanup previews list the disposable entries under **REMOVE**, including in the
compact TUI view. Execution reloads the configuration and rechecks the anchor
before deleting its entries. Revoked permission or newly
appearing entries absent from the preview stop anchor cleanup; preview again
after reviewing the changes. Unknown content blocks conservative cleanup;
expired registered-workspace GC separately authorises its removal.

Radar unlinks disposable symlinks without following their targets, including
links inside disposable directories. It refuses symlinked anchors and symlinked
parents below the configured workspace root. Conservative cleanup recursively
removes only approved entries; the anchor must then be empty. The allowlist does
not shorten task-completion retention or the eight-day destructive grace period. Expiry can remove unknown entries inside the
validated anchor, without following symlink targets or overriding canonical-note,
primary-repository, or member ownership protections.

## Explicit cleanup versus garbage collection

GC cleans only completed, eligible workspaces. `radar gc` and TUI `X` can bypass
the initial 24-hour safe-cleanup wait, but **never** the eight-day destructive
grace period or hard structural safety conditions. Before expiry they retain
local-data protections; after expiry they use the same registered-workspace
policy as the hourly daemon run.

The selected task's `x` / `radar cleanup <task-id>` is a separate, explicitly
confirmed cleanup and can discard local work without waiting for GC retention.
Read its warnings and use it only after deciding what to preserve. Confirmation
is not permission to ignore ownership/path boundaries or delete canonical notes,
primary repositories, protected/shared branches, external mount targets, or
remote resources.

## Cleanup response timing

Explicit cleanup returns after refreshing local resource observations. That
refresh can include remote Git safety checks for other workspaces, so an
unavailable remote can delay the response even after the selected workspace
has been removed. Publication-verification failures remain unresolved issues;
before registered-workspace expiry they are not permission for GC to discard
local work. Expiry permits that data loss but not structural safety failures.

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
