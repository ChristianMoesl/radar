# Attention algorithm

Radar categorizes work by asking one question:

> What should I care about next?

It combines signals from Obsidian, GitHub, Jira, git worktrees, tmux sessions, and sbx sandboxes into one visible task per piece of work.

## One task from many sources

Radar first groups related source refs into a single task:

1. Configured linking mark, for example `ABC-123`.
2. Workspace path, for local-only work.
3. Source identity, for standalone items such as a single GitHub PR.

An explicitly ignored task also has durable source bindings in its canonical Obsidian note. Exact provider-owned identities join the note to the same work after a cold cache start, without relying on a numeric Radar task ID or a title. Bindings are tracking intent, not cached source statuses; informational refs do not become authoritative contributors through ignoring.

This means an Obsidian-authored task, Jira issue, GitHub PR, local worktree, tmux session, and sbx sandbox can appear as one Radar task when they describe the same work. There is no source-less authored task.

Every source ref has an explicit role. `authoritative` refs participate in grouping, title selection, attention, and lifecycle. `informational` refs are attached for inspection and opening only. Authoritative refs also declare lifecycle authority: `primary` owns completion, `contributing` participates directly when no primary exists and can trigger completion through the primary source, and `none` marks workspaces/resources that never complete a task.

## Categories

Radar uses these visible categories:

- `immediate`: urgent action is needed.
- `attention`: you should look at this.
- `in_progress`: active work is being tracked, but no action is currently required.
- `low_priority`: tracked work that is not currently active, or a task deprioritized by filters.
- `ignored`: unfinished work explicitly excluded from your active overview until you unignore it.
- `done`: completed work within the existing three-day display retention, plus older tasks with current unresolved workspace resources.

`ignored` is a display group, not an authored lifecycle state or a source attention signal. `Task.Attention` retains the actual classification; `Task.Ignored` carries the independent preference. `Task.DisplayGroup()` returns `done` for actual done work, otherwise `ignored` when the preference is set, otherwise the current attention category.

## Runtime activity

Activity is independent of attention and lifecycle. It describes an active source as idle, busy processing, or waiting for interaction. Radar projects the strongest current activity from active authoritative refs: `waiting > busy > idle`. Informational refs do not contribute; done tasks show no activity. This does not change categorization, sorting, acknowledgements, filters, or notifications.

Pi sessions in registered Radar workspaces are the first producer. Pi reports busy from `agent_start` until `agent_settled`, including automatic retries and queued follow-ups. An extension UI prompt temporarily takes precedence as waiting. Closing the last overlapping prompt restores busy or idle. Startup and shutdown clear state, and dead panes contribute nothing. The TUI replaces `● busy` with amber `! waiting` for an open prompt. Waiting means a reported prompt lifetime, not an inference from inactivity or the agent's final answer.

## Category decision order

Radar applies lifecycle and user policy in this order:

1. Terminal completion establishes the actual `done` lifecycle.
2. Mute hides matching tasks, including ignored or done tasks.
3. For unfinished work, choose the strongest active source signal: `immediate`, `attention`, `in_progress`, then `low_priority`.
4. Deprioritization may lower naturally classified active work to `low_priority`, but never lowers an urgent primary signal.
5. Apply the effective display group: actual done → `done`; otherwise ignored → `ignored`; otherwise the current attention category.

Ignoring does not change the source signals, actual completion, or existing filters. Urgency and deprioritization cannot move ignored work back into an active display group.

The key rules are:

> Contributing completion does not override active contributing work. Authoritative active remote work reopens a completed authored task through its source provider.

A merged PR should not hide an active Jira issue when no primary owner exists. Once all contributing work items complete, the task is done. If a primary ref exists, it owns the projected lifecycle. A full refresh reconciles its authored task through the source provider: confirmed active contributors reopen it; confirmed completion of every contributor can complete it, subject to the explicit reopen baseline. At least one contributor is required, and every involved source must have completed collection. Display filters and supporting resources cannot override the primary lifecycle.

## Obsidian lifecycle and urgency

An open normal Obsidian note starts in `low_priority`. Linked tmux/SBX activity may promote it to `in_progress`, and actionable linked sources may promote it to `attention`. `radar-state: done` is terminal without authoritative remote work. Confirmed active remote contributors reopen a completed note on a full refresh; informational refs and local resources do not. Explicit reopening returns the note to its strongest active source classification. Automatic completion persists this state and its timestamp in the canonical note before projecting done. An explicit reopen records already-completed work on the next successful full refresh, so unchanged historical completion cannot immediately close it again. Observing active work or newly linked work allows later automatic completion. The note stores this baseline across restarts and cache resets.

