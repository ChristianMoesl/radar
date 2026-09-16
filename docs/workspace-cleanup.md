# Workspace cleanup and Unresolved issues

**Unresolved** means Radar found local work that may need preserving, or a
persistent problem requiring a decision before automatic cleanup. It does not
mean that a task is unfinished or that a terminal is currently being used.

The overview has one `unresolved` badge. Inspect (`i`) shows task details first,
then **Unresolved** with the affected resources and reasons, then **Source refs**.
The section is omitted when there are no issues. GC feedback stays counts-only.

## Remaining issues and how to resolve them

| Issue | Why Radar stops | Potential resolution activities |
| --- | --- | --- |
| **Uncommitted changes** | Staged changes, modified tracked files, or untracked files could be discarded with the worktree. | Review the affected worktree with `git status` and `git diff`. Commit and publish work you need, stash it for later, or move untracked files outside the workspace. Discard changes only after deciding they are unnecessary. A local commit can still need publication verification. |
| **Local commits not verified as published or merged** | A branch scheduled for deletion has commits that cannot be verified on origin or as the exact head of a merged GitHub PR. A completed task or reused branch name is not sufficient proof. | Inspect the branch history and its PR. Publish any local-only work. Check for commits added **after** the PR merged. If the PR used squash/rebase merging, refresh Radar after the merge and confirm the local tip matches the merged PR's head. On other Git hosts, publish the branch if its original tip is no longer reachable on origin. |
| **Publication or merge verification unavailable** | Fetching origin or obtaining GitHub merge evidence still fails after one retry, or a verification check times out. Radar does not treat uncertainty as permission to delete commits. | Check connectivity and repository access; use `gh auth status` for GitHub API access. Correct invalid remotes or credentials, then refresh Radar. A transient failure that succeeds on retry does not become an issue. |
| **Unknown workspace-root files** | The workspace anchor contains files Radar does not own. A familiar filename or extension is not enough to declare them disposable. | Inspect the exact paths listed. Move valuable scripts, notes, or downloads into an appropriate repository or another directory. Remove only files you recognise as unnecessary. Use the workspace's advertised shared directory for disposable screenshots and temporary exchange files. |
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
change automatic GC's 24-hour retention period.

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
  tasks eligible for GC, or bypass dirty-file and unpublished-commit checks.
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
  Arbitrary root files are **not** newly allowlisted or recursively deleted.
- **Temporary remote failure:** retry the read-only verification once before
  raising an issue. Persistent failures remain visible and conservative.

## Explicit cleanup versus garbage collection

GC cleans only completed, eligible workspaces and never silently overrides
remaining local-data protections. `X` bypasses the retention wait, not those
protections. The selected task's `x` cleanup confirmation can explicitly discard
local work; read its warnings and use it only after deciding what to preserve.
Remote branches, PRs, issues, and primary repositories are not deleted by GC.
