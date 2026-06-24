package taskrefs

import (
	"testing"

	"radar/internal/protocol"
)

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
