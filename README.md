# Radar

Radar is a CLI-first tool for keeping track of engineering work that needs your attention. It combines a terminal UI, scriptable commands, a background daemon, GitHub/Jira/Git/tmux collection, and workstream creation in one Go binary.

## Build

```sh
go build -o radar ./cmd/radar
```

## Run

Start the daemon:

```sh
./radar daemon
```

Open the terminal UI:

```sh
./radar
```

Open Radar in a tmux popup:

```sh
./radar tmux popup
```

The TUI supports:

```text
j / down     move down
k / up       move up
enter        open task URL or switch tmux session
i            inspect selected task
c            create workstream
f            edit filters
r            refresh
R            reset local state and refresh
q / esc      quit
```

The create flow is step-by-step: fuzzy search a repository, fuzzy search a base branch, then enter the workstream name. Repository paths are displayed as `~/...` when they are inside your home directory.

## Workstreams

Create a workstream from the CLI:

```sh
./radar create --repo /path/to/repo --base origin/main --name my-feature
```

Radar creates:

- a Git worktree at `~/workstreams/<repo>/<name>`
- a branch named after the workstream
- a matching tmux session
- `pi` and `nvim` tmux windows

When run inside tmux, Radar switches to the new session.

Delete a clean workstream:

```sh
./radar delete --path ~/workstreams/<repo>/my-feature
```

Deletion refuses dirty worktrees. There is intentionally no force flag yet; keep the command path conservative until the TUI has a confirmation flow.

## Scriptable commands

```sh
./radar status
./radar tasks
./radar refresh
./radar reset
./radar stop
./radar restart
./radar filters-path
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

The daemon keeps collection centralized so UI/status reads can use cached local state instead of polling external services repeatedly.

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

Muted tasks are hidden from the TUI and counts. Deprioritized tasks move to the low-priority section. Repository and user patterns support `*` wildcards, and rule matches are case-insensitive.

## Local state

The daemon stores the latest attention tasks locally. Task IDs are Radar-owned integers assigned from this local state.

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
