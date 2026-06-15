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

The pipeline is:

```text
collect SourceRefs
→ match/update TaskRecords
→ project Tasks
→ serve Tasks
```

The local state file persists explicit `TaskRecord`s and `SourceRefRecord`s. `Task`s are disposable projections for the socket protocol, CLI, and TUI. Task records own durable identity, stable numeric task IDs, lifecycle state, source-ref ownership, and user acknowledgement state. Source refs remain source-system facts with first/last seen timestamps and an active flag.

Radar groups work ticket-first: if any linked source ref contains a ticket key, that ticket is the canonical work item. Without a ticket, local workspaces group by worktree path, then GitHub PRs, Jira issues, or standalone source refs become the canonical key. Each source ref is assigned to one task record at a time, so local and remote refs do not duplicate across multiple projected tasks.

## Task lifecycle

Radar has three active categories and one historical category:

- `immediate`
- `attention`
- `in_progress`
- `done`

Ingestion and linking are separate steps. Ingestion code talks to external systems and produces raw active tasks/source refs. Linker code connects source refs from different sources into one user-facing task.

`done` is a durable task-record state. If a tracked GitHub PR or Jira issue disappears from active collection, the relevant integration checks the remote state and emits a done transition. The state store applies that transition to the existing task record. If the same source ref becomes active again later, Radar reopens the same task record instead of creating a duplicate.

Local cleanup has explicit semantics: removing a tmux session only marks that source ref inactive, while removing a local worktree marks the local workspace record done when no GitHub or Jira source remains attached.

## Local state

The daemon stores durable task records and source-ref records on disk:

```text
$XDG_STATE_HOME/radar/tasks.json
```

Projected tasks are rebuilt from this state. The file also stores source statuses so the TUI can show cached status immediately. User acknowledgement state lives on task records, not inside source-ref metadata.

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

Workspaces absorb the useful workflow from `fork.nvim`. The application layer discovers repositories under configured repository directories, creates Git worktrees under the configured workspace root, applies repo-local `.radar.json` workspace setup, and creates matching tmux sessions with `pi` and `nvim` windows.

Creation is available from the TUI and `radar create`. Deletion is intentionally conservative: `radar delete` refuses dirty worktrees and there is no force flag yet.

## Terminal UI

The Bubble Tea TUI is the default interface. It reads cached daemon state, groups tasks by attention, shows source details, switches tmux sessions, opens task URLs, edits config, refreshes/resets state, and launches step-by-step workspace creation.

## Logging

Daemon logging goes through `internal/logging` and writes to the user state directory by default. Routine refresh details should stay at debug level so normal logs remain readable.
