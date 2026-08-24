package taskrefs

import (
	"testing"

	"radar/internal/protocol"
)

func TestWorkspaceCandidateIgnoresInformationalReference(t *testing.T) {
	task := protocol.Task{SourceRefs: []protocol.SourceRef{{
		ID: "jira:mention:1:XYZ-7", Source: "jira", Kind: "issue", Role: protocol.SourceRefRoleInformational,
		Title: "XYZ-7 Informational",
	}}}
	if _, ok := WorkspaceCandidate(task); ok {
		t.Fatal("WorkspaceCandidate() returned informational reference")
	}
	if got := WorkspaceName(protocol.Task{Title: "Manual title", SourceRefs: task.SourceRefs}); got != "Manual title" {
		t.Fatalf("WorkspaceName() = %q, want manual title", got)
	}
}

func TestTaskCursorMatchesRegisteredWorkspaceAnchor(t *testing.T) {
	tasks := []protocol.Task{{SourceRefs: []protocol.SourceRef{{
		ID: "workspace:one", Source: "workspace", Kind: "workspace", Path: "/work/plan", ProvidesWorkspace: true,
	}}}}
	index, found := TaskCursorForCurrent(tasks, protocol.CurrentContext{CWD: "/work/plan"})
	if !found || index != 0 {
		t.Fatalf("TaskCursorForCurrent() = %d, %v", index, found)
	}
}

func TestTaskLinkingKeyPrefersPrimaryWorkItemStableLink(t *testing.T) {
	task := protocol.Task{SourceRefs: []protocol.SourceRef{
		{ID: "review:change:2", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, CanonicalKey: "review:change:2", LinkingKeys: []string{"review:change:2"}, Presentation: protocol.SourceRefPresentation{WorkspaceName: "feature"}},
		{ID: "notes:task:1", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityPrimary, CanonicalKey: "notes:task:1", LinkingKeys: []string{"notes:task:1"}, Presentation: protocol.SourceRefPresentation{WorkspaceName: "One task"}},
	}}
	if got := TaskLinkingKey(task); got != "notes:task:1" {
		t.Fatalf("TaskLinkingKey() = %q, want primary task key", got)
	}
}

func TestTaskLinkingKeyFallsBackToWorkspaceCandidateLinkingMark(t *testing.T) {
	task := protocol.Task{SourceRefs: []protocol.SourceRef{{
		ID: "tracker:issue:1", Role: protocol.SourceRefRoleAuthoritative, CanonicalKey: "tracker:issue:1",
		LinkingKeys: []string{"mark:ABC-1"}, Presentation: protocol.SourceRefPresentation{WorkspaceName: "ABC-1 One"},
	}}}
	if got := TaskLinkingKey(task); got != "mark:ABC-1" {
		t.Fatalf("TaskLinkingKey() = %q, want linking mark", got)
	}
}

func TestWorkspaceCandidatePrefersReadyCodeWorkspace(t *testing.T) {
	task := protocol.Task{SourceRefs: []protocol.SourceRef{
		{ID: "tracker:issue:1", Role: protocol.SourceRefRoleAuthoritative, Presentation: protocol.SourceRefPresentation{WorkspaceName: "Issue one"}},
		{ID: "review:change:2", Role: protocol.SourceRefRoleAuthoritative, Repo: "owner/repo", Branch: "feature", Presentation: protocol.SourceRefPresentation{WorkspaceName: "feature"}},
	}}
	ref, ok := WorkspaceCandidate(task)
	if !ok || ref.ID != "review:change:2" {
		t.Fatalf("WorkspaceCandidate() = %+v, %v", ref, ok)
	}
}

func TestWorktreeFindsGitWorktreeSource(t *testing.T) {
	task := protocol.Task{SourceRefs: []protocol.SourceRef{{Source: "git", Kind: "worktree", Path: "/repo/worktrees/small-fix"}}}

	ref, ok := Worktree(task)
	if !ok || ref.Path != "/repo/worktrees/small-fix" {
		t.Fatalf("Worktree() = %#v, %v", ref, ok)
	}
}

func TestCurrentWorktreeSelectsMatchingWorkspace(t *testing.T) {
	task := protocol.Task{SourceRefs: []protocol.SourceRef{
		{Source: "git", Kind: "worktree", Path: "/repo/worktrees/other"},
		{Source: "git", Kind: "worktree", Path: "/repo/worktrees/current"},
	}}

	ref, ok := CurrentWorktree(task, protocol.CurrentContext{CWD: "/repo/worktrees/current/internal", Worktree: "/repo/worktrees/current"})
	if !ok || ref.Path != "/repo/worktrees/current" {
		t.Fatalf("CurrentWorktree() = %#v, %v; want current workspace", ref, ok)
	}
}

func TestCurrentWorktreeRejectsNonCurrentWorkspace(t *testing.T) {
	task := protocol.Task{SourceRefs: []protocol.SourceRef{{Source: "git", Kind: "worktree", Path: "/repo/worktrees/other"}}}

	ref, ok := CurrentWorktree(task, protocol.CurrentContext{CWD: "/repo/worktrees/current", Worktree: "/repo/worktrees/current"})
	if ok {
		t.Fatalf("CurrentWorktree() = %#v, true; want no match", ref)
	}
}

func TestTaskCursorForCurrentPrefersCurrentWorktree(t *testing.T) {
	tasks := []protocol.Task{
		{Title: "other", SourceRefs: []protocol.SourceRef{{Source: "git", Kind: "worktree", Path: "/workspaces/repo/other"}}},
		{Title: "current", SourceRefs: []protocol.SourceRef{{Source: "git", Kind: "worktree", Path: "/workspaces/repo/current"}}},
	}

	cursor, ok := TaskCursorForCurrent(tasks, protocol.CurrentContext{CWD: "/workspaces/repo/current/internal", Worktree: "/workspaces/repo/current"})
	if !ok || cursor != 1 {
		t.Fatalf("TaskCursorForCurrent() = %d, %v; want 1, true", cursor, ok)
	}
}

func TestTaskCursorForCurrentMatchesTmuxSession(t *testing.T) {
	tasks := []protocol.Task{
		{Title: "other", SourceRefs: []protocol.SourceRef{{Source: "tmux", Kind: "session", Metadata: map[string]string{"session_id": "$1", "session": "other"}}}},
		{Title: "current", SourceRefs: []protocol.SourceRef{{Source: "tmux", Kind: "session", Metadata: map[string]string{"session_id": "$2", "session": "repo-current"}}}},
	}

	cursor, ok := TaskCursorForCurrent(tasks, protocol.CurrentContext{SessionName: "repo-current", SessionID: "$2"})
	if !ok || cursor != 1 {
		t.Fatalf("TaskCursorForCurrent() = %d, %v; want 1, true", cursor, ok)
	}
}

func TestSessionTargetUsesStableSessionID(t *testing.T) {
	task := protocol.Task{SourceRefs: []protocol.SourceRef{{
		Source: "tmux",
		Kind:   "session",
		Title:  "radar",
		Metadata: map[string]string{
			"session_id":    "$3",
			"switch_target": "$3",
		},
	}}}

	if got := SessionTarget(task); got != "$3" {
		t.Fatalf("SessionTarget() = %q, want $3", got)
	}
}
