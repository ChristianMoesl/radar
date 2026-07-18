# macOS SBX Tool Execution Plan

## Scope

- macOS host only.
- Use `sbx`, not `sbx.exe`.
- Keep Pi and tmux on the host.
- Execute Pi's built-in tools in SBX through the separately installed [`pi-sbx`](https://github.com/ChristianMoesl/pi-sbx) extension.
- Do not mount Pi configuration, credentials, extensions, or sessions into the sandbox.

## Target behavior

When Radar creates a workspace with sandboxing enabled, it should:

1. Create the Git worktree.
2. Create an SBX shell sandbox that mounts the workspace and its external common Git directory when needed.
3. Create the tmux session on the host.
4. Start Pi normally in the host-side `pi` window.
5. Let `pi-sbx` discover the sandbox from its workspace mount and route tool calls into it.
6. Start nvim normally in its host-side window.

Pi provider authentication, extension loading, and session persistence remain entirely on the host.

## Prerequisite

Install `pi-sbx` globally before using Radar's sandbox option:

```sh
pi install git:github.com/ChristianMoesl/pi-sbx
```

The extension fails tool calls closed when no sandbox mounts Pi's current working directory. Its `/sbx` command can switch between multiple matching sandboxes.

## SBX sandbox creation

Radar creates the sandbox with the workspace and the writable common Git directory used by a linked worktree:

```sh
sbx create \
  --name <sandbox-name> \
  --template christianmoesl/radar-sandbox:latest \
  shell <workspace-path> <common-git-dir>
```

The common Git directory is omitted when it is already inside the workspace, as with a repository's main working tree. Linked worktrees contain an absolute `.git` pointer into this directory, so it must be mounted at the same host path for status, staging, commits, objects, and refs to work inside SBX. The deterministic sandbox name uses the workspace slug plus a short hash suffix and remains at or below 63 characters.

## Pi tmux command

Radar starts Pi directly on the host:

```sh
pi --model <model> --thinking <level> --session-id <session-name> --name <session-name>
```

Radar does not pass `--approve`; normal Pi project trust applies. It does not set `PI_CODING_AGENT_DIR` or `PI_CODING_AGENT_SESSION_DIR` because Pi uses its normal host configuration.

## Sandbox image

The Radar sandbox image contains development tools used by tool calls, including Node and pnpm. It does not install Pi because the agent process no longer runs in the image.

## Non-goals

- No Linux or WSL host support in Radar's workspace sandbox flow.
- No Docker CLI fallback.
- No automatic installation or upgrade of `pi-sbx`.
- No mounting of host Pi state.
- No path translation beyond macOS host paths.

## Validation

For a sandbox-enabled Radar workspace:

1. Confirm `sbx ls --json` lists the workspace first and the linked worktree's common Git directory as the only additional mount.
2. Confirm `git status` works through Pi's bash tool.
3. Confirm the tmux `pi` pane runs a host `pi` command, not `sbx exec`.
4. Confirm the Pi footer shows the matching `sbx` sandbox.
5. Run `uname -s` through Pi's bash tool and confirm it returns `Linux`.
6. Confirm Pi provider authentication and session files remain available on the host without Pi mounts in SBX.
