# Attention Algorithm Migration Plan

## Purpose

Rework Radar's attention algorithm so it more reliably answers one question:

> What work needs attention now?

The migration should keep Radar simple and opinionated: source systems report facts, Radar groups those facts into work items, and the user sees one clear category per task.

## Current functional problems

1. **Done can hide active work**
   - When multiple source refs are linked into one task, a done signal can currently dominate the projected task.
   - This can make a still-active ticket, workspace, or Jira issue look done because one linked PR was merged or closed.

2. **Remote completion with local leftovers must stay out of attention**
   - If authoritative remote work is done, leftover workspaces, sessions, or sandboxes are cleanup bookkeeping rather than active work.
   - Radar should keep the task done and handle eligible local cleanup conservatively in the background.

3. **Filters can affect stored task state**
   - Muting and deprioritizing are intended to affect what the user sees, not the raw tracked state.
   - Today they can influence collected/projected tasks before persistence, which makes the durable state less clean.

4. **GitHub attention is still too task-shaped**
   - GitHub still largely decides task categories before core projection.
   - Radar core then has to infer meaning from GitHub-specific task kinds and reasons.

5. **Attention is stored too coarsely**
   - The currently visible task category is derived from a winning snapshot rather than from all active source signals.
   - This makes merged work items harder to reason about when different sources disagree.

6. **Acknowledgement behavior is mixed across layers**
   - Acknowledgement state belongs to the Radar task record, but GitHub also uses previous projected task metadata to suppress already-seen activity.
   - Functionally, the user expectation is simple: acknowledged comment activity should stop asking for attention unless new relevant activity appears.

## Target functional behavior

### Source facts

Each integration reports source facts and a simple work signal:

- `in_progress`: this source indicates active work.
- `attention`: this source indicates the user should look at it.
- `immediate`: this source indicates urgent attention.
- `done`: this source indicates a tracked remote item has completed.

Integrations should not decide the final task category shown in the UI.

### Task grouping

Radar continues to group work as today:

1. Ticket key first, when available.
2. Workspace key next, for local-only work.
3. Source-owned canonical key for standalone work.

The result is still one visible task per work item.

### Task category calculation

For each grouped task, Radar should derive the visible category from the active source signals:

1. If any active source says `immediate`, show `immediate`.
2. Else if any active source says `attention`, show `attention`.
3. Else if any active source says `in_progress`, show `in_progress`.
4. Show `done` when the authoritative work is complete.

In other words: **done is a terminal attention state.** It remains outside `immediate`, `attention`, `in_progress`, and `low_priority` unless a source becomes active again.

### Cleanup flow

When remote work is done and local resources remain, Radar should keep the task done. Manual cleanup remains available, and automatic garbage collection removes eligible local workspaces, tmux sessions, and sbx sandboxes after the retention period. Cleanup bookkeeping must not return completed work to the user's attention.

### GitHub activity quality

GitHub activity should distinguish actionable human feedback from routine noise.

Functional expectation:

- Direct review requests should remain attention.
- Unresolved review threads should remain attention only while another human is waiting for the user's response.
- Comments and reviews on authored PRs can ask for attention.
- Activity from actors matched by mute or deprioritize filters should not move a task to attention.
- Bot identity should provide configuration aliases, not an unconditional filtering policy.
- CI/check failures should become attention only when they are actionable and tied to the user's PR.

### Acknowledgements

Acknowledgement remains a Radar-owned task-record behavior.

Functional expectation:

- Acknowledging a task suppresses already-seen general comment attention.
- New comments after acknowledgement can ask for attention again.
- Unresolved review threads continue to ask for attention until resolved or no longer relevant.
- Acknowledgement should not be stored inside source-owned facts.

### Filters

Filters are display behavior:

- `mute`: hide the task and remove it from counts.
- `deprioritize`: keep an active task visible in `low_priority`; done tasks remain `done`.

