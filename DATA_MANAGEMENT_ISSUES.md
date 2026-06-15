
 ### 1. Durable identity is inferred, not owned

 The architecture doc already calls this out: there is no explicit TaskRecord yet. Task identity is reconstructed from:

 - source ref IDs
 - ticket keys found in titles/branches/URLs/metadata

 This is fragile. If a task’s references change — renamed branch, moved worktree, changed tmux session, PR title updated, Jira key not present — Radar may treat it as a different task or reuse an ID incorrectly.

 For daily work, I would want:

 ```text
   SourceRef facts are ephemeral.
   TaskRecord is durable.
   Task projection is disposable.
 ```

 Right now the projected task is doing all three jobs.

 ### 2. Linking is many-to-many and can duplicate work

 The linker attaches refs to every task that matches by ticket/path. If two PRs share a Jira key, or a Jira issue plus multiple branches/sessions share a key, the same worktree/tmux/Jira ref can appear on multiple projected tasks.

 That can make the UI look like multiple work items when the user thinks of it as one piece of work.

 This is likely the biggest “day-to-day work” product issue: engineers commonly have one ticket with several PRs, a worktree, and a tmux session. Radar needs a clearer rule for “one work item per ticket/workspace” vs “one task per
 PR”.

 ### 3. Local work has no completion semantics

 Git and tmux refs disappearing just removes them. Deleting a worktree/session does not become “done”; it just stops existing unless there is still a GitHub/Jira ref attached.

 That is probably fine for sessions, but less clear for workspaces. A user may expect “I deleted the workspace” to mean “this local work is finished/cleaned up”, while Radar treats it as no longer observable.

 ### 4. done is short-lived and source-limited

 done only exists for:

 - own GitHub PRs closed/merged today
 - Jira issues moved to done today-ish

 It is retained only for “today”; tomorrow it disappears. That means Radar cannot answer “what did I finish yesterday?” and cannot provide a durable history.

 Also, GitHub done only checks github_own_pr, not arbitrary tracked/review/activity PRs. That may be intentional, but it means “done” is not a general lifecycle state.

 ### 5. Reopened/completed-same-day cases can duplicate

 Previously done tasks are kept for today without checking whether the same source ref is active again. If a PR is closed, marked done, and later reopened the same day, Radar may show both the active task and the done task, possibly
 with the same stable ID.

 Rare, but it reveals that done is appended as a projection artifact, not a state transition on a durable record.

 ### 6. Acknowledgement state is stored inside source ref metadata

 Acknowledge writes user state into SourceRef.Metadata:

 - general_comments_ack_at
 - removes new_general_comments

 But source refs are supposed to be source-system facts. This mixes local user intent with collected facts. It works because the next GitHub refresh reads previous projected tasks, but it is brittle: if linking changes or a source
 ref disappears temporarily, ack continuity can be lost.

 This belongs in a durable task/source-record state layer.

 ### 7. Local refresh can attach fresh local refs to stale remote tasks

 CollectLocal preserves previous non-local tasks and relinks new git/tmux refs into them. This keeps the UI responsive, but it also means stale GitHub/Jira tasks remain until a full refresh. Usually okay, but it can briefly attach
 current local state to a remote task that has already completed/changed.

 My take

 The current model is good enough for “live radar of observable things”, but it is weak for “tracking work I manage over days”.

 The next model should probably be:

 ```text
   SourceRef table:
     id, source, kind, title, url, path, status, first_seen, last_seen, observed_at, active

   TaskRecord table:
     id, canonical_key/work_key, source_ref_ids, created_at, updated_at,
     state, user_ack_state, done_at, archived_at

   Projection:
     rebuild UI tasks from current active source refs + durable task records
 ```

 Then deletion/marking done becomes a state transition on a record, not a side effect of source disappearance.

 If keeping product scope tight, I’d prioritize:

 1. Introduce explicit TaskRecords.
 2. Pick one canonical grouping rule: likely “ticket/workspace first, PRs/Jira/git/tmux as refs under it”.
 3. Move acknowledgement out of source metadata.
 4. Define local deletion semantics: removing a tmux session is cleanup; removing a worktree may mark local work closed only if no remote/Jira remains.
 5. Make done a durable record state, even if the UI only shows “done today”.

