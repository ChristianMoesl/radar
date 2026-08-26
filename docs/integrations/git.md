# Git integration

Git supplies local code-workspace facts and worktree cleanup.

## Capabilities

`Source`, `StatusReporter`, `LocalSource`, `WorkspaceProvider` for current code-workspace detection, and `CleanupProvider`.

## Configuration

`repository_dirs` controls repository discovery. Linking marks come from the shared allowlist. Git remote and branch normalization uses source-neutral linking helpers rather than GitHub identity code.

## Collection and refs

Local refreshes inspect configured repositories and emit `git:worktree:<absolute-path>` refs. Unmanaged worktrees set `ProvidesWorkspace`; registered members carry their workspace ID and link to the managed anchor without competing as the entry workspace.

Cleanup previews reject main working trees, inspect local changes, and plan Radar-owned branch deletion. Safety items block automatic cleanup for local changes, unpublished commits, or unavailable publication checks. The provider description explains all manual-cleanup effects.

## Validation

```sh
go test ./internal/integration/git
```
