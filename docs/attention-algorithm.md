# Attention algorithm

Radar categorizes work by asking one question:

> What should I care about next?

It combines signals from Obsidian, GitHub, Jira, git worktrees, tmux sessions, and sbx sandboxes into one visible task per piece of work.

## One task from many sources

Radar first groups related source refs into a single task:

1. Configured linking mark, for example `ABC-123`.
2. Workspace path, for local-only work.
3. Source identity, for standalone items such as a single GitHub PR.

This means an Obsidian-authored task, Jira issue, GitHub PR, local worktree, tmux session, and sbx sandbox can appear as one Radar task when they describe the same work. There is no source-less authored task.

Every source ref has an explicit role. `authoritative` refs participate in grouping, title selection, attention, and lifecycle. `informational` refs are attached for inspection and opening only. Authoritative refs also declare lifecycle authority: `primary` owns completion, `contributing` participates directly when no primary exists and can trigger completion through the primary source, and `none` marks workspaces/resources that never complete a task.

## Categories

Radar uses these visible categories:

- `immediate`: urgent action is needed.
- `attention`: you should look at this.
- `in_progress`: active work is being tracked, but no action is currently required.
- `low_priority`: tracked work that is not currently active, or a task deprioritized by filters.
- `done`: the work completed within the last three days.

## Runtime activity

Activity is independent of attention and lifecycle. It describes an active source as idle, busy processing, or waiting for interaction. Radar projects the strongest current activity from active authoritative refs: `waiting > busy > idle`. Informational refs do not contribute; done tasks show no activity. This does not change categorization, sorting, acknowledgements, filters, or notifications.

Pi sessions in registered Radar workspaces are the first producer. Pi reports busy from `agent_start` until `agent_settled`, including automatic retries and queued follow-ups. An extension UI prompt temporarily takes precedence as waiting. Closing the last overlapping prompt restores busy or idle. Startup and shutdown clear state, and dead panes contribute nothing. The TUI replaces `● busy` with amber `! waiting` for an open prompt. Waiting means a reported prompt lifetime, not an inference from inactivity or the agent's final answer.

## Category decision order

Radar applies lifecycle and user policy in this order:

1. Terminal completion → `done`.
2. Mute → hidden.
3. Otherwise, choose the strongest active source signal: `immediate`, `attention`, `in_progress`, then `low_priority`.
4. Deprioritization may lower naturally classified active work to `low_priority`, but never lowers an urgent primary signal.

The key rules are:

> Contributing completion does not override active contributing work. Authoritative active remote work reopens a completed authored task through its source provider.

A merged PR should not hide an active Jira issue when no primary owner exists. Once all contributing work items complete, the task is done. If a primary ref exists, it owns the projected lifecycle. A full refresh reconciles its authored task through the source provider: confirmed active contributors reopen it; confirmed completion of every contributor can complete it, subject to the explicit reopen baseline. At least one contributor is required, and every involved source must have completed collection. Display filters and supporting resources cannot override the primary lifecycle.

## Obsidian lifecycle and urgency

An open normal Obsidian note starts in `low_priority`. Linked tmux/SBX activity may promote it to `in_progress`, and actionable linked sources may promote it to `attention`. `radar-state: done` is terminal without authoritative remote work. Confirmed active remote contributors reopen a completed note on a full refresh; informational refs and local resources do not. Explicit reopening returns the note to its strongest active source classification. Automatic completion persists this state and its timestamp in the canonical note before projecting done. An explicit reopen records already-completed work on the next successful full refresh, so unchanged historical completion cannot immediately close it again. Observing active work or newly linked work allows later automatic completion. The note stores this baseline across restarts and cache resets.

`radar-priority: urgent` emits a primary immediate signal. Returning it to `normal` restores the current source-derived category. Priority cannot reopen done work, bypass mute, or generate an OS notification for the user's own mutation.

## Cleanup after remote completion

Completion and local cleanup are separate. A remotely completed task remains `done` while Radar conservatively garbage-collects eligible local worktrees, tmux sessions, and sandboxes. Cleanup bookkeeping must not make completed work compete for the user's attention.

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

- `mute`: hide the task and remove it from counts.
- `deprioritize`: move an active task to `low_priority`; a `done` task remains `done`.

Changing filters should affect the displayed view without changing the raw tracked state. In particular, no display filter may turn completed work back into an attention category.