Raw tracked state should stay unfiltered so changing config immediately changes the displayed view without needing a new collection cycle.

## Migration phases

### Phase 1: Agree on the functional model

Confirm the target behavior above, especially:

- whether `done` should never outrank active work,
- how acknowledgement should affect GitHub activity,
- how GitHub activity actors should use the existing mute and deprioritize filters,
- whether `low_priority` remains purely a display category,
- and whether GitHub-specific task kinds still matter to users.

Expected user-visible change: none yet.

### Phase 2: Make stored state represent raw tracked work

Ensure Radar keeps the raw collected work state separate from the filtered display view.

Expected user-visible change:

- Muting/deprioritizing should become easier to change dynamically.
- Removing a filter should restore the task's natural category without waiting for another refresh.

### Phase 3: Make active signals drive task categories

Change the functional rule for merged tasks so the final visible category comes from active source signals, not from whichever task snapshot wins a merge.

Expected user-visible change:

- Active Jira work should no longer disappear into `done` just because a linked PR closed.
- If authoritative remote work is complete, leftover local resources should not return the task to attention.
- Attention should be more predictable for tasks that have several linked sources.

### Phase 4: Treat done as lifecycle only

Make done transitions close a task only when no active source keeps the grouped work alive.

Expected user-visible change:

- A merged PR can show as a done source ref while the overall task remains alive if Jira work still exists.
- If only local cleanup resources remain after authoritative remote completion, the task stays `done`.
- A standalone PR becomes `done` when it is merged or closed.

### Phase 5: Move GitHub to source facts

Functionally simplify GitHub collection so it reports:

- review requested → attention,
- authored PR → in progress,
- authored/participated PR with relevant unfiltered activity → attention,
- configured muted or deprioritized actor activity → no attention,
- actionable automation failures → attention when they indicate the user needs to act,
- closed/merged tracked PR → done.

Radar core should decide how those signals combine with Jira, git, tmux, and sbx.

Expected user-visible change:

- GitHub behavior should stay the same for standalone PRs with human review activity.
- Activity from configured noisy actors should stop pushing tasks into `attention`.
- Linked GitHub work should combine more cleanly with local/Jira work.

### Phase 6: Revisit task labels and reasons

Once the category logic is clean, refine what users see in the reason text.

Functional goals:

- Show the most actionable reason first.
- Preserve useful source detail, such as `review requested`, `unresolved review thread`, `new PR comment`, `open PR`, or `draft PR`.
- Avoid confusing merged reasons like `done` overriding `review requested` or `in progress`.

Expected user-visible change:

- Task rows should be easier to scan and explain why a task is in its category.

### Phase 7: Validate with real workflows

Check the new behavior against common scenarios:

1. Standalone authored PR.
2. Standalone review request.
3. Authored PR with new human comments.
4. Authored PR with comments from a configured muted or deprioritized actor.
5. Authored PR with actionable CI/check failure.
6. Participated PR with unresolved review thread.
7. Jira issue plus local workspace.
8. Jira issue plus PR plus workspace.
9. Merged PR while Jira issue remains active.
10. Merged/closed remote work while local workspace/session/sandbox remains open.
11. Closed local worktree with no remote source.
12. Muted and deprioritized repos/users.
13. Acknowledged GitHub comment activity.

Expected user-visible change:

- Radar should feel more stable: one task per work item, clear category, no active work hidden as done.

## Open product questions

1. Should `immediate` be used by any current source, or is it reserved for future integrations?
2. Should low-priority tasks preserve their natural category somewhere in the UI?
3. Should an acknowledged task move from `attention` to `in_progress`, or disappear only when it was purely an activity item?
4. For linked tasks, should the displayed title prefer Jira, GitHub, or the most urgent source?
5. Should done tasks show all historical source refs or only the source refs that caused completion?
6. Which automation signals are actionable enough to become attention, for example failed checks or security alerts?
