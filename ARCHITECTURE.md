# Architecture

Radar is a CLI-first Go application with a terminal UI, scriptable commands, workspace management, and a shared backend daemon.

## Components

- `cmd/radar/`: single Go binary with TUI, CLI, and daemon modes.
- `internal/tui/`: Bubble Tea terminal UI.
- `internal/integration/`: source-neutral capability interfaces, observation model, and source-compiled implementations. Provider commands, parsers, identities, settings, and workspace drivers stay below this boundary.
- `internal/integration/obsidian/`: Obsidian-authored task notes, mutations, status, and open action.
- `internal/integration/github/`: GitHub source facts and remote state resolution.
- `internal/integration/datadog/`: Datadog monitor source facts and recovery reconciliation.
- `internal/integration/git/`: Git worktree source facts, repository identity, cleanup, and code-workspace provider.
- `internal/integration/jira/`: Jira Cloud issue source facts and remote state resolution.
- `internal/integration/tmux/`: tmux session source facts and active multiplexer provider.
- `internal/integration/sbx/`: Docker sbx sandbox source facts, authentication, actions, runtime naming, and cleanup.
- `internal/app/`: explicit assembly of the active integration set.
- `internal/cleanup/`: shared application service for cleanup preview aggregation and ordered provider execution.
- `internal/integration/workspace/`: managed-workspace source, workspace manager, declarative reconciliation, Pi launch arguments, provider command orchestration, and the versioned workspace-group registry.
- `internal/pi/`: Pi option validation, default agent instructions, the launch-only install notice, and extension contract tests.
- `extensions/pi-radar/`: normally installed Pi package for registered-workspace tools, instructions, skills, and activity.
- `internal/workspacegc/`: completion-based retention, registered-workspace expiry, and target selection for garbage collection.
- `internal/server/`: Unix socket API used by TUI and CLI commands.
- `internal/taskservice/`: authored mutations, source-scoped cache publication, and coordination with in-flight collections.
- `internal/collector/`: orchestrates integration collection, observation projection, and remote state resolution.
- `internal/notification/`: detects newly actionable tasks and delivers host OS notifications through the optional macOS notifier companion.
- `internal/state/`: local persistent task cache/state and durable source-ref linking.

## Process model

There is one long-running daemon per user:

```text
TUI / CLI -> Unix socket -> radar daemon -> collectors
```

Activity producers publish through the generic `ActivityPublisher` capability using `radar activity <idle|busy|waiting>`. The tmux provider owns pane state; core never parses Pi events or tmux options. Activity publication has a two-second deadline and then requests a best-effort one-second `refresh-local` socket round trip. The normal local poll recovers a missed refresh.

All frontends share the same daemon and state. This avoids duplicated polling and keeps interactive and scriptable status reads fast. The daemon refreshes local sources every 15 seconds and performs a full refresh, including remote sources, every two minutes. After each refresh, it compares the previous and current filtered task views and sends a host notification for tasks that newly enter `immediate` or `attention`. Completed garbage-collection runs also report their result through a host notification; automatic runs notify only when they delete workspaces, while explicitly triggered runs always report their result. On macOS, the optional `RadarNotifier.app` companion delivers notifications and opens the relevant task or GitHub pull-request URL when clicked. If the companion is absent, notifications are disabled without affecting the daemon. Other operating systems currently use a no-op notifier.

