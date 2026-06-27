package tui

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"radar/internal/protocol"
)

func TestViewShowsErrorsAsToastWithoutReplacingTasks(t *testing.T) {
	model := model{
		err:     errors.New("boom"),
		summary: protocol.Summary{Attention: 1},
		tasks: []protocol.Task{{
			Title:     "Review change",
			Reason:    "review requested",
			Attention: "attention",
		}},
	}

	view := model.View()
	for _, want := range []string{"Review change", "Error: boom"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Could not load Radar tasks") {
		t.Fatalf("View() rendered inline load error:\n%s", view)
	}
}

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

func TestWatchResponseDoesNotResetSelection(t *testing.T) {
	m := model{
		cursor:              1,
		selectedCurrentTask: true,
		revision:            1,
		tasks: []protocol.Task{
			{ID: 1, Title: "current", Attention: "in_progress"},
			{ID: 2, Title: "selected", Attention: "attention"},
		},
	}

	updated, cmd := m.Update(watchMsg{response: protocol.Response{OK: true, Revision: 2, Tasks: []protocol.Task{
		{ID: 3, Title: "new", Attention: "attention"},
		{ID: 1, Title: "current", Attention: "in_progress"},
		{ID: 2, Title: "selected", Attention: "attention"},
	}}})
	got := updated.(model)
	if cmd == nil {
		t.Fatal("watch response should start next watch")
	}
	if got.cursor != 2 {
		t.Fatalf("cursor = %d, want 2", got.cursor)
	}
}

func TestWatchResponseSelectsSameTaskBySourceRef(t *testing.T) {
	m := model{
		cursor:              1,
		selectedCurrentTask: true,
		revision:            1,
		tasks: []protocol.Task{
			{Title: "first", Attention: "attention", SourceRefs: []protocol.SourceRef{{ID: "github:pr:org/repo:1"}}},
			{Title: "selected", Attention: "attention", SourceRefs: []protocol.SourceRef{{ID: "github:pr:org/repo:2"}}},
		},
	}

	updated, _ := m.Update(watchMsg{response: protocol.Response{OK: true, Revision: 2, Tasks: []protocol.Task{
		{Title: "new", Attention: "attention", SourceRefs: []protocol.SourceRef{{ID: "github:pr:org/repo:3"}}},
		{Title: "first", Attention: "attention", SourceRefs: []protocol.SourceRef{{ID: "github:pr:org/repo:1"}}},
		{Title: "selected", Attention: "attention", SourceRefs: []protocol.SourceRef{{ID: "github:pr:org/repo:2"}}},
	}}})
	got := updated.(model)
	if got.cursor != 2 {
		t.Fatalf("cursor = %d, want 2", got.cursor)
	}
}

