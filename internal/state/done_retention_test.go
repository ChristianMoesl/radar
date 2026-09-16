package state

import (
	"testing"
	"time"

	"radar/internal/protocol"
)

func TestProjectTasksOnlyShowsRecentlyDoneTasks(t *testing.T) {
	now := time.Now().UTC()
	recentRef := protocol.SourceRef{ID: "jira:issue:RECENT", Source: "jira", Kind: "issue", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityContributing, RetainInactive: true, Title: "recently done", Signal: "done"}
	oldRef := protocol.SourceRef{ID: "jira:issue:OLD", Source: "jira", Kind: "issue", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityContributing, RetainInactive: true, Title: "old done", Signal: "done"}
	state := persistedState{
		Version: stateVersion,
		Records: []TaskRecord{
			{ID: "task:1", NumericID: 1, State: "done", DoneAt: now.Add(-2 * 24 * time.Hour).Format(time.RFC3339), Snapshot: protocol.Task{Title: "recently done", Attention: "done"}},
			{ID: "task:2", NumericID: 2, State: "done", DoneAt: now.Add(-4 * 24 * time.Hour).Format(time.RFC3339), Snapshot: protocol.Task{Title: "old done", Attention: "done"}},
		},
		SourceRefs: []SourceRefRecord{
			{ID: recentRef.ID, TaskRecordID: "task:1", Snapshot: recentRef},
			{ID: oldRef.ID, TaskRecordID: "task:2", Snapshot: oldRef},
		},
	}

	tasks := projectTasks(state)
	if len(tasks) != 1 || tasks[0].Title != "recently done" {
		t.Fatalf("tasks = %+v, want only the task done within the last three days", tasks)
	}
}

func TestOldDoneWorkspaceRemainsVisibleOnlyWhileCurrentIssuesExist(t *testing.T) {
	for _, issue := range []string{"local changes", "unpublished branch", "verification failed", "unknown files", "outside workspace root", "attached session"} {
		t.Run(issue, func(t *testing.T) {
			doneAt := time.Now().Add(-7 * 24 * time.Hour).UTC().Format(time.RFC3339)
			anchor := protocol.SourceRef{ID: "workspace:one", Source: "workspace", Kind: "workspace", Role: protocol.SourceRefRoleAuthoritative, ProvidesWorkspace: true, Path: "/workspaces/one"}
			resource := protocol.SourceRef{ID: "git:one", Source: "git", Kind: "worktree", Path: "/workspaces/one/repo", CleanupIssues: []string{issue}, RetainInactive: true}
			if issue == "attached session" {
				resource = protocol.SourceRef{ID: "tmux:one", Source: "tmux", Kind: "session", Path: anchor.Path, InUse: true, RetainInactive: true}
			}
			state := persistedState{
				Version: stateVersion,
				Records: []TaskRecord{{ID: "task:1", NumericID: 1, State: "done", DoneAt: doneAt, Snapshot: protocol.Task{Title: "Old workspace", Attention: "done"}}},
				SourceRefs: []SourceRefRecord{
					{ID: anchor.ID, TaskRecordID: "task:1", Active: true, Snapshot: anchor},
					{ID: resource.ID, TaskRecordID: "task:1", Active: true, Snapshot: resource},
				},
			}
			visible := func(want bool) {
				t.Helper()
				tasks := projectTasks(state)
				if (len(tasks) == 1) != want {
					t.Fatalf("visible = %v, want %v: %+v", len(tasks) == 1, want, tasks)
				}
				if want && (tasks[0].Attention != "done" || tasks[0].DoneAt != doneAt) {
					t.Fatalf("visibility changed lifecycle: %+v", tasks[0])
				}
			}
			visible(true)
			// Resolving an issue restores ordinary retention without resetting DoneAt.
			state.SourceRefs[1].Snapshot.CleanupIssues = nil
			state.SourceRefs[1].Snapshot.InUse = false
			visible(false)
			// A returning issue resurfaces the workspace, even after it was hidden.
			state.SourceRefs[1].Snapshot = resource
			visible(true)
			// Stale retained issue snapshots are not live cleanup blockers.
			state.SourceRefs[1].Active = false
			visible(false)
			state.SourceRefs[1].Active = true
			// An orphan resource without a current workspace does not extend retention.
			state.SourceRefs[0].Active = false
			visible(false)
		})
	}
}

func TestOldDoneStandaloneWorktreeWithIssuesRemainsVisible(t *testing.T) {
	ref := protocol.SourceRef{ID: "git:one", Source: "git", Kind: "worktree", Role: protocol.SourceRefRoleAuthoritative, ProvidesWorkspace: true, Path: "/workspaces/one", CleanupIssues: []string{"local changes"}}
	state := persistedState{
		Version:    stateVersion,
		Records:    []TaskRecord{{ID: "task:1", NumericID: 1, State: "done", DoneAt: time.Now().Add(-7 * 24 * time.Hour).Format(time.RFC3339), Snapshot: protocol.Task{Title: "Standalone worktree"}}},
		SourceRefs: []SourceRefRecord{{ID: ref.ID, TaskRecordID: "task:1", Active: true, Snapshot: ref}},
	}
	if got := projectTasks(state); len(got) != 1 {
		t.Fatalf("standalone workspace hidden: %+v", got)
	}
}
