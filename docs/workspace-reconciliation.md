# Workspace reconciliation

Radar exposes one declarative agent mutation path: `radar_reconcile_workspace`.

## Contract

1. Call `radar_workspace_context` from the workspace anchor or any member directory.
2. Copy its revision and complete `desired` state.
3. Change only the requested resources.
4. Submit the original revision and modified state to `radar_reconcile_workspace`.
5. Radar previews the exact difference. The Pi adapter asks for interactive confirmation unless `workspace.auto_confirm` is enabled in the user config.
6. If host observations change the plan before apply, the Pi adapter applies the same confirmation policy to the replacement plan.

The CLI equivalent is:

```sh
radar reconcile-workspace --workspace <path> --request <json> --preview
radar reconcile-workspace --workspace <path> --request <json>
```

## Workspace model

Every managed workspace has a stable anchor below `workspace_root`:

```text
<workspace_root>/plan-authentication/
├── note.md
├── frontend--feature-auth/
└── api--feature-auth/
```

The anchor is Pi, tmux, nvim, and SBX's working directory. Members are real Git worktrees and direct children named from repository and branch. A workspace may contain zero members. No member is primary or protected because it was added first.

`<workspace_root>/.radar-workspaces.json` remains the single authoritative registry file. It stores every anchor, optional note path, runtime settings, sandbox intent, and worktree member. The registry is versioned and rejects the former primary-worktree schema rather than interpreting or migrating it implicitly.

## Desired state

```json
{
  "revision": "workspace-state-hash",
  "desired": {
    "worktrees": [
      {
        "repository": "/absolute/source/repository",
        "branch_mode": "existing",
        "branch": "feature/example"
      },
      {
        "repository": "/absolute/other/repository",
        "branch_mode": "new",
        "name": "feature-example",
        "base": "origin/main"
      }
    ],
    "sandbox": {
      "additional_mounts": [
        {"path": "/absolute/host/directory", "read_only": true}
      ],
      "ports": [
        {"host_port": 3000, "sandbox_port": 3000}
      ]
    }
  }
}
```

Worktrees, requested mounts, and ports use replacement semantics. Omitting a member removes it. `worktrees: []` is valid and returns the workspace to planning without removing its anchor or note.

Members use repository-and-branch identity. One workspace may contain several repositories or several branches from one repository, while a repository-and-branch pair may belong to only one registered workspace.

The note is not part of desired state. Radar owns `note.md` and the canonical note association, so the agent cannot remove either through reconciliation.

## Inspection

`radar_workspace_context` resolves the registry before trying Git. Calls from the anchor, `note.md`, a member root, or a nested member path return the same workspace ID. The result includes:

- `workspace_path` and workspace identity
- optional canonical and workspace note paths, without note contents
- revision and typed capabilities
- complete desired state
- every member's repository, path, branch, dirty status, instruction files, and skill paths
- current sandbox mounts and observed ports
- repositories discovered through Radar configuration

The tool returns only the current logical workspace, not every record in `.radar-workspaces.json`.

## Safety

- Revisions cover workspace, note, member, runtime, sandbox intent, and observed port state.
- Dirty worktrees cannot be removed.
- Removing a clean member deletes its local branch unless it is a protected default branch.
- Plans warn when publication cannot be checked or commits are absent from remote-tracking refs.
- Remote branches are never deleted.
- Additional mounts require absolute paths, default to read-only, and cannot replace Radar-managed mounts.
- Host ports are unique and published as TCP4 on IPv4 loopback.
- Sandbox attachment cannot be enabled or removed through reconciliation.
- A sandbox-less workspace requires `desired.sandbox: null`.
- Apply fails closed without an interactive confirmation channel unless `workspace.auto_confirm` is enabled.

## SBX mounts

Radar derives the effective managed mount set from:

1. the workspace anchor
2. the private canonical task directory, when present
3. one external Git common directory per repository represented by a member
4. global and member repository configured mounts
5. agent-requested mounts

Nested members are already visible through the anchor and are not mounted separately. Parent and duplicate mounts are reduced when their access mode is equivalent.

Changing the effective set removes and recreates the sandbox under the same name. This interrupts sandbox processes and discards private VM state. Radar retries transient create failures with bounded cleanup and backoff. It does not roll back completed note or worktree changes when SBX or port reconciliation fails.

## Pi resources

The embedded Radar extension contributes member `.pi/skills/` and `.agents/skills/` paths, injects path-labelled repository instruction files with repository-only scope, and reloads resources after membership changes without losing conversation history. Root instruction discovery is case-insensitive and selects the first available file in this order: `AGENTS.override.md`, `AGENTS.md`, `AGENT.md`, then `CLAUDE.md`. Generic documents such as `STYLEGUIDE.md` are loaded only when an instruction file tells the agent to read them. The extension reports additions, removals, duplicate skill names, and refresh failures. Member `.pi/settings.json`, extensions, prompts, and themes are not loaded.

## Cleanup

Cleanup order is tmux, SBX, Git members, then the workspace anchor. The anchor provider removes only Radar-owned entries and refuses unknown files. It never removes the canonical Obsidian note.
