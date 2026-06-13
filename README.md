# radar.nvim

`radar.nvim` is a small Neovim radar for keeping track of engineering work that needs your attention.

The goal is to show a tiny statusline summary like:

```text
Radar 🔴1 🟡3 🔵2
```

Where:

- 🔴 needs immediate attention
- 🟡 needs attention
- 🔵 is in progress

Details are available from Neovim with `:Radar`.

## Architecture

This project is split into two parts:

- `radar.nvim`: a small Lua Neovim plugin for the statusline and detail UI
- `radar`: a Go binary with CLI commands and a daemon mode

Neovim talks to the daemon through a Unix socket. This lets multiple Neovim sessions share the same state without each session polling GitHub/Jira/etc. independently.

```text
Neovim statusline -> radar.nvim -> Unix socket -> radar daemon -> GitHub/Jira/Git/tmux/etc.
```

## Current status

This is early scaffolding. The daemon currently uses the `gh` CLI to fetch GitHub review request notifications and open pull requests authored by you. Jira/Pi integrations will be added later.

## Build

```sh
go build -o radar ./cmd/radar
```

## Run

Start the daemon:

```sh
./radar daemon
```

Query it from the CLI:

```sh
./radar status
./radar tasks
./radar refresh
```

Stop or restart the daemon:

```sh
./radar stop
./radar restart
```

## GitHub

GitHub integration currently uses the GitHub CLI. Make sure this works first:

```sh
gh auth status
```

The daemon currently tracks:

- PR review requests assigned directly to you as `needs attention`
- open PRs authored by you as `in progress`

Radar checks GitHub rate limits before GitHub collection. When a budget is low, Radar pauses GitHub collection until GitHub's reset time. Neovim statusline polling reads cached daemon state and does not trigger GitHub requests.

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

## tmux panes

Radar can collect tmux panes and attach them to matching tasks by ticket key, e.g. `ABC-123` in the session name, window name, pane path, or pane title.

Radar reads the local tmux server with `tmux list-panes -a`. If tmux is not installed or no tmux server is running, the source is disabled.

## Filters

Radar can hide or deprioritize noisy repositories and users with an editable JSON file:

```sh
./radar filters-path
```

By default this is `$XDG_CONFIG_HOME/radar/filters.json` or `~/.config/radar/filters.json`.
Override it with `RADAR_FILTERS=/path/to/filters.json`.
The daemon creates an example file on startup if it does not exist yet.

Example:

```json
{
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
```

Muted tasks are hidden from the GUI and statusline counts. Deprioritized tasks move to the low-priority section. Repository and user patterns support `*` wildcards, and rule matches are case-insensitive. Rules use AND semantics across keys; if both `repos` and `users` are set, both must match.

Rules with both `repos` and exact `users` also drive GitHub PR ingestion: Radar expands wildcard repositories by listing the owning org, caches that repository list for 24 hours, then searches exact repositories for open PRs by those users. This keeps rules like Renovate-in-owned-repos narrow without broad org-wide PR searches.

In Neovim, use `:RadarFilters` or press `f` in the Radar window to edit the file. Changes are picked up on the next `:RadarRefresh`, periodic refresh, or Radar window reopen.

## Local state

The daemon stores the latest attention tasks locally. Task IDs are Radar-owned integers assigned from this local state.

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

Development logs use a pretty human-readable colored format with source locations by default. Routine refresh details are hidden unless debug logging is enabled.

Set log level with:

```sh
RADAR_LOG_LEVEL=debug ./radar daemon
```

Supported levels: `debug`, `info`, `warn`, `error`. Default is `info`.

Set `RADAR_ENV=production` to disable source locations. Set `RADAR_LOG_COLOR=0` to disable colored logs.

## Neovim setup

Example:

```lua
require("radar").setup({
  radar_cmd = "/path/to/radar",
})
```

Statusline example:

```lua
vim.o.statusline = vim.o.statusline .. "%{v:lua.require'radar'.statusline()}"
```

Commands:

```vim
:Radar
:RadarRefresh
:RadarFilters
:RadarStart
```
