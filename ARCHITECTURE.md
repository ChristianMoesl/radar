# Architecture

Radar is a CLI-first Go application with a terminal UI, scriptable commands, workspace management, and a shared backend daemon.

## Components

- `cmd/radar/`: single Go binary with TUI, CLI, and daemon modes.
- `internal/tui/`: Bubble Tea terminal UI.
- `internal/workspace/`: repository discovery and Git worktree/tmux workspace lifecycle.
- `internal/server/`: Unix socket API used by TUI and CLI commands.
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
TUI / CLI -> Unix socket -> radar daemon -> collectors
```

All frontends share the same daemon and state. This avoids duplicated polling and keeps interactive and scriptable status reads fast.

The binary is intentionally single-file from a user perspective:

```sh
radar
radar daemon
radar status
radar tasks
radar refresh
radar reset
radar stop
radar restart
radar create --repo <repo> --base <branch> --name <name>
radar delete --path <workspace-path>
```

## Communication

The TUI and scriptable commands do not call integrations directly. They talk to the daemon over a Unix socket.

The socket protocol is newline-delimited JSON with a tiny request model:

```json
{ "method": "tasks" }
{ "method": "summary" }
{ "method": "refresh" }
{ "method": "reset" }
```

## Task model

Radar separates source-system facts from the user-facing task shown in the UI:

```text
SourceRef + TaskRecord => Task
```

- `SourceRef`: a normalized reference/fact from a source system, such as a GitHub PR, Jira issue, local git worktree, or tmux session. Source refs have source-stable IDs like `github:pr:owner/repo:123`, `jira:issue:DPSCAP-544`, `git:worktree:<path>`, or `tmux:session:<session_id>`.
- `TaskRecord`: persistent Radar-owned tracking state. It gives continuity across refreshes and will own local state such as stable numeric task IDs, known source ref IDs, first/last seen timestamps, and acknowledgements.
- `Task`: the current projected user-facing task served to the CLI/TUI. It has a Radar-owned integer ID and is computed from current source refs plus the matching task record.

The target pipeline is:

```text
collect SourceRefs
→ match/update TaskRecords
→ project Tasks
→ serve/cache Tasks
```

The current implementation assigns stable integer task IDs by matching new projections against the previous task cache by source ref IDs and ticket keys. It still persists the latest projected tasks as the cache/state boundary. The next state-model step is to introduce explicit `TaskRecord`s and make projected `Task`s cache/output rather than durable source of truth.

## Task lifecycle

Radar has three active categories and one historical category:

- `immediate`
- `attention`
- `in_progress`
- `done`

Ingestion and linking are separate steps. Ingestion code talks to external systems and produces raw active tasks/source refs. Linker code connects source refs from different sources into one user-facing task.

`done` is derived from previously tracked tasks. If a task was active in the local store and later disappears from the active collector result, the relevant integration checks the remote state. If the remote task resolved today, Radar moves it to `done`.

## Local state

The daemon currently stores the latest known projected tasks on disk:

```text
$XDG_STATE_HOME/radar/tasks.json
```

This allows fast startup and lets the TUI show cached information immediately. The stored model will eventually move to explicit task records plus an optional task cache.

## Config

Config is user-owned JSON, not daemon state:

```text
$XDG_CONFIG_HOME/radar/config.json
```

The daemon creates an example file on startup when it is missing. The TUI exposes it with `f`.

The config controls repository discovery roots, the workspace root, and filters. Filters are applied when serving tasks from the daemon, so CLI and TUI see the same view. Raw collected state stays unmodified on disk.

There are two filter effects:

- `mute`: hide the task and remove it from counts
- `deprioritize`: keep tracking the task, but move it to `low_priority`

## GitHub integration

GitHub access currently uses the `gh` CLI. Radar tracks GitHub core/search rate limits through `gh api rate_limit`. When a budget is low, Radar pauses GitHub collection until GitHub's reset time instead of repeatedly retrying.

Current GitHub collectors:

- review requests assigned directly to the user -> `attention`
- open PRs authored by the user -> `in_progress`

## Jira integration

Jira access uses Jira Cloud REST APIs. Assigned non-done issues become source refs and are linked to matching GitHub/Git/tmux work through ticket keys.

## Git worktrees

Git worktree integration collects configured local repositories and attaches worktrees to matching tasks by ticket key. Worktrees that do not attach to another task become standalone `in_progress` tasks.

## tmux integration

Tmux integration collects sessions from the local tmux server. Radar attaches sessions to matching tasks when their name contains a ticket key or when the session working directory matches a Git worktree path. Sessions that do not attach to another task become standalone `in_progress` tasks.

Open the TUI in a tmux popup with `tmux display-popup -E "radar"`. Selecting a tmux-backed task switches the current client by stable session ID.

## Workspaces

Workspaces absorb the useful workflow from `fork.nvim`. The application layer discovers repositories under configured repository directories, creates Git worktrees under the configured workspace root, copies local setup files, and creates matching tmux sessions with `pi` and `nvim` windows.

Creation is available from the TUI and `radar create`. Deletion is intentionally conservative: `radar delete` refuses dirty worktrees and there is no force flag yet.

## Terminal UI

The Bubble Tea TUI is the default interface. It reads cached daemon state, groups tasks by attention, shows source details, switches tmux sessions, opens task URLs, edits config, refreshes/resets state, and launches step-by-step workspace creation.

## Logging

Daemon logging goes through `internal/logging` and writes to the user state directory by default. Routine refresh details should stay at debug level so normal logs remain readable.
