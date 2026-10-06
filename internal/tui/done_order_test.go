package tui

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"radar/internal/protocol"
)

func TestTaskGroupOrderSortsDoneByCompletionInstant(t *testing.T) {
	for _, tt := range []struct {
		name  string
		dates []string
		want  []int
	}{
		{name: "empty"},
		{
			name:  "newest first",
			dates: []string{"2026-01-01T10:00:00Z", "2026-01-03T10:00:00Z", "2026-01-02T10:00:00Z"},
			want:  []int{1, 2, 0},
		},
		{
			name: "fractional seconds and stable equivalent offsets",
			dates: []string{
				"2026-01-01T10:00:00Z", "2026-01-01T12:00:00+02:00",
				"2026-01-01T10:00:00.000Z", "2026-01-01T05:00:00-05:00",
				"2026-01-01T10:00:00.000000001Z", "2026-01-01T09:59:59.999999999Z",
			},
			want: []int{4, 0, 1, 2, 3, 5},
		},
		{
			name: "valid dates including zero time precede stable invalid dates",
			dates: []string{
				"", "invalid", "0001-01-01T00:00:00Z", "2026-02-30T10:00:00Z",
				"2026-01-01", "2026-01-01T10:00:00Z", "",
			},
			want: []int{5, 2, 0, 1, 3, 4, 6},
		},
		{
			name:  "all invalid retain input order",
			dates: []string{"invalid", "", "2026-01-01T10:00:00"},
			want:  []int{0, 1, 2},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := model{}
			for i, date := range tt.dates {
				m.tasks = append(m.tasks, protocol.Task{ID: i + 1, Attention: "done", DoneAt: date})
			}
			before := slices.Clone(m.tasks)
			if got := m.taskGroupOrder("done"); !slices.Equal(got, tt.want) {
				t.Fatalf("taskGroupOrder(done) = %v, want %v", got, tt.want)
			}
			if !reflect.DeepEqual(m.tasks, before) {
				t.Fatal("ordering mutated incoming tasks")
			}
		})
	}
}

func TestTaskGroupOrderPreservesUnfinishedOrder(t *testing.T) {
	for _, key := range []string{"immediate", "attention", "in_progress", "low_priority"} {
		t.Run(key, func(t *testing.T) {
			m := model{tasks: []protocol.Task{
				{ID: 1, Attention: "done", DoneAt: "2026-01-03T10:00:00Z"},
				{ID: 9, Attention: key, DoneAt: "2026-01-01T10:00:00Z"},
				{ID: 2, Attention: "done"},
				{ID: 3, Attention: key, DoneAt: "2026-01-02T10:00:00Z"},
				{ID: 7, Attention: key},
			}}
			if got, want := m.taskGroupOrder(key), []int{1, 3, 4}; !slices.Equal(got, want) {
				t.Fatalf("taskGroupOrder(%s) = %v, want %v", key, got, want)
			}
		})
	}
}

