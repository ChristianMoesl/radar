# Attention algorithm

Radar categorizes work by asking one question:

> What should I care about next?

It combines signals from GitHub, Jira, git worktrees, tmux sessions, and sbx sandboxes into one visible task per piece of work.

## One task from many sources

Radar first groups related source refs into a single task:

1. Ticket key, for example `ABC-123`.
2. Workspace path, for local-only work.
3. Source identity, for standalone items such as a single GitHub PR.

This means a Jira issue, GitHub PR, local worktree, tmux session, and sbx sandbox can all appear as one Radar task when they describe the same work. A Radar-owned manual task can exist without any source ref; attaching a Jira key lets matching refs join it without changing its numeric Radar ID.

## Categories

Radar uses these visible categories:

- `immediate`: urgent action is needed.
- `attention`: you should look at this.
- `in_progress`: active work is being tracked, but no action is currently required.
- `low_priority`: tracked work that is not currently active, or a task deprioritized by filters.
- `done`: the work completed within the last three days.

## Category decision order

Radar applies lifecycle and user policy in this order:

1. Terminal completion → `done`.
2. Mute → hidden.
3. A manual urgent override → `immediate`.
4. Otherwise, choose the strongest active source signal: `immediate`, `attention`, `in_progress`, then `low_priority`.
5. Deprioritization may lower naturally classified active work to `low_priority`, but never lowers a manual urgent override.

The key rules are:

> Done does not override active work, and done work does not return to an attention category unless a source becomes active again.

A merged PR should not hide an active Jira issue. Once the authoritative remote work is complete, however, its task is `done`; display filters and leftover cleanup resources must not move it into `attention` or `low_priority`.

## Manual lifecycle

A manual-only task starts in `low_priority`. Completing it explicitly moves it to `done`; reopening restores `low_priority`. Its original title is retained as Radar-owned intent even when an attached Jira issue supplies the display title. Once Jira or GitHub is attached, that remote source controls completion and manual done/reopen is unavailable.

## Manual urgency

Any active task can be marked manually urgent. This durable override promotes it to `immediate`; clearing the override restores its current natural classification, including new source information observed while it was urgent. It never reopens a done task, bypasses mute, or sends an OS notification for the user's own mutation. A task already naturally immediate is not lowered by the TUI priority key.

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

Jira's Done status category remains authoritative for completion. Assigned non-done issues are classified by exact status name, after trimming whitespace and ignoring case. The defaults are:

- `In Progress` and `In Review` → `in_progress`.
- Every other assigned non-done status → `low_priority`.

Configure `jira.status_mapping` to map status names to `low_priority`, `in_progress`, `attention`, or `immediate`, and use `jira.unmapped_status` as the fallback. An explicitly empty mapping sends every non-done issue to the fallback. Linked GitHub and local refs can still promote a low-priority Jira task because the strongest active signal wins.

## Datadog monitors

Datadog contributes current unhealthy monitor state during the five-minute full refresh:

- `Alert` needs immediate attention.
- `Warn` and `No Data` need attention.
- A monitor that disappears from a complete unhealthy-monitor search is done because it recovered or otherwise stopped matching the configured query.

Radar tracks one task per monitor ID, not one event per alert transition. It intentionally does not retain Datadog alert events that both start and recover between polls.

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
