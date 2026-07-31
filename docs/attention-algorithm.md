# Attention algorithm

Radar categorizes work by asking one question:

> What should I care about next?

It combines signals from GitHub, Jira, git worktrees, tmux sessions, and sbx sandboxes into one visible task per piece of work.

## One task from many sources

Radar first groups related source refs into a single task:

1. Ticket key, for example `ABC-123`.
2. Workspace path, for local-only work.
3. Source identity, for standalone items such as a single GitHub PR.

This means a Jira issue, GitHub PR, local worktree, tmux session, and sbx sandbox can all appear as one Radar task when they describe the same work.

## Categories

Radar uses these visible categories:

- `immediate`: urgent action is needed.
- `attention`: you should look at this.
- `in_progress`: active work is being tracked, but no action is currently required.
- `done`: the work completed within the last three days.
- `low_priority`: the task was deprioritized by filters.

`low_priority` is a display category. The underlying task still has its natural state.

## Category decision order

For each grouped task, Radar looks at all active source signals and chooses the most useful category:

1. If any active source says `immediate`, show `immediate`.
2. Else if any active source says `attention`, show `attention`.
3. Else if any active source says `in_progress`, show `in_progress`.
4. Else, when no active source keeps the task alive, show `done`.

The key rules are:

> Done does not override active work, and done work does not return to an attention category unless a source becomes active again.

A merged PR should not hide an active Jira issue. Once the authoritative remote work is complete, however, its task is `done`; display filters and leftover cleanup resources must not move it into `attention` or `low_priority`.

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