func TestDoneOrderMatchesRenderingAndNavigation(t *testing.T) {
	t.Setenv("TMUX", "")
	m := model{width: 100, height: 20, expandedSections: map[string]bool{"done": true}, tasks: []protocol.Task{
		{Title: "oldest done", Attention: "done", DoneAt: "2026-01-01T10:00:00Z", SourceRefs: []protocol.SourceRef{{ID: "jira:issue:ABC-123"}, {ID: "github:pr:owner/repo:1"}}},
		{Title: "first active", Attention: "attention", SourceRefs: []protocol.SourceRef{{ID: "jira:issue:ABC-456"}}},
		{Title: "middle done", Attention: "done", DoneAt: "2026-01-02T10:00:00Z"},
		{Title: "second active", Attention: "attention"},
		{Title: "newest done", Attention: "done", DoneAt: "2026-01-03T10:00:00Z", SourceRefs: []protocol.SourceRef{{ID: "github:pr:owner/repo:2"}}},
		{Title: "low priority", Attention: "low_priority"},
	}}
	// Keep a six-row viewport regardless of the help/footer height.
	m.height += 6 - m.taskListHeight(m.contentWidth())
	if height := m.taskListHeight(m.contentWidth()); height != 6 {
		t.Fatalf("task list height = %d, want 6", height)
	}
	wantOrder := []visibleEntry{{task: 1}, {task: 3}, {task: 5}, {section: "done"}, {task: 4}, {task: 2}, {task: 0}}
	if got := m.overviewLayout().entries; !slices.Equal(got, wantOrder) {
		t.Fatalf("taskCursorOrder() = %v, want %v", got, wantOrder)
	}
	positions, count := m.taskRowPositions()
	wantPositions := map[visibleEntry]int{{task: 1}: 1, {task: 3}: 4, {task: 5}: 7, {section: "done"}: 9, {task: 4}: 10, {task: 2}: 13, {task: 0}: 15}
	if !reflect.DeepEqual(positions, wantPositions) || count != 18 {
		t.Fatalf("taskRowPositions() = %v, %d; want %v, 18", positions, count, wantPositions)
	}
	starts, ends := []int{0, 4, 6, 9, 10, 13, 15}, []int{2, 4, 7, 9, 11, 13, 17}
	for i, entry := range wantOrder {
		m.selectEntry(entry)
		lines, start, end := m.taskLines(m.contentWidth())
		view := strings.Join(lines, "\n")
		label := "Done (3)"
		if entry.section == "" {
			label = m.tasks[entry.task].Title
		}
		if len(lines) != count || renderedLineIndex(view, label) != positions[entry] {
			t.Fatalf("entry %+v: rendering disagrees with row positions:\n%s", entry, ansi.Strip(view))
		}
		if start != starts[i] || end != ends[i] {
			t.Fatalf("entry %+v: selected block = %d..%d, want %d..%d", entry, start, end, starts[i], ends[i])
		}
	}

	m.selectEntry(wantOrder[0])
	m.syncTaskScroll()
	for _, step := range []struct {
		key   tea.KeyType
		entry visibleEntry
	}{
		{tea.KeyDown, visibleEntry{task: 3}}, {tea.KeyDown, visibleEntry{task: 5}},
		{tea.KeyDown, visibleEntry{section: "done"}}, {tea.KeyDown, visibleEntry{task: 4}},
		{tea.KeyDown, visibleEntry{task: 2}}, {tea.KeyDown, visibleEntry{task: 0}}, {tea.KeyDown, visibleEntry{task: 0}},
		{tea.KeyUp, visibleEntry{task: 2}}, {tea.KeyUp, visibleEntry{task: 4}},
		{tea.KeyUp, visibleEntry{section: "done"}}, {tea.KeyUp, visibleEntry{task: 5}},
		{tea.KeyUp, visibleEntry{task: 3}}, {tea.KeyUp, visibleEntry{task: 1}}, {tea.KeyUp, visibleEntry{task: 1}},
		{tea.KeyEnd, visibleEntry{task: 0}}, {tea.KeyHome, visibleEntry{task: 1}},
		{tea.KeyCtrlD, visibleEntry{task: 5}}, {tea.KeyCtrlD, visibleEntry{task: 2}}, {tea.KeyCtrlD, visibleEntry{task: 0}},
		{tea.KeyCtrlU, visibleEntry{section: "done"}}, {tea.KeyCtrlU, visibleEntry{task: 3}}, {tea.KeyCtrlU, visibleEntry{task: 1}},
	} {
		key := tea.KeyMsg{Type: step.key}
		updated, _ := m.Update(key)
		m = updated.(model)
		if m.selectedEntry() != step.entry {
			t.Fatalf("after %s entry = %+v, want %+v", key.String(), m.selectedEntry(), step.entry)
		}
		_, start, end := m.taskLines(m.contentWidth())
		if start < m.scroll || end >= m.scroll+6 {
			t.Fatalf("after %s selected block %d..%d outside viewport %d..%d", key.String(), start, end, m.scroll, m.scroll+5)
		}
		selectedLabel := "› ▾ Done"
		if step.entry.section == "" {
			selectedLabel = "›   " + m.tasks[m.cursor].Title
		}
		if view := ansi.Strip(m.taskList(m.contentWidth(), 6)); !strings.Contains(view, selectedLabel) {
			t.Fatalf("after %s selected task is not visible:\n%s", key.String(), view)
		}
	}
}

func TestWatchKeepsAlreadyDoneSelectionWhenNewerDoneTaskArrives(t *testing.T) {
	m := model{cursor: 0, expandedSections: map[string]bool{"done": true}, selectedCurrentTask: true, revision: 1, tasks: []protocol.Task{
		{ID: 1, Title: "selected done", Attention: "done", DoneAt: "2026-01-01T10:00:00Z"},
		{ID: 2, Title: "active", Attention: "attention"},
		{ID: 3, Title: "recent done", Attention: "done", DoneAt: "2026-01-02T10:00:00Z"},
	}}
	updated, cmd := m.Update(watchMsg{response: protocol.Response{OK: true, Revision: 2, Tasks: []protocol.Task{
		{ID: 4, Title: "newest done", Attention: "done", DoneAt: "2026-01-03T10:00:00Z"},
		m.tasks[0], m.tasks[1], m.tasks[2],
	}}})
	got := updated.(model)
	if cmd == nil {
		t.Fatal("watch response should start next watch")
	}
	if got.cursor != 1 || got.tasks[got.cursor].ID != 1 || got.cursorPosition() != 4 {
		t.Fatalf("selected cursor = %d, rendered position = %d; want task 1 at cursor 1, rendered position 4", got.cursor, got.cursorPosition())
	}
}
