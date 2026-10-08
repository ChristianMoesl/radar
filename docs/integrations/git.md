# Git integration

Git supplies local code-workspace facts and worktree cleanup.

## Capabilities

`Source`, `StatusReporter`, `LocalSource`, `WorkspaceProvider` for current code-workspace detection, and `CleanupProvider`.

## Configuration

`repository_dirs` controls repository discovery. Linking marks come from the shared allowlist. Git remote and branch normalization uses source-neutral linking helpers rather than GitHub identity code.

## Collection and refs

Radar stores registered Git members under a stable workspace anchor, using `<workspace_root>/<workspace>/<repo>--<branch>`. It sanitizes the repository and workspace names as one path component. Names longer than 120 characters are truncated and receive a deterministic eight-character hash suffix. Registered members emit a shared `workspace-group:<id>` linking key, so worktrees from different repositories appear in one task even without a configured linking mark. Radar also attaches worktrees by configured linking marks such as `ABC-123`. Regular repositories outside the configured workspace root are ignored. Branch names do not affect collection, so a workspace checked out directly on `main` remains visible.

Local refreshes inspect configured repositories and emit `git:worktree:<absolute-path>` refs. Unmanaged worktrees set `ProvidesWorkspace`; registered members carry their workspace ID and link to the managed anchor without competing as the entry workspace.

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
