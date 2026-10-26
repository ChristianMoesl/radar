# Git integration

Git supplies local code-workspace facts and worktree cleanup.

## Capabilities

`Source`, `StatusReporter`, `LocalSource`, `WorkspaceProvider` for current code-workspace detection, `RepositoryCloneProvider`, and `CleanupProvider`.

## Configuration

`repository_dirs` controls repository discovery. Linking marks come from the shared allowlist. Git remote and branch normalization uses source-neutral linking helpers rather than GitHub identity code.

## Collection and refs

Radar stores registered Git members under a stable workspace anchor, using `<workspace_root>/<workspace>/<repo>--<branch>`. It sanitizes the repository and workspace names as one path component. Names longer than 120 characters are truncated and receive a deterministic eight-character hash suffix. Registered members emit a shared `workspace-group:<id>` linking key, so worktrees from different repositories appear in one task even without a configured linking mark. Radar also attaches worktrees by configured linking marks such as `ABC-123`. Regular repositories outside the configured workspace root are ignored. Branch names do not affect collection, so a workspace checked out directly on `main` remains visible.

Local refreshes inspect configured repositories and emit `git:worktree:<absolute-path>` refs. Unmanaged worktrees set `ProvidesWorkspace`; registered members carry their workspace ID and link to the managed anchor without competing as the entry workspace.

## Host source cloning

`radar_clone_repository` / `radar clone-repository` provisions missing source repositories on the **host**, not inside a sandbox or disposable task anchor. First-version remotes are GitHub.com HTTPS URLs and `git@github.com:owner/repo.git`; local/file URLs, remote helpers, URL credentials/parameters, custom hosts and caller-supplied Git options are rejected.

Inspect `radar_workspace_context.repository_dirs` first. These are existing configured source roots, exposed as canonical absolute host paths. The requested destination must be a new direct child of one of them, outside managed workspace directories. Missing roots do not trigger directory/configuration creation. Destination symlinks, traversal, root/configuration changes, and existing unrelated files/directories fail closed.

Preview is local and read-only: no remote probe, fetch, authentication flow, or destination creation. Initial cloning **always requires explicit approval** of the exact normalized remote, host root, and destination—even with `workspace.auto_confirm: true`. The Pi tool has no unattended approval flag; without a confirmation channel it refuses cloning. The CLI's readable path asks for explicit consent; JSON apply is the trusted adapter transport and requires the accepted preview plan ID, which detects drift but is not evidence of consent by itself.

An unambiguous matching source checkout is returned as `status: reused` without approval or mutation, including dirty checkouts and non-default branches. HTTPS/SSH origins can identify the same repository without rewriting the remote. Linked worktrees, bare/invalid repositories, ambiguous origins and multiple matching checkouts are not silently adopted. Reuse never fetches, pulls, switches branches, resets, or cleans files.

Apply revalidates the plan/root identity, exclusively reserves the destination, and runs fixed Git argv with hooks disabled for the operation. It does not initialize submodules or run setup/install scripts. Git uses trusted host authentication and transport configuration; Radar neither exports credentials to the sandbox nor installs/logs in to resolve failures. Trusted Git helpers/configuration can affect transport, so this is not a general network-isolation guarantee.

Commands are cancellable and bounded to ten minutes; captured inspection stdout is capped and Git stderr is not exposed. Failure/cancellation returns `ok: false` and a `partial_path` when present. Partial directories are **not recursively removed or blindly retried**; inspect them before recovery. Completed cloning returns `status: cloned` and the canonical source path.

Then refresh workspace context, inspect refs and reconcile the desired branch. Cloning itself changes no worktree membership, branch selection, sandbox mounts or ports.

## Cleanup and expiry

Cleanup previews reject primary repository checkouts, inspect local changes, and plan eligible Radar-owned branch deletion. Local changes, unpublished commits, and unavailable publication verification block conservative GC. The provider description explains all manual-cleanup effects, and the selected task's confirmed `radar cleanup <task-id>` / TUI `x` can explicitly discard local work.

`internal/workspacegc` owns retention and expiry, not the Git provider. Clean workspaces become eligible after 24 hours done; `radar gc` / TUI `X` can skip that initial wait. Only registered Radar workspaces expire after eight continuous days done (192 hours from the task's valid `DoneAt`). At the next hourly GC run after the deadline, expiry can discard local changes and unpublished commits in deletable Radar-owned branches, even when remote publication verification is unavailable. Unknown anchor content can also be discarded by the workspace provider. There is no recovery archive. Manual GC never shortens the eight-day destructive grace period.

Reopening cancels expiry. File timestamps, new local commits, and background activity do not extend the deadline. Standalone observed worktrees have no destructive expiry and keep conservative checks, even if they are linked to a task that also has a registered workspace.

Hard structural checks remain enforced after expiry: invalid member registrations, primary checkouts, unsafe locations, locked/uninspectable worktrees, and unidentified targets do not become removable through age. Protected/default branches and branches used by other worktrees are kept, as are all remote branches and primary repositories. An already-missing managed worktree loses only its stale registration; its branch and commits remain preserved. Canonical notes and external mount targets are outside destructive cleanup, and symlink targets are not followed.

Inspect uses the shared read-only expiry projection to show the timezone-labelled deadline and permanent-loss warning, or **Eligible since** for a past deadline. It does not execute Git commands or promise deletion at an exact time. Existing already-done workspaces use their existing completion timestamps, with no install/startup grace or schema/configuration change; review local data before installing. See [workspace cleanup and rollout](../workspace-cleanup.md#workspace-expiry).

## Validation

```sh
go test ./internal/integration/git
```