Refresh work must scale with current active work, not accumulated history. Authoring sources are read first to obtain durable tracking bindings, including after a cold cache reset. Other sources then collect concurrently from isolated copies of the previous task projection, resolving explicit bindings through provider-owned capabilities before Radar aggregates observations in integration registration order. Remote reconciliation remains sequential and deterministic because reconcilers may perform follow-up API requests for disappeared items. They must not repeatedly fetch tasks or source refs already known to be `done`; durable terminal state remains authoritative unless the source appears active again.

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
- `SourceRef.Activity`: reports transient `busy` or `waiting` activity owned by that authoritative source; the zero value is idle. Active authoritative refs reduce with waiting above busy above idle, independently of attention and lifecycle.
- `SourceRef.InUse`: reports that a local resource is currently occupied, without requiring core to understand provider status metadata. Occupancy alone is not a cleanup blocker: attached tmux sessions can be removed with eligible completed workspaces, terminating their commands.
- `SourceRef.Authored`: marks the ref handled by the task-authoring capability, so frontends can offer mutations without checking a source name or metadata key.
- `SourceRef.Lifecycle`: classifies an authoritative ref as a `work_item`, `workspace`, or supporting `resource`.
- `SourceRef.Authority`: declares lifecycle ownership as `primary`, `contributing`, or `none`. Core lifecycle rules consume this generic value without source-name checks.
- `SourceRef.ProvidesWorkspace`: declares that an authoritative ref owns the persistent local working directory in its absolute `Path`. `WorkspaceEntry` selects the anchor to open, `WorkspaceID` groups related resources, and `WorkspaceAnchorPath` carries a source-owned canonical artifact such as an authored note. These fields let frontends and cleanup operate without source-name checks. The ref also emits the matching cleaned `workspace:<path>` linking key.
- `SourceRef.Presentation`: source-compiled title preference/order and workspace-name hints consumed generically by state and frontends.
- `TaskRecord`: rebuildable Radar cache state. It provides cache-local numeric task IDs, projected lifecycle, known source ref IDs, first/last seen timestamps, and acknowledgements. A record without authoritative refs is not projectable.
- `Task`: the current projected user-facing task served to the CLI/TUI. It has a Radar-owned integer ID, projects the strongest current activity from active authoritative source refs (waiting before busy), and is computed from current source refs plus the matching task record.

The pipeline is:

```text
collect integration Observations
→ project observed SourceRefs into candidate Tasks
→ match/update SourceRefRecords
→ durably link SourceRefRecords into TaskRecords
→ project Tasks
→ serve Tasks
```

The local state file persists `TaskRecord`s and `SourceRefRecord`s as a rebuildable cache. `Task`s are disposable projections for the socket protocol, CLI, and TUI. Source refs remain source-system facts with first/last seen timestamps and an active flag. Authored task mutations go through `TaskAuthoringProvider`, then collect only that provider (currently Obsidian) and re-project using cached observations from the other sources. They bump the daemon revision so watchers receive the new projection without waiting for Git, tmux, SBX, or remote collection.

Authored task deletion uses `task-delete-preview` and `task-delete` through the same authoring capability. The confirmed preview binds the cache-local task ID to its source identity, exact target path, and provider-owned revision; a reassigned ID or changed note fails closed. Obsidian holds the shared workspace note lock while checking for workspace references and atomically moving a private directory or archived note to a unique vault `.trash/` container. The task service fences in-flight collection, excludes the deleted ref from collection fallbacks, and publishes only the refreshed authoring source. The response carries a deletion result with original/trash paths and the current task list, not a required surviving task. Remote refs are untouched and can keep the aggregate visible. Deletion needs no tombstone or cache schema change because the source artifact is outside collection; restoring it manually restores its source identity.

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

An open normal Obsidian note emits `low_priority`; urgent emits `immediate`; done emits `done`. Live supporting refs may promote an open task to `in_progress` or `attention`. If any primary work-item ref is active, only primary refs decide the projected completion and reopening. After full collection, remote reconciliation, and state linking, `collector.ReconcileAuthoredTasks` asks the primary source's `TaskLifecycleProvider` to reopen on confirmed active work or persist completion when all authoritative contributing work items are confirmed done and the reopen baseline permits it. The state reducer never writes source files. Without a primary, contributing Jira, GitHub, and Datadog work items retain their combined lifecycle behavior. Workspace and resource refs never own completion.

`done` is projected into task-record cache state and is terminal for attention display. If a tracked GitHub PR or Jira issue disappears from active collection, the relevant integration checks the remote state once and emits a done transition. The state store applies that transition to the existing task record. Already-done items are not remotely revalidated on subsequent refreshes. If the same source ref becomes active again later, Radar reopens the same task record instead of creating a duplicate. Done-task projections preserve historical remote refs, but omit inactive local worktree, tmux, and SBX refs after those resources are removed. While a record remains done, neither cleanup state nor display filtering may move it to `immediate`, `attention`, `in_progress`, or `low_priority`.

