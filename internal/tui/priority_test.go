package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"radar/internal/protocol"
)

func priorityFixture() model {
	m := operationFixture()
	m.tasks[0].SourceRefs[0].Role = protocol.SourceRefRoleAuthoritative
	return m
}

func TestTaskPriorityPickerSetsAndRedefersWithoutReplacingUrgentShortcut(t *testing.T) {
	for choice, priority := range priorityChoices {
		t.Run(priority, func(t *testing.T) {
			m := priorityFixture()
			m.tasks[0].Priority = "low"
			path, requests := deletionSocket(t, protocol.Response{OK: true})
			m.socketPath = path
			updated, cmd := m.Update(runeKey('P'))
			m = updated.(model)
			if cmd != nil || m.mode != "task_priority" || m.priorityChoice != 0 {
				t.Fatal("opening priority picker mutated task or failed to offer Low re-deferral")
			}
			m = updateOperation(m, tea.KeyMsg{Type: tea.KeyTab})
			if m.mode != "task_priority" || m.activeView != "" {
				t.Fatal("Tab escaped the priority modal")
			}
			for range choice {
				m = updateOperation(m, tea.KeyMsg{Type: tea.KeyDown})
			}
			updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = updated.(model)
			if cmd == nil || m.mode != "" || !m.loading {
				t.Fatal("confirmed choice did not submit the existing task mutation")
			}
			if action := cmd().(actionMsg); action.err != nil {
				t.Fatal(action.err)
			}
			request := <-requests
			if request.Method != "task-priority" || request.TaskMutation == nil || request.TaskMutation.TaskID != 1 || request.TaskMutation.Priority != priority {
				t.Fatalf("wrong priority mutation: %+v", request)
			}
		})
	}
	for _, current := range []string{"low", "normal", "urgent"} {
		m := priorityFixture()
		m.tasks[0].Priority = current
		path, requests := deletionSocket(t, protocol.Response{OK: true})
		m.socketPath = path
		_, cmd := m.Update(runeKey('p'))
		if cmd == nil {
			t.Fatal("existing urgent shortcut was removed")
		}
		if action := cmd().(actionMsg); action.err != nil {
			t.Fatal(action.err)
		}
		want := "urgent"
		if current == "urgent" {
			want = "normal"
		}
		if request := <-requests; request.TaskMutation.Priority != want {
			t.Fatal("urgent shortcut was reinterpreted as a Low toggle")
		}
	}
}

func TestPriorityPickerCancelAndTaskDisappearanceCannotMutate(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		m := updateOperation(priorityFixture(), runeKey('P'))
		if unavailable {
			m.tasks = nil
		}
		key := tea.KeyMsg{Type: tea.KeyEsc}
		if unavailable {
			key.Type = tea.KeyEnter
		}
		updated, cmd := m.Update(key)
		if cmd != nil || updated.(model).mode != "" || updated.(model).loading {
			t.Fatal("cancel or disappeared task submitted a mutation")
		}
	}
	for _, width := range []int{40, 80, 120} {
		m := priorityFixture()
		m.width = width
		m = updateOperation(m, runeKey('P'))
		view := ansi.Strip(m.View())
		if !strings.Contains(view, "Low") || !strings.Contains(view, "Normal") || !strings.Contains(view, "Urgent") {
			t.Fatal("priority picker hid an action")
		}
		assertNoWideLines(t, m.View(), width)
	}
}

func TestInspectShowsRetainedLowDuringBreakthrough(t *testing.T) {
	view := ansi.Strip(taskDetailView(protocol.Task{Title: "Review", Priority: "low", Attention: "attention"}, 80))
	if !strings.Contains(view, "Priority") || !strings.Contains(view, "low") || !strings.Contains(view, "attention") {
		t.Fatal("Inspect did not distinguish retained preference from effective category")
	}
}

func TestPriorityPickerFitsShortTerminalsAndKeepsSelectionVisible(t *testing.T) {
	for _, width := range []int{40, 80} {
		for _, height := range []int{12, 16, 24} {
			m := priorityFixture()
			m.width, m.height = width, height
			m = updateOperation(m, runeKey('P'))
			for index, choice := range []string{"Low", "Normal", "Urgent"} {
				m.priorityChoice = index
				view := m.View()
				if !strings.Contains(ansi.Strip(view), "› "+choice) || lipgloss.Height(view) > height {
					t.Fatalf("choice %s not visible within %dx%d (height %d):\n%s", choice, width, height, lipgloss.Height(view), view)
				}
				assertNoWideLines(t, view, width)
			}
		}
	}
}
