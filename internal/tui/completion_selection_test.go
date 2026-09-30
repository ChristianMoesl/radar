package tui

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"radar/internal/protocol"
)

func TestCompletionKeepsCursorAmongUnfinishedTasks(t *testing.T) {
	// Input order intentionally differs from rendered group order: 2, 3, 1, 4.
	tasks := []protocol.Task{
		{ID: 1, Attention: "low_priority"},
		{ID: 2, Attention: "attention"},
		{ID: 3, Attention: "in_progress"},
		{ID: 4, Attention: "done", DoneAt: "2026-08-01T10:00:00Z"},
	}
	for _, tt := range []struct {
		name   string
		cursor int
		wantID int
	}{
		{name: "first", cursor: 1, wantID: 3},
		{name: "middle", cursor: 2, wantID: 1},
		{name: "last", cursor: 0, wantID: 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := model{tasks: tasks, cursor: tt.cursor, selectedCurrentTask: true}
			updated := slices.Clone(tasks)
			updated[tt.cursor].Attention = "done"
			updated[tt.cursor].DoneAt = "2026-08-02T10:00:00Z"
			m.applyResponse(protocol.Response{Tasks: updated}, false)
			if got := m.tasks[m.cursor].ID; got != tt.wantID {
				t.Fatalf("selected ID = %d, want %d", got, tt.wantID)
			}
		})
	}
}

func TestCompletionSelectionBySourceRef(t *testing.T) {
	selected := protocol.Task{Attention: "attention", SourceRefs: []protocol.SourceRef{{ID: "obsidian:task:selected"}}}
	m := model{tasks: []protocol.Task{selected, {ID: 2, Attention: "low_priority"}}}
	selected.Attention = "done"
	m.applyResponse(protocol.Response{Tasks: []protocol.Task{selected, {ID: 2, Attention: "low_priority"}}}, false)
	if m.tasks[m.cursor].ID != 2 {
		t.Fatalf("selected cursor = %d, want remaining unfinished task", m.cursor)
	}
}

func TestCompletionWithNoUnfinishedTasks(t *testing.T) {
	for _, tt := range []struct {
		name  string
		tasks []protocol.Task
		want  int
	}{
		{name: "sole task", tasks: []protocol.Task{{ID: 1, Attention: "done"}}, want: 1},
		{name: "only done remain", tasks: []protocol.Task{
			{ID: 1, Attention: "done", DoneAt: "2026-08-01T10:00:00Z"},
			{ID: 2, Attention: "done", DoneAt: "2026-08-02T10:00:00Z"},
		}, want: 2},
		{name: "empty", tasks: []protocol.Task{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := model{tasks: []protocol.Task{{ID: 1, Attention: "attention"}}}
			m.applyResponse(protocol.Response{Tasks: tt.tasks}, false)
			if len(m.tasks) == 0 {
				if m.cursor != 0 || m.scroll != 0 {
					t.Fatalf("empty list cursor=%d scroll=%d, want zero", m.cursor, m.scroll)
				}
			} else if m.tasks[m.cursor].ID != tt.want {
				t.Fatalf("selected ID = %d, want %d", m.tasks[m.cursor].ID, tt.want)
			}
		})
	}
}

func TestCompletionResponsesDoNotFollowTaskBackToDone(t *testing.T) {
	response := protocol.Response{OK: true, Revision: 2, Tasks: []protocol.Task{
		{ID: 1, Attention: "done"}, {ID: 2, Attention: "low_priority"},
	}}
	for _, tt := range []struct {
		name     string
		messages []tea.Msg
	}{
		{name: "action then watch", messages: []tea.Msg{actionMsg{response: &response}, watchMsg{response: response}}},
		{name: "watch then action", messages: []tea.Msg{watchMsg{response: response}, actionMsg{response: &response}}},
		{name: "refresh", messages: []tea.Msg{fetchMsg{response: response}, fetchMsg{response: response}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := model{selectedCurrentTask: true, revision: 1, tasks: []protocol.Task{
				{ID: 1, Attention: "attention"}, {ID: 2, Attention: "low_priority"},
			}}
			for _, msg := range tt.messages {
				updated, _ := m.Update(msg)
				m = updated.(model)
				if m.tasks[m.cursor].ID != 2 {
					t.Fatalf("after %T selected ID=%d, want 2", msg, m.tasks[m.cursor].ID)
				}
			}
		})
	}
}

