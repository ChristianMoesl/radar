# macOS SBX Pi Session Plan

## Scope

- macOS only.
- Use `sbx`, not `sbx.exe`.
- Do not handle WSL paths or `/wsl.localhost/...` paths.
- Only run the Pi tmux window inside SBX.
- Do not sandbox ordinary Radar shell windows yet.
- Mount Pi auth directly for this first version.

## Target behavior

When Radar creates a new workspace and tmux session, the Pi window should run Pi inside an SBX sandbox for that workspace.

The sandbox should have access to:

- the workspace path
- the host Pi agent directory as read-only state
- the host Pi sessions directory as writable state

This should support both Pi providers:

- `openai-codex/*`
- `github-copilot/*`

## SBX sandbox creation

On macOS, Radar should create a sandbox with the Pi agent directory mounted read-only and the sessions directory mounted writable. SBX requires mount paths to be directories, so mount `~/.pi/agent` instead of the individual auth file:

```sh
sbx create \
  --name <sandbox-name> \
  --template christianmoesl/radar-sandbox:latest \
  shell <workspace-path> \
  ~/.pi/agent:ro \
  ~/.pi/agent/sessions
```

Only the sessions directory should be writable from inside SBX. Pi configuration, prompts, extensions, helper binaries, and auth state should not be silently modified by sandboxed processes.

## Pi tmux command

Radar should create the Pi tmux window with an SBX exec command like:

```sh
sbx exec \
  --workdir <workspace-path> \
  <sandbox-name> \
  sh -lc 'PI_CODING_AGENT_DIR="$HOME/.pi/agent" pi --model <model>'
```

If the mounted auth file is exposed at the host path on macOS, use:

```sh
PI_CODING_AGENT_DIR=/Users/<user>/.pi/agent
```

## Radar implementation shape

### 1. Add a macOS-only SBX Pi path

Only enable this behavior when:

```go
runtime.GOOS == "darwin"
```

No Linux or WSL support in this feature.

### 2. Use deterministic sandbox names

Use Radar's existing workspace naming convention, for example:

```text
radar-<workspace-slug>
```

The name should be deterministic so Radar can reconnect to existing sandboxes and tmux sessions.

### 3. Workspace creation flow

When creating a new workspace:

1. Clone or create the workspace as Radar does today.
2. Create the SBX sandbox for that workspace.
3. Mount the workspace path.
4. Mount `~/.pi/agent` read-only and `~/.pi/agent/sessions` writable.
5. Create the tmux session.
6. Start the Pi window through `sbx exec`.
7. Leave other tmux windows unchanged.

### 4. Auth behavior

Pi should use the mounted host auth file through `PI_CODING_AGENT_DIR`.

This lets Pi read provider credentials and write conversation sessions while preventing sandboxed processes from changing persistent host Pi behavior.

## Non-goals

- No SBX credential proxy integration yet.
- No synthetic `auth.json` generation.
- No Linux support.
- No WSL support.
- No sandboxed shell window.
- No Docker CLI fallback.
- No path translation beyond macOS host paths.

## Risks

- Mounting `auth.json` exposes Pi OAuth tokens to the sandbox.
- Mounting `~/.pi/agent` exposes more than `auth.json`, but the read-only mount prevents persistent modification of host Pi behavior.
- Read-only auth mounts may break token refresh flows that need to update credentials.

## Validation

After implementation, test in a new Radar workspace with:

```sh
pi --model openai-codex/gpt-5.4-mini
pi --model github-copilot/gpt-5-mini
```
