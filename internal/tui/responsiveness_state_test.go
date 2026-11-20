package tui

import (
	"errors"
	"slices"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"radar/internal/protocol"
)

func TestResponsivenessSnapshotBurstPreservesFocusAndFailures(t *testing.T) {
	m := reactivityBaselineModel()
	m.cursor = 150
	failure := errors.New("fixture cleanup failed")
	cleanupID := m.cleanupOperation.id
	m = updateOperation(m, taskActionMsg{action: actionMsg{err: failure}})
	m.startOperation(m.tasks[1], "session", "Starting session…", "Session start failed", nil)
	activeID := m.operation.id
	m.revision = 10
	m.resourceRefreshPending = true
	sources := []protocol.SourceStatus{{Name: "workspace", Status: "partial", Detail: "fixture provider stalled"}}
	operations := []protocol.ManagedOperation{{ID: "fixture:1", Kind: "cleanup", TaskID: 1, Status: "failed"}}
	m.sources = sources
	m.managedOperations = operations
	m.syncTaskScroll()
	scroll := m.scroll
	tasks := slices.Clone(m.tasks)
	selectedID := tasks[150].ID
	assertContext := func() {
		t.Helper()
		if len(m.taskFailures) != 1 || m.taskFailures[0].operation.id != cleanupID || m.taskFailures[0].operation.task.ID != 1 || !errors.Is(m.taskFailures[0].err, failure) {
			t.Fatal("valid revision erased or reassigned the correlated cleanup failure")
		}
		if m.operation.id != activeID || m.operation.task.ID != 2 || m.operation.kind != "session" {
			t.Fatal("revision completed or reassigned an unrelated active operation")
		}
		if !m.resourceRefreshPending || len(m.managedOperations) != 1 || m.managedOperations[0].ID != "fixture:1" || m.managedOperations[0].Status != "failed" {
			t.Fatal("revision lost pending/native operation context")
		}
	}
	for revision := int64(11); revision <= 210; revision++ {
		response := protocol.Response{Revision: revision, Tasks: slices.Clone(tasks), Sources: sources, Operations: operations, ResourceRefreshPending: true}
		updated, _ := m.Update(watchMsg{response: response})
		m = updated.(model)
		selected, ok := m.selectedTask()
		if !ok || selected.ID != selectedID || m.scroll != scroll || m.revision != revision {
			t.Fatalf("burst changed selection/scroll/order at revision %d", revision)
		}
		assertContext()
	}
	// Inspect stays pinned when its selected task disappears; the overview falls
	// back at the vacated position rather than jumping to the first task.
	updated, _ := m.Update(runeKey('i'))
	m = updated.(model)
	if m.mode != "detail" || m.detail.task.ID != selectedID {
		t.Fatal("Inspect did not pin the selected task")
	}
	removed := append(slices.Clone(tasks[:150]), tasks[151:]...)
	updated, _ = m.Update(watchMsg{response: protocol.Response{Revision: 211, Tasks: removed, Sources: sources, Operations: operations, ResourceRefreshPending: true}})
	m = updated.(model)
	if m.tasks[m.cursor].ID != tasks[151].ID || m.detail.available || m.detail.task.ID != selectedID || m.mode != "detail" {
		t.Fatal("selected-item removal lost pinned Inspect or the adjacent overview fallback")
	}
	assertContext()
	// A delayed older reply cannot resurrect the removed item, certify a pending
	// graph as fresh, erase a failure, or advance another operation's generation.
	updated, _ = m.Update(watchMsg{response: protocol.Response{Revision: 210, Tasks: tasks, Sources: []protocol.SourceStatus{{Name: "workspace", Status: "ok"}}, Operations: []protocol.ManagedOperation{{ID: "fixture:1", Kind: "cleanup", TaskID: 1, Status: "running"}}}})
	m = updated.(model)
	if m.revision != 211 || len(m.tasks) != 199 || m.tasks[m.cursor].ID != tasks[151].ID || m.detail.available || m.sources[0].Status != "partial" {
		t.Fatal("stale reply restored removed state or changed focus/source health")
	}
	assertContext()
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	if m.mode != "" || m.tasks[m.cursor].ID != tasks[151].ID {
		t.Fatal("leaving missing-task Inspect lost the fallback selection")
	}
}
