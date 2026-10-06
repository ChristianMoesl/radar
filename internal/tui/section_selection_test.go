package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"radar/internal/protocol"
)

func TestInspectMutationKeepsOverviewAmongActiveEvenWithHistoryExpanded(t *testing.T) {
	for _, destination := range []string{"ignored", "done"} {
		m := sectionFixture()
		m.tasks = append(m.tasks, protocol.Task{ID: 5, Title: "Next active work", Attention: "low_priority"})
		m.expandedSections = map[string]bool{"ignored": true, "done": true}
		m = pressSectionKey(t, m, "i")
		tasks := slices.Clone(m.tasks)
		if destination == "ignored" {
			tasks[0].Ignored = true
		} else {
			tasks[0].Attention = "done"
		}
		for range 3 {
			m.applyResponse(protocol.Response{Tasks: tasks}, false)
			if !m.detail.available || m.detail.task.ID != 1 || m.detail.task.DisplayGroup() != destination {
				t.Fatal("Inspect no longer follows its task state")
			}
			if selected, ok := m.selectedTask(); !ok || selected.ID != 5 {
				t.Fatal("pinned details stole the overview fallback back into expanded history")
			}
		}
		m = pressSectionKey(t, m, "esc")
		if selected, ok := m.selectedTask(); !ok || selected.ID != 5 || !m.sectionExpanded(destination) {
			t.Fatal("returning from Inspect lost active selection/session expansion")
		}
	}
}

func TestSelectedHeaderDisappearanceRestoresValidEntryWithoutAutoexpanding(t *testing.T) {
	for _, selectedGroup := range []string{"ignored", "done"} {
		for _, expanded := range []bool{false, true} {
			m := sectionFixture()
			m.expandedSections = map[string]bool{selectedGroup: expanded}
			m.selectEntry(visibleEntry{section: selectedGroup})
			var tasks []protocol.Task
			for _, task := range m.tasks {
				if task.DisplayGroup() != selectedGroup {
					tasks = append(tasks, task)
				}
			}
			m.applyResponse(protocol.Response{Tasks: tasks}, false)
			assertVisibleSelection(t, m)
			if m.selectedSection == selectedGroup || m.sectionExpanded(selectedGroup) != expanded {
				t.Fatal("missing header retained an invisible cursor or reset expansion")
			}
			otherGroup := "done"
			if selectedGroup == "done" {
				otherGroup = "ignored"
			}
			if m.sectionExpanded(otherGroup) {
				t.Fatal("fallback autoexpanded a remaining section")
			}
		}
	}
}

func TestBackgroundUpdateMovesChildToCollapsedHeader(t *testing.T) {
	for _, sourceGroup := range []string{"ignored", "done"} {
		m := sectionFixture()
		m.tasks = m.tasks[1:] // No active work; the selected child must use a header.
		m.expandedSections = map[string]bool{sourceGroup: true}
		cursor := 0
		if sourceGroup == "done" {
			cursor = 1
		}
		m.selectEntry(visibleEntry{task: cursor})
		tasks := slices.Clone(m.tasks)
		if sourceGroup == "ignored" {
			tasks[cursor].Attention = "done"
		} else {
			tasks[cursor].Attention = "attention" // Reopen preserves ignored=true.
		}
		m.applyResponse(protocol.Response{Tasks: tasks}, false)
		assertVisibleSelection(t, m)
		if m.selectedSection == "" || m.sectionExpanded(tasks[cursor].DisplayGroup()) {
			t.Fatal("regrouping kept an invisible child or expanded its destination")
		}
	}
}

func TestStaleSnapshotCannotUndoIgnoreSelectionOrSessionExpansion(t *testing.T) {
	m := sectionFixture()
	m.expandedSections = map[string]bool{"ignored": true}
	old := protocol.Response{Revision: 1, Tasks: slices.Clone(m.tasks)}
	new := protocol.Response{Revision: 2, Tasks: slices.Clone(m.tasks)}
	new.Tasks[0].Ignored = true
	m.applyResponse(new, false)
	selection, scroll := m.selectedEntry(), m.scroll
	m.applyResponse(old, false)
	if m.revision != 2 || !m.tasks[0].Ignored || m.selectedEntry() != selection || m.scroll != scroll || !m.sectionExpanded("ignored") {
		t.Fatal("stale refresh reverted preference, focus or expansion")
	}
}

func TestHiddenResourceOperationStillAppearsOutsideCollapsedList(t *testing.T) {
	m := sectionFixture()
	m.operation = taskOperation{task: m.tasks[1], kind: "cleanup", label: "Cleaning up…"}
	if m.operationOnRow() {
		t.Fatal("collapsed child is still treated as an operation row")
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Cleaning up…") || !strings.Contains(view, "Ignored work") {
		t.Fatal("collapsing history hid an in-flight operation's status")
	}
	m.expandedSections = map[string]bool{"ignored": true}
	if !m.operationOnRow() {
		t.Fatal("expanding history failed to restore its task operation row")
	}
}

func TestOverviewSummaryShowsSeparateIgnoredCount(t *testing.T) {
	m := sectionFixture()
	m.summary = protocol.Summary{Immediate: 1, Attention: 2, InProgress: 3, LowPriority: 4, Ignored: 5, Done: 6}
	view := ansi.Strip(m.header(140))
	for _, label := range []string{"1 urgent", "2 attention", "3 progress", "4 low", "5 ignored", "6 done"} {
		if !strings.Contains(view, label) {
			t.Fatalf("summary lost %q: %s", label, view)
		}
	}
}
