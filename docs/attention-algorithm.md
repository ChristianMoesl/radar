# Attention algorithm

Radar categorizes work by asking one question:

> What should I care about next?

It combines signals from Obsidian, GitHub, Jira, git worktrees, tmux sessions, and sbx sandboxes into one visible task per piece of work.

## One task from many sources

Radar first groups related source refs into a single task:

1. Configured linking mark, for example `ABC-123`.
2. Workspace path, for local-only work.
3. Source identity, for standalone items such as a single GitHub PR.

An explicitly muted task also has durable source bindings in its canonical Obsidian note. Exact provider-owned identities join the note to the same work after a cold cache start, without relying on a numeric Radar task ID or a title. Bindings are tracking intent, not cached source statuses; informational refs do not become authoritative contributors through muting.

This means an Obsidian-authored task, Jira issue, GitHub PR, local worktree, tmux session, and sbx sandbox can appear as one Radar task when they describe the same work. There is no source-less authored task.

Every source ref has an explicit role. `authoritative` refs participate in grouping, title selection, attention, and lifecycle. `informational` refs are attached for inspection and opening only. Authoritative refs also declare lifecycle authority: `primary` owns completion, `contributing` participates directly when no primary exists and can trigger completion through the primary source, and `none` marks workspaces/resources that never complete a task.

## Categories

Radar uses these visible categories:

- `immediate`: urgent action is needed.
- `attention`: you should look at this.
- `in_progress`: active work is being tracked, but no action is currently required.
- `low_priority`: tracked work that is not currently active, or a task deprioritized by filters.
- `muted`: unfinished work with the per-task muted preference, still visible in the Muted section but excluded from active sections until you unmute it.
- `done`: completed work within the existing three-day display retention, plus older tasks with current unresolved workspace resources.

`muted` is a display group, not an authored lifecycle state or a source attention signal. `Task.Attention` retains the actual classification; `Task.Muted` carries the independent preference. `Task.DisplayGroup()` returns `done` for actual done work, otherwise `muted` when the preference is set, otherwise the current attention category.

## Runtime activity

Activity is independent of attention and lifecycle. It describes an active source as idle, busy processing, or waiting for interaction. Radar projects the strongest current activity from active authoritative refs: `waiting > busy > idle`. Informational refs do not contribute; done tasks show no activity. This does not change categorization, sorting, acknowledgements, filters, or notifications.

Pi sessions in registered Radar workspaces are the first producer. Pi reports busy from `agent_start` until `agent_settled`, including automatic retries and queued follow-ups. An extension UI prompt temporarily takes precedence as waiting. Closing the last overlapping prompt restores busy or idle. Parent and in-process child sessions that activate the extension contribute independently to one process-wide publisher, using the same waiting-before-busy precedence. A session's startup or shutdown resets only its own contribution; the pane remains busy while another session is running. Dead panes contribute nothing. The TUI replaces `● busy` with amber `! waiting` for an open prompt. Waiting means a reported prompt lifetime, not an inference from inactivity or the agent's final answer.

## Category decision order

Radar applies lifecycle and user policy in this order:

1. Terminal completion establishes the actual `done` lifecycle.
2. Apply acknowledgement fallback and GitHub PR policy independently to each source contribution, retaining the raw refs.
3. Hide a task only when no independent visible contribution remains. Informational refs do not make it visible.
4. For unfinished work, choose the strongest surviving signal: `immediate`, `attention`, `in_progress`, then `low_priority`. Deprioritizing one PR cannot lower another source’s signal.
5. Apply the effective display group: actual done → `done`; otherwise muted → `muted`; otherwise the current attention category.

Muting does not change the source signals, actual completion, or existing filters. Urgency and deprioritization cannot move muted work back into an active display group.

The key rules are:

> Contributing completion does not override active contributing work. Authoritative active remote work reopens a completed authored task through its source provider.

