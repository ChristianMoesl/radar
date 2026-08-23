package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/workspace"
)

type branchOptionsRunner struct{}

func (branchOptionsRunner) LookPath(string) error { return nil }

func (branchOptionsRunner) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	command := name + " " + strings.Join(args, " ")
	switch command {
	case "git fetch --prune origin":
		return "", errors.New("network is offline")
	case "git for-each-ref --format=%(refname)\t%(refname:short)\t%(symref) refs/heads refs/remotes/origin":
		return strings.Join([]string{
			"refs/remotes/origin/main\torigin/main\t",
			"refs/heads/main\tmain\t",
		}, "\n"), nil
	default:
		return "", fmt.Errorf("unexpected command %q", command)
	}
}

func TestLoadBranchOptionsUsesCachedBranchesWhenFetchFails(t *testing.T) {
	msg := loadBranchOptions(context.Background(), branchOptionsRunner{}, "/repo")
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	want := []string{"origin/main", "main"}
	if fmt.Sprint(msg.branches) != fmt.Sprint(want) {
		t.Fatalf("branches = %#v, want %#v", msg.branches, want)
	}
	if !strings.Contains(msg.warning, "using cached branches") {
		t.Fatalf("warning = %q, want cached-branch warning", msg.warning)
	}
}

var _ workspace.Runner = branchOptionsRunner{}

type pullRequestHeadResult struct {
	output string
	err    error
}

type pullRequestHeadRunner struct {
	results map[string]pullRequestHeadResult
	calls   []string
}

func (runner *pullRequestHeadRunner) LookPath(string) error { return nil }

func (runner *pullRequestHeadRunner) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	command := name + " " + strings.Join(args, " ")
	runner.calls = append(runner.calls, command)
	result, ok := runner.results[command]
	if !ok {
		return "", fmt.Errorf("unexpected command %q", command)
	}
	return result.output, result.err
}

func TestEnsurePullRequestHeadBranchCreatesLocalBranchForFork(t *testing.T) {
	notFound := errors.New("not found")
	runner := &pullRequestHeadRunner{results: map[string]pullRequestHeadResult{
		"git show-ref --verify --quiet refs/heads/feature":            {err: notFound},
		"git show-ref --verify --quiet refs/remotes/origin/feature":   {},
		"git fetch --no-tags origin refs/pull/7/head":                 {},
		"git rev-parse --verify FETCH_HEAD^{commit}":                  {output: "fork-commit\n"},
		"git rev-parse --verify refs/remotes/origin/feature^{commit}": {output: "origin-commit\n"},
		"git branch -- feature fork-commit":                           {},
	}}

	warning, err := ensurePullRequestHeadBranch(context.Background(), runner, "/repo", "feature", "7")
	if err != nil || warning != "" {
		t.Fatalf("ensurePullRequestHeadBranch() warning=%q error=%v", warning, err)
	}
	if got := runner.calls[len(runner.calls)-1]; got != "git branch -- feature fork-commit" {
		t.Fatalf("last command = %q, want local branch creation", got)
	}
}

func TestEnsurePullRequestHeadBranchUsesMatchingOriginBranch(t *testing.T) {
	notFound := errors.New("not found")
	runner := &pullRequestHeadRunner{results: map[string]pullRequestHeadResult{
		"git show-ref --verify --quiet refs/heads/feature":            {err: notFound},
		"git show-ref --verify --quiet refs/remotes/origin/feature":   {},
		"git fetch --no-tags origin refs/pull/7/head":                 {},
		"git rev-parse --verify FETCH_HEAD^{commit}":                  {output: "same-commit\n"},
		"git rev-parse --verify refs/remotes/origin/feature^{commit}": {output: "same-commit\n"},
	}}

	if warning, err := ensurePullRequestHeadBranch(context.Background(), runner, "/repo", "feature", "7"); err != nil || warning != "" {
		t.Fatalf("ensurePullRequestHeadBranch() warning=%q error=%v", warning, err)
	}
	for _, call := range runner.calls {
		if strings.HasPrefix(call, "git branch ") {
			t.Fatalf("matching origin branch unexpectedly created local branch: %q", call)
		}
	}
}

