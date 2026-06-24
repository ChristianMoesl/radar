# Radar

Radar is a CLI-first tool for keeping track of engineering work that needs your attention. It combines a terminal UI, scriptable commands, a background daemon, GitHub/Jira/Git/tmux collection, and workspace creation in one Go binary.

## Build

```sh
go build -o radar ./cmd/radar
```

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
o            open task link, then press g for GitHub or j for Jira
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

`copy_files` paths are relative to the repository root. `setup` commands run in order from the new worktree before tmux windows are created. On macOS, if `sandbox` is configured, Radar creates an SBX sandbox for the workspace with `sbx create --template <sandbox_template>`, mounts the workspace plus `~/.pi/agent/auth.json`, and starts only the `pi` tmux window through `sbx exec`. The `nvim` window and any ordinary shell windows remain on the host. `model` and `thinking` are passed to Pi as `--model` and `--thinking` for the workspace session.

Configure the SBX template image in the user config at `radar config-path`:

```json
{
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
TUI / CLI -> Unix socket -> radar daemon -> GitHub/Jira/Git/tmux/etc.
```

The daemon keeps collection centralized so UI/status reads can use cached local state instead of polling external services repeatedly. It refreshes local Git/tmux state every 15 seconds and runs a full GitHub/Jira/Git/tmux refresh every 5 minutes.

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

Use `./radar reset` or `R` in the TUI to delete this state and ingest everything again from scratch.

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
