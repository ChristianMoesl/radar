# SBX integration

SBX supplies local Docker sandbox resources and shell actions.

## Capabilities

`Source`, `StatusReporter`, `LocalSource`, `RuntimeProvider`, `ActionProvider`, `InteractiveAuthenticator`, and `CleanupProvider`.

## Configuration and authentication

`sbx.enabled`, `sbx.kit`, `sbx.additional_mounts`, optional `sbx.env_file`, and optional `sbx.ready_command` configure managed runtimes. Repository-local settings use the same shape. Omitted `enabled` automatically enables new workspaces on macOS when `sbx` is on PATH. Explicit repository `enabled` overrides explicit global `enabled`; otherwise automatic detection applies. Explicit enablement with missing tools or unsupported platforms fails before provisioning; runtime/authentication failures never fall back to the host. On dashboard startup, Radar detects reported SBX authentication failures and runs the provider-owned login flow before opening the TUI. `radar create`, `radar fork`, and CLI cleanup of sandbox targets also check authentication before proceeding. The check is bounded and only authentication failures trigger login; missing tools, healthy sessions, and unrelated runtime failures do not. Successful startup login refreshes local sources. Background collection never prompts. You can also sign in manually with `sbx login`.

### Environment files

Pass one machine-local environment file through to SBX with Radar's user config:

```json
{
  "sbx": {
    "env_file": "~/.config/sbx/sandbox.env"
  }
}
```

The path must be absolute or begin with `~/`. Radar expands it to a host-native
absolute path and passes separate `--env-file` and path arguments to `sbx create`.
The selected file must exist, be regular, and be readable before provisioning or
destructive sandbox recreation. A missing file does not prevent unrelated
listing or cleanup. Radar does not interpret or log file contents, copy the file
into the sandbox, or expose it as a mount. SBX controls parsing and runtime
injection; the values are not baked into the image. For creation failures with
an env-file, Radar withholds raw SBX diagnostics because a parser error could
echo a private value; retry classification and attempt counts remain available.

A repository's `.radar.json` can select another file with the same field. Omission
inherits the user setting; `"env_file": ""` explicitly disables it. Multi-repository
creation uses the first member's repository settings, matching kit selection.
Only one file is selected, not a merged list.

The workspace registry stores the resolved path in `sandbox.env_file`, not the
contents. Missing-runtime recovery and mount-triggered recreation reuse that
recorded selection, including creation retries. Later user/repository setting
changes affect new workspaces only. Editing file contents does not change a
running sandbox or trigger recreation; the next actual creation reads the
current file. Existing version-2 registrations without this field remain valid
and keep no env-file. No automatic migration or backfill is performed. Restart
the Radar daemon after updating the binary so older processes do not drop the
new field when rewriting registrations.

A file can supply generic image inputs such as:

```dotenv
SBX_STARTUP_DIR=/absolute/host/startup.d
```

The directory must be mounted separately, preferably as a read-only requested
workspace mount. On macOS, mounts retain their host absolute paths, so the value
must name the sandbox-visible path. Radar does not expand expressions inside
the file. The development image includes the generic `sandbox-startup` runner;
the kit registers it through SBX's native `setup.startup` hook. Other images
must provide their own initialization. Keep machine-specific files untracked
and private keys in the host's signing agent.

### Optional readiness command

The default is no readiness command: **Radar performs no check and does not wait**.
To gate a workspace using the development kit's startup scripts:

```json
{
  "sbx": {
    "env_file": "~/.config/sbx/sandbox.env",
    "ready_command": ["sandbox-startup", "wait"]
  }
}
```

