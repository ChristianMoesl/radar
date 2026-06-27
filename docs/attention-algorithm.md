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
- `done`: the work is complete.
- `low_priority`: the task was deprioritized by filters.

`low_priority` is a display category. The underlying task still has its natural state.

## Category decision order

For each grouped task, Radar looks at all active source signals and chooses the most useful category:

1. If any active source says `immediate`, show `immediate`.
2. Else if any active source says `attention`, show `attention`.
3. Else if remote work is done and only local cleanup remains, show `attention`.
4. Else if any active source says `in_progress`, show `in_progress`.
5. Else, when no active source keeps the task alive, show `done`.

The key rule is:

> Done does not override active work.

A merged PR should not hide an active Jira issue or an open local workspace. But if the remote work is finished and the only remaining thing is local cleanup, Radar should ask for attention so the workspace/session/sandbox can be cleaned up.

## Cleanup after remote completion

When a remote item is done but local resources remain, Radar treats cleanup as the next action.

Example:

```text
GitHub PR: merged
Git worktree: still open
tmux session: still open
```

Radar should show this as `attention`, with a cleanup-oriented reason.

The expected flow is:

1. Radar shows the task as needing cleanup attention.
2. You delete the local workspace/session/sandbox from Radar.
3. Radar recalculates the task.
4. If nothing active remains, the task becomes `done`.

Radar should not need a separate "mark done" action for this common cleanup path.

## GitHub activity

GitHub signals should focus on actionable feedback:

- Direct review requests need attention.
- Relevant unresolved review threads need attention.
- Human comments on your PR can need attention.
- Routine bot comments should not need attention by default.
- Automation failures should need attention only when they are actionable for your PR.
- Open authored PRs without actionable activity are `in_progress`.
- Merged or closed tracked PRs are done source facts.

## Acknowledgements

Acknowledgement is for activity that you have already seen.

- Acknowledging a task can suppress already-seen general comment activity.
- New relevant comments can bring the task back to attention.
- Unresolved review threads continue to need attention until resolved or no longer relevant.

## Filters

Filters are applied last, when tasks are shown:

- `mute`: hide the task and remove it from counts.
- `deprioritize`: move the task to `low_priority`.

Changing filters should affect the displayed view without changing the raw tracked state.
