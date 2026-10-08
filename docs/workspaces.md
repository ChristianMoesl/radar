# Workspaces

[Documentation](README.md) · [Resource API](workspace-reconciliation.md) · [Cleanup](workspace-cleanup.md)

A workspace keeps a task note, zero or more Git worktrees, and its tmux/Pi
session together. Start with a plan and add code when you need it.

## Create a workspace

The workspace editor starts with a name and a draft. Add zero or more repositories, then press Enter to create and open the workspace without a confirmation dialog. Radar still validates the complete plan before applying it. Every workspace automatically gets a canonical Obsidian note exposed as `notes.md`. Press `w` to edit an existing workspace, `a` to add a repository, `x` to remove the selected repository. Repository addition uses repository search and branch selection. New branch names are prefilled once from the workspace name using Radar's branch-name sanitization. You can edit or clear the suggestion without changing the workspace name; Radar does not overwrite your edits. Each added repository starts with its own suggestion. It tries to refresh origin before listing branches. If that fetch fails, Radar shows a warning and continues with locally cached refs, so previously fetched branches remain available offline. Repository paths are shortened to `~/...` when they are inside your home directory.

Open the interactive workspace creation flow:

```sh
radar create
```

Create an empty workspace non-interactively:

```sh
radar create --name investigation
```

Create a Git-first workspace non-interactively:

```sh
radar create --repo /path/to/repo --base origin/main --name my-feature
```

## Files and notes

Radar creates one stable anchor with nested worktree members:

```text
<workspace_root>/my-feature/
├── notes.md -> <vault>/Tasks/my-feature--2c965c99/my-feature.md
└── repository--my-feature/
```

Pi, tmux, any configured editor, and optional SBX start in the anchor. Repo-specific copy and setup rules run in the member. Additional members become siblings and no member is primary.

Activating an Obsidian-only task prefills its note in the same workspace editor, without requiring repository selection:

```text
<workspace_root>/plan-authentication/
└── notes.md -> <vault>/Tasks/Plan authentication--2c965c99/Plan authentication.md
```

An automatically created note starts with an empty body. Note creation happens only during apply, after plan validation. A task-notes directory is required and has no enable/disable switch; an unavailable or unconfigured directory prevents creation before resources are provisioned. Obsidian itself is optional. If the task already has an authored note, Radar reuses it. Attached notes cannot be replaced or detached through workspace controls. The same Pi session remains active while the task moves between planning and zero or more Git members.

Radar stores every workspace record in the single `<workspace_root>/.radar-workspaces.json` registry. `radar reset` does not remove it. The current registry schema rejects old primary-worktree records rather than migrating them implicitly.

## Edit workspace resources

With `pi-radar` installed, Pi sessions started inside a registered Radar workspace receive three host tools:

- `radar_workspace_context` resolves the current anchor or member and returns its revision, complete desired state, note metadata, member status, sandbox resources, and discovered repositories.
- `radar_repository_refs` refreshes one selected repository when possible and returns canonical branches, base refs, and checkout paths.
- `radar_reconcile_workspace` previews and applies complete worktree, requested-mount, and port state. It applies validated plans automatically by default; set `workspace.auto_confirm` to `false` to require confirmation.

The context tool returns only the current logical workspace, not all registry records. It never returns note contents.

Creation and edits share the resource planner and apply engine. New workspace creation in the TUI applies the validated plan directly. Edits to existing workspaces still show a preview and confirmation, including dirty-worktree protection, unpublished-commit warnings, and sandbox recreation; this also applies when a creation request resolves to an existing workspace. A changed plan is never silently applied. The editor preserves mounts and ports; edit those through the existing agent tools or CLI. After manual membership changes, use `/radar-reload-workspace-resources` in an active Pi session to refresh member skills without restarting it.

Desired worktrees use replacement semantics. `worktrees: []` is valid. Omitting a clean member removes its worktree and eligible local branch while leaving the anchor, note, and Pi session intact. Dirty removals fail closed. Protected default branches and all remote branches remain untouched. Sandbox state must stay `null` for a sandbox-less workspace, and ordinary reconciliation cannot enable or remove SBX.

The same operations are scriptable:

```sh
radar workspace-context --workspace /path/to/workspace-or-member
radar repository-refs --repo /path/to/source-repository
radar reconcile-workspace --workspace /path/to/workspace-or-member --request "$request" --preview
radar reconcile-workspace --workspace /path/to/workspace-or-member --request "$request"
```

The request is `{"revision":"<context revision>","desired":<context desired state>}`. Append either `{"repository":"/repo","branch_mode":"new","name":"feature","base":"origin/main"}` or `{"repository":"/repo","branch_mode":"existing","branch":"feature"}` to add a member. Requested mounts default to read-only. Ports bind TCP4 on host IPv4 loopback.

## Existing branches

For an existing branch, Radar reuses its local branch or creates a same-named local branch that tracks `origin/<branch>`. Standalone workspace creation defaults the workspace name to the branch. Creation from a selected task keeps the task-derived workspace name, so sequential tasks can each use a shared branch such as `main` after the previous workspace is cleaned up. A branch already checked out in a Radar workspace reopens that workspace; Radar rejects attaching it to a different task until the existing workspace is cleaned up. To keep a normal source checkout for `.env` files, local services, and database setup while making `main` available to a Radar worktree, park the clean source checkout on the remote-tracking commit:

```sh
git fetch
git switch --no-overwrite-ignore --detach origin/main
```

Ignored development files remain in place. Radar refuses to move a branch that is still checked out in the source repository.

## Repository setup

Configure repo-specific workspace setup with a repo-local `.radar.yaml` file:

```yaml
# Copy these files from the source checkout into each new worktree.
copy_files:
  - .env
  - .env.local
# Run these commands in order in the new worktree, inside the sandbox when enabled.
setup:
  - pnpm install --frozen-lockfile
sbx:
  enabled: true
  additional_mounts:
    - ~/repo-tools
model: anthropic/claude-sonnet-4
thinking: high
```

`copy_files` paths are relative to the repository root. `setup` commands run in
order in the new member, in a temporary setup window after tmux and any sandbox
are ready. Without sandboxing they run on the host. `model` and `thinking`
override the user defaults for Pi.

On macOS, installed SBX enables sandboxing by default unless explicitly disabled.
Pi and editors still run on the host; pi-sbx routes Pi's tools into the sandbox.
See [SBX configuration, mounts, startup and recovery](integrations/sbx.md).

## Session layout

The default is one tmux window running Pi. Customize windows, panes and optional
editors with [tmux layouts](integrations/tmux.md#workspace-layout).

## Fork a workspace

Fork the current tmux workspace into a sibling workspace and fork its Pi session:

```sh
radar fork
```

`radar fork` detects the current git worktree and tmux session, asks for the base branch with the current branch prefilled, asks for the new workspace name, starts Pi with `--fork`, and switches to the new tmux session.

## Finishing work

Use `radar cleanup <task-id>` or `x` in the dashboard to preview and confirm
removal of a task's local resources. Remote issues/PRs and canonical notes stay.

**Completed workspaces are disposable.** Automatic cleanup starts after 24 hours
when safe. After eight continuous days done, registered workspaces can expire
**even with local changes or unpublished commits**, with no recovery archive.
Publish or move valuable work out, or reopen the task to cancel expiry.
Read [cleanup, expiry and safety](workspace-cleanup.md) before marking work done.
