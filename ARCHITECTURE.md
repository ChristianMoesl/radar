# Architecture

Radar is a CLI-first Go application with a terminal UI, scriptable commands, workspace management, and a shared backend daemon.

## Components

- `cmd/radar/`: single Go binary with TUI, CLI, and daemon modes.
- `internal/tui/`: Bubble Tea terminal UI.
- `internal/integration/`: integration capability interfaces, observation model, and source-compiled implementations.
- `internal/integration/github/`: GitHub source facts and remote state resolution.
- `internal/integration/datadog/`: Datadog monitor source facts and recovery reconciliation.
- `internal/integration/git/`: Git worktree source facts and workspace provider.
- `internal/integration/jira/`: Jira Cloud issue source facts and remote state resolution.
- `internal/integration/tmux/`: tmux session source facts and active multiplexer provider.
- `internal/integration/sbx/`: Docker sbx sandbox source facts, actions, and cleanup.
- `internal/app/`: explicit assembly of the active integration set.
- `internal/cleanup/`: shared application service for cleanup preview aggregation and ordered provider execution.
- `internal/workspace/`: repository discovery, workspace creation, and source-specific Git worktree/tmux removal primitives.
- `internal/workspacegc/`: conservative eligibility and target selection for automatic cleanup of completed work.
- `internal/server/`: Unix socket API used by TUI and CLI commands.
- `internal/collector/`: orchestrates integration collection, observation projection, and remote state resolution.
- `internal/notification/`: detects newly actionable tasks and delivers host OS notifications through the optional macOS notifier companion.
- `internal/state/`: local persistent task cache/state and durable source-ref linking.

## Process model

There is one long-running daemon per user:

```text
TUI / CLI -> Unix socket -> radar daemon -> collectors
```

All frontends share the same daemon and state. This avoids duplicated polling and keeps interactive and scriptable status reads fast. After each refresh, the daemon compares the previous and current filtered task views and sends a host notification for tasks that newly enter `immediate` or `attention`. Completed garbage-collection runs also report their result through a host notification; automatic runs notify only when they delete workspaces, while explicitly triggered runs always report their result. On macOS, the optional `RadarNotifier.app` companion delivers notifications and opens the relevant task or GitHub pull-request URL when clicked. If the companion is absent, notifications are disabled without affecting the daemon. Other operating systems currently use a no-op notifier.

Refresh work must scale with current active work, not accumulated history. Remote reconcilers may fetch an item that was previously active and has disappeared from active collection in order to detect its terminal transition. They must not repeatedly fetch tasks or source refs already known to be `done`; durable terminal state remains authoritative unless the source appears active again.

