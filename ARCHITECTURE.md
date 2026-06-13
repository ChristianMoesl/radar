# Architecture

`radar.nvim` is split into a small Neovim UI plugin and a Go backend daemon.

## Components

- `lua/radar/`: Neovim integration, statusline component, notifications, and floating detail window.
- `cmd/radar/`: single Go binary with CLI and daemon mode.
- `internal/server/`: Unix socket API used by Neovim and CLI commands.
- `internal/collector/`: orchestrates ingestion, linking, and resolution.
- `internal/github/`: GitHub ingestion and remote state resolution.
- `internal/git/`: Git worktree ingestion.
- `internal/jira/`: Jira Cloud issue ingestion.
- `internal/tmux/`: tmux session ingestion.
- `internal/linker/`: connects ingested source refs to user-facing tasks.
- `internal/state/`: local persistent task cache/state.

## Process model

There is one long-running daemon per user:

```text
Neovim sessions -> radar CLI -> Unix socket -> radar daemon -> collectors
```

Multiple Neovim sessions share the same daemon and state. This avoids duplicated polling and keeps statusline reads fast.

The binary is intentionally single-file from a user perspective:

```sh
radar daemon
radar status
radar tasks
radar refresh
radar stop
radar restart
```

## Communication

Neovim does not call GitHub directly. It shells out to `radar`, which talks to the daemon over a Unix socket.

The socket protocol is newline-delimited JSON with a tiny request model:

```json
{ "method": "tasks" }
{ "method": "summary" }
{ "method": "refresh" }
```

## Task model

Radar separates source-system facts from the user-facing task shown in the UI:

```text
SourceRef + TaskRecord => Task
```

- `SourceRef`: a normalized reference/fact from a source system, such as a GitHub PR, Jira issue, local git worktree, or tmux session. Source refs have source-stable IDs like `github:pr:owner/repo:123`, `jira:issue:DPSCAP-544`, `git:worktree:<path>`, or `tmux:session:<session_id>`.
- `TaskRecord`: persistent Radar-owned tracking state. It gives continuity across refreshes and will own local state such as stable numeric task IDs, known source ref IDs, first/last seen timestamps, and acknowledgements.
- `Task`: the current projected user-facing task served to the CLI/Neovim UI. It has a Radar-owned integer ID and is computed from current source refs plus the matching task record.

The target pipeline is:

```text
collect SourceRefs
→ match/update TaskRecords
→ project Tasks
→ serve/cache Tasks
```

The current implementation has completed the first naming step (`Entity` → `SourceRef`, `Item` → `Task`) and assigns stable integer task IDs by matching new projections against the previous task cache by source ref IDs and ticket keys. It still persists the latest projected tasks as the cache/state boundary. The next state-model step is to introduce explicit `TaskRecord`s and make projected `Task`s cache/output rather than durable source of truth.

## Task lifecycle

Radar has three active categories and one historical category:

- `immediate`
- `attention`
- `in_progress`
- `done`

Ingestion and linking are separate steps. Ingestion code talks to external systems and produces raw active tasks/source refs. Linker code connects source refs from different sources into one user-facing task. Collectors should not blindly import already-completed remote tasks.

`done` is derived from previously tracked tasks. If a task was active in the local store and later disappears from the active collector result, the relevant integration checks the remote state. If the remote task resolved today, Radar moves it to `done`.

This keeps `done` meaningful: it means “something Radar was tracking became resolved today.”

## Local state

The daemon currently stores the latest known projected tasks on disk:

```text
$XDG_STATE_HOME/radar/tasks.json
```

This allows fast startup and lets Neovim show cached information immediately. The stored model will eventually move to explicit task records plus an optional task cache.

## Filters

Filters are user-owned JSON config, not daemon state:

```text
$XDG_CONFIG_HOME/radar/filters.json
```

The daemon creates an example file on startup when it is missing. Neovim exposes it with `:RadarFilters` / `f` in the floating window.