func TestCompletionWaitsForResponseAndRespectsNavigation(t *testing.T) {
	for _, navigate := range []bool{false, true} {
		t.Run(fmt.Sprintf("navigate=%v", navigate), func(t *testing.T) {
			task := authoredTaskForTUITest("open", "normal", "attention")
			m := model{tasks: []protocol.Task{task, {ID: 8, Attention: "in_progress"}, {ID: 9, Attention: "low_priority"}}}
			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
			m = updated.(model)
			if cmd == nil || m.cursor != 0 {
				t.Fatalf("d changed cursor before confirmation: cursor=%d command=%v", m.cursor, cmd)
			}
			if !navigate {
				updated, _ = m.Update(actionMsg{err: errors.New("completion failed")})
				m = updated.(model)
				if m.cursor != 0 || m.tasks[m.cursor].Attention == "done" || m.err == nil {
					t.Fatalf("failed action changed selection or state: %+v", m)
				}
				return
			}
			for range 2 {
				updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
				m = updated.(model)
			}
			responseTasks := slices.Clone(m.tasks)
			responseTasks[0].Attention = "done"
			updated, _ = m.Update(actionMsg{response: &protocol.Response{Tasks: responseTasks}})
			m = updated.(model)
			if m.tasks[m.cursor].ID != 9 {
				t.Fatalf("completion stole selection from task 9: cursor=%d", m.cursor)
			}
		})
	}
}

func TestReopeningStillFollowsSelectedTask(t *testing.T) {
	m := model{cursor: 1, tasks: []protocol.Task{{ID: 1, Attention: "low_priority"}, {ID: 2, Attention: "done"}}}
	m.applyResponse(protocol.Response{Tasks: []protocol.Task{{ID: 2, Attention: "immediate"}, {ID: 1, Attention: "low_priority"}}}, false)
	if m.tasks[m.cursor].ID != 2 || m.cursorPosition() != 0 {
		t.Fatalf("reopening did not follow selected task: cursor=%d", m.cursor)
	}
}

func TestCompletionKeepsInspectPinnedToTask(t *testing.T) {
	selected := protocol.Task{ID: 1, Attention: "attention"}
	m := model{mode: "detail", tasks: []protocol.Task{selected, {ID: 2, Attention: "low_priority"}}, detail: detailState{task: selected, available: true}}
	selected.Attention = "done"
	m.applyResponse(protocol.Response{Tasks: []protocol.Task{selected, {ID: 2, Attention: "low_priority"}}}, false)
	if !m.detail.available || m.detail.task.ID != 1 || m.detail.task.Attention != "done" || m.tasks[m.cursor].ID != 1 {
		t.Fatalf("Inspect lost completed task: detail=%+v cursor=%d", m.detail, m.cursor)
	}
}

func TestCompletionPreservesOverviewViewport(t *testing.T) {
	for _, cursor := range []int{10, 29} {
		t.Run(fmt.Sprintf("cursor=%d", cursor), func(t *testing.T) {
			tasks := make([]protocol.Task, 40)
			for i := range tasks {
				attention := "attention"
				if i >= 30 {
					attention = "done"
				}
				tasks[i] = protocol.Task{ID: i + 1, Title: fmt.Sprintf("task %d", i+1), Attention: attention}
			}
			m := model{width: 100, height: 20, tasks: tasks, cursor: cursor, scroll: 2*cursor - 3}
			m.syncTaskScroll()
			previousScroll := m.scroll
			updated := slices.Clone(tasks)
			updated[cursor].Attention = "done"
			updated[cursor].DoneAt = "2026-08-02T10:00:00Z"
			m.applyResponse(protocol.Response{Tasks: updated}, false)
			if m.scroll != previousScroll {
				t.Fatalf("scroll = %d, want unchanged %d", m.scroll, previousScroll)
			}
			_, start, end := m.taskLines(m.contentWidth())
			if start < m.scroll || end >= m.scroll+m.taskListHeight(m.contentWidth()) {
				t.Fatalf("selection %d..%d outside viewport at %d", start, end, m.scroll)
			}
		})
	}
}