Automatic reopening needs one confirmed active contributor and successful note collection; failure of another contributing source does not prevent it. Automatic authored completion requires at least one contributor and complete collections for the primary source and every contributing source. `Store.CollectionTasks` supplies retained inactive work-item refs as well as visible refs, so a missing unresolved item remains a blocker and remote reconcilers can retry it. Previously confirmed terminal refs remain valid when remote collection no longer emits them. Missing non-terminal refs are not completion evidence. Informational refs, workspaces, and resources cannot trigger or block completion.

The Obsidian provider writes lifecycle fields atomically before returning the updated observation. A content hash rejects a note edited since collection, and provider mutations are serialized. Errors are reported in source status without projecting a successful mutation. The optional `radar-completion-baseline` field holds a hash of completed contributor IDs, or `pending` after a manual lifecycle mutation. On reopening, a successful full refresh captures the already-completed set without closing the note. Observing active work updates that baseline; completing all work with a different set permits automatic completion. The baseline survives cache resets. The baseline itself requires no note migration or cache schema change; the task-muting rename has its separate version 7 → 8 rollout below.

Completion and local cleanup are separate. A task becomes `done` when its authoritative work is complete; remaining local worktrees, tmux sessions, or SBX sandboxes do not keep it active. The daemon checks GC hourly. Clean linked workspaces under the configured workspace root become eligible after 24 hours done. A registered workspace is one bundle: before expiry, dirty members, potentially unpublished branches, unavailable publication verification, and unknown anchor files block the whole bundle. Attached tmux sessions alone do not block cleanup; removing them terminates their running shells or commands.

`internal/workspacegc.ExpiryAt` is the shared read-only deadline projection. It returns a deadline only for a done task with a valid `DoneAt` and a workspace-owning ref (`ProvidesWorkspace`) with a nonempty registered `WorkspaceID` and path. `ExpiryRetention` is eight days (192 hours). At or after that completion-based deadline, GC may discard local changes, unpublished commits in deletable Radar-owned branches, and unknown anchor content without an archive. Reopening cancels eligibility; filesystem timestamps and background activity do not postpone it. Standalone observed worktrees have no destructive expiry. Invalid registrations, unsafe paths, provider inspection/target-identification failures, and protected resources remain hard blockers or preservation boundaries. An eligible expired workspace is attempted at the next GC run, not guaranteed to disappear at an exact instant.

Manual cleanup and garbage collection both preview and execute targets through `internal/cleanup.Service`. The selected task's explicit cleanup executes a confirmed preview in `CleanupConfirmed` mode, immediately refreshes local sources, and returns the updated projection. GC filters each preview to one standalone workspace path or one registered workspace group and authorises local-data loss only for expired registered candidates, not through broad manual force. `radar gc` and the TUI's `X` include newly done tasks without the initial 24-hour wait, but never shorten the destructive eight-day grace period or bypass structural safety checks. Providers remove only their own resources, in deterministic tmux, SBX, Git, then Workspace order. Successful cleanup removes managed members, eligible Radar-owned local branches, and the shared runtime resources once; primary repositories, protected/shared branches, remote resources, canonical notes, and external mount targets remain preserved.