func TestEnsurePullRequestHeadBranchRejectsUnavailableHead(t *testing.T) {
	notFound := errors.New("not found")
	offline := errors.New("network is offline")
	runner := &pullRequestHeadRunner{results: map[string]pullRequestHeadResult{
		"git show-ref --verify --quiet refs/heads/feature":          {err: notFound},
		"git show-ref --verify --quiet refs/remotes/origin/feature": {err: notFound},
		"git fetch --no-tags origin refs/pull/7/head":               {err: offline},
	}}

	_, err := ensurePullRequestHeadBranch(context.Background(), runner, "/repo", "feature", "7")
	if err == nil || !strings.Contains(err.Error(), "unavailable on origin") {
		t.Fatalf("ensurePullRequestHeadBranch() error=%v, want unavailable branch error", err)
	}
}

var _ workspace.Runner = (*pullRequestHeadRunner)(nil)

func TestWorkspaceCreationMessageIncludesSetupWarning(t *testing.T) {
	message := workspaceCreationMessage(integration.Workspace{
		SessionName: "repo-small-fix",
		Warning:     "workspace setup could not be started: tmux failed",
	})
	want := "Created repo-small-fix. Warning: workspace setup could not be started: tmux failed"
	if message != want {
		t.Fatalf("workspaceCreationMessage() = %q, want %q", message, want)
	}
}

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

func TestTaskListShowsBusyOnTaskRow(t *testing.T) {
	m := model{tasks: []protocol.Task{{
		Title:     "Workspace",
		Attention: "in_progress",
		Busy:      true,
		SourceRefs: []protocol.SourceRef{{
			ID: "tmux:session:$1", Source: "tmux", Kind: "session", Busy: true,
			Presentation: protocol.SourceRefPresentation{Label: "tmux:session:repo-workspace"},
		}},
	}}}
	view := ansi.Strip(m.taskList(100, 20))
	if !strings.Contains(view, "● busy  Workspace") {
		t.Fatalf("taskList() does not show busy on task row:\n%s", view)
	}
	if !strings.Contains(view, "tmux:session:repo-workspace") || strings.Contains(view, "tmux:session:$1") {
		t.Fatalf("taskList() does not use the tmux presentation label:\n%s", view)
	}
	if strings.Contains(view, "tmux:session:repo-workspace    ● busy") {
		t.Fatalf("taskList() shows busy on source row:\n%s", view)
	}
}