Filters are applied when serving tasks from the daemon, so CLI and Neovim see the same view. Raw collected state stays unmodified on disk.

There are two filter effects:

- `mute`: hide the task and remove it from counts
- `deprioritize`: keep tracking the task, but move it to `low_priority`

Simple lists are supported for broad rules, and wildcard patterns (`*`) are supported for repository/user matches. Matching is case-insensitive.

```json
{
  "deprioritize_repos": ["some-org/archive-*"],
  "mute_users": ["*[bot]"]
}
```

For conditional cases, use ordered rules. A rule matches with AND semantics: if both `repos` and `users` are present, both must match. Later matching rules override earlier broad defaults, and `keep` can cancel a previous mute/deprioritize.

```json
{
  "mute_users": ["renovate[bot]"],
  "rules": [
    {
      "name": "Track renovate PRs in owned repos",
      "repos": ["company/platform-*"],
      "users": ["renovate[bot]"],
      "action": "deprioritize"
    }
  ]
}
```

This lets noisy bot PRs be hidden globally while still tracked as low-priority work in selected repositories.

Rules with both `repos` and exact `users` are also used as GitHub PR ingestion hints. Radar expands wildcard repository patterns by listing the owning org, caches that list for 24 hours in the user cache directory, and then searches exact repositories for open PRs by those users. This avoids broad org-wide PR searches for rules such as Renovate in owned repositories.

## GitHub integration

GitHub access currently uses the `gh` CLI. Radar tracks GitHub core/search rate limits through `gh api rate_limit`. When a budget is low, Radar pauses GitHub collection until GitHub's reset time instead of repeatedly retrying.

Current GitHub collectors:

- review requests assigned directly to the user -> `attention`
- open PRs authored by the user -> `in_progress`

Resolution:

- previously tracked authored PR disappears from open PRs
- daemon fetches that PR remotely
- if it was closed or merged today, it becomes `done`

## Jira integration

Jira integration uses the Jira Cloud REST API directly. Credentials are read from environment variables and are never written to Radar state:

```sh
RADAR_JIRA_BASE_URL=https://your-site.atlassian.net
RADAR_JIRA_EMAIL=you@example.com
RADAR_JIRA_API_TOKEN=...
RADAR_JIRA_CLOUD_ID=...
# alternatively: RADAR_JIRA_API_BASE_URL=https://api.atlassian.com/ex/jira/<cloud-id>/rest/api/3
```

The Jira collector emits issue source refs for assigned non-done tickets. These source refs are linked to GitHub PRs and Git worktrees by matching ticket keys such as `ABC-123`.

## Git integration

Git integration collects worktree source refs from configured repositories.

Configure roots with:

```sh
RADAR_GIT_REPOS=/path/to/repo:/path/to/another/repo radar daemon
```

If unset, Radar tries the daemon's current working directory.

Worktree source refs include path, branch, HEAD, dirty file count, and ahead/behind information when available.

The linker attaches worktrees to GitHub/Jira-style tasks by matching ticket keys such as `ABC-123` in titles, branches, paths, URLs, or metadata.

Worktrees that do not attach to another task become standalone `in_progress` tasks, except common base branches like `main`, `master`, `develop`, and `dev`.

## tmux integration

Tmux integration collects sessions from the local tmux server. Radar attaches sessions to matching tasks when their name contains a ticket key such as `ABC-123`. Sessions that do not attach to another task become standalone `in_progress` tasks.

## Neovim UI

The statusline is the primary UI and must stay cheap and glanceable.

The floating window is secondary and shows detailed task information. Opening it should use cached daemon state and must not block on network refreshes.

Manual refresh is available from the floating window with `r`. Periodic Neovim statusline updates only load cached daemon state; they must not trigger network refreshes.

## Logging

The daemon writes logs to:

```text
$XDG_STATE_HOME/radar/radar.log
```

Default log level is `info`. Routine polling details should stay at `debug` to avoid excessive disk logging.

Use:

```sh
RADAR_LOG_LEVEL=debug radar daemon
```

for development debugging.
