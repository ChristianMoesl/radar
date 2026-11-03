package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest"

	"radar/internal/protocol"
)

func TestCleanupEnterExplainsTemporarySwitchRestriction(t *testing.T) {
	for _, kind := range []string{"cleanup", "cleanup-check"} {
		for _, cursor := range []int{0, 1} {
			for _, width := range []int{40, 120} {
				t.Run(fmt.Sprintf("%s/row=%d/width=%d", kind, cursor, width), func(t *testing.T) {
					m := operationFixture()
					m.width, m.cursor = width, cursor
					m.startOperation(m.tasks[0], kind, "Cleaning up…", "Cleanup failed", nil)
					operation, generation := m.operation, m.operationGeneration
					updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
					m = updated.(model)
					if cmd != nil || m.operation.kind != operation.kind || m.operation.task.ID != operation.task.ID || m.operationGeneration != generation || m.cursor != cursor {
						t.Fatal("blocked Enter changed selection, cleanup or submitted another operation")
					}
					view := ansi.Strip(m.View())
					for _, want := range []string{"Cleanup in progress", "Retry Enter when finished"} {
						if !strings.Contains(view, want) {
							t.Fatalf("blocked switch has no visible %q feedback:\n%s", want, view)
						}
					}
					assertNoWideLines(t, m.View(), width)
				})
			}
		}
	}
}

func TestCleanupCompletionRestoresWorkspaceEnter(t *testing.T) {
	for _, outcome := range []string{"success", "daemon error", "transport error", "initiating task removed"} {
		t.Run(outcome, func(t *testing.T) {
			m := operationFixture()
			m.tasks[1].SourceRefs = []protocol.SourceRef{{ID: "workspace:other", Source: "workspace", Kind: "workspace", Path: "/work/other", WorkspaceEntry: true, ProvidesWorkspace: true}}
			m.startOperation(m.tasks[0], "cleanup", "Cleaning up…", "Cleanup failed", nil)
			m = updateOperation(m, tea.KeyMsg{Type: tea.KeyDown})
			m = updateOperation(m, tea.KeyMsg{Type: tea.KeyEnter})
			if m.message == "" {
				t.Fatal("blocked request was silently discarded")
			}
			response := protocol.Response{Tasks: m.tasks, ResourceRefreshPending: true}
			action := actionMsg{response: &response}
			switch outcome {
			case "daemon error":
				action.err = errors.New("sandbox removal failed")
			case "transport error":
				action.response = nil
				action.err = errors.New("daemon disconnected")
			case "initiating task removed":
				response.Tasks = m.tasks[1:]
			}
			updated, cmd := m.Update(taskActionMsg{action: action})
			m = updated.(model)
			if cmd != nil || m.operation.kind != "" || m.message != "" {
				t.Fatal("completion retained a switch restriction or automatically replayed the blocked request")
			}
			if action.err != nil && (len(m.taskFailures) != 1 || m.taskFailures[0].operation.task.ID != 1 || m.taskFailures[0].err != action.err) {
				t.Fatal("cleanup failure was not retained on its initiating task")
			}
			updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = updated.(model)
			if cmd == nil || m.operation.kind != "session" || m.operation.task.ID != 2 {
				t.Fatal("Enter did not resume opening the selected workspace after cleanup finished")
			}
			if action.response != nil && !m.resourceRefreshPending {
				t.Fatal("switching recovery falsely marked the pending resource snapshot fresh")
			}
		})
	}
}

func TestCleanupSwitchFeedbackDoesNotInterceptOtherEnterContexts(t *testing.T) {
	for _, mode := range []string{"search", "detail", "cleanup_confirm"} {
		t.Run(mode, func(t *testing.T) {
			m := operationFixture()
			m.startOperation(m.tasks[0], "cleanup", "Cleaning up…", "Cleanup failed", nil)
			m.mode, m.searchQuery = mode, "ABC"
			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = updated.(model)
			if cmd != nil || m.message != "" || m.operation.kind != "cleanup" {
				t.Fatal("non-overview Enter changed cleanup or displayed a workspace-switch warning")
			}
			if mode == "search" && (m.mode != "" || m.searchQuery != "ABC") {
				t.Fatal("Enter no longer applies search independently of cleanup")
			}
		})
	}
	m := sectionFixture()
	m.startOperation(m.tasks[0], "cleanup", "Cleaning up…", "Cleanup failed", nil)
	m.selectEntry(visibleEntry{section: "done"})
	m = pressSectionKey(t, m, "enter")
	if !m.sectionExpanded("done") || m.message != "" || m.operation.kind != "cleanup" {
		t.Fatal("section-header Enter was mistaken for a workspace-switch request")
	}
}

func TestCleanupSwitchRecoversAsynchronouslyWithTeatest(t *testing.T) {
	m := operationFixture()
	m.tasks[1].SourceRefs = []protocol.SourceRef{{ID: "workspace:other", Source: "workspace", Kind: "workspace", Path: "/work/other", WorkspaceEntry: true, ProvidesWorkspace: true}}
	result := make(chan actionMsg, 1)
	defer close(result)
	initial := m.startOperation(m.tasks[0], "cleanup", "Cleaning up…", "Cleanup failed", taskAction(func() tea.Msg {
		return <-result
	}))
	tm := teatest.NewTestModel(t, asynchronousOperationModel{staticTUIModel: staticTUIModel{model: m}, initial: initial}, teatest.WithInitialTermSize(120, 30))
	waitFor := func(text string) {
		t.Helper()
		teatest.WaitFor(t, tm.Output(), func(output []byte) bool {
			return strings.Contains(ansi.Strip(string(output)), text)
		}, teatest.WithDuration(time.Second))
	}
	waitFor("Cleaning up…")
	tm.Send(tea.KeyMsg{Type: tea.KeyDown})
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	waitFor("Retry Enter when finished")
	// The completion is delivered by Bubble Tea's real command loop, not by
	// assuming that moving to another row also moves the cleanup operation.
	result <- actionMsg{err: errors.New("sandbox removal failed"), response: &protocol.Response{Tasks: m.tasks, ResourceRefreshPending: true}}
	waitFor("Cleanup failed")
	tm.Send(runeKey('q'))
	final := tm.FinalModel(t, teatest.WithFinalTimeout(time.Second)).(staticTUIModel).model
	if final.operation.kind != "" || final.message != "" || !final.resourceRefreshPending || len(final.taskFailures) != 1 || final.taskFailures[0].operation.task.ID != 1 {
		t.Fatal("asynchronous completion retained the lock/warning or lost cleanup context")
	}
	updated, cmd := final.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || updated.(model).operation.kind != "session" || updated.(model).operation.task.ID != 2 {
		t.Fatal("switching to the selected unrelated workspace did not recover")
	}
}