`radar-priority: urgent` emits a primary immediate signal. Returning it to `normal` restores the current source-derived category. Priority cannot reopen done work, bypass mute, or generate an OS notification for the user's own mutation.

## Ignored preference and history sections

Ignore means “keep tracking this whole task, but do not ask for my attention until I explicitly unignore it.” It applies to the aggregated task, not one selected issue, PR, or resource ref.

```sh
radar task ignore <task-id>
radar task unignore <task-id>
```

The TUI's `m` key toggles Ignore / Unignore on a selected task; `i` remains Inspect and `d` remains completion/reopening. Radar reuses the canonical note or creates a normal, bound Obsidian task note on first ignore for source-only work. It creates no workspace, worktree, branch, tmux/Pi session, sandbox, mount, or port, and does not change external source state.

The preference survives refreshes, restarts, cache resets, note renames, and workspace cleanup. Sources continue collecting while ignored; source statuses, runtime activity, links, and cleanup warnings remain inspectable. New comments, review requests, urgent signals, or reopened work do not unignore the task. Only explicit unignore clears the preference; there is no timer or automatic resurfacing policy.

An ignored unfinished task counts only as ignored, not in any active category, and emits no actionable attention notifications. A completed ignored task counts only as Done. Completion retains the ignored preference and durable bindings; if the underlying work later reopens, it returns to Ignored. Unignore returns unfinished work to its current source-derived category and leaves completed work done. The note and bindings remain after unignore, and these user mutations do not emit self-notifications.

Cold collection reads the note's bindings before parallel provider collection. Providers resolve bound identities that have left normal active discovery, such as a PR merged while Radar was stopped. Missing unresolved contributors and failed or incomplete source lookups block unsupported automatic completion; absence alone is not a done signal. Confirmed terminal facts may be reused. Binding local resources does not make them remote completion contributors or complete a note when those resources disappear. The existing explicit reopen baseline still governs automatic completion.

The overview shows existing active sections, then Ignored, then Done. Both history sections start collapsed in a new TUI session. Nonempty headers such as `▸ Ignored (12)` and `▸ Done (8) · last 3 days` are selectable with normal navigation; Enter toggles only that header. Expanded children retain their normal supported actions. Empty sections disappear, but independent expansion choices survive refreshes, mutations, and temporary emptiness within the same session; they are not persisted or configurable. Active headers remain non-selectable and non-collapsible.

Collapsed children and source-ref rows are absent from navigation and layout. Completing or ignoring selected active work stays among remaining active tasks, or selects an available history header when none remain; it never automatically expands the destination. Unignore follows an unfinished selected task back to active work. Task-specific keys do nothing on a header. Done retains its existing ordering, display retention, and unresolved-workspace exception.

## Cleanup after remote completion

Completion and local cleanup are separate. A remotely completed task remains `done` while Radar conservatively garbage-collects eligible local worktrees, tmux sessions, and sandboxes. Cleanup bookkeeping must not make completed work compete for the user's attention. Ignoring alone never sets a completion timestamp, archives an open note, authorizes cleanup, or makes a workspace eligible for automatic garbage collection. Existing manual cleanup remains available, and genuinely done ignored work follows the same retention and safety checks as any other done task.

## GitHub activity

GitHub signals should focus on actionable feedback:

- Direct review requests need attention.
- Unresolved review threads need attention when another human is waiting for your response.
- Comments and reviews on your PR can need attention when their actors are not filtered.
- `mute_users`, `deprioritize_users`, and matching repository/user rules prevent configured actors' activity from promoting a PR to attention.
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

## Filters

Filters are applied last, when tasks are shown:

- `mute`: hide the task and remove it from all counts, including ignored and done counts.
- `deprioritize`: lower naturally classified active work to `low_priority` subject to primary urgency; a `done` task remains `done`, and an ignored unfinished task remains in Ignored.

Changing filters should affect the displayed view without changing the raw tracked state. In particular, no display filter may turn completed work back into an attention category or defeat an explicit ignored preference. Per-task ignore does not replace or broaden repository/user mute or deprioritize rules.
