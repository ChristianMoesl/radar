package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/exp/teatest"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"radar/internal/integration"
	"radar/internal/protocol"
)

func operationFixture() model {
	return model{width: 120, height: 30, tasks: []protocol.Task{
		{ID: 1, Title: "ABC-123 Improve navigation", Attention: "attention", SourceRefs: []protocol.SourceRef{{ID: "jira:issue:ABC-123"}}},
		{ID: 2, Title: "ABC-456 Fix login", Attention: "attention"},
	}}
}

func updateOperation(m model, msg tea.Msg) model {
	updated, _ := m.Update(msg)
	return updated.(model)
}

func runeKey(key rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}}
}

func TestResourceOperationEntryPoints(t *testing.T) {
	for _, tc := range []struct {
		name  string
		start func(model) (tea.Model, tea.Cmd)
		label string
	}{
		{"inspect", func(m model) (tea.Model, tea.Cmd) { return m.Update(runeKey('w')) }, "Inspecting workspace…"},
		{"prepare", func(m model) (tea.Model, tea.Cmd) {
			m.editor = workspaceEditor{active: true, task: m.tasks[0]}
			return m.previewWorkspace()
		}, "Preparing changes…"},
		{"create", func(m model) (tea.Model, tea.Cmd) {
			m.editor = workspaceEditor{active: true, task: m.tasks[0], plan: integration.WorkspaceReconcilePlan{Changes: []integration.WorkspaceChange{{Action: "add", Resource: "workspace"}}}}
			return m.applyWorkspace()
		}, "Creating workspace…"},
		{"update", func(m model) (tea.Model, tea.Cmd) {
			m.editor = workspaceEditor{active: true, task: m.tasks[0], state: integration.WorkspaceState{Path: "/work/plan"}}
			return m.applyWorkspace()
		}, "Updating workspace…"},
		{"check cleanup", func(m model) (tea.Model, tea.Cmd) { return m.Update(runeKey('x')) }, "Checking local resources…"},
		{"cleanup", func(m model) (tea.Model, tea.Cmd) {
			m.mode, m.cleanupTask = "cleanup_confirm", m.tasks[0]
			m.cleanup = cleanupFixture()
			return m.Update(runeKey('y'))
		}, "Cleaning up…"},
		{"workspace session", func(m model) (tea.Model, tea.Cmd) {
			m.tasks[0].SourceRefs = []protocol.SourceRef{{ID: "workspace:one", WorkspaceEntry: true, Path: "/work/plan"}}
			return m.activateSelected()
		}, "Starting session…"},
		{"worktree session", func(m model) (tea.Model, tea.Cmd) {
			m.tasks[0].SourceRefs = []protocol.SourceRef{{ID: "git:one", Source: "git", Kind: "worktree", ProvidesWorkspace: true, Path: "/work/plan/app"}}
			return m.activateSelected()
		}, "Starting session…"},
		{"chosen worktree session", func(m model) (tea.Model, tea.Cmd) {
			m.mode, m.worktreeTask = "worktree_session", m.tasks[0]
			m.worktrees = []protocol.SourceRef{{ID: "git:one", Path: "/work/plan/app"}}
			return m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		}, "Starting session…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			updated, cmd := tc.start(operationFixture())
			m := updated.(model)
			if cmd == nil || m.operation.label != tc.label || m.message != "" || m.loading || m.err != nil {
				t.Fatalf("operation=%+v, notification=%q, error=%v", m.operation, m.message, m.err)
			}
			if batch, ok := cmd().(tea.BatchMsg); !ok || len(batch) != 2 {
				t.Fatal("operation and animation must both be scheduled")
			}
			view := ansi.Strip(m.View())
			if !strings.Contains(view, "› ⠋ ABC-123") || !strings.Contains(view, tc.label) || !strings.Contains(view, m.tasks[1].Title) {
				t.Fatalf("operation is not on its task row:\n%s", view)
			}
		})
	}
}