The command is an argv array, not shell text. Radar runs it with
`sbx exec --workdir <anchor> <sandbox> <argv...>` before repository setup and
before releasing auxiliary pane commands. **New sandboxed workspaces start and
switch to host Pi earlier**, after local note/worktrees/shared-directory setup
but before SBX creation. The originating Radar operation continues provisioning;
it still returns final completion, not an early success. Ordinary reopening,
recreation and missing-runtime recovery retain their sandbox-first ordering.
`sbx exec` starts a stopped sandbox first. Failure stops dependent work and is
reported without destroying the early Pi conversation. The bound is 60 seconds and the
parent context can cancel earlier. Raw command output and provider errors are
withheld to avoid logging private values. A readiness failure is not a create
failure: the existing sandbox is retained for inspection/retry, and repository
setup is not marked scheduled.

Repository `ready_command` overrides the user value; an explicit `[]` disables
an inherited command. Omission inherits it. The selected argv is recorded as
`sandbox.ready_command`, participates in the workspace revision, and survives
recovery/recreation independently of later user configuration. Existing
version-2 registrations without this optional field remain unchanged. Restart
the Radar daemon after upgrading before creating records with the new field.

The image's [startup hook contract](../../sandbox/README.md#startup-hooks)
executes regular executable files in byte-sorted filename order as UID 1000,
with no shell sourcing. Readiness is atomically recorded for the current VM
boot ID and PID-1 start time, not merely a persistent `ready` file. Script
failure stops later scripts; output stays in private sandbox-local state.
An unset `SBX_STARTUP_DIR` is an immediate no-op.

SBX native startup commands do not gate the agent's entrypoint, regardless of
`background: false`. Radar's command gates its auxiliary panes and repository
setup, **not the early Pi tools**. Tool readiness is independently owned by
**pi-sbx >=0.6.0**: a nonempty sandbox `SBX_STARTUP_DIR` requires the image's
`sandbox-startup wait --timeout 60` contract before the worker becomes ready.
The helper must exist and report current-boot readiness; failure never enables
implicit host execution. With no startup directory, pi-sbx only checks worker
readiness. Put every tool-critical prerequisite in the image-owned contract;
an arbitrary Radar `ready_command` alone cannot protect early tool calls.
Manual `sbx exec` callers still need to wait explicitly when readiness matters.

### Early Pi launch and recovery

Install `pi-sbx` in the host Pi profile (`pi install npm:@christianmoesl/pi-sbx`)
and keep its extension enabled. Radar's launch-only guard checks the **active**
`/sbx` command, ownership of all seven routed tools, and the owning package's
stable version, not just installation declarations. Missing, old, filtered-out,
or conflicting providers refuse the Pi launch with installation guidance; no
automatic host/slow-path fallback is selected. `/sbx off` remains explicit host
consent within a verified pi-sbx session. `pi-radar` remains separately recommended
for workspace tools/context, not responsible for routing.

Custom agent commands must invoke host Pi, forward `$RADAR_PI_ARGS` including
its required guard extension, and be safe to start before SBX exists. Do not
suppress the guard or do sandbox-dependent work in a wrapper preamble. All
non-agent pane commands keep their layout/cwd and wait on unique, one-shot tmux
gates. Their pending gate names live in pane user options; ordinary open or
reconciliation releases them after readiness, without replaying started commands.
A failed Pi launch retains its pane for diagnostics; normal successful exits
are unchanged. New creation will not reuse an unrelated existing tmux session
whose early-start guard cannot be verified.

If provisioning fails or the creator is terminated after switching, the note,
completed worktrees, early session and completed runtime resources remain for
inspection. Creation writes phase/failure diagnostics to `radar log-path`; inspect
that log and reconcile/open the retained workspace. An early switch explicitly
closes the dashboard popup so it no longer captures keyboard input. Popup
hangup/graceful termination dismisses the TUI but the same Radar process drains
the accepted creation operation before exiting. Force-killing that process or
canceling the operation still interrupts provisioning; there is no detached job.
Managed
open, reconciliation and cleanup serialize against creation; read-only context
inspection remains available. If SBX appears after pi-sbx's 60-second discovery
window, retry explicitly with `/sbx on`; earlier tool calls are never replayed.
Repository setup is still scheduled asynchronously, not awaited to completion.

