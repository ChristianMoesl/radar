package tui

import (
	"path/filepath"
	"strings"
	"testing"

	"radar.nvim/internal/protocol"
)

func TestViewRendersTasksAndSources(t *testing.T) {
	model := model{
		summary: protocol.Summary{Attention: 1},
		tasks: []protocol.Task{{
			Title:     "Review change",
			Reason:    "review requested",
			Attention: "attention",
			SourceRefs: []protocol.SourceRef{{
				ID: "github:pr:owner/repo:1",
			}},
		}},
		sources: []protocol.SourceStatus{{Name: "github", Status: "ok", SourceRefCount: 1}},
	}

	view := model.View()
	for _, want := range []string{"Review change", "review requested", "github:pr:owner/repo:1", "github", "1 refs"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q:\n%s", want, view)
		}
	}
}

func TestCreateRepoViewRendersFuzzySearch(t *testing.T) {
	model := model{mode: "create_repo", create: createForm{repoList: picker{query: "rad", options: []string{"/repo/radar", "/repo/other"}}}}

	view := model.View()
	for _, want := range []string{"Create workspace", "Repository", "rad", "/repo/radar"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q:\n%s", want, view)
		}
	}
}

func TestCreateNameViewRendersSelectedRepoAndBase(t *testing.T) {
	model := model{mode: "create_name", create: createForm{repo: "/repo/radar", base: "origin/main", name: "small-fix"}}

	view := model.View()
	for _, want := range []string{"Create workspace", "Repository /repo/radar", "Base       origin/main", "Name       small-fix"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q:\n%s", want, view)
		}
	}
}

func TestCreateRepoViewShortensHomePaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, "workspace", "radar")
	model := model{mode: "create_repo", create: createForm{repoList: picker{options: []string{path}}}}

	view := model.View()
	if !strings.Contains(view, "~/workspace/radar") {
		t.Fatalf("View() did not shorten home path:\n%s", view)
	}
	if strings.Contains(view, path) {
		t.Fatalf("View() contains unshortened home path:\n%s", view)
	}
}

func TestTaskCursorOrderFollowsRenderedGroups(t *testing.T) {
	model := model{tasks: []protocol.Task{
		{Title: "low", Attention: "low_priority"},
		{Title: "attention", Attention: "attention"},
		{Title: "done", Attention: "done"},
		{Title: "progress", Attention: "in_progress"},
	}}

	got := model.taskCursorOrder()
	want := []int{1, 3, 2, 0}
	if len(got) != len(want) {
		t.Fatalf("taskCursorOrder() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("taskCursorOrder() = %v, want %v", got, want)
		}
	}
}

func TestMoveCursorUsesRenderedTaskOrder(t *testing.T) {
	model := model{cursor: 1, tasks: []protocol.Task{
		{Title: "low", Attention: "low_priority"},
		{Title: "attention", Attention: "attention"},
		{Title: "done", Attention: "done"},
		{Title: "progress", Attention: "in_progress"},
	}}

	model.moveCursor(1)
	if model.cursor != 3 {
		t.Fatalf("cursor after down = %d, want 3", model.cursor)
	}
	model.moveCursor(1)
	if model.cursor != 2 {
		t.Fatalf("cursor after second down = %d, want 2", model.cursor)
	}
	model.moveCursor(-1)
	if model.cursor != 3 {
		t.Fatalf("cursor after up = %d, want 3", model.cursor)
	}
}

func TestDeleteConfirmViewShowsTmuxSessionOnlyDelete(t *testing.T) {
	model := model{mode: "delete_confirm", delete: deletePreview{SessionName: "$3", SessionOnly: true}}

	view := model.View()
	for _, want := range []string{"Delete tmux session?", "kill only the tmux session", "$3"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Path") {
		t.Fatalf("View() contains path for session-only delete:\n%s", view)
	}
}

func TestDeleteConfirmViewWarnsAboutDirtyWorkspace(t *testing.T) {
	model := model{mode: "delete_confirm", delete: deletePreview{Path: "/repo/worktrees/small-fix", Branch: "small-fix", SessionName: "repo-small-fix", Dirty: true}}

	view := model.View()
	for _, want := range []string{"Delete dirty workspace?", "uncommitted changes", "/repo/worktrees/small-fix", "small-fix", "repo-small-fix"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q:\n%s", want, view)
		}
	}
}

func TestActivateSelectedAsksForWorktreeWhenTaskHasMultipleWorktrees(t *testing.T) {
	m := model{tasks: []protocol.Task{{SourceRefs: []protocol.SourceRef{
		{Source: "git", Kind: "worktree", Path: "/repo/one"},
		{Source: "git", Kind: "worktree", Path: "/repo/two"},
	}}}}

	updated, cmd := m.activateSelected()
	if cmd != nil {
		t.Fatal("activateSelected() returned command for multiple worktrees")
	}
	got := updated.(model)
	if got.mode != "worktree_session" || len(got.worktrees) != 2 {
		t.Fatalf("activateSelected() mode=%q worktrees=%d, want worktree_session/2", got.mode, len(got.worktrees))
	}
}

func TestWorktreeRefFindsGitWorktreeSource(t *testing.T) {
	task := protocol.Task{SourceRefs: []protocol.SourceRef{{Source: "git", Kind: "worktree", Path: "/repo/worktrees/small-fix"}}}

	ref, ok := worktreeRef(task)
	if !ok || ref.Path != "/repo/worktrees/small-fix" {
		t.Fatalf("worktreeRef() = %#v, %v", ref, ok)
	}
}

func TestFuzzyMatch(t *testing.T) {
	if !fuzzyMatch("/repo/radar", "rdr") {
		t.Fatal("fuzzyMatch() did not match ordered characters")
	}
	if fuzzyMatch("/repo/radar", "zzz") {
		t.Fatal("fuzzyMatch() matched missing characters")
	}
}

func TestTmuxSessionTargetUsesStableSessionID(t *testing.T) {
	task := protocol.Task{SourceRefs: []protocol.SourceRef{{
		Source: "tmux",
		Kind:   "session",
		Title:  "radar",
		Metadata: map[string]string{
			"session_id":    "$3",
			"switch_target": "$3",
		},
	}}}

	if got := tmuxSessionTarget(task); got != "$3" {
		t.Fatalf("tmuxSessionTarget() = %q, want $3", got)
	}
}

func TestTaskLinksUsesSourceKeys(t *testing.T) {
	task := protocol.Task{SourceRefs: []protocol.SourceRef{
		{ID: "jira:issue:RAD-123", Source: "jira", Kind: "issue", URL: "https://jira.example.test/browse/RAD-123"},
		{ID: "github:pr:owner/repo:7", Source: "github", Kind: "pull_request", URL: "https://github.com/owner/repo/pull/7"},
	}}

	links := taskLinks(task)
	if len(links) != 2 {
		t.Fatalf("taskLinks() returned %d links, want 2: %+v", len(links), links)
	}
	if links[0].Key != "j" || links[0].Source != "Jira" {
		t.Fatalf("jira link = %+v, want j/Jira", links[0])
	}
	if links[1].Key != "g" || links[1].Source != "GitHub" {
		t.Fatalf("github link = %+v, want g/GitHub", links[1])
	}
}