A merged PR should not hide an active Jira issue when no primary owner exists. Once all contributing work items complete, the task is done. If a primary ref exists, it owns the projected lifecycle. A full refresh reconciles its authored task through the source provider: confirmed active contributors reopen it; confirmed completion of every contributor can complete it, subject to the explicit reopen baseline. At least one contributor is required, and every involved source must have completed collection. Display filters and supporting resources cannot override the primary lifecycle.

## Obsidian lifecycle and urgency

An open normal Obsidian note starts in `low_priority`. Linked tmux/SBX activity may promote it to `in_progress`, and actionable linked sources may promote it to `attention`. `radar-state: done` is terminal without authoritative remote work. Confirmed active remote contributors reopen a completed note on a full refresh; informational refs and local resources do not. Explicit reopening returns the note to its strongest active source classification. Automatic completion persists this state and its timestamp in the canonical note before projecting done. An explicit reopen records already-completed work on the next successful full refresh, so unchanged historical completion cannot immediately close it again. Observing active work or newly linked work allows later automatic completion. The note stores this baseline across restarts and cache resets.

`radar-priority: urgent` emits a primary immediate signal. Returning it to `normal` restores the current source-derived category. Priority cannot reopen done work, bypass the per-task muted display group, or generate an OS notification for the user’s own mutation. GitHub PR policy cannot suppress the note’s urgency.

## Muted preference and history sections

Per-task mute means “keep tracking this whole task, but do not ask for my attention until I explicitly unmute it.” It applies to the aggregated task, not one selected issue, PR, or resource ref. Unlike GitHub PR `mute` rules, this preference applies to the whole task and keeps unfinished work in Muted. A GitHub rule cannot hide the authored note that owns this preference.

```sh
radar task mute <task-id>
radar task unmute <task-id>
```

The TUI's `m` key toggles Mute / Unmute on a selected task; `i` remains Inspect and `d` remains completion/reopening. Radar reuses the canonical note or creates a normal, bound Obsidian task note on first mute for source-only work. It creates no workspace, worktree, branch, tmux/Pi session, sandbox, mount, or port, and does not change external source state.

The preference survives refreshes, restarts, cache resets, note renames, and workspace cleanup. Sources continue collecting while muted; source statuses, runtime activity, links, and cleanup warnings remain inspectable. New comments, review requests, urgent signals, or reopened work do not unmute the task. Only explicit unmute clears the preference; there is no timer or automatic resurfacing policy.

A muted unfinished task counts only as Muted, not in any active category, and emits no actionable attention notifications. A completed muted task counts only as Done. Completion retains the muted preference and durable bindings; if the underlying work later reopens, it returns to Muted. Unmute returns unfinished work to its current source-derived category and leaves completed work done. The note and bindings remain after unmute, and these user mutations do not emit self-notifications.

Cold collection reads the note's bindings before parallel provider collection. Providers resolve bound identities that have left normal active discovery, such as a PR merged while Radar was stopped. Missing unresolved contributors and failed or incomplete source lookups block unsupported automatic completion; absence alone is not a done signal. Confirmed terminal facts may be reused. Binding local resources does not make them remote completion contributors or complete a note when those resources disappear. The existing explicit reopen baseline still governs automatic completion.

The overview shows existing active sections, then Muted, then Done. Both history sections start collapsed in a new TUI session. Nonempty headers such as `▸ Muted (12)` and `▸ Done (8) · last 3 days` are selectable with normal navigation; Enter toggles only that header. Expanded children retain their normal supported actions. Empty sections disappear, but independent expansion choices survive refreshes, mutations, and temporary emptiness within the same session; they are not persisted or configurable. Active headers remain non-selectable and non-collapsible.

Collapsed children and source-ref rows are absent from navigation and layout. Completing or muting selected active work stays among remaining active tasks, or selects an available history header when none remain; it never automatically expands the destination. Unmute follows an unfinished selected task back to active work. Task-specific keys do nothing on a header. Done retains its existing ordering, display retention, and unresolved-workspace exception.

## Cleanup after remote completion