No registry schema or configuration migration is introduced. The added launch
helper is content-addressed cache data, and pending pane gates are scoped to
fresh tmux panes rather than persisted workspace records.

## Windows / WSL2

Radar detects Windows Docker Sandboxes when running in WSL2. Install SBX for
Windows, enable WSL interoperability, and expose `sbx.exe` on the WSL PATH:

```sh
sbx.exe version
sbx.exe ls --json
```

A native `sbx` takes precedence if both are installed, matching `pi-sbx` discovery.
Radar selects one installation for each operation and never switches after a
runtime/authentication failure. No executable override or wrapper is required.
Automatic login uses that same selected executable: `sbx login` on macOS or
native Linux, and `sbx.exe login` for Windows SBX from WSL2. Both the Windows
authentication check and login run with `/` as their working directory; login
inherits the foreground terminal's stdin/stdout/stderr and process group. You
can also authenticate manually with `sbx.exe login`. Restart Radar's daemon if
its PATH predates the installation.

Supported operations are collection, task/workspace matching, opening sandbox
shells, and cleanup. `wslpath -u` translates Windows drive and WSL UNC mount paths
to local Linux paths, including custom drive mount roots and paths with spaces.
Read-only mount suffixes are preserved. Sandboxes in other WSL distributions
remain resources that can be opened/removed by name, but their paths do not link
to this distribution's workspaces. Other conversion failures report an error.
Shell text and paths passed to commands **inside** a sandbox are not rewritten.
Windows SBX processes run outside host workspace directories to avoid holding
Windows directory handles that prevent removal.

### Managed workspace limitation

**Detection does not mean managed workspace provisioning is supported.** A real
WSL2 preflight with Windows SBX v0.43.0 verified ordinary file reads/writes, but
both relative and absolute symlinks in WSL-mounted directories failed with
`Invalid argument` from `readlink`/`cat` inside the sandbox. Radar requires its
canonical `notes.md` symlink, so enabling workspace sandboxing would produce a
broken workspace. Windows SBX also exposes mounts at different sandbox paths
(`/wsl.localhost/<distribution>/...` or `/c/...`), affecting absolute Git worktree
pointers and shared-directory references.

New workspace sandboxing therefore remains macOS-only. Explicit enablement on
WSL reports the limitation before provisioning the workspace bundle; existing
Windows-backed managed workspaces cannot be opened/reconciled through Radar.
Collection, sandbox shell actions and cleanup remain available. Radar does not
rewrite host note links/Git metadata, install mount aliases, or silently execute
sandboxed setup on the host. No config, registry, or sandbox filesystem migration
is introduced.

Before enabling full WSL workspace support, validate canonical note links, Git
worktrees with external common directories, shared files, read-only mounts,
recreation, restart, and agent tool routing against a backend that supports the
required filesystem semantics. The separate `pi-sbx` extension's WSL support does
not by itself solve host symlink transport.

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

Authentication tests use fake CLIs to cover macOS and WSL2 executable selection,
Windows working directories, forwarded login streams, expired/healthy sessions,
login failures without backend fallback, and foreground entry points. They do
not authenticate a real account; the WSL2 dispatch tests can run on either OS.

```sh
go test ./cmd/radar ./internal/integration/sbx/... ./internal/integration/workspace/... ./internal/pi
# Extension runtime tests use Node.js with native TypeScript stripping (Node 22.18+).
# Optional macOS/SBX integration tests:
RADAR_SBX_E2E=1 go test ./internal/integration/workspace -run 'SharedDirectoryRoundTripE2E'
# Optional WSL2/Windows SBX test (creates and removes one disposable sandbox):
RADAR_SBX_WSL_E2E=1 go test ./internal/integration/sbx -run TestWindowsSBXLifecycleE2E -count=1
```
