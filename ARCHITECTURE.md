# Architecture

Radar is a CLI-first Go application with a terminal UI, scriptable commands, workspace management, and a shared backend daemon.

## Components

- `cmd/radar/`: single Go binary with TUI, CLI, and daemon modes.
- `internal/tui/`: Bubble Tea terminal UI.
- `internal/integration/`: integration capability interfaces, observation model, and source-compiled implementations.
- `internal/integration/obsidian/`: Obsidian-authored task notes, mutations, status, and open action.
- `internal/integration/github/`: GitHub source facts and remote state resolution.
- `internal/integration/datadog/`: Datadog monitor source facts and recovery reconciliation.
- `internal/integration/git/`: Git worktree source facts and workspace provider.
- `internal/integration/jira/`: Jira Cloud issue source facts and remote state resolution.
- `internal/integration/tmux/`: tmux session source facts and active multiplexer provider.
- `internal/integration/sbx/`: Docker sbx sandbox source facts, actions, and cleanup.
- `internal/app/`: explicit assembly of the active integration set.
- `internal/cleanup/`: shared application service for cleanup preview aggregation and ordered provider execution.
- `internal/workspace/`: repository discovery, declarative workspace reconciliation, shared worktree planning/creation, workspace creation, Pi injection, and Git/tmux/SBX primitives.
- `internal/workspacegroup/`: versioned logical-workspace registry, validation, lookup, locking, and atomic persistence.
- `internal/pi/`: embedded Radar Pi extension and atomic runtime materialization.
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

All frontends share the same daemon and state. This avoids duplicated polling and keeps interactive and scriptable status reads fast. The daemon refreshes local sources every 15 seconds and performs a full refresh, including remote sources, every two minutes. After each refresh, it compares the previous and current filtered task views and sends a host notification for tasks that newly enter `immediate` or `attention`. Completed garbage-collection runs also report their result through a host notification; automatic runs notify only when they delete workspaces, while explicitly triggered runs always report their result. On macOS, the optional `RadarNotifier.app` companion delivers notifications and opens the relevant task or GitHub pull-request URL when clicked. If the companion is absent, notifications are disabled without affecting the daemon. Other operating systems currently use a no-op notifier.

