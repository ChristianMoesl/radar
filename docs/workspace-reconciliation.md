# Workspace reconciliation

Radar exposes one declarative host mutation path for an agent: `radar_reconcile_workspace`.

## Contract

1. Call `radar_workspace_context` to obtain the current revision, capabilities, and complete `desired` state.
2. Copy the complete desired state and change only the requested resources.
3. Submit the original revision and modified desired state to `radar_reconcile_workspace`.
4. Radar previews the exact difference and requires interactive confirmation before apply.
5. If volatile host observations change the plan between preview and apply, the Pi adapter presents the updated plan and asks again, up to three confirmations. It never applies an unconfirmed plan.
6. Repeating the same request converges after partial failures or returns a stale-revision error when another durable workspace change won.

The scriptable equivalent is:

```sh
radar reconcile-workspace --workspace <path> --request <json> --preview
radar reconcile-workspace --workspace <path> --request <json>
```

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

Worktrees, agent-requested additional mounts, and ports use replacement semantics. An omitted member, requested mount, or port is removed. Worktree membership is identified by repository and branch, so one logical workspace may contain multiple branches from the same repository; each repository-and-branch pair must be unique. Radar derives worktree paths, sandbox names, tmux resources, Git common directories, and configured mounts rather than accepting them from the agent. Agent-requested mounts remain a separate typed set so they cannot remove Radar-managed mounts.

## Safety

- Revisions are compare-and-swap hashes over durable workspace membership and observed sandbox ports.
- The primary worktree cannot be removed.
- `radar_workspace_context` reports `dirty` for every member worktree.
- Dirty member worktrees cannot be removed by reconciliation. A blocked removal returns the `dirty_removal` reason, member identity, and changed-entry count, and explains whether to retain or clean the member.
- Existing member branches are retained by repository-and-branch identity; replacing a clean non-primary branch is planned as a worktree removal and addition rather than an in-place branch switch.
- Host ports are validated, unique, and published by SBX as TCP4 on IPv4 loopback only; reconciliation replaces existing dual-stack bindings.
- Additional mounts require absolute host paths, default to read-only, and require an explicit `read_only: false` for writable host access.
- Requested mounts cannot overlap mounts managed by worktree membership or Radar configuration.
- Sandbox attachment is immutable through reconciliation.
- A workspace without SBX reports `sandbox: false`, `additional_mounts: false`, `port_forwarding: false`, and `desired.sandbox: null`.
- Submitting sandbox state for a sandbox-less workspace fails validation and never enables SBX implicitly.
- The Pi adapter fails closed without an interactive confirmation channel.

## Persistence and convergence

`<workspace_root>/.radar-workspaces.json` remains the durable workspace registry. Its optional sandbox record stores agent-requested additional mounts, effective mounts, and desired ports. Radar also observes current worktrees, SBX mounts, and SBX published ports while planning so retries repair drift instead of trusting persistence alone.

Apply ensures added worktrees, removes clean omitted members, persists desired membership, reconciles sandbox mounts when present, reconciles the complete port set, and schedules member setup once. After a successful command apply, Radar asks the running daemon to refresh local sources immediately so newly added worktrees are linked without waiting for the periodic refresh. If the daemon cannot be reached, reconciliation remains successful and returns a warning.

Sandbox recreation waits for the removed runtime to disappear, allows a short cleanup interval, and retries transient SBX container-start failures up to three times with bounded backoff. Failed runtime remnants are removed between attempts. Sandbox and port failures return a structured retryable partial result without rolling back completed work; the Pi tool summarizes completed work and asks the agent to re-inspect before retrying.

Plans report the effective sandbox mount count. Plans that recreate a sandbox with 20 or more effective mounts warn that unusually large mount sets can make SBX recreation less reliable, but Radar does not impose a mount limit.

Reconciliation previews and apply phases are appended to `radar log-path`. Records include workspace and plan identifiers, completed worktree and port counts, sandbox creation attempts, effective mount counts, structured failure reasons, and retryable failure phases. Reconfirmation records include the expected and current plan IDs plus the confirmed and current change counts. Radar does not log the complete desired request, dirty filenames, diffs, or SBX create command, avoiding sensitive or oversized output.