This policy needs no schema or configuration change. Existing already-done registered workspaces use their existing `DoneAt`; there is no startup/update-time grace. Before installing the expiry-capable binary, disclose and inspect already-expired workspaces so needed local data can be preserved or tasks reopened. See [workspace expiry and rollout](docs/workspace-cleanup.md#workspace-expiry).

Removing a tmux session only marks that source ref inactive, while removing a local worktree marks the local workspace record done when no GitHub or Jira source remains attached.

## Muted preference and durable source bindings

Muted is an authored boolean preference orthogonal to lifecycle and raw attention. `SourceRef.Muted` on an authoritative authored note projects to `Task.Muted`; `Task.DisplayGroup()` chooses actual done first, then `muted`, then raw attention. Summaries count muted unfinished work separately, and attention notification paths suppress it. GitHub PR `mute` rules remain distinct: they remove only matching PR contributions, never the authored note or other independent sources. A task supported only by muted PRs is hidden, including from Done. External changes never clear the per-task preference. Completion retains it, unmute does not reopen work, and GC continues to use actual completed records rather than display grouping.

`TaskMuteProvider` adopts a source-only task by atomically creating a fully bound canonical note, or updates the existing note under the shared note lock. It creates no workspace/runtime. Obsidian owns optional `radar-muted` and structured `radar-source-refs`, preserving unknown YAML/body bytes and rejecting ambiguous ownership. Preferences/binding edits do not archive or restore files. `TaskBindingProvider` adds newly associated authoritative work to an already-adopted note; informational refs never become authoritative through this path.

Bindings carry provider-owned source/kind/identity and optional concrete resource lifetime keys, not remote status snapshots or numeric Radar IDs. Generic exact binding keys join freshly observed refs to their note after reset. Git and SBX supply lifetime keys so unrelated replacements at a reused path/name cannot inherit the muted preference; tmux already has a concrete lifetime source identity. Replacing a bound runtime lifetime detaches cached old ownership before ordinary relationships are evaluated again, allowing registered resources to rejoin their genuine workspace. An unreadable lifetime reports `BindingError`, preserving collection/cleanup capabilities while refusing unsafe preference authoring.

`BoundSourceResolver` handles work that left active discovery. Jira and GitHub can fetch explicit bound identities; Datadog retains its complete-filtered-search recovery semantics. A binding alone never provides source-state evidence. Missing bound contributors prevent automatic completion. Confirmed terminal facts are reused from cache; hidden adopted Done records remain resolver inputs via transient `Task.TrackingOnly`, but do not re-enter ordinary discovery/title scanning or the served projection. An unavailable authoring source preserves cached preferences without granting lifecycle completeness.

Mutations recollect only the authoring source and use the existing publication/revision fence. A concurrent refresh rereads that authority before publication; newly linked bindings must still be persisted while stale lifecycle evidence is deferred. No separate muting database, timer, configuration switch, command alias, or runtime legacy field reader is introduced. The rename replaces `radar-ignored` with `radar-muted` and the typed snapshot property `ignored` with `muted`, raising the cache version from 7 to 8. Users of the prior ignore version must run the [explicit offline migration](docs/integrations/obsidian.md#migrating-task-muting) before starting the new daemon; ordinary notes without the old field need no note edits.

## Local state

The daemon stores durable task records and source-ref records on disk:

```text
$XDG_STATE_HOME/radar/tasks.json
```

Projected tasks are rebuilt from this state. Done tasks remain in durable history but are included in the user-facing projection only for three days. Full refreshes reconcile all source refs; local refreshes reconcile refs from sources that declare themselves local and leave remote GitHub/Jira refs untouched. The file also stores source statuses so the TUI can show cached status immediately. User acknowledgement state lives on task records, not inside source-ref metadata.

`taskservice.Service` separates slow collection from short cache publication. The daemon's collection mutex still serializes background refreshes, reset, and garbage collection; authored mutations do not acquire it. A separate publication mutex serializes note mutations, source-scoped cache updates, and full-refresh automatic completion. An in-memory authoring revision fences collections started before a mutation: before publishing such a result, the service replaces only its authoring-source observations, status, and completeness evidence with a fresh collection. Other sources' results are retained, not retried. This also protects task creation, reopening, priority changes, and provider errors after partial writes. The socket mutation methods are `task-mute` and `task-unmute`, with typed `Muted` preference and binding fields. The persisted source-ref snapshot property is `muted`, and the cache version is 8; no runtime aliases or old-field readers are provided.

State writes are atomic and serialized. The daemon refuses to start when an existing file is malformed. An incompatible state version is intentionally discarded and recollected because authored work lives in source systems. Reset clears collected observations and may retain acknowledgements; it does not need compatibility readers or migration fallbacks.

## Config

Config is user-owned YAML, not daemon state:

```text
$XDG_CONFIG_HOME/radar/config.yaml
```

The foreground setup wizard creates a commented file after review. The daemon never bypasses onboarding; the TUI exposes configuration with `f`.

The config controls the required task-notes directory (optionally an Obsidian vault), repository discovery roots, the workspace root, SBX settings, GitHub filters, and Datadog monitor collection. SBX enablement, kit selection, and global additional mounts live together under `sbx`; repository-local `.radar.yaml` files use the same shape. GitHub discovery lives under `github.track`; presentation policy lives under `github.pull_request_rules` and `github.activity_rules`. CLI, TUI and notifications share the effective projection without changing raw source facts. `datadog.monitor_query` scopes Datadog collection, `datadog.monitor_statuses` selects the unhealthy states to ingest, and Jira and Datadog secrets are read from the separate owner-only `secrets.yaml`, with environment overrides. Connection metadata remains in `config.yaml`. Raw collected state stays unmodified on disk.

`$XDG_CONFIG_HOME/radar/AGENTS.md`, falling back to `~/.config/radar/AGENTS.md`, is the user-owned instruction file for Radar-managed Pi sessions. Installers copy the committed default only when the file does not exist and never update an existing file.

GitHub PR policy is source-local: `keep` leaves a contribution unchanged, `mute` removes only that PR’s display contribution, and `deprioritize` caps only that PR at low priority. All source refs remain available for linking, inspection, completion and cleanup. A task with other independent sources remains visible; one supported only by muted PRs is hidden even in Done. Explicit authored task muting remains a separate preference.

`protocol.ProjectAttention` applies acknowledgement fallback independently per ref and combines effective contributions. State projection uses it without GitHub policy; the provider reprojects the preserved refs with its policy when serving tasks. Both passes use raw per-ref facts, so an acknowledged or muted PR cannot cap a Jira/Obsidian signal. `SourceRef.Presentation.Hidden` marks collected participation without relevant activity; it retains lifecycle and linking identity without surfacing independently. `CollectionTasks` includes hidden active work for subsequent collection and reconciliation. Raw `SourceRef.Signal` never carries configured mute/deprioritize results. `Task.AttentionSourceRefID` identifies the effective notification destination.

Activity rules match a repository and comment/review actor after normal involvement-based relevance. They cannot force unrelated activity to attention, cancel a direct review request, or bypass PR policy. Author and actor matching are separate and expose equivalent bot aliases only for confirmed bots. Rule lists use first-match precedence. Task mutations do not run the external-transition notification path, preventing self-notification.

## Foreground onboarding

`internal/integration/onboarding` owns the foreground setup workflow, its inline
prompts, dependency installers, and service access checks. It lives under the
integration boundary because it invokes provider CLIs and APIs. `internal/config`
owns settings/secrets storage; previews contain only Config, never the Secrets
map. Saving config is the completion marker, with directory locking to prevent
concurrent setup bundles and exclusive publication for first-time setup.
`radar setup` also edits existing configs: a snapshot detects concurrent changes,
and a typed delta is applied to the original YAML document to preserve unedited settings, comments, and key order.
Secrets are retained on blank input and only explicit replacements are written.
Neither the daemon nor informational commands generate configuration. Automatic
setup is limited to missing configs; malformed files are not silently reset.

Tmux owns session attachment and its configuration plan. The multiplexer capability
exposes `AttachCommand`; the TUI hands terminal ownership to it through Bubble Tea's
ExecProcess only when opening a workspace. Bare `radar` always opens the dashboard
directly. Outside tmux, detaching returns to the dashboard; inside tmux, opening
switches the current client and closes the dashboard. Setup proposes a starter profile only for newly installed tmux,
or the minimal prefix + r binding for existing tmux, and preserves user config.

## GitHub integration

GitHub access currently uses the `gh` CLI. The main GraphQL request returns the viewer login together with review-requested, authored, and participated PRs. Explicit tracked-PR collection runs concurrently with that main request. Repository patterns expand through paginated owner repository connections (cached for 24 hours), then paginated open-PR connections return author identity and branch facts together. Concrete repositories avoid the search-result ceiling and unrelated repositories cannot crowd out matching PRs. Overlapping scopes share requests. Failed pages or exhausted budgets retain complete previous observations, report incomplete collection and prevent unsupported lifecycle conclusions. When creating a PR-only workspace without an existing local branch, Radar compares the pull ref with the recorded origin branch and creates a local branch from `refs/pull/<number>/head` when the origin branch is missing or points elsewhere, as with fork PRs. Radar tracks GitHub core/search rate limits through `gh api rate_limit`. When a budget is low, Radar pauses GitHub collection until GitHub's reset time instead of repeatedly retrying.

Current GitHub collectors:

- review requests assigned directly to the user -> `attention`
- open PRs authored by the user -> `in_progress`

## Jira integration

Jira access uses Jira Cloud REST APIs with two collection inputs. Assigned non-done issues are searched only for `jira.authoritative_issue_types`, which defaults to Story, Task, Bug, and Sub-task. An explicit empty list skips assigned search. Radar also scans projected titles and active non-Jira source-ref titles for ticket keys and fetches up to 50 distinct issues through one batched Jira search, preserving deterministic title order regardless of assignee or issue type. The assigned and title-reference searches run concurrently and their results are deduplicated afterward.

An assigned issue or a title discovery whose issue type matches the configured set is authoritative. Other title discoveries use per-task `jira:mention:<radar-task-id>:<key>` identities and are informational. They retain Jira URL/status/type/priority metadata but have no signal, canonical key, or linking keys. Batch failures and requested keys missing from a batch preserve previously known refs and report partial source status; complete refreshes remove derived refs whose keys disappeared. Explicit attachments remain durable. When a title contains multiple authoritative keys, the first supplies the Jira title and all must complete before the task completes.

## Datadog integration

Datadog access uses the monitor search API with `datadog.api_key` and `datadog.app_key` from `secrets.yaml`, or the `RADAR_DATADOG_API_KEY` and `RADAR_DATADOG_APP_KEY` environment overrides. The user-owned `datadog.monitor_query` config value scopes collection, and `datadog.monitor_statuses` selects one or more of `Alert`, `Warn`, and `No Data` to append to the query. All three statuses are selected by default. Radar performs one search request during the two-minute full refresh. It does not collect Datadog logs, traces, metrics, events, or historical alert transitions.

Each monitor is a standalone source ref keyed by monitor ID. A configured `Alert` emits `immediate`; configured `Warn` and `No Data` states emit `attention`. A previously active monitor missing from a complete search is reconciled to `done`. Failed or truncated searches preserve the previous observations and do not resolve monitors. The source is disabled unless both credentials and a non-empty query are present.

The source ref URL points directly to the Datadog monitor. Existing actionable-transition notification handling therefore sends one macOS notification when the monitor first appears and opens that monitor when the notification is clicked.

## Git worktrees

Git worktree integration collects configured local repositories and attaches worktrees to matching tasks by configured linking mark. Worktrees that do not attach to another task become standalone `in_progress` tasks.

## tmux integration

Tmux integration collects sessions from the local tmux server. A source ref combines the server PID, session creation time, and server-local session ID so a restarted tmux server cannot reuse a historical Radar identity. Radar attaches sessions to matching tasks when their name contains a configured linking mark or when the session working directory matches a Git worktree path. Sessions that do not attach to another task become standalone `in_progress` tasks.

Open the TUI in a tmux popup with `tmux display-popup -E "radar"`. Selecting a tmux-backed task switches the current client by stable session ID.

## Docker sbx sandboxes

Docker sbx integration collects sandboxes with `sbx ls --json`. Radar attaches sandboxes to matching tasks when their name or primary workspace contains a configured linking mark, or when the primary workspace matches a Git worktree path. Sandboxes that do not attach to another task become standalone `in_progress` tasks. The sbx source owns its open action: pressing `o` then `s` opens `sbx run --name <sandbox>` in a new tmux window, creating the matching tmux session first when needed. New sandboxes mount the workspace, the linked worktree's external common Git directory, and every directory configured in `sbx.additional_mounts`. Radar expands home-relative paths and creates missing additional mount directories before invoking SBX. Optional `sbx.env_file` selects one host environment file, with repository overrides and an explicit empty-value opt-out. Radar records its resolved absolute path during workspace creation, validates the file only before provisioning/recreation, and passes it through as `sbx create --env-file`; it does not interpret or persist the contents. Existing workspaces retain their recorded selection, including no selection, and configuration/content changes do not automatically recreate a runtime. Image startup behavior remains owned by the image/kit, not Radar. Optional `sbx.ready_command` records a selected argv array and executes it inside the sandbox with a 60-second bound before setup or starting/reusing a workspace session. An absent/empty command adds no waiting. Failures retain the runtime for inspection/retry and suppress raw diagnostics. The development kit invokes its generic startup-directory runner via native SBX startup hooks; the readiness command bridges their asynchronous lifecycle without teaching Radar about scripts, Git or identities. Repository setup commands run through `sbx exec` after sandbox creation; without an effective sandbox configuration they run directly on the host.

## Integration development

New integrations are source-compiled packages under `internal/integration/<name>` and registered once in `internal/app.DefaultIntegrations`. Core packages consume registry capabilities and may not import concrete providers or invoke provider commands. Boundary tests enforce both rules. See [docs/integrations.md](docs/integrations.md) for the capability checklist, SourceRef contract, and Zellij/GitLab examples.

## Workspaces

A managed Radar workspace is a stable anchor directory with zero or more nested Git worktree members, one tmux session, one Pi session, a required canonical Obsidian note link, and at most one SBX sandbox. The anchor, not a Git member, owns workspace identity and remains Pi, tmux, configured editors, and SBX's working directory for the workspace lifetime.

`<workspace_root>/.radar-workspaces.json` is the single authoritative durable registry. Each record stores the anchor path, task linking key, canonical note path, persisted runtime settings, sandbox intent, and member records. IDs derive from anchor paths. Members must be direct children and repository-and-branch identities remain globally unique. The version 2 registry accepts empty member sets and rejects version 1 primary-worktree data without aliases or implicit migration.

The local `workspace` source emits one no-signal workspace ref per registered anchor. It links through the clean anchor path, `workspace-group:<id>`, and the persisted task key. Registered Git members emit the group and task keys but do not provide a competing logical workspace. Unmanaged external Git worktrees continue to provide their own paths.

Obsidian activation prefills a note-only workspace draft. After confirmation, Radar creates a note-only anchor with `notes.md` as an absolute symlink to the canonical note in its private task directory. Git-first creation uses the same resource planner and apply engine as note-only creation and later edits. It supports several initial members or none. Reconciliation can add or remove any member, including the first or last. Dirty checks, branch publication warnings, protected branches, and complete desired-state replacement semantics remain unchanged.

The optional `note_linking_key` adds an authored note to a workspace without replacing its original `task_linking_key` or Pi session identity. A note can be attached but never detached or replaced through normal controls. The creation planner automatically prepares a stable note identity when no existing note is provided. Creation previews return it for reuse during confirmation and apply; the canonical note is written only during apply, and its association is persisted before worktree or runtime provisioning. The editor has no manual note control. Missing vaults fail creation rather than falling back to detached local files. Opening a workspace does not change task lifecycle, and missing notes fail closed. Existing note-less records need explicit one-time local association; no migration logic is built into Radar.

SBX uses the anchor as its primary workspace. Radar adds the private note directory, distinct external Git common directories, configured mounts, and requested mounts. Nested worktrees need no separate mount because the anchor already contains them. Effective mount changes recreate SBX and may interrupt processes, while failed recreation never rolls back completed filesystem or Git changes.

Radar distributes `extensions/pi-radar/index.ts` as the `@christianmoesl/pi-radar` Pi package, installed with `pi install npm:@christianmoesl/pi-radar`. The npm package ships TypeScript without compilation or bundled Pi dependencies, and requires a separately installed Radar CLI. The CLI and extension share release versions; after binary release validation and publication, GitHub's tag workflow stages the extension using npm trusted publishing. A maintainer reviews and approves the staged package with 2FA before the npm version becomes public. Binary releases do not wait for npm approval. pnpm manages development dependencies and checks, while npm CLI handles OIDC staging.

The integration is never embedded or injected by the launcher. A separate embedded notice-only helper is materialized atomically in a content-addressed user-cache file and passed with `--extension`. After extension startup it checks the actual Pi runtime and known package declarations, respecting disabled extensions. It shows optional installation advice once per Pi agent directory in interactive mode, with a dismissal command. It has no model tools, subprocesses, automatic installs, or settings edits; detection/storage failures skip the advice. Manual Pi launches do not load this helper. Its cache and notice-seen marker are new, disposable data, not a workspace registry change.

On `session_start`, the package checks `radar workspace-context --registration-only --workspace <cwd>` with a bounded timeout. Only a positive registration result installs workspace hooks, tools and commands. This registry-only check avoids Git/SBX discovery, so missing runtime resources cannot hide repair tools. Pi recreates extension instances on reload and session replacement; shutdown restores the host temporary directory. Outside registered anchors and member directories the package remains inactive. The active extension provides workspace inspection, repository-ref inspection, and confirmed reconciliation tools. Before every agent turn it loads Radar's user instruction file and scopes those instructions to workspace and resource management across all members. It also contributes member skills, injects path-labelled repository instructions with repository-only scope, reports duplicate skills, and reloads resources after membership changes without replacing conversation history. Member settings and extensions are not loaded.

Workspace resolution is registry-first. The anchor, `notes.md`, member roots, and nested member paths resolve to the same workspace and compare-and-swap revision. The context result exposes only that workspace, including note metadata without note contents, rather than every registry record.

Cleanup runs tmux, SBX, Git, then Workspace providers. Conservative anchor cleanup removes configured disposable root entries, `notes.md`, and the empty anchor only when no unknown content remains. The global `workspace.cleanup.disposable_entries` allowlist defaults to empty and accepts exact direct-child names; collection and cleanup use the same policy. Previewed entries are carried as provider-owned operation data and revalidated before deletion. Expired registered-workspace GC separately authorises deletion of unknown anchor content, but still validates registration, location, membership, and canonical-note boundaries. Rooted filesystem handles confine recursive deletion; symlinks are unlinked without following their targets, external mounts remain untouched, and managed members remain Git-owned. Cleanup never deletes the canonical Obsidian note, including one found inside a proposed recursive deletion boundary.

After successful anchor and registry removal, the existing note lifecycle still relocates a completed note to `Tasks/Archived/<filename>.md` only if no workspace references its identity or path. This canonical-note relocation is unchanged by expiry and is not a workspace recovery archive: discarded local files and commits cannot be restored from it. Completion without a workspace uses the same check; reopening restores a private directory before activation. Obsidian collection reads both layouts without moving files. A separate workspace-root note lock coordinates mutations and relocation with creation, attachment, and anchor removal. Atomic no-replace renames avoid destination overwrites; notes with accompanying files or detected relative links remain in place with an error. Shared archive notes cannot seed, attach to, or become implicit mounts of a workspace. Garbage collection applies the same retention, expiry, and structural bundle checks to note-only and multi-member workspaces.

## Terminal UI

Muted and Done share a visible-entry model with selectable headers, independent in-memory expansion state, and collapsed defaults. Enter toggles a selected header; task actions resolve only task entries. Rendering, navigation, scrolling, and refresh selection use the same entries so hidden children cannot retain an invisible cursor. Active headers stay non-selectable, and completion/mute never opens a destination section automatically.

The Bubble Tea TUI is the default interface. It reads cached daemon state, groups tasks by attention, shows source details, switches tmux sessions, opens task URLs, edits config, refreshes state, and provides a workspace draft editor for creation and modification. Inspect renders the shared `workspacegc.ExpiryAt` deadline read-only, with local date/time and timezone, past **Eligible since** wording, and an explicit permanent-loss warning. It explains that clean GC can run earlier and structural safety may still block expiry; rendering does not preview cleanup or fetch remotes. `D` previews authored-task deletion in a modal with explicit `y` confirmation, separate from `d` completion and `x` workspace cleanup. Responses older than the displayed daemon revision are ignored so delayed watchers cannot undo a mutation's projection. `c` starts a new draft; `w` edits the selected task's workspace. Repository pickers only stage changes. One preview and confirmation applies the draft through workspace reconciliation.

## Logging

Daemon logging goes through `internal/logging` and writes to the user state directory by default. Routine refresh details stay at debug level so normal logs remain readable. Debug logs include status, collection, and sequential reconciliation durations per source for refresh-performance diagnosis.
