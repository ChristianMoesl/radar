# Radar

Radar is a CLI-first tool for keeping track of engineering work that needs your attention. It combines a terminal UI, scriptable commands, a background daemon, GitHub/Jira/Git/tmux/sbx collection, and workspace creation in one Go binary.

## Install

Download the matching archive from the [latest release](https://github.com/ChristianMoesl/radar.nvim/releases/latest), verify it with `checksums.txt`, and place the binary on your `PATH`:

```sh
archive=radar_<version>_<os>_<arch>.tar.gz
grep -F "  $archive" checksums.txt | shasum -a 256 -c -
tar -xzf "$archive"
install -m 0755 "${archive%.tar.gz}/radar" ~/.local/bin/radar
radar version
```

## Update

Download the new release archive, verify it with `checksums.txt`, and install it over the old binary. Run `radar restart` after updating if the daemon is already running.

## Development setup

Install the local development tools:

```sh
brew install go fd git tmux neovim
curl -fsSL https://pi.dev/install.sh | sh
```

Linux developers also need `xdg-open`, usually provided by the system `xdg-utils` package:

```sh
sudo apt-get install xdg-utils
```

Build, test, and install a local Radar binary:

```sh
make test
make build
make install
radar version
```

## Build

```sh
make build
```

Install a local build:

```sh
make install
```

## Release

Releases are tag-driven. To publish versioned Linux and macOS binaries from a clean, up-to-date `main`:

```sh
make release VERSION=v0.1.0
```

The release script tests, builds the release archives, creates a signed annotated tag, and pushes it. The release workflow then publishes `linux/amd64`, `linux/arm64`, `darwin/amd64`, and `darwin/arm64` tarballs, plus `checksums.txt`, with generated notes from the changes since the previous tag.

Release assets should not be replaced after publishing. If a release is wrong, publish a new patch version.

The sandbox image is released separately because it packages frequently updated tools such as Pi, Node, pnpm, and gh. The sandbox image workflow runs weekly and can be triggered manually. It publishes:

```text
christianmoesl/radar-sandbox:YYYY.MM.DD
christianmoesl/radar-sandbox:latest
```

Publishing the sandbox image requires the `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN` GitHub secrets.

## Prerequisites

Radar uses these local tools:

- `fd` for fast repository discovery in `radar create`
- `git` for repository and worktree operations
- `tmux`, `pi`, and `nvim` for workspace creation
- `sbx` on macOS for repositories that enable sandboxed Pi sessions

Radar opens task URLs with the platform URL opener when you press `o` and choose a URL-backed source such as Jira or GitHub:

- Linux: requires `xdg-open`, usually provided by `xdg-utils`
- macOS: uses the built-in `open` command

## Run

Start the daemon:

```sh
./radar daemon
```

Open the terminal UI:

```sh
./radar
```

Open Radar in a tmux popup from tmux:

```sh
tmux display-popup -E "radar"
```

Useful tmux bindings:

```tmux
bind-key R display-popup -E "radar"
bind-key F display-popup -E "radar fork"
```

The TUI supports:

```text
j / down     move down
k / up       move up
enter        switch tmux session when connected
o            open task link/action, then press g for GitHub, j for Jira, or s for sbx
i            inspect selected task
c            create workspace
d            delete selected workspace after confirmation
D            delete current open workspace after confirmation
f            edit config
r            refresh
R            reset local state and refresh
q / esc      quit
```

The create flow is step-by-step: fuzzy search a repository, fuzzy search a base branch, then enter the workspace name. Repository paths are displayed as `~/...` when they are inside your home directory.

## Workspaces

Open the interactive workspace creation flow:

```sh
./radar create
```

Create a workspace non-interactively:

```sh
./radar create --repo /path/to/repo --base origin/main --name my-feature
```

Radar creates:

- a Git worktree at `<workspace_root>/<repo>/<name>`
- a sanitized branch named after the workspace
- files copied from the source repo when configured
- setup commands run in the new worktree when configured
- a matching tmux session
- `pi` and `nvim` tmux windows

Configure repo-specific workspace setup with a repo-local `.radar.json` file:

```json
{
  "copy_files": [".env", ".env.local"],
  "setup": ["pnpm install --frozen-lockfile"],
  "sandbox": {},
  "model": "anthropic/claude-sonnet-4",
  "thinking": "high"
}
```

`copy_files` paths are relative to the repository root. `setup` commands run in order from the new worktree before tmux windows are created. On macOS, if `sandbox` is configured in either `.radar.json` or the user config, Radar creates an SBX sandbox for the workspace with `sbx create --template <sandbox_template>`, mounts the workspace plus `~/.pi/agent` read-only with `~/.pi/agent/sessions` writable, and starts only the `pi` tmux window through `sbx exec`. The `nvim` window and any ordinary shell windows remain on the host. `model` and `thinking` are passed to Pi as `--model` and `--thinking` for the workspace session.

Enable sandboxes by default and configure the SBX template image in the user config at `radar config-path`:

```json
{
  "sandbox": {},
  "sandbox_template": "christianmoesl/radar-sandbox:latest"
}
```

When run inside tmux, Radar switches to the new session.

Fork the current tmux workspace into a sibling workspace and fork its Pi session:

```sh
./radar fork
```

`radar fork` detects the current git worktree and tmux session, asks for the base branch with the current branch prefilled, asks for the new workspace name, starts Pi with `--fork`, and switches to the new tmux session.

Delete a clean workspace:

```sh
./radar delete --path <workspace_root>/<repo>/my-feature
```

Delete only a tmux session:

```sh
./radar delete --session <tmux-session-name-or-id>
```

Workspace deletion refuses dirty worktrees. There is intentionally no force flag yet; keep the command path conservative until the TUI has a confirmation flow.

## Scriptable commands

```sh
./radar status
./radar tasks
./radar refresh
./radar reset
./radar stop
./radar restart
./radar config-path
./radar state-path
./radar log-path
```

Task commands return JSON.

## Architecture

Radar is a single Go binary with three modes:

- terminal UI, opened by `radar`
- scriptable commands, such as `radar status` and `radar create`
- daemon mode, started with `radar daemon`

```text
TUI / CLI -> Unix socket -> radar daemon -> source-compiled integrations
```

The daemon keeps collection centralized so UI/status reads can use cached local state instead of polling external services repeatedly. It refreshes local Git/tmux/sbx state every 15 seconds and runs a full GitHub/Jira/Git/tmux/sbx refresh every 5 minutes.

For internals, see [ARCHITECTURE.md](ARCHITECTURE.md). For how Radar decides what needs attention, see [docs/attention-algorithm.md](docs/attention-algorithm.md). To add a source-compiled integration, see [docs/integrations.md](docs/integrations.md).

## GitHub

GitHub integration uses the GitHub CLI. Make sure this works first:

```sh
gh auth status
```

Radar currently tracks:

- PR review requests assigned directly to you as `needs attention`
- open PRs authored by you as `in progress`

Radar checks GitHub rate limits before collection. When a budget is low, Radar pauses GitHub collection until GitHub's reset time. TUI and CLI status reads use cached daemon state and do not trigger GitHub requests.

## Jira

Radar can collect assigned Jira Cloud issues and attach them to matching tasks by ticket key, e.g. `ABC-123`.

Configure credentials through the environment:

```sh
RADAR_JIRA_BASE_URL="https://your-site.atlassian.net"
RADAR_JIRA_EMAIL="you@example.com"
RADAR_JIRA_API_TOKEN="..."
RADAR_JIRA_CLOUD_ID="..."
# alternatively: RADAR_JIRA_API_BASE_URL="https://api.atlassian.com/ex/jira/<cloud-id>/rest/api/3"
```

The current JQL is:

```sql
assignee = currentUser() AND statusCategory != Done ORDER BY updated DESC
```

## Git worktrees

Radar can collect Git worktree information and attach it to matching tasks by ticket key, e.g. `ABC-123`.

Configure repositories with:

```sh
RADAR_GIT_REPOS=/path/to/repo:/path/to/another/repo ./radar daemon
```

If unset, Radar tries the daemon's current working directory.

## tmux sessions

Radar collects tmux sessions from the local tmux server and attaches them to matching tasks when their name contains a ticket key, or when the session working directory matches a Git worktree path. Sessions without matches are shown as standalone in-progress tasks.

Tmux session refs use `#{session_id}` for stable identity, so renaming a tmux session does not create a new Radar task. Selecting a tmux-backed task switches to the stable session target.

## Docker sbx sandboxes

Radar collects Docker sbx sandboxes with `sbx ls --json` when `sbx` is installed. Sandboxes attach to matching tasks through ticket keys in the sandbox/workspace name and through their primary workspace path. Sandboxes without matches are shown as standalone in-progress tasks.

The default sandbox template is `christianmoesl/radar-sandbox:latest`. Pin a date tag such as `christianmoesl/radar-sandbox:2026.06.25` in the user config when you want a reproducible tool snapshot.

## Config

Radar uses one editable JSON config file:

```sh
./radar config-path
```

By default this is `$XDG_CONFIG_HOME/radar/config.json` or `~/.config/radar/config.json`.
The daemon creates an example file on startup if it does not exist yet.

Example:

```json
{
  "repository_dirs": ["~/workspace", "~/code", "~/src", "~/dev", "~/projects"],
  "workspace_root": "~/workspaces",
  "model": "github-copilot/claude-sonnet-4.5",
  "thinking": "medium",
  "filters": {
    "mute_repos": ["some-org/noisy-repo"],
    "deprioritize_repos": ["some-org/archive-*"],
    "mute_users": ["dependabot[bot]"],
    "deprioritize_users": ["renovate[bot]"],
    "rules": [
      {
        "name": "Track bot PRs in owned repos",
        "repos": ["some-org/platform-*"],
        "users": ["renovate[bot]", "dependabot[bot]"],
        "action": "deprioritize"
      }
    ]
  }
}
```

`repository_dirs` controls where `radar create` discovers base repositories. `workspace_root` controls where Radar creates worktrees. `model` and `thinking` are passed to Pi as `--model` and `--thinking` for new workspace sessions unless the repository's `.radar.json` defines its own values.

Muted tasks are hidden from the TUI and counts. Deprioritized tasks move to the low-priority section. Repository and user patterns support `*` wildcards, and rule matches are case-insensitive.

## Local state

The daemon stores durable task records and source-ref records locally. Task IDs are Radar-owned integers assigned from this local state, while CLI/TUI tasks are rebuilt as disposable projections.

Radar groups work ticket-first when a Jira-style key is present, then by local workspace, then by PR/issue/source-ref identity. Done state and acknowledgement state are kept on durable task records instead of being inferred from the latest projected task.

Use `./radar reset` or `R` in the TUI to delete this state and collect everything again from scratch.

```sh
./radar state-path
```

By default this is `$XDG_STATE_HOME/radar/tasks.json` or `~/.local/state/radar/tasks.json`.

Override it with `RADAR_STATE=/path/to/tasks.json`.

## Logs

The daemon writes logs to:

```sh
./radar log-path
```

By default this is `$XDG_STATE_HOME/radar/radar.log` or `~/.local/state/radar/radar.log`.

Follow logs with:

```sh
tail -f "$(./radar log-path)"
```

Override the log path with `RADAR_LOG=/path/to/radar.log`.

Set log level with:

```sh
RADAR_LOG_LEVEL=debug ./radar daemon
```

Supported levels: `debug`, `info`, `warn`, `error`. Default is `info`.