func TestOperationAnimationAndNavigation(t *testing.T) {
	m := updateOperation(operationFixture(), runeKey('w'))
	generation := m.operationGeneration
	before := m.operationMarker()
	updated, cmd := m.Update(operationTickMsg(generation))
	m = updated.(model)
	if cmd == nil || m.operationMarker() == before {
		t.Fatal("spinner did not advance and schedule the next frame")
	}
	m = updateOperation(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.cursor != 1 || m.operation.task.ID != 1 {
		t.Fatal("navigation moved the operation to another task")
	}
	for _, key := range []tea.KeyMsg{runeKey('w'), runeKey('x'), runeKey('X'), runeKey('c'), runeKey('n'), runeKey('p'), runeKey('d'), runeKey('o'), {Type: tea.KeyEnter}} {
		updated, cmd = m.Update(key)
		if cmd != nil || updated.(model).operationGeneration != generation {
			t.Fatalf("%s submitted a conflicting action", key.String())
		}
	}
	m = updateOperation(m, runeKey('i'))
	if m.mode != "detail" || m.detail.task.ID != 2 {
		t.Fatal("cannot inspect another task while processing")
	}
	m = updateOperation(m, tea.KeyMsg{Type: tea.KeyEsc})
	m = updateOperation(m, tea.KeyMsg{Type: tea.KeyUp})
	m = updateOperation(m, runeKey('i'))
	if !strings.Contains(m.View(), "Inspecting workspace…") {
		t.Fatal("Inspect does not expose the active operation")
	}
	m = updateOperation(m, workspaceStateMsg{err: errors.New("workspace registry unavailable")})
	if m.operation.kind != "" || m.err != nil || len(m.taskFailures) != 1 {
		t.Fatal("failure did not stop animation and attach to task")
	}
	_, cmd = m.Update(operationTickMsg(generation))
	if cmd != nil {
		t.Fatal("completed operation kept scheduling animation")
	}
	m = updateOperation(m, runeKey('w'))
	updated, cmd = m.Update(operationTickMsg(generation))
	if cmd != nil || updated.(model).operationFrame != 0 {
		t.Fatal("stale animation tick affected a new operation")
	}
}

func TestCleanupSpinsOnlyDuringWorkAndRetainsTarget(t *testing.T) {
	m := updateOperation(operationFixture(), runeKey('x'))
	m = updateOperation(m, tea.KeyMsg{Type: tea.KeyDown})
	preview := cleanupFixture()
	preview.TaskID = 1
	m = updateOperation(m, cleanupPreviewMsg{preview: preview})
	if m.operation.kind != "" || m.mode != "cleanup_confirm" || m.cleanupTask.ID != 1 {
		t.Fatal("cleanup confirmation lost the initiating task or kept spinning")
	}
	m = updateOperation(m, runeKey('y'))
	if m.mode != "" || m.operation.task.ID != 1 || m.operation.label != "Cleaning up…" {
		t.Fatal("confirmed cleanup runs on the current selection instead of the preview target")
	}
	m = updateOperation(m, taskActionMsg{action: actionMsg{err: errors.New("sandbox removal failed")}})
	if m.err != nil || m.message != "" || m.operation.kind != "" || len(m.taskFailures) != 1 {
		t.Fatal("cleanup error was not attached to its task")
	}
}

func TestTaskFailureSurvivesRefreshAndClearsOnlyOnSuccessfulOperation(t *testing.T) {
	m := operationFixture()
	origin := m.tasks[0]
	m.startOperation(origin, "cleanup", "Cleaning up…", "Cleanup failed", nil)
	m = updateOperation(m, taskActionMsg{action: actionMsg{err: errors.New("cannot remove /work/plan: permission denied")}})
	m = updateOperation(m, tea.KeyMsg{Type: tea.KeyDown})
	// Task IDs may change when sources regroup; follow source identity as the
	// existing overview selection does, not the current row index.
	replacement := origin
	replacement.ID = 99
	m = updateOperation(m, watchMsg{response: protocol.Response{Tasks: []protocol.Task{m.tasks[1], replacement}}})
	m = updateOperation(m, fetchMsg{response: protocol.Response{Tasks: m.tasks}})
	if len(m.taskFailures) != 1 || m.err != nil {
		t.Fatal("watch or refresh cleared the contextual failure")
	}
	if marker, _ := m.taskOperationStatus(m.tasks[0]); strings.TrimSpace(marker) != "" {
		t.Fatal("error leaked onto another task")
	}
	if _, label := m.taskOperationStatus(replacement); label != "Cleanup failed · i inspect" {
		t.Fatalf("failure lost after regrouping: %q", label)
	}
	m.cursor = 1
	m = updateOperation(m, runeKey('i'))
	if !strings.Contains(m.View(), "permission denied") {
		t.Fatal("full error is not accessible in Inspect")
	}
	m = updateOperation(m, tea.KeyMsg{Type: tea.KeyEsc})
	m.startOperation(replacement, "cleanup-check", "Checking local resources…", "Resource check failed", nil)
	m = updateOperation(m, cleanupPreviewMsg{preview: cleanupFixture()})
	if len(m.taskFailures) != 1 {
		t.Fatal("successful inspection erased the previous cleanup failure")
	}
	m.mode = ""
	m.startOperation(replacement, "cleanup", "Cleaning up…", "Cleanup failed", nil)
	if _, label := m.taskOperationStatus(replacement); label != "Cleaning up…" || !strings.Contains(m.operationDetails(replacement, 100), "permission denied") {
		t.Fatal("new attempt must replace row error but retain explanation until success")
	}
	m = updateOperation(m, taskActionMsg{action: actionMsg{err: errors.New("sandbox is still busy")}})
	if len(m.taskFailures) != 1 || m.taskFailures[0].err.Error() != "sandbox is still busy" {
		t.Fatal("new failure did not replace the previous explanation")
	}
	m.startOperation(replacement, "cleanup", "Cleaning up…", "Cleanup failed", nil)
	m = updateOperation(m, taskActionMsg{action: actionMsg{message: "Cleaned up 3 local resources"}})
	if len(m.taskFailures) != 0 || m.operation.kind != "" || m.message != "" {
		t.Fatal("success did not clear the error quietly")
	}
}

func TestWorkspaceOperationPhasesAndWarnings(t *testing.T) {
	t.Setenv("TMUX", "")
	m := updateOperation(operationFixture(), runeKey('w'))
	editor := m.editor
	editor.create.Name = "Plan"
	m = updateOperation(m, workspaceStateMsg{editor: editor})
	if m.operation.kind != "" || m.mode != "workspace_edit" {
		t.Fatal("workspace editor kept spinning while awaiting input")
	}
	m = updateOperation(m, tea.KeyMsg{Type: tea.KeyEnter})
	m = updateOperation(m, workspacePlanMsg{plan: integration.WorkspaceReconcilePlan{Changes: []integration.WorkspaceChange{{Action: "add", Resource: "workspace"}}}})
	if m.operation.label != "Creating workspace…" {
		t.Fatal("creation plan did not advance the row status")
	}
	m = updateOperation(m, workspaceAppliedMsg{created: integration.Workspace{Path: "/work/plan", Warning: "Session could not be opened"}})
	if m.operation.kind == "" {
		t.Fatal("spinner stopped before local resources were refreshed")
	}
	m = updateOperation(m, taskActionMsg{action: actionMsg{message: m.message}})
	if m.operation.kind != "" || !strings.Contains(m.message, "Session could not be opened") {
		t.Fatal("completion discarded a meaningful workspace warning")
	}
}

func TestWorkspaceFailureRefreshDoesNotEraseExplanation(t *testing.T) {
	m := operationFixture()
	m.editor = workspaceEditor{active: true, task: m.tasks[0], state: integration.WorkspaceState{Path: "/work/plan"}}
	updated, _ := m.applyWorkspace()
	m = updated.(model)
	m = updateOperation(m, workspaceAppliedMsg{result: integration.WorkspaceReconcileResult{Error: "could not mount repository"}})
	if m.editor.active || m.err != nil || len(m.taskFailures) != 1 || m.operation.kind != "" {
		t.Fatal("partial apply must discard the stale draft and preserve a task error")
	}
	m = updateOperation(m, actionMsg{response: &protocol.Response{Tasks: m.tasks}})
	m = updateOperation(m, runeKey('i'))
	if !strings.Contains(m.View(), "could not mount repository") || !strings.Contains(m.View(), "reopen the editor") {
		t.Fatal("refresh erased the partial-apply explanation or recovery guidance")
	}
}

func TestOperationLayoutHasFixedSlotAndFitsNarrowViews(t *testing.T) {
	t.Setenv("TMUX", "test")
	for _, width := range []int{40, 60, 80, 120} {
		m := operationFixture()
		m.width, m.height = width, 40
		column := renderedColumnIndex(m.View(), "ABC-123")
		m = updateOperation(m, runeKey('x'))
		if got := renderedColumnIndex(m.View(), "ABC-123"); got != column {
			t.Fatalf("width %d: title shifted from %d to %d", width, column, got)
		}
		assertNoWideLines(t, m.View(), width)
		m = updateOperation(m, cleanupPreviewMsg{err: errors.New(strings.Repeat("very long error path/", 20))})
		if got := renderedColumnIndex(m.View(), "ABC-123"); got != column {
			t.Fatalf("width %d: error shifted title from %d to %d", width, column, got)
		}
		assertNoWideLines(t, m.View(), width)
		m = updateOperation(m, runeKey('i'))
		assertNoWideLines(t, m.View(), width)
		if lipgloss.Height(m.View()) > m.height {
			t.Fatal("wrapped error overflowed Inspect viewport")
		}
	}
}

func TestWorkspaceWithoutTaskShowsInlineSpinner(t *testing.T) {
	m := model{mode: "workspace_edit", editor: workspaceEditor{active: true, create: integration.ManagedWorkspaceRequest{Name: "Plan"}}}
	m = updateOperation(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.message != "" || !strings.Contains(m.View(), "⠋ Preparing changes…") || strings.Contains(m.View(), "enter create") {
		t.Fatal("standalone workspace preparation lacks inline progress or advertises duplicate submission")
	}
}

func TestUnrelatedMessagesDoNotCompleteResourceOperation(t *testing.T) {
	m := updateOperation(operationFixture(), runeKey('x'))
	for _, msg := range []tea.Msg{watchMsg{}, fetchMsg{}, actionMsg{message: "Unrelated action"}} {
		m = updateOperation(m, msg)
		if m.operation.kind != "cleanup-check" {
			t.Fatal("unrelated message completed resource operation")
		}
	}
}

func TestTaskActionTagsResult(t *testing.T) {
	problem := errors.New("session provider unavailable")
	cmd := taskAction(func() tea.Msg { return actionMsg{err: problem} })
	if msg, ok := cmd().(taskActionMsg); !ok || msg.action.err != problem {
		t.Fatal("operation result lost its tag or original error")
	}
}

func TestWorkspaceConfirmationStopsAnimationAndPlanFailureStaysAccessible(t *testing.T) {
	m := operationFixture()
	m.editor = workspaceEditor{active: true, task: m.tasks[0], state: integration.WorkspaceState{Path: "/work/plan"}}
	updated, _ := m.previewWorkspace()
	m = updated.(model)
	m = updateOperation(m, workspacePlanMsg{err: errors.New("cannot inspect branch publication")})
	if m.operation.kind != "" || m.mode != "workspace_edit" || m.err != nil || !strings.Contains(m.View(), "cannot inspect branch publication") {
		t.Fatal("preview failure did not preserve the draft and contextual explanation")
	}
	updated, _ = m.previewWorkspace()
	m = updated.(model)
	plan := integration.WorkspaceReconcilePlan{Changes: []integration.WorkspaceChange{{Action: "remove", Resource: "worktree", Summary: "Remove app worktree"}}}
	m = updateOperation(m, workspacePlanMsg{plan: plan})
	if m.operation.kind != "" || m.mode != "workspace_confirm" || len(m.taskFailures) != 0 {
		t.Fatal("successful preview did not clear its error and stop for confirmation")
	}
	m = updateOperation(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.operation.label != "Updating workspace…" {
		t.Fatal("confirmed edit did not start processing")
	}
	m = updateOperation(m, workspaceAppliedMsg{result: integration.WorkspaceReconcileResult{ReconfirmRequired: true, Plan: &plan}})
	if m.operation.kind != "" || m.mode != "workspace_confirm" {
		t.Fatal("changed plan kept spinning instead of awaiting renewed approval")
	}
}

func TestCleanupCompletionCanRemoveInitiatingTask(t *testing.T) {
	m := operationFixture()
	m.startOperation(m.tasks[0], "cleanup", "Cleaning up…", "Cleanup failed", nil)
	m = updateOperation(m, taskActionMsg{action: actionMsg{response: &protocol.Response{Tasks: m.tasks[1:]}}})
	if len(m.tasks) != 1 || m.operation.kind != "" || m.cursor != 0 || m.tasks[0].ID != 2 {
		t.Fatal("cleanup completion did not safely remove the initiating task")
	}
	if marker, status := m.taskOperationStatus(m.tasks[0]); strings.TrimSpace(marker) != "" || status != "" {
		t.Fatal("completed operation leaked onto the remaining task")
	}
}

func TestTaskActionFailureCannotSwitchOrRefresh(t *testing.T) {
	m := operationFixture()
	m.startOperation(m.tasks[0], "session", "Starting session…", "Session start failed", nil)
	updated, cmd := m.Update(taskActionMsg{action: actionMsg{err: errors.New("failed"), quit: true, refresh: true}})
	if cmd != nil || len(updated.(model).taskFailures) != 1 {
		t.Fatal("moving the error into task state changed completion semantics")
	}
}

// Exercise the actual Bubble Tea command loop without touching real resources.
type asynchronousOperationModel struct {
	staticTUIModel
	initial tea.Cmd
}

func (m asynchronousOperationModel) Init() tea.Cmd { return m.initial }

func TestOperationRunsAsynchronouslyWithTeatest(t *testing.T) {
	t.Setenv("TMUX", "test")
	m := operationFixture()
	result := make(chan tea.Msg, 1)
	defer close(result)
	cmd := m.startOperation(m.tasks[0], "session", "Starting session…", "Session start failed", taskAction(func() tea.Msg {
		if msg, ok := <-result; ok {
			return msg
		}
		return actionMsg{}
	}))
	tm := teatest.NewTestModel(t, asynchronousOperationModel{staticTUIModel: staticTUIModel{model: m}, initial: cmd}, teatest.WithInitialTermSize(120, 30))
	teatest.WaitFor(t, tm.Output(), func(output []byte) bool { return strings.Contains(ansi.Strip(string(output)), "Starting session…") }, teatest.WithDuration(time.Second))
	tm.Send(tea.KeyMsg{Type: tea.KeyDown})
	result <- actionMsg{err: errors.New("session launcher unavailable")}
	teatest.WaitFor(t, tm.Output(), func(output []byte) bool { return strings.Contains(ansi.Strip(string(output)), "Session start failed") }, teatest.WithDuration(time.Second))
	tm.Send(tea.KeyMsg{Type: tea.KeyUp})
	tm.Send(runeKey('i'))
	teatest.WaitFor(t, tm.Output(), func(output []byte) bool {
		return strings.Contains(ansi.Strip(string(output)), "session launcher unavailable")
	}, teatest.WithDuration(time.Second))
	tm.Send(runeKey('q'))
	final := tm.FinalModel(t, teatest.WithFinalTimeout(time.Second)).(staticTUIModel).model
	if final.operation.kind != "" || final.detail.task.ID != 1 || len(final.taskFailures) != 1 {
		t.Fatal("asynchronous operation lost its initiating task or failure")
	}
}