Refresh work must scale with current active work, not accumulated history. Sources collect concurrently from isolated copies of the previous task projection, then Radar aggregates their observations in integration registration order. Remote reconciliation remains sequential and deterministic because reconcilers may perform follow-up API requests for disappeared items. They must not repeatedly fetch tasks or source refs already known to be `done`; durable terminal state remains authoritative unless the source appears active again.

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
radar reconcile-workspace --request <json> [--workspace <path>] [--preview]
radar workspace-context [--workspace <path>]
radar repository-refs --repo <repo>
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
SourceRef(s) + rebuildable TaskRecord cache => Task
```

- `SourceRef`: a normalized reference/fact from a source system, such as a GitHub PR, Jira issue, Datadog monitor, local git worktree, or tmux session. Source refs have source-stable IDs like `github:pr:owner/repo:123`, `jira:issue:ABC-544`, `datadog:monitor:123`, `git:worktree:<path>`, or `tmux:session:<server_pid>:<session_created>:<session_id>`.
- `SourceRef.Role`: every ref is explicitly `authoritative` or `informational`. Authoritative refs participate in title, attention, identity, linking, lifecycle, and active-resource decisions. Informational refs are inspectable and openable only. Providers emit authoritative refs unless they intentionally collect an informational association.
- `SourceRef.LinkingKeys`: source-owned join keys that tell Radar which authoritative refs describe the same work. Examples: `mark:ABC-544`, `workspace:/repo/worktree`, `workspace-group:<id>`, `branch:owner/repo:feature-ABC-544`, or `github:pr:owner/repo:123`. Configured linking marks use the mandatory `linking_mark_prefixes` allowlist and are extracted inside each source provider through the generic matcher; other keys remain source-owned. Jira's structured Development pull-request relationships contribute exact GitHub PR and repository-branch keys without parsing free text. Informational refs expose no linking keys.
- `SourceRef.CanonicalKey`: the source-owned fallback identity for a standalone ref when no linking mark exists. Examples: a Git worktree uses `workspace:<path>`, while a GitHub PR uses its PR source-ref ID.
- `SourceRef.URL`: a generic openable URL. If a source ref has a URL, frontends may offer an open-link action without source-specific URL inspection.
- `SourceRef.SourceLabel` and `SourceRef.DisplayOrder`: generic presentation values stamped from the integration descriptor, so frontends and state never need source-name switches.
- `SourceRef.EntityID`: an opaque source-owned external entity identity used to correlate authoritative and informational representations without contributing task identity or linking.
- `SourceRef.Busy`: reports transient active processing owned by that authoritative source. Busy refs are projected onto the task independently of attention and lifecycle.
- `SourceRef.Lifecycle`: classifies an authoritative ref as a `work_item`, `workspace`, or supporting `resource`.
- `SourceRef.Authority`: declares lifecycle ownership as `primary`, `contributing`, or `none`. Core lifecycle rules consume this generic value without source-name checks.
- `SourceRef.ProvidesWorkspace`: declares that an authoritative ref owns the persistent local working directory in its absolute `Path`. The ref also emits the matching cleaned `workspace:<path>` linking key. This capability is independent of lifecycle ownership: Git workspace refs provide workspaces, while Obsidian work items have no workspace and tmux and SBX resources only consume workspace paths. A workspace created from a selected task persists one stable task linking key in the local workspace-group registry; Git emits that opaque key so the workspace rejoins its originating task without making the work item a workspace provider.
- `SourceRef.Presentation`: source-compiled title preference/order and workspace-name hints consumed generically by state and frontends.
- `TaskRecord`: rebuildable Radar cache state. It provides cache-local numeric task IDs, projected lifecycle, known source ref IDs, first/last seen timestamps, and acknowledgements. A record without authoritative refs is not projectable.
- `Task`: the current projected user-facing task served to the CLI/TUI. It has a Radar-owned integer ID, is busy when any active authoritative source ref is busy, and is computed from current source refs plus the matching task record.

The pipeline is:

```text
collect integration Observations
→ project observed SourceRefs into candidate Tasks
→ match/update SourceRefRecords
→ durably link SourceRefRecords into TaskRecords
→ project Tasks
→ serve Tasks
```

The local state file persists `TaskRecord`s and `SourceRefRecord`s as a rebuildable cache. `Task`s are disposable projections for the socket protocol, CLI, and TUI. Source refs remain source-system facts with first/last seen timestamps and an active flag. Authored task mutations go through `TaskAuthoringProvider`, refresh local sources immediately, and bump the daemon revision so watchers receive the new projection.

Radar groups authoritative work by linking mark and source-owned linking keys. Without a linking mark, authoritative source-provided canonical keys decide standalone identity; for example, Obsidian tasks use `obsidian:task:<uuid>`, local workspaces use `workspace:<path>`, GitHub PRs use `github:pr:<repo>:<number>`, and Jira issues use `jira:issue:<KEY>`. Informational refs never provide canonical or linking identity. Each source ref belongs to one task record at a time.

Source providers own all source-specific identity, linking, lifecycle, workspace capability, and presentation rules. Adding a new source should not require editing `internal/state` to teach it about the source's name, IDs, branch formats, URLs, linking-mark extraction, title precedence, or remote/local behavior. The source must populate `SourceRef.ID`, `SourceRef.EntityID`, and `SourceRef.Role`; authoritative refs also declare `SourceRef.Lifecycle`, standalone/linkable refs populate `SourceRef.CanonicalKey` and `SourceRef.LinkingKeys`, and persistent-workspace owners set `SourceRef.ProvidesWorkspace` with an absolute path. State persists refs, matches targeted observations first, links only authoritative refs, chooses linking marks first, and projects tasks.

## Task lifecycle

Radar has four active categories and one historical category:

- `immediate`
- `attention`
- `in_progress`
- `low_priority`
- `done`

The high-level categorization rules are documented in [docs/attention-algorithm.md](docs/attention-algorithm.md).

Collection and durable linking are separate steps. Integration code talks to external systems and produces observations/source refs with source-owned linking keys. Core collection projects those observations into candidate tasks. An observation may carry a generic target Radar task ID when a ref must associate with an existing task without contributing identity. State matches this stable numeric ID before canonical/source-ref matching. This is how title-discovered informational Jira refs stay on each mentioning task, and it is available to future integrations without teaching state to parse source-specific keys. The state store links only authoritative refs, merges records that describe the same work, and then projects one user-facing task per task record.

An open normal Obsidian note emits `low_priority`; urgent emits `immediate`; done emits `done`. Live supporting refs may promote an open task to `in_progress` or `attention`. If any primary work-item ref is active, only primary refs decide completion and reopening. Without a primary, contributing Jira, GitHub, and Datadog work items retain their combined lifecycle behavior. Workspace and resource refs never own completion.

`done` is projected into task-record cache state and is terminal for attention display. If a tracked GitHub PR or Jira issue disappears from active collection, the relevant integration checks the remote state once and emits a done transition. The state store applies that transition to the existing task record. Already-done items are not remotely revalidated on subsequent refreshes. If the same source ref becomes active again later, Radar reopens the same task record instead of creating a duplicate. Done-task projections preserve historical remote refs, but omit inactive local worktree, tmux, and SBX refs after those resources are removed. While a record remains done, neither cleanup state nor display filtering may move it to `immediate`, `attention`, `in_progress`, or `low_priority`.

Completion and local cleanup are separate. A task becomes `done` when its remote work is complete: if both GitHub and Jira refs are linked, both must be done; if only one remote source is linked, that source is authoritative. Remaining local worktrees, tmux sessions, or sbx sandboxes do not keep the task active. The daemon garbage-collects clean linked worktrees under the configured workspace root after the task has been done for 24 hours. A registered multi-worktree workspace is one bundle: any dirty member, potentially unpublished branch, failed publication check, or attached shared tmux session skips the whole bundle. Successful cleanup removes all members, their Radar-owned local branches, and the shared session and sandbox once.

Manual cleanup and garbage collection both preview and execute targets through `internal/cleanup.Service`. Manual cleanup executes the confirmed preview with force enabled, immediately refreshes local sources, and returns the updated task projection to the caller. `internal/workspacegc` owns eligibility and filters each preview to either one standalone workspace path or one registered workspace group before executing without force. The hourly daemon run waits until a task has been done for 24 hours. `radar gc` and the TUI's `X` key include newly done tasks immediately, while preserving the same clean-worktree, published-branch, and detached-session safety checks. Providers exclusively remove their own resources in deterministic tmux, SBX, then Git order.

Removing a tmux session only marks that source ref inactive, while removing a local worktree marks the local workspace record done when no GitHub or Jira source remains attached.

## Local state

The daemon stores durable task records and source-ref records on disk:

```text
$XDG_STATE_HOME/radar/tasks.json
```

Projected tasks are rebuilt from this state. Done tasks remain in durable history but are included in the user-facing projection only for three days. Full refreshes reconcile all source refs; local refreshes reconcile refs from sources that declare themselves local and leave remote GitHub/Jira refs untouched. The file also stores source statuses so the TUI can show cached status immediately. User acknowledgement state lives on task records, not inside source-ref metadata.

State writes are atomic and serialized. The daemon refuses to start when an existing file is malformed. An incompatible state version is intentionally discarded and recollected because authored work lives in source systems. Reset clears collected observations and may retain acknowledgements; it does not need compatibility readers or migration fallbacks.

## Config

Config is user-owned JSON, not daemon state:

```text
$XDG_CONFIG_HOME/radar/config.json
```

The daemon creates an example file on startup when it is missing. The TUI exposes it with `f`.

The config controls the required Obsidian vault, repository discovery roots, the workspace root, SBX settings, GitHub filters, and Datadog monitor collection. SBX enablement, kit selection, and global additional mounts live together under `sbx`; repository-local `.radar.json` files use the same shape. Filters live under `github.filters` and are applied when serving tasks from the daemon, so CLI and TUI see the same view. `datadog.monitor_query` scopes Datadog collection, `datadog.monitor_statuses` selects the unhealthy states to ingest, and Datadog credentials are read only from environment variables. Raw collected state stays unmodified on disk.

There are two filter effects:

- `mute`: hide the task and remove it from counts
- `deprioritize`: keep tracking an active task, but move it to `low_priority`; done tasks remain `done`

Mute is applied before display, so muted tasks stay hidden. A primary urgent source signal wins over deprioritization. Task mutations are handled through the authoring provider and do not run the external-transition notification path, preventing self-notification.

User filters also apply while GitHub activity is classified. Activity from a muted or deprioritized actor does not promote a PR to attention. GitHub's actor type is used only to generate equivalent bot login aliases (`name` and `name[bot]`) for configuration matching; bot identity is not itself a filtering policy.

## GitHub integration

GitHub access currently uses the `gh` CLI. The main GraphQL request returns the viewer login together with review-requested, authored, and participated PRs. Configured tracked-PR searches run concurrently with that main request. Because GitHub search results omit head branch names, Radar resolves their node IDs in one GraphQL batch before ingestion, then merges them in deterministic presentation order. A failed branch batch keeps the previous tracked-PR cache rather than replacing complete refs with incomplete ones. When creating a PR-only workspace without an existing local branch, Radar compares the pull ref with the recorded origin branch and creates a local branch from `refs/pull/<number>/head` when the origin branch is missing or points elsewhere, as with fork PRs. Radar tracks GitHub core/search rate limits through `gh api rate_limit`. When a budget is low, Radar pauses GitHub collection until GitHub's reset time instead of repeatedly retrying.

Current GitHub collectors:

- review requests assigned directly to the user -> `attention`
- open PRs authored by the user -> `in_progress`

## Jira integration

Jira access uses Jira Cloud REST APIs with two collection inputs. Assigned non-done issues are searched only for `jira.authoritative_issue_types`, which defaults to Task, Bug, and Sub-task. An explicit empty list skips assigned search. Radar also scans projected titles and active non-Jira source-ref titles for ticket keys and fetches up to 50 distinct issues through one batched Jira search, preserving deterministic title order regardless of assignee or issue type. The assigned and title-reference searches run concurrently and their results are deduplicated afterward.

An assigned issue or a title discovery whose issue type matches the configured set is authoritative. Other title discoveries use per-task `jira:mention:<radar-task-id>:<key>` identities and are informational. They retain Jira URL/status/type/priority metadata but have no signal, canonical key, or linking keys. Batch failures and requested keys missing from a batch preserve previously known refs and report partial source status; complete refreshes remove derived refs whose keys disappeared. Explicit attachments remain durable. When a title contains multiple authoritative keys, the first supplies the Jira title and all must complete before the task completes.

## Datadog integration

Datadog access uses the monitor search API with `RADAR_DATADOG_API_KEY` and `RADAR_DATADOG_APP_KEY` from the environment. The user-owned `datadog.monitor_query` config value scopes collection, and `datadog.monitor_statuses` selects one or more of `Alert`, `Warn`, and `No Data` to append to the query. All three statuses are selected by default. Radar performs one search request during the two-minute full refresh. It does not collect Datadog logs, traces, metrics, events, or historical alert transitions.

Each monitor is a standalone source ref keyed by monitor ID. A configured `Alert` emits `immediate`; configured `Warn` and `No Data` states emit `attention`. A previously active monitor missing from a complete search is reconciled to `done`. Failed or truncated searches preserve the previous observations and do not resolve monitors. The source is disabled unless both credentials and a non-empty query are present.

The source ref URL points directly to the Datadog monitor. Existing actionable-transition notification handling therefore sends one macOS notification when the monitor first appears and opens that monitor when the notification is clicked.

## Git worktrees

Git worktree integration collects configured local repositories and attaches worktrees to matching tasks by configured linking mark. Worktrees that do not attach to another task become standalone `in_progress` tasks.

## tmux integration

Tmux integration collects sessions from the local tmux server. A source ref combines the server PID, session creation time, and server-local session ID so a restarted tmux server cannot reuse a historical Radar identity. Radar attaches sessions to matching tasks when their name contains a configured linking mark or when the session working directory matches a Git worktree path. Sessions that do not attach to another task become standalone `in_progress` tasks.

Open the TUI in a tmux popup with `tmux display-popup -E "radar"`. Selecting a tmux-backed task switches the current client by stable session ID.

## Docker sbx sandboxes

Docker sbx integration collects sandboxes with `sbx ls --json`. Radar attaches sandboxes to matching tasks when their name or primary workspace contains a configured linking mark, or when the primary workspace matches a Git worktree path. Sandboxes that do not attach to another task become standalone `in_progress` tasks. The sbx source owns its open action: pressing `o` then `s` opens `sbx run --name <sandbox>` in a new tmux window, creating the matching tmux session first when needed. New sandboxes mount the workspace, the linked worktree's external common Git directory, and every directory configured in `sbx.additional_mounts`. Radar expands home-relative paths and creates missing additional mount directories before invoking SBX. Repository setup commands run through `sbx exec` after sandbox creation; without an effective sandbox configuration they run directly on the host.

## Integration development

New integrations are source-compiled packages under `internal/integration/<name>` and registered once in `internal/app.DefaultIntegrations`. The registry discovers collection, reconciliation, task-authoring, action, cleanup, workspace, and multiplexer capabilities through interfaces. See [docs/integrations.md](docs/integrations.md) for the capability checklist, SourceRef contract, and Zellij/GitLab examples.

## Workspaces

A Radar workspace is a logical resource bundle with one primary Git worktree, zero or more member worktrees from other repositories, one tmux session, one Pi session, and at most one SBX sandbox. `<workspace_root>/.radar-workspaces.json` is authoritative durable metadata rather than rebuildable task cache state. It uses stable IDs derived from primary paths, normalized absolute paths, deterministic ordering, process locking, version rejection, and atomic fsync/rename persistence. Existing workspaces enroll lazily when reconciliation first targets them. Agent-requested additional mounts and port intent are stored with the optional sandbox; sandbox-less workspaces contain no sandbox, mount, or port intent.

The application layer discovers repositories, shares one worktree planner/creator between `radar create` and workspace reconciliation, applies repo-local `.radar.json` copy/setup rules, and creates matching tmux sessions. Git source refs for all registered members emit `workspace-group:<id>` and workspace-ID metadata, while tmux and SBX remain linked through the primary path. This joins all resources transitively without requiring matching branch names or ticket marks.

Radar embeds a TypeScript Pi extension and atomically materializes it under `$XDG_DATA_HOME/radar/pi` (or `~/.local/share`). Every Radar-started Pi command receives `--extension`, and its tmux session receives the absolute `RADAR_BINARY`. Task-created workspaces derive Pi's session ID from the stable task linking key instead of the mutable title or selected branch; standalone workspaces continue to use the tmux session name. The extension publishes a pane-scoped busy flag between Pi's `agent_start` and `agent_settled` events. The tmux source projects that activity onto its source ref, state projects busy when any active authoritative source ref is busy, and the TUI renders the task-level signal independently of attention. Settling or exiting Pi removes the flag. The host-side `radar_reconcile_workspace` adapter calls JSON preview, requires `ctx.ui.confirm`, then calls JSON apply. Git, registry, tmux, and SBX rules remain in the Go application service.

The embedded extension also exposes host-side `radar_workspace_context` and `radar_repository_refs` tools so an SBX-isolated model does not need to guess host paths. The first resolves Pi's working directory to the logical workspace and returns a compare-and-swap revision, typed capabilities, complete desired state, durable member metadata, observed SBX ports, and configured repository discovery. The second tries to refresh only the selected repository and returns canonical branch capabilities, valid base refs, and checkout paths. If the refresh fails, it returns locally cached refs with a warning so workspace creation still works offline. Both are thin adapters over JSON CLI commands and make no workspace changes.

Workspace apply is an idempotent three-way reconciliation of requested desired state, durable registry state, and observed Git/SBX state. The submitted revision rejects stale descriptions. Worktree sets use replacement semantics; the primary cannot be removed, dirty removals fail closed, and additions reuse the shared planner. A null sandbox is required for sandbox-less workspaces, while an attached sandbox accepts complete agent-requested additional-mount and IPv4-loopback TCP port sets. Radar-managed worktree, Git, and configured mounts stay derived and cannot be removed through desired state. Additional mounts default to read-only, require absolute host paths, and may only be writable when explicitly requested. Reconciliation never enables or removes SBX. Changed mounts recreate the sandbox under the same name after verified removal and a cleanup delay; transient container-start failures receive bounded cleanup-and-backoff retries. Plans expose effective mount counts and warn for unusually large sets. `sbx ports` then publishes TCP4 bindings, replaces incompatible dual-stack bindings, and unpublishes the observed difference. Failure keeps completed work and desired registry state and returns a structured retryable partial result. Standalone preview and apply processes append phase summaries to the shared Radar log without recording complete desired requests or sandbox commands.

Creation is available from the TUI and `radar create`; resource changes are available through `radar_reconcile_workspace` and scriptable `radar reconcile-workspace`. Task cleanup is available with `x` in the TUI and `radar cleanup <task-id>`. Cleanup preflights every linked local resource, shows consolidated dirty-worktree and branch-publication warnings, then removes all member worktrees and shared resources. It also deletes local branches owned by registered Radar-managed worktrees while preserving branches from merely observed worktrees and every remote resource. Automatic garbage collection skips the whole workspace when local changes or potentially unpublished commits would be lost.

## Terminal UI

The Bubble Tea TUI is the default interface. It reads cached daemon state, groups tasks by attention, shows source details, switches tmux sessions, opens task URLs, edits config, refreshes state, and launches step-by-step workspace creation.

## Logging

Daemon logging goes through `internal/logging` and writes to the user state directory by default. Routine refresh details stay at debug level so normal logs remain readable. Debug logs include status, collection, and sequential reconciliation durations per source for refresh-performance diagnosis.