Completion and local cleanup are separate. A remotely completed task remains `done` while Radar conservatively garbage-collects eligible local worktrees, tmux sessions, and sandboxes. Cleanup bookkeeping must not make completed work compete for the user's attention. Muting alone never sets a completion timestamp, archives an open note, authorizes cleanup, or makes a workspace eligible for automatic garbage collection. Existing manual cleanup remains available, and genuinely done muted work follows the same retention and safety checks as any other done task.

## GitHub activity

GitHub signals should focus on actionable feedback:

- Direct review requests need attention.
- Unresolved review threads need attention when another human is waiting for your response.
- Comments and reviews on your PR can need attention when their actors are not filtered.
- `github.activity_rules` can ignore relevant activity by repository, actor, or both; `keep` uses normal relevance rather than subscribing to every discussion.
- `github.pull_request_rules` subsequently controls that PR’s contribution without affecting other linked work.
- Extra `github.track` entries discover open PRs but do not subscribe the viewer to unrelated discussions.
- Bot identity does not determine priority by itself; confirmed GitHub bots expose equivalent `name` and `name[bot]` aliases for configuration matching.
- Automation failures should need attention only when they are actionable for your PR.
- Open authored PRs without actionable activity are `in_progress`.
- Merged or closed tracked PRs are done source facts.

## Jira workflow status

Jira's Done status category supplies the completion signal for authoritative contributing Jira refs. It participates directly in task lifecycle without a primary, or in the all-contributors-done check that completes an authored note. Assigned authoritative non-done issues and authoritative title discoveries are classified by exact status name, after trimming whitespace and ignoring case. Informational Jira refs expose status metadata but never participate in title, attention, or lifecycle precedence. The defaults are:

- `In Progress` and `In Review` → `in_progress`.
- Every other authoritative non-done status → `low_priority`.

Configure `jira.status_mapping` to map status names to `low_priority`, `in_progress`, `attention`, or `immediate`, and use `jira.unmapped_status` as the fallback. An explicitly empty mapping sends every non-done issue to the fallback. Linked GitHub and local refs can still promote a low-priority Jira task because the strongest active signal wins.

## Datadog monitors

Datadog contributes configured unhealthy monitor states during the two-minute full refresh:

- A configured `Alert` needs immediate attention.
- Configured `Warn` and `No Data` states need attention.
- A monitor that disappears from a complete unhealthy-monitor search is done because it recovered or otherwise stopped matching the configured query and statuses.

`datadog.monitor_statuses` selects one or more of these states and defaults to all three. Radar tracks one task per monitor ID, not one event per alert transition. It intentionally does not retain Datadog alert events that both start and recover between polls.

## Acknowledgements

Acknowledgement is for activity that you have already seen.

- Acknowledging a task can suppress already-seen general comment activity.
- New relevant comments can bring the task back to attention.
- An unresolved review thread stops needing attention when you are the latest person to respond, and needs attention again if another human replies.

## GitHub policies

`github.pull_request_rules` match repository/author on the same PR; `github.activity_rules` match repository/actor for relevant activity. First match wins independently for each PR/actor. Fields use AND, list values use OR. Local-only tasks never match GitHub rules.

- PR `keep`: normal contribution.
- PR `mute`: no display/attention contribution from that PR, but retain linking and lifecycle facts. Other independent sources keep the task visible. A PR-only muted task remains hidden even in Done.
- PR `deprioritize`: cap that PR’s active contribution at `low_priority`; never alter completion or other sources.
- Activity `ignore`: exclude relevant feedback from that actor in that scope.
- Activity `keep`: normal involvement-based relevance, not forced attention or an override of PR policy.

Acknowledgements also apply per source: acknowledging PR feedback cannot lower an unrelated Jira issue or urgent note. Notification reason and URL follow the effective contributing source, not a suppressed raw PR signal.

Policies do not change raw tracked state or explicit task mute preferences. An open muted PR still prevents automatic completion and can reopen an authored note; muting is not abandoning work. See [the GitHub guide](integrations/github.md) for configuration and rollout.
