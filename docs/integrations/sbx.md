# SBX integration

SBX supplies local Docker sandbox resources and shell actions.

## Capabilities

`Source`, `StatusReporter`, `LocalSource`, `RuntimeProvider`, `ActionProvider`, `InteractiveAuthenticator`, and `CleanupProvider`.

## Configuration and authentication

`sbx.enabled`, `sbx.kit`, and `sbx.additional_mounts` configure managed runtimes. Repository-local settings use the same shape. Omitted `enabled` automatically enables new workspaces on macOS when `sbx` is on PATH. Explicit repository `enabled` overrides explicit global `enabled`; otherwise automatic detection applies. Explicit enablement with missing tools or unsupported platforms fails before provisioning; runtime/authentication failures never fall back to the host. Sign in with `sbx login`. Only explicit cleanup can initiate the provider-owned interactive login flow; dashboard startup and creation never prompt.

## Published development kit

With SBX 0.43.0 or newer, enabling `sbx.enabled` selects `docker.io/christianmoesl/radar-kit:latest` for new workspaces unless a user or repository kit overrides it. No `kit.name` or `kit.path` is required to use this default. Set `sbx.kit.name` only to select another kit or pin a digest. The [sandbox guide](../../sandbox/README.md) documents its tool inventory, Pi requirements, private Docker daemon, publication and rollout.

Sandboxing is automatic on supported systems. Existing `sbx.enabled: false` values remain opt-outs; remove the field manually to adopt automatic detection. Explicit kit settings, including `"shell"` in an older generated configuration, are preserved; remove that override manually to opt into the new default. Existing workspaces retain their recorded kit even during sandbox recreation. No configuration or registry migration is performed.

## Clipboard images and shared screenshots

Radar provisions one private host directory per sandbox-backed workspace:

```text
<host temporary directory>/radar-workspaces/<workspace-id>/
```

The directory and its parent are created with mode `0700`. Only the workspace's child is mounted read/write, at the same absolute path in the sandbox. The shell image needs no special screenshot support: screenshot tools accept an explicit output path. Radar's image includes `file` so `pi-sbx` can recognize image MIME types when reading the resulting files; browser/screenshot tools remain project-specific dependencies.

The recorded `sandbox.shared_directory` is a managed resource, separate from agent-requested `additional_mounts`. New workspaces provision it during creation. An existing workspace without this resource keeps its current behavior until explicit workspace reconciliation provisions it. Preview is read-only and shows the new writable mount and any sandbox recreation; apply persists the path so later host `TMPDIR` changes do not relocate files.

The installed `pi-radar` extension sets **Pi's host-process `TMPDIR`**, on startup and `/reload`, only when workspace introspection confirms the directory exists and the running sandbox mounts it writable. Pi's Ctrl+V handler writes `pi-clipboard-<uuid>.<extension>` there. `RADAR_HOST_TMPDIR` preserves the original host temp root for Radar subprocesses, preventing nested workspaces from allocating directories inside each other's shared storage and keeping Radar's daemon socket and PID paths unchanged. Switching to a workspace without a ready shared directory, or shutting down the active Radar extension, restores the host temp root. Sandbox discovery and tool routing remain owned by `pi-sbx`.

Agent guidance names the shared directory for outgoing screenshots. Use unique filenames and pass the absolute output path to tools such as `playwright-cli screenshot`; do not change the sandbox's `TMPDIR`. This does not alter tmux, other host panes, or the sandbox image's environment. Pi's other temporary files also use this directory.

Shared files survive Pi exits and sandbox recreation but are **temporary**, not durable attachments: the OS may purge them. Workspace cleanup removes the recorded directory, including screenshots, after managed worktrees are removed. Save files worth retaining inside the workspace before cleanup. Cleanup rejects symlinked shared roots and never follows links inside the directory into another workspace.

### Existing installations

Before rollout, inventory `.radar-workspaces.json` and existing shared directories. An absent `shared_directory` means the resource is unprovisioned, not a guessed path; no note or registry reset is needed. Restart the Radar daemon after updating the binary so old processes do not rewrite newer workspace resource state. Reconcile each existing workspace through Radar, then `/reload` its Pi session. Reconciliation may restart the sandbox and interrupt its running processes.

Existing configured mounts are preserved. If configuration explicitly mounts the host's entire `/tmp`, review and remove that mount separately when its other uses are no longer needed; otherwise `/tmp` remains shared despite the new isolated directory. Update any personal screenshot instructions that hardcode `/tmp/screenshots` to prefer Radar's shared directory when available.

## Collection and refs

With SBX installed, local refreshes parse `sbx ls --json` and emit stable sandbox refs linked by name, mark, and primary workspace. Global `sbx.enabled: false` disables collection of unmanaged sandboxes, but registered workspace runtimes are still observed and remain available for explicit cleanup. This preserves existing workspace behavior and repository opt-ins. Changing defaults never adds or removes a sandbox from an existing registration. The runtime capability resolves provider-owned sandbox names. Shell actions use the registered multiplexer rather than invoking tmux themselves.

Cleanup descriptions and opaque resource IDs are provider-owned. Workspace reconciliation preserves mount, port, recreation, and failure behavior; failed runtime recreation does not roll back completed filesystem or Git changes.

## Validation

```sh
go test ./internal/integration/sbx ./internal/integration/workspace/... ./internal/pi
# Extension runtime tests use Node.js with native TypeScript stripping (Node 22.18+).
# Optional macOS/SBX integration tests:
RADAR_SBX_E2E=1 go test ./internal/integration/workspace -run 'SharedDirectoryRoundTripE2E'
```
