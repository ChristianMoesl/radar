# Workspace reconciliation

Radar exposes one declarative host mutation path for an agent: `radar_reconcile_workspace`.

## Contract

1. Call `radar_workspace_context` to obtain the current revision, capabilities, and complete `desired` state.
2. Copy the complete desired state and change only the requested resources.
3. Submit the original revision and modified desired state to `radar_reconcile_workspace`.
4. Radar previews the exact difference and requires one interactive confirmation before apply.
5. Repeating the same request converges after partial failures or returns a stale-revision error when another change won.

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
      "ports": [
        {"host_port": 3000, "sandbox_port": 3000}
      ]
    }
  }
}
```

Both worktrees and ports use replacement semantics. An omitted member or port is removed. Radar derives paths, sandbox names, mounts, tmux resources, and Git common directories rather than accepting them from the agent.

## Safety

- Revisions are compare-and-swap hashes over durable workspace membership and observed sandbox ports.
- The primary worktree cannot be removed.
- Dirty member worktrees cannot be removed by reconciliation.
- Existing member branches cannot be changed in place.
- Host ports are validated, unique, and published by SBX on loopback.
- Sandbox attachment is immutable through reconciliation.
- A workspace without SBX reports `sandbox: false`, `port_forwarding: false`, and `desired.sandbox: null`.
- Submitting sandbox state for a sandbox-less workspace fails validation and never enables SBX implicitly.
- The Pi adapter fails closed without an interactive confirmation channel.

## Persistence and convergence

`<workspace_root>/.radar-workspaces.json` remains the durable workspace registry. Its optional sandbox record stores desired mounts and ports. Radar also observes current worktrees, SBX mounts, and SBX published ports while planning so retries repair drift instead of trusting persistence alone.

Apply ensures added worktrees, removes clean omitted members, persists desired membership, reconciles sandbox mounts when present, reconciles the complete port set, and schedules member setup once. Sandbox and port failures return a retryable partial result without rolling back completed work.