func TestWatchResponseSelectsNextRenderedTaskWhenSelectedTaskDisappears(t *testing.T) {
	m := model{
		cursor:              1,
		selectedCurrentTask: true,
		revision:            1,
		tasks: []protocol.Task{
			{ID: 1, Title: "low", Attention: "low_priority"},
			{ID: 2, Title: "selected", Attention: "attention"},
			{ID: 3, Title: "progress", Attention: "in_progress"},
		},
	}

	updated, _ := m.Update(watchMsg{response: protocol.Response{OK: true, Revision: 2, Tasks: []protocol.Task{
		{ID: 1, Title: "low", Attention: "low_priority"},
		{ID: 3, Title: "progress", Attention: "in_progress"},
	}}})
	got := updated.(model)
	if got.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", got.cursor)
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

func TestSubmitCreateShowsCreatingWorkspaceNotification(t *testing.T) {
	m := model{mode: "create_name", create: createForm{repo: "/repo/radar", base: "origin/main", name: "small-fix"}}

	updated, cmd := m.submitCreate()
	if cmd == nil {
		t.Fatal("submitCreate() command = nil")
	}
	got := updated.(model)
	if !got.loading || got.message != creatingWorkspaceMessage {
		t.Fatalf("submitCreate() loading=%v message=%q, want loading with creating notification", got.loading, got.message)
	}
}

func TestPreparingWorkspaceNotificationUpdatesCreateMessage(t *testing.T) {
	m := model{loading: true, message: creatingWorkspaceMessage}

	updated, _ := m.Update(preparingWorkspaceMsg{})
	got := updated.(model)
	if got.message != preparingWorkspaceMessage {
		t.Fatalf("message = %q, want %q", got.message, preparingWorkspaceMessage)
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

func TestTaskListKeepsSelectedSourceRefsVisible(t *testing.T) {
	model := model{cursor: 1, tasks: []protocol.Task{
		{Title: "first", Attention: "attention"},
		{Title: "selected", Attention: "attention", SourceRefs: []protocol.SourceRef{
			{ID: "git:worktree:/repo/selected", Source: "git", Kind: "worktree", Path: "/repo/selected"},
			{ID: "jira:issue:ABC-1", Source: "jira", Kind: "issue", Title: "ABC-1 Do thing"},
		}},
	}}

	view := model.taskList(100, 4)
	for _, want := range []string{"selected", "/repo/selected", "ABC-1"} {
		if !strings.Contains(view, want) {
			t.Fatalf("taskList() missing %q:\n%s", want, view)
		}
	}
}

func TestTaskListCanReturnToTopOfLargeSelectedBlock(t *testing.T) {
	model := model{cursor: 0, scroll: 5, tasks: []protocol.Task{{
		Title:     "selected",
		Attention: "attention",
		SourceRefs: []protocol.SourceRef{
			{ID: "git:worktree:/repo/selected", Source: "git", Kind: "worktree", Path: "/repo/selected"},
			{ID: "jira:issue:ABC-1", Source: "jira", Kind: "issue", Title: "ABC-1 Do thing"},
			{ID: "github:pr:owner/repo:1", Source: "github", Kind: "pull_request", Title: "PR 1"},
		},
	}}}

	view := model.taskList(100, 3)
	if !strings.Contains(view, "Need attention") || !strings.Contains(view, "selected") {
		t.Fatalf("taskList() did not show top of selected block:\n%s", view)
	}
}

func TestTaskListTruncatesLongRows(t *testing.T) {
	model := model{tasks: []protocol.Task{{
		Title:     "selected task with a very very very long title that should not wrap",
		Repo:      "redbullmediahouse/rb3ca-experience-center",
		Reason:    "2 unresolved review thread(s), 1 new PR comment(s)",
		Attention: "attention",
		SourceRefs: []protocol.SourceRef{{
			ID:     "git:worktree:/very/very/very/very/very/very/very/long/path/that/would/wrap",
			Source: "git",
			Kind:   "worktree",
			Path:   "/very/very/very/very/very/very/very/long/path/that/would/wrap",
		}},
	}}}

	view := model.taskList(60, 20)
	for _, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(ansi.Strip(line)); got > 60 {
			t.Fatalf("taskList() line width = %d, want <= 60 for %q:\n%s", got, ansi.Strip(line), view)
		}
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

func TestScrollDoesNotMoveUpUntilCursorHitsTop(t *testing.T) {
	tasks := make([]protocol.Task, 12)
	for i := range tasks {
		tasks[i] = protocol.Task{Title: fmt.Sprintf("task %d", i), Attention: "attention"}
	}
	model := model{width: 100, height: 10, tasks: tasks}

	for i := 0; i < 6; i++ {
		model.moveCursor(1)
	}
	scrolledDown := model.scroll
	if scrolledDown == 0 {
		t.Fatalf("scroll after moving down = 0, want > 0")
	}

	model.moveCursor(-1)
	if model.scroll != scrolledDown {
		t.Fatalf("scroll after moving up one row = %d, want %d", model.scroll, scrolledDown)
	}
}

func TestDeleteConfirmViewShowsTmuxSessionOnlyDelete(t *testing.T) {
	model := model{mode: "delete_confirm", delete: protocol.DeletePreview{SessionName: "$3", SessionOnly: true, ConfirmTitle: "Delete tmux session?", Warning: "This will kill only the tmux session."}}

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

func TestDeleteConfirmViewShowsSbxSandboxDelete(t *testing.T) {
	model := model{mode: "delete_confirm", delete: protocol.DeletePreview{Title: "radar-repo-small-fix", Path: "/repo/small-fix", ConfirmTitle: "Delete sbx sandbox?", Warning: "This will remove the sbx sandbox."}}

	view := model.View()
	for _, want := range []string{"Delete sbx sandbox?", "remove the sbx sandbox", "radar-repo-small-fix", "/repo/small-fix"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q:\n%s", want, view)
		}
	}
}

func TestDeleteConfirmViewWarnsAboutDirtyWorkspace(t *testing.T) {
	model := model{mode: "delete_confirm", delete: protocol.DeletePreview{Path: "/repo/worktrees/small-fix", Branch: "small-fix", SessionName: "repo-small-fix", Dirty: true, ConfirmTitle: "Delete dirty workspace?", Warning: "This worktree has uncommitted changes. Deleting will permanently discard them."}}

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

func TestActivateSelectedStartsWorkspaceCreateForJiraOnlyTask(t *testing.T) {
	m := model{tasks: []protocol.Task{{
		Title: "ABC-123 Build the thing",
		SourceRefs: []protocol.SourceRef{{
			ID:     "jira:issue:ABC-123",
			Source: "jira",
			Kind:   "issue",
			Title:  "ABC-123 Build the thing",
		}},
	}}}

	updated, cmd := m.activateSelected()
	if cmd == nil {
		t.Fatal("activateSelected() returned no command")
	}
	got := updated.(model)
	if got.mode != "create_repo" {
		t.Fatalf("activateSelected() mode = %q, want create_repo", got.mode)
	}
	if got.create.name != "ABC-123 Build the thing" {
		t.Fatalf("create name = %q, want task title", got.create.name)
	}
	if !got.create.repoList.loading {
		t.Fatal("repo picker is not loading")
	}
}

func TestWorkspaceNameForTaskFallsBackToJiraKey(t *testing.T) {
	task := protocol.Task{SourceRefs: []protocol.SourceRef{{
		ID:       "jira:issue:ABC-123",
		Source:   "jira",
		Kind:     "issue",
		Metadata: map[string]string{"key": "ABC-123"},
	}}}

	if got := workspaceNameForTask(task); got != "ABC-123" {
		t.Fatalf("workspaceNameForTask() = %q, want Jira key", got)
	}
}

func TestWorkspaceNameForTaskUsesPullRequestOriginBranchWithoutOriginPrefix(t *testing.T) {
	task := protocol.Task{Title: "Review", SourceRefs: []protocol.SourceRef{{
		ID:     "github:pr:owner/repo:7",
		Source: "github",
		Kind:   "pull_request",
		Branch: "origin/feature/build-thing",
	}}}

	if got := workspaceNameForTask(task); got != "feature/build-thing" {
		t.Fatalf("workspaceNameForTask() = %q, want PR branch without origin prefix", got)
	}
}

func TestActivateSelectedCreatesWorkspaceForPullRequestOnlyTask(t *testing.T) {
	m := model{tasks: []protocol.Task{{
		Title: "Review",
		SourceRefs: []protocol.SourceRef{{
			ID:     "github:pr:owner/repo:7",
			Source: "github",
			Kind:   "pull_request",
			Repo:   "owner/repo",
			Branch: "feature/build-thing",
		}},
	}}}

	updated, cmd := m.activateSelected()
	if cmd == nil {
		t.Fatal("activateSelected() returned no command")
	}
	got := updated.(model)
	if !got.loading || got.message != creatingWorkspaceMessage {
		t.Fatalf("activateSelected() loading=%v message=%q, want workspace creation", got.loading, got.message)
	}
}

func TestGitHubPullRequestRepoKeepsRepositoryColons(t *testing.T) {
	ref := protocol.SourceRef{ID: "github:pr:enterprise:owner/repo:7"}
	if got := githubPullRequestRepo(ref); got != "enterprise:owner/repo" {
		t.Fatalf("githubPullRequestRepo() = %q, want repo with colon", got)
	}
}

func TestGitHubPullRequestNumber(t *testing.T) {
	if got := githubPullRequestNumber("github:pr:enterprise:owner/repo:7"); got != "7" {
		t.Fatalf("githubPullRequestNumber() = %q, want PR number", got)
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

func TestTaskLinksUsesMnemonicFallbackForDuplicateSourceLabels(t *testing.T) {
	task := protocol.Task{SourceRefs: []protocol.SourceRef{
		{Source: "github", SourceLabel: "GitHub", URL: "https://github.com/owner/repo/pull/7"},
		{Source: "gitlab", SourceLabel: "GitLab", URL: "https://gitlab.example.test/owner/repo/-/merge_requests/1"},
	}}

	links := taskLinks(task)
	if len(links) != 2 {
		t.Fatalf("taskLinks() returned %d links, want 2: %+v", len(links), links)
	}
	if links[0].Key != "g" || links[1].Key != "i" {
		t.Fatalf("links = %+v, want first available mnemonic per label", links)
	}
}

func TestTaskLinksIncludesSbxSandboxAction(t *testing.T) {
	task := protocol.Task{SourceRefs: []protocol.SourceRef{{
		ID:          "sbx:sandbox:radar-repo-DPSCAP-600-shell",
		Source:      "sbx",
		SourceLabel: "Docker sbx",
		Kind:        "sandbox",
		Title:       "radar-repo-DPSCAP-600-shell",
		Metadata:    map[string]string{"name": "radar-repo-DPSCAP-600-shell"},
	}}}

	links := taskLinks(task)
	if len(links) != 1 {
		t.Fatalf("taskLinks() returned %d links, want 1: %+v", len(links), links)
	}
	if links[0].Key != "s" || links[0].Action != "sbx_shell" || links[0].Source != "Docker sbx" {
		t.Fatalf("sbx link = %+v, want s/Docker sbx action", links[0])
	}
}

func TestTaskLinksUsesSourceLabels(t *testing.T) {
	task := protocol.Task{SourceRefs: []protocol.SourceRef{
		{ID: "jira:issue:RAD-123", Source: "jira", SourceLabel: "Jira", Kind: "issue", URL: "https://jira.example.test/browse/RAD-123"},
		{ID: "github:pr:owner/repo:7", Source: "github", SourceLabel: "GitHub", Kind: "pull_request", URL: "https://github.com/owner/repo/pull/7"},
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