Radar keeps one command-line entry point; the macOS installer additionally provides a noninteractive notifier companion:

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
radar cleanup <task-id>
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
SourceRef(s) + TaskRecord intent => Task
```

- `SourceRef`: a normalized reference/fact from a source system, such as a GitHub PR, Jira issue, Datadog monitor, local git worktree, or tmux session. Source refs have source-stable IDs like `github:pr:owner/repo:123`, `jira:issue:ABC-544`, `datadog:monitor:123`, `git:worktree:<path>`, or `tmux:session:<session_id>`.
- `SourceRef.LinkingKeys`: source-owned join keys that tell Radar which refs describe the same work. Examples: `ticket:ABC-544`, `workspace:/repo/worktree`, `branch:owner/repo:feature-ABC-544`, or `github:pr:owner/repo:123`. These keys are derived inside each source provider, not in the state store.
- `SourceRef.CanonicalKey`: the source-owned fallback identity for a standalone ref when no ticket key exists. Examples: a Git worktree uses `workspace:<path>`, while a GitHub PR uses its PR source-ref ID.
- `SourceRef.URL`: a generic openable URL. If a source ref has a URL, frontends may offer an open-link action without source-specific URL inspection.
- `SourceRef.SourceLabel`: the source-owned display label for frontend link/source presentation, such as `GitHub` or `Jira`.
- `TaskRecord`: persistent Radar-owned tracking state. It gives continuity across refreshes and owns stable numeric task IDs, optional Radar-owned manual intent, lifecycle state, an optional urgent priority override, association keys, known source ref IDs, first/last seen timestamps, and acknowledgements. A record with manual intent remains projectable without source refs.
- `Task`: the current projected user-facing task served to the CLI/TUI. It has a Radar-owned integer ID and is computed from current source refs plus the matching task record.

The pipeline is:

```text
collect integration Observations
→ project observed SourceRefs into candidate Tasks
→ match/update SourceRefRecords
→ durably link SourceRefRecords into TaskRecords
→ project Tasks
→ serve Tasks
```

The local state file persists explicit `TaskRecord`s and `SourceRefRecord`s. `Task`s are disposable projections for the socket protocol, CLI, and TUI. Task records own durable identity, stable numeric task IDs, optional manual intent, lifecycle state, source-ref ownership, and user acknowledgement state. Source refs remain source-system facts with first/last seen timestamps and an active flag. Manual task mutations save immediately and bump the daemon revision so watchers receive the new projection.

Radar groups work ticket-first: if any linked source ref or explicit manual association exposes a `ticket:<KEY>` linking key, that ticket is the canonical work item. Without a ticket, source-provided canonical keys decide the standalone identity; for example, local workspaces and sbx sandboxes use `workspace:<path>`, GitHub PRs use `github:pr:<repo>:<number>`, and Jira issues use `jira:issue:<KEY>`. Each source ref is assigned to one task record at a time, so local and remote refs do not duplicate across multiple projected tasks.

Source providers own all source-specific identity and linking rules. Adding a new source should not require editing `internal/state` to teach it about the source's IDs, branch formats, URLs, or ticket extraction. The source should populate `SourceRef.ID`, `SourceRef.CanonicalKey`, and `SourceRef.LinkingKeys`; state only persists refs, matches equal linking keys, chooses ticket keys first, and projects tasks.

## Task lifecycle

Radar has four active categories and one historical category:

- `immediate`
- `attention`
- `in_progress`
- `low_priority`
- `done`

The high-level categorization rules are documented in [docs/attention-algorithm.md](docs/attention-algorithm.md).

Collection and durable linking are separate steps. Integration code talks to external systems and produces observations/source refs with source-owned linking keys. Core collection projects those observations into candidate tasks. The state store matches active persisted source refs by those keys, merges records that describe the same work, and then projects one user-facing task per task record.

A manual-only task naturally projects as `low_priority`; it can be completed and reopened explicitly. Any active task may carry a Radar-owned `urgent` override, which projects as `immediate` until cleared. Clearing it recomputes priority from source refs rather than forcing low priority. Terminal completion wins over the override. Its original intent remains on the task record when a source title takes over. Attaching Jira adds a `ticket:<KEY>` association and merges any existing ticket record into the manual record while preserving the manual numeric ID. Once Jira or GitHub is attached, remote lifecycle is authoritative and manual completion is disabled.

`done` is a durable task-record state and is terminal for attention display. If a tracked GitHub PR or Jira issue disappears from active collection, the relevant integration checks the remote state once and emits a done transition. The state store applies that transition to the existing task record. Already-done items are not remotely revalidated on subsequent refreshes. If the same source ref becomes active again later, Radar reopens the same task record instead of creating a duplicate. Done-task projections preserve historical remote refs, but omit inactive local worktree, tmux, and SBX refs after those resources are removed. While a record remains done, neither cleanup state nor display filtering may move it to `immediate`, `attention`, `in_progress`, or `low_priority`.

Completion and local cleanup are separate. A task becomes `done` when its remote work is complete: if both GitHub and Jira refs are linked, both must be done; if only one remote source is linked, that source is authoritative. Remaining local worktrees, tmux sessions, or sbx sandboxes do not keep the task active. The daemon garbage-collects clean linked worktrees under the configured workspace root after the task has been done for 24 hours, deleting the linked detached tmux session and sbx sandbox with the worktree. Attached tmux sessions are skipped and retried later.

Manual cleanup and garbage collection both preview and execute targets through `internal/cleanup.Service`. Manual cleanup executes the confirmed preview with force enabled. `internal/workspacegc` owns eligibility and filters each preview to one workspace path before executing without force. The hourly daemon run waits until a task has been done for 24 hours. `radar gc` and the TUI's `X` key include newly done tasks immediately, while preserving the same clean-worktree and detached-session safety checks. Providers exclusively remove their own resources in deterministic tmux, SBX, then Git order.

Removing a tmux session only marks that source ref inactive, while removing a local worktree marks the local workspace record done when no GitHub or Jira source remains attached.

## Local state

The daemon stores durable task records and source-ref records on disk:

```text
$XDG_STATE_HOME/radar/tasks.json
```

Projected tasks are rebuilt from this state. Done tasks remain in durable history but are included in the user-facing projection only for three days. Full refreshes reconcile all source refs; local refreshes reconcile refs from sources that declare themselves local and leave remote GitHub/Jira refs untouched. The file also stores source statuses so the TUI can show cached status immediately. User acknowledgement state lives on task records, not inside source-ref metadata.

## Config

Config is user-owned JSON, not daemon state:

```text
$XDG_CONFIG_HOME/radar/config.json
```

The daemon creates an example file on startup when it is missing. The TUI exposes it with `f`.

The config controls repository discovery roots, the workspace root, SBX settings, GitHub filters, and the Datadog monitor query. SBX enablement, kit selection, and global additional mounts live together under `sbx`; repository-local `.radar.json` files use the same shape. Filters live under `github.filters` and are applied when serving tasks from the daemon, so CLI and TUI see the same view. `datadog.monitor_query` scopes Datadog collection, while Datadog credentials are read only from environment variables. Raw collected state stays unmodified on disk.

There are two filter effects:

- `mute`: hide the task and remove it from counts
- `deprioritize`: keep tracking an active task, but move it to `low_priority`; done tasks remain `done`

Mute is applied before manual priority display, so muted tasks stay hidden. An explicit urgent override wins over deprioritization. Task mutations are handled directly by the daemon and do not run the external-transition notification path, preventing self-notification.

User filters also apply while GitHub activity is classified. Activity from a muted or deprioritized actor does not promote a PR to attention. GitHub's actor type is used only to generate equivalent bot login aliases (`name` and `name[bot]`) for configuration matching; bot identity is not itself a filtering policy.

## GitHub integration

GitHub access currently uses the `gh` CLI. Radar tracks GitHub core/search rate limits through `gh api rate_limit`. When a budget is low, Radar pauses GitHub collection until GitHub's reset time instead of repeatedly retrying.

Current GitHub collectors:

- review requests assigned directly to the user -> `attention`
- open PRs authored by the user -> `in_progress`

## Jira integration

Jira access uses Jira Cloud REST APIs. Assigned non-done issues become source refs and are linked to matching GitHub/Git/tmux work through ticket keys. The optional `jira.issue_types` user config adds an issue-type allowlist to the search JQL; an omitted or empty list collects every issue type.

## Datadog integration

Datadog access uses the monitor search API with `RADAR_DATADOG_API_KEY` and `RADAR_DATADOG_APP_KEY` from the environment. The user-owned `datadog.monitor_query` config value scopes collection; Radar appends unhealthy monitor states and performs one search request during the five-minute full refresh. It does not collect Datadog logs, traces, metrics, events, or historical alert transitions.

Each monitor is a standalone source ref keyed by monitor ID. `Alert` emits `immediate`; `Warn` and `No Data` emit `attention`. A previously active monitor missing from a complete search is reconciled to `done`. Failed or truncated searches preserve the previous observations and do not resolve monitors. The source is disabled unless both credentials and a non-empty query are present.

The source ref URL points directly to the Datadog monitor. Existing actionable-transition notification handling therefore sends one macOS notification when the monitor first appears and opens that monitor when the notification is clicked.

## Git worktrees

Git worktree integration collects configured local repositories and attaches worktrees to matching tasks by ticket key. Worktrees that do not attach to another task become standalone `in_progress` tasks.

## tmux integration

Tmux integration collects sessions from the local tmux server. Radar attaches sessions to matching tasks when their name contains a ticket key or when the session working directory matches a Git worktree path. Sessions that do not attach to another task become standalone `in_progress` tasks.

Open the TUI in a tmux popup with `tmux display-popup -E "radar"`. Selecting a tmux-backed task switches the current client by stable session ID.

## Docker sbx sandboxes

Docker sbx integration collects sandboxes with `sbx ls --json`. Radar attaches sandboxes to matching tasks when their name or primary workspace contains a ticket key, or when the primary workspace matches a Git worktree path. Sandboxes that do not attach to another task become standalone `in_progress` tasks. The sbx source owns its open action: pressing `o` then `s` opens `sbx run --name <sandbox>` in a new tmux window, creating the matching tmux session first when needed. New sandboxes mount the workspace, the linked worktree's external common Git directory, and every directory configured in `sbx.additional_mounts`. Radar expands home-relative paths and creates missing additional mount directories before invoking SBX. Repository setup commands run through `sbx exec` after sandbox creation; without an effective sandbox configuration they run directly on the host.

## Integration development

New integrations are source-compiled packages under `internal/integration/<name>` and registered explicitly in `internal/app.DefaultIntegrationSet`. See [docs/integrations.md](docs/integrations.md) for the capability checklist, SourceRef contract, and Zellij/GitLab examples.

## Workspaces

Workspaces absorb the useful workflow from `fork.nvim`. The application layer discovers repositories under configured repository directories, creates Git worktrees under the configured workspace root, applies repo-local `.radar.json` workspace setup, and creates matching tmux sessions with `pi` and `nvim` windows. Sandboxed linked worktrees mount both the workspace and its writable common Git directory so sandboxed tools can follow the worktree's absolute `.git` pointer.

Creation is available from the TUI and `radar create`. Task cleanup is available with `x` in the TUI and `radar cleanup <task-id>`. Cleanup preflights every linked local resource, shows a consolidated dirty-worktree warning, then removes linked tmux sessions, SBX sandboxes, and Git worktrees while preserving branches and remote resources.

## Terminal UI

The Bubble Tea TUI is the default interface. It reads cached daemon state, groups tasks by attention, shows source details, switches tmux sessions, opens task URLs, edits config, refreshes/resets state, and launches step-by-step workspace creation.

## Logging

Daemon logging goes through `internal/logging` and writes to the user state directory by default. Routine refresh details should stay at debug level so normal logs remain readable.