func TestTaskListHidesBusyWhenTaskIsNotBusy(t *testing.T) {
	m := model{tasks: []protocol.Task{{Title: "Workspace", Attention: "in_progress"}}}
	if view := ansi.Strip(m.taskList(100, 20)); strings.Contains(view, "● busy") {
		t.Fatalf("taskList() shows inactive busy signal:\n%s", view)
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

func TestAuthoredTaskTitleEntry(t *testing.T) {
	updated, _ := (model{}).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m := updated.(model)
	if m.mode != "task_authoring" {
		t.Fatalf("mode = %q", m.mode)
	}
	for _, r := range "Write notes" {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(model)
	}
	if m.authoredTitle != "Write notes" || !strings.Contains(m.View(), "Write notes") {
		t.Fatalf("manual title = %q, view=%s", m.authoredTitle, m.View())
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if cmd == nil || m.mode != "" || !m.loading {
		t.Fatalf("submit mode=%q loading=%v command=%v", m.mode, m.loading, cmd)
	}
}

func TestTextInputsAcceptSpaceKey(t *testing.T) {
	tests := []struct {
		name  string
		model model
		want  func(model) string
	}{
		{name: "authored task title", model: model{mode: "task_authoring", authoredTitle: "Write"}, want: func(m model) string { return m.authoredTitle }},
		{name: "workspace name", model: model{mode: "create_name", create: createForm{name: "release"}}, want: func(m model) string { return m.create.name }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			updated, _ := tt.model.Update(tea.KeyMsg{Type: tea.KeySpace})
			got := updated.(model)
			if value := tt.want(got); !strings.HasSuffix(value, " ") {
				t.Fatalf("input after space = %q", value)
			}
		})
	}
}

func authoredTaskForTUITest(state, priority, attention string) protocol.Task {
	return protocol.Task{ID: 7, Attention: attention, SourceRefs: []protocol.SourceRef{{
		ID: "obsidian:task:test", Source: "obsidian", Kind: "task", Role: protocol.SourceRefRoleAuthoritative,
		Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityPrimary,
		Metadata: map[string]string{"authoring": "true", "state": state, "priority": priority},
	}}}
}

func TestAuthoredDoneAndReopenKeys(t *testing.T) {
	for _, state := range []string{"open", "done"} {
		m := model{tasks: []protocol.Task{authoredTaskForTUITest(state, "normal", "low_priority")}}
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
		got := updated.(model)
		if cmd == nil || !got.loading {
			t.Fatalf("state=%s: command=%v loading=%v", state, cmd, got.loading)
		}
	}
}

func TestAuthoredDoneRejectsNonAuthoredTask(t *testing.T) {
	m := model{tasks: []protocol.Task{{ID: 7}}}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	got := updated.(model)
	if cmd != nil || !strings.Contains(got.message, "does not support") {
		t.Fatalf("command=%v message=%q", cmd, got.message)
	}
}

func TestPriorityKeyTogglesAuthoredPriority(t *testing.T) {
	for _, priority := range []string{"normal", "urgent"} {
		task := authoredTaskForTUITest("open", priority, "low_priority")
		m := model{tasks: []protocol.Task{task}}
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
		got := updated.(model)
		if cmd == nil || !got.loading {
			t.Fatalf("task=%+v: command=%v loading=%v", task, cmd, got.loading)
		}
	}
}

func TestPriorityKeyRejectsUnsupportedAndDoneTasks(t *testing.T) {
	tests := []struct {
		task protocol.Task
		want string
	}{
		{task: protocol.Task{ID: 1, Attention: "immediate"}, want: "does not support"},
		{task: authoredTaskForTUITest("done", "normal", "done"), want: "cannot change priority"},
	}
	for _, tt := range tests {
		m := model{tasks: []protocol.Task{tt.task}}
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
		got := updated.(model)
		if cmd != nil || !strings.Contains(got.message, tt.want) {
			t.Fatalf("task=%+v: command=%v message=%q", tt.task, cmd, got.message)
		}
	}
}

func TestPriorityResponseKeepsSelectionByTaskID(t *testing.T) {
	m := model{cursor: 1, tasks: []protocol.Task{{ID: 1, Attention: "attention"}, {ID: 2, Attention: "low_priority"}}}
	m.applyResponse(protocol.Response{Tasks: []protocol.Task{{ID: 1, Attention: "attention"}, {ID: 2, Attention: "immediate"}}}, false)
	if m.cursor != 1 || m.tasks[m.cursor].ID != 2 {
		t.Fatalf("cursor=%d tasks=%+v", m.cursor, m.tasks)
	}
}

func TestResetIsNotAvailableAsUnconfirmedTUIKey(t *testing.T) {
	m := model{width: 240, height: 30}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	got := updated.(model)
	if cmd != nil || got.loading {
		t.Fatalf("R started an action: command=%v loading=%v", cmd, got.loading)
	}
	if strings.Contains(got.View(), "R reset") {
		t.Fatalf("TUI help still advertises reset: %s", got.View())
	}
}

func TestHAndLDoNotNavigateTaskDetails(t *testing.T) {
	m := model{tasks: []protocol.Task{{Title: "Task"}}}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	got := updated.(model)
	if cmd != nil || got.mode != "" {
		t.Fatalf("l command=%v mode=%q, want no navigation", cmd, got.mode)
	}

	m.mode = "detail"
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	got = updated.(model)
	if cmd != nil || got.mode != "detail" {
		t.Fatalf("h command=%v mode=%q, want detail mode unchanged", cmd, got.mode)
	}
	if strings.Contains(got.View(), "/h back") {
		t.Fatalf("detail help still advertises h as back: %s", got.View())
	}
}

func TestHIsNotABackKeyInDialogs(t *testing.T) {
	for _, mode := range []string{"open_link", "worktree_session", "cleanup_confirm"} {
		t.Run(mode, func(t *testing.T) {
			m := model{mode: mode}
			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
			got := updated.(model)
			if cmd != nil || got.mode != mode {
				t.Fatalf("h command=%v mode=%q, want %q unchanged", cmd, got.mode, mode)
			}
		})
	}
}

func TestGarbageCollectionKeyStartsCollection(t *testing.T) {
	updated, cmd := (model{}).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	if cmd == nil {
		t.Fatal("X command = nil")
	}
	got := updated.(model)
	if !got.loading || got.message != "Garbage collecting…" {
		t.Fatalf("X loading=%v message=%q", got.loading, got.message)
	}
}

func TestGarbageCollectionMessageSummarizesResult(t *testing.T) {
	result := protocol.GarbageCollectionResult{
		Deleted: []protocol.GarbageCollectionItem{{Path: "/workspaces/one"}},
		Skipped: []protocol.GarbageCollectionItem{{Path: "/workspaces/two"}},
	}
	if got, want := garbageCollectionMessage(result), "Garbage collection: deleted 1, skipped 1"; got != want {
		t.Fatalf("garbageCollectionMessage() = %q, want %q", got, want)
	}
	if got, want := garbageCollectionMessage(protocol.GarbageCollectionResult{}), "No workspaces eligible for garbage collection"; got != want {
		t.Fatalf("empty garbageCollectionMessage() = %q, want %q", got, want)
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

func TestCreateFlowExplicitlySelectsBranchIntent(t *testing.T) {
	m := model{mode: "create_repo", create: newCreateForm()}
	m.create.repoList = picker{options: []string{"/repo/radar"}}

	updated, cmd := m.selectCreateStep()
	m = updated.(model)
	if cmd != nil || m.mode != "create_intent" || m.create.repo != "/repo/radar" {
		t.Fatalf("repository selection mode=%q repo=%q command=%v", m.mode, m.create.repo, cmd)
	}
	view := m.View()
	newIndex := strings.Index(view, createIntentNew)
	existingIndex := strings.Index(view, createIntentExisting)
	if newIndex == -1 || existingIndex == -1 || newIndex > existingIndex {
		t.Fatalf("intent view choices are not in the expected order:\n%s", view)
	}

	updated, cmd = m.selectCreateStep()
	m = updated.(model)
	if cmd == nil || m.mode != "create_base" || m.create.branchMode != integration.WorkspaceBranchNew {
		t.Fatalf("intent selection mode=%q branchMode=%q command=%v", m.mode, m.create.branchMode, cmd)
	}
}

func TestExistingBranchNamesCombineLocalAndOriginRefs(t *testing.T) {
	got := existingBranchNames([]string{"origin/main", "origin/feature/one", "main", "feature/one", "local-only"})
	want := []string{"main", "feature/one", "local-only"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("existingBranchNames() = %v, want %v", got, want)
	}
}

func TestSelectingExistingBranchSubmitsWithoutNameStep(t *testing.T) {
	m := model{
		mode: "create_branch",
		create: createForm{
			repo:       "/repo/radar",
			branchMode: integration.WorkspaceBranchExisting,
			branchList: picker{options: []string{"main"}},
		},
	}

	updated, cmd := m.selectCreateStep()
	got := updated.(model)
	if cmd == nil || got.mode != "" || !got.loading || got.create.branch != "main" || got.create.name != "main" {
		t.Fatalf("existing selection mode=%q loading=%v branch=%q name=%q command=%v", got.mode, got.loading, got.create.branch, got.create.name, cmd)
	}
}

func TestSelectingExistingBranchPreservesTaskWorkspaceName(t *testing.T) {
	m := model{
		mode: "create_branch",
		create: createForm{
			repo:           "/repo/radar",
			branchMode:     integration.WorkspaceBranchExisting,
			branchList:     picker{options: []string{"main"}},
			name:           "Investigate workspace identity",
			taskLinkingKey: "obsidian:task:one",
		},
	}

	updated, cmd := m.selectCreateStep()
	got := updated.(model)
	if cmd == nil || got.create.branch != "main" || got.create.name != "Investigate workspace identity" {
		t.Fatalf("existing task selection branch=%q name=%q command=%v", got.create.branch, got.create.name, cmd)
	}
}

func TestCreateFormAllowsJAndKInFilters(t *testing.T) {
	m := model{mode: "create_repo", create: createForm{repoList: picker{
		cursor:  1,
		options: []string{"alpha", "beta", "gamma"},
	}}}

	for _, key := range []rune{'j', 'k'} {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
		m = updated.(model)
	}

	if got, want := m.create.repoList.query, "jk"; got != want {
		t.Fatalf("filter query = %q, want %q", got, want)
	}
	if got, want := m.create.repoList.cursor, 0; got != want {
		t.Fatalf("filter cursor = %d, want %d", got, want)
	}
}

func TestCreateFormNavigatesWithCtrlPAndCtrlN(t *testing.T) {
	m := model{mode: "create_repo", create: createForm{repoList: picker{options: []string{"alpha", "beta", "gamma"}}}}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	m = updated.(model)
	if got, want := m.create.repoList.cursor, 1; got != want {
		t.Fatalf("cursor after ctrl+n = %d, want %d", got, want)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = updated.(model)
	if got, want := m.create.repoList.cursor, 0; got != want {
		t.Fatalf("cursor after ctrl+p = %d, want %d", got, want)
	}
}

func TestCreateNameViewRendersSelectedRepoAndBase(t *testing.T) {
	model := model{mode: "create_name", create: createForm{repo: "/repo/radar", base: "origin/main", name: "small-fix"}}

	view := model.View()
	for _, want := range []string{"Create workspace", "Repository /repo/radar", "Start      origin/main", "New branch small-fix"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q:\n%s", want, view)
		}
	}
}

func TestSubmitCreateShowsCreatingWorkspaceNotification(t *testing.T) {
	m := model{mode: "create_name", create: createForm{repo: "/repo/radar", branchMode: integration.WorkspaceBranchNew, base: "origin/main", name: "small-fix"}}

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

func TestTaskDetailsShortenHomePathsWithoutChangingSourceRefs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, "workspaces", "radar", "small-fix")
	notePath := filepath.Join(home, "Documents", "Tasks", "Small fix.md")
	id := "git:worktree:" + path
	model := model{tasks: []protocol.Task{{
		Title:    "small fix",
		Metadata: map[string]string{"task_path": notePath},
		SourceRefs: []protocol.SourceRef{{
			ID: id, Source: "git", Kind: "worktree", Path: path,
			Metadata: map[string]string{"working_directory": path},
		}},
	}}}

	view := model.detailView(120)
	for _, want := range []string{
		"git:worktree:" + filepath.Join("~", "workspaces", "radar", "small-fix"),
		filepath.Join("~", "Documents", "Tasks", "Small fix.md"),
		filepath.Join("~", "workspaces", "radar", "small-fix"),
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("detailView() missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, home) {
		t.Fatalf("detailView() contains absolute home path:\n%s", view)
	}
	if got := model.tasks[0].SourceRefs[0]; got.ID != id || got.Path != path || got.Metadata["working_directory"] != path {
		t.Fatalf("detailView() changed source ref: %+v", got)
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
	want := []int{1, 3, 0, 2}
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
	if model.cursor != 0 {
		t.Fatalf("cursor after second down = %d, want 0", model.cursor)
	}
	model.moveCursor(-1)
	if model.cursor != 3 {
		t.Fatalf("cursor after up = %d, want 3", model.cursor)
	}
}

func TestCtrlDAndCtrlUMoveByOnePage(t *testing.T) {
	t.Setenv("TMUX", "")
	tasks := make([]protocol.Task, 30)
	for i := range tasks {
		tasks[i] = protocol.Task{Title: fmt.Sprintf("task %d", i), Attention: "attention"}
	}
	m := model{width: 100, height: 20, tasks: tasks}
	pageHeight := m.taskListHeight(m.contentWidth())

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	m = updated.(model)
	if m.cursor != pageHeight || m.scroll != pageHeight {
		t.Fatalf("after ctrl+d cursor=%d scroll=%d, want %d for both", m.cursor, m.scroll, pageHeight)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	m = updated.(model)
	if m.cursor != 0 || m.scroll != 0 {
		t.Fatalf("after ctrl+u cursor=%d scroll=%d, want 0 for both", m.cursor, m.scroll)
	}
}

func TestCtrlDAndCtrlUOnlyMoveInMainView(t *testing.T) {
	tasks := make([]protocol.Task, 30)
	for i := range tasks {
		tasks[i] = protocol.Task{Title: fmt.Sprintf("task %d", i), Attention: "attention"}
	}

	for _, mode := range []string{"detail", "open_link", "worktree_session", "cleanup_confirm", "task_authoring", "create_repo"} {
		t.Run(mode, func(t *testing.T) {
			for _, key := range []tea.KeyType{tea.KeyCtrlD, tea.KeyCtrlU} {
				m := model{width: 100, height: 20, mode: mode, tasks: tasks}
				if key == tea.KeyCtrlU {
					m.cursor = 10
					m.scroll = 10
				}
				wantCursor, wantScroll := m.cursor, m.scroll
				updated, _ := m.Update(tea.KeyMsg{Type: key})
				got := updated.(model)
				if got.cursor != wantCursor || got.scroll != wantScroll {
					t.Fatalf("key %s moved cursor=%d scroll=%d, want %d and %d", tea.KeyMsg{Type: key}.String(), got.cursor, got.scroll, wantCursor, wantScroll)
				}
			}
		})
	}
}

func TestTaskListRendersLowPriorityBeforeDone(t *testing.T) {
	model := model{tasks: []protocol.Task{
		{Title: "done", Attention: "done"},
		{Title: "low", Attention: "low_priority"},
	}}

	lines, _, _ := model.taskLines(100)
	view := ansi.Strip(strings.Join(lines, "\n"))
	low := strings.Index(view, "Low priority")
	done := strings.Index(view, "Done (last 3 days)")
	if low < 0 || done < 0 || low > done {
		t.Fatalf("low priority should render before done:\n%s", view)
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

func TestCleanupConfirmViewShowsEveryLocalResourceAndDirtyWarning(t *testing.T) {
	model := model{mode: "cleanup_confirm", cleanup: protocol.CleanupPreview{Targets: []protocol.CleanupTarget{
		{Source: "tmux", Kind: "session", SessionName: "repo-small-fix"},
		{Source: "sbx", Kind: "sandbox", SandboxName: "small-fix-12345678"},
		{Source: "git", Kind: "worktree", Path: "/repo/worktrees/small-fix", Branch: "small-fix", Dirty: true, DeleteBranch: true, Unpublished: true},
	}}}

	view := model.View()
	for _, want := range []string{"Clean up local resources?", "Uncommitted changes will be discarded", "Radar-managed worktrees", "may exist only locally", "repo-small-fix", "small-fix-12345678", "/repo/worktrees/small-fix", "deletes branch small-fix", "unpublished", "Press y to clean up"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q:\n%s", want, view)
		}
	}
}

func TestActivateSelectedStartsLinkedWorkspaceCreateForAuthoredTask(t *testing.T) {
	m := model{tasks: []protocol.Task{{Title: "One task", SourceRefs: []protocol.SourceRef{{
		ID: "obsidian:task:1", Source: "obsidian", Kind: "task", Role: protocol.SourceRefRoleAuthoritative,
		Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityPrimary,
		CanonicalKey: "obsidian:task:1", LinkingKeys: []string{"obsidian:task:1"},
		Presentation: protocol.SourceRefPresentation{WorkspaceName: "One task"},
		Metadata:     map[string]string{"authoring": "true", "radar_id": "1", "note_path": "/vault/Tasks/One.md"},
	}}}}}

	updated, cmd := m.activateSelected()
	if cmd == nil {
		t.Fatal("activateSelected() returned no command")
	}
	got := updated.(model)
	if got.mode != "create_repo" || got.create.name != "One task" || got.create.taskLinkingKey != "obsidian:task:1" {
		t.Fatalf("activateSelected() mode=%q create=%+v", got.mode, got.create)
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
			ID:           "jira:issue:ABC-123",
			Source:       "jira",
			Kind:         "issue",
			Role:         protocol.SourceRefRoleAuthoritative,
			Title:        "ABC-123 Build the thing",
			Presentation: protocol.SourceRefPresentation{WorkspaceName: "ABC-123 Build the thing"},
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
		ID:           "jira:issue:ABC-123",
		Source:       "jira",
		Kind:         "issue",
		Role:         protocol.SourceRefRoleAuthoritative,
		Presentation: protocol.SourceRefPresentation{WorkspaceName: "ABC-123"},
	}}}

	if got := workspaceNameForTask(task); got != "ABC-123" {
		t.Fatalf("workspaceNameForTask() = %q, want Jira key", got)
	}
}

func TestWorkspaceNameForTaskUsesPullRequestOriginBranchWithoutOriginPrefix(t *testing.T) {
	task := protocol.Task{Title: "Review", SourceRefs: []protocol.SourceRef{{
		ID:           "github:pr:owner/repo:7",
		Source:       "github",
		Kind:         "pull_request",
		Role:         protocol.SourceRefRoleAuthoritative,
		Branch:       "origin/feature/build-thing",
		Presentation: protocol.SourceRefPresentation{WorkspaceName: "feature/build-thing"},
	}}}

	if got := workspaceNameForTask(task); got != "feature/build-thing" {
		t.Fatalf("workspaceNameForTask() = %q, want PR branch without origin prefix", got)
	}
}

func TestActivateSelectedCreatesWorkspaceForPullRequestOnlyTask(t *testing.T) {
	m := model{tasks: []protocol.Task{{
		Title: "Review",
		SourceRefs: []protocol.SourceRef{{
			ID:           "github:pr:owner/repo:7",
			Source:       "github",
			Kind:         "pull_request",
			Role:         protocol.SourceRefRoleAuthoritative,
			Repo:         "owner/repo",
			Branch:       "feature/build-thing",
			Presentation: protocol.SourceRefPresentation{WorkspaceName: "feature/build-thing"},
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

func TestTaskLinksDoesNotAssignQuitKey(t *testing.T) {
	links := taskLinks(protocol.Task{SourceRefs: []protocol.SourceRef{{
		Source: "queue", SourceLabel: "Queue", URL: "https://example.test/queue",
	}}})
	if len(links) != 1 || links[0].Key != "u" {
		t.Fatalf("links = %+v, want u because q quits the dialog", links)
	}
}

func TestOpenLinkHActivatesDisplayedChoice(t *testing.T) {
	m := model{
		mode:  "open_link",
		tasks: []protocol.Task{{Title: "Task"}},
		links: []linkChoice{{Key: "h", URL: "https://example.test/task"}},
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	got := updated.(model)
	if cmd == nil || !got.loading || got.mode != "" || got.links != nil {
		t.Fatalf("h command=%v loading=%v mode=%q links=%+v", cmd, got.loading, got.mode, got.links)
	}
}

func TestTaskLinksIncludesSbxSandboxAction(t *testing.T) {
	task := protocol.Task{SourceRefs: []protocol.SourceRef{{
		ID:          "sbx:sandbox:radar-repo-ABC-600-shell",
		Source:      "sbx",
		SourceLabel: "Docker sbx",
		Kind:        "sandbox",
		Title:       "radar-repo-ABC-600-shell",
		Metadata:    map[string]string{"name": "radar-repo-ABC-600-shell"},
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

func TestObsidianTaskOffersOneSourceOwnedOpenAction(t *testing.T) {
	uri := "obsidian://open?vault=Work&file=Tasks%2FOne+task.md"
	task := protocol.Task{Title: "Task", URL: uri, SourceRefs: []protocol.SourceRef{{
		ID: "obsidian:task:1", Source: "obsidian", SourceLabel: "Obsidian", Kind: "task", URL: uri,
	}}}
	links := taskLinks(task)
	if len(links) != 1 || links[0].Action != "obsidian_open" || links[0].Source != "Obsidian" {
		t.Fatalf("links = %+v", links)
	}
}
