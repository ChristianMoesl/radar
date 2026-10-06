package tui

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest"

	"radar/internal/protocol"
)

func sectionFixture() model {
	return model{selectedCurrentTask: true, tasks: []protocol.Task{
		{ID: 1, Title: "Active work", Attention: "attention", SourceRefs: []protocol.SourceRef{{ID: "jira:issue:ABC-123"}}},
		{ID: 2, Title: "Ignored work", Attention: "immediate", Ignored: true, SourceRefs: []protocol.SourceRef{{ID: "github:pr:acme/app:7"}}},
		{ID: 3, Title: "Completed work", Attention: "done", Ignored: true, DoneAt: "2026-01-02T10:00:00Z", SourceRefs: []protocol.SourceRef{{ID: "jira:issue:ABC-456"}}},
		{ID: 4, Title: "Older completed work", Attention: "done", DoneAt: "2026-01-01T10:00:00Z"},
	}}
}

func pressSectionKey(t *testing.T, m model, key string) model {
	t.Helper()
	updated, cmd := m.Update(inspectKey(key))
	if cmd != nil {
		t.Fatalf("%s dispatched a command while browsing sections", key)
	}
	return updated.(model)
}

func assertVisibleSelection(t *testing.T, m model) {
	t.Helper()
	layout := m.overviewLayout()
	if len(layout.entries) == 0 {
		if m.cursor != 0 || m.selectedSection != "" || m.scroll != 0 {
			t.Fatalf("empty view has invalid selection: %+v, scroll=%d", m.selectedEntry(), m.scroll)
		}
		return
	}
	bounds, ok := layout.bounds[m.selectedEntry()]
	if !ok {
		t.Fatalf("invisible selection %+v, entries=%+v", m.selectedEntry(), layout.entries)
	}
	lines, start, end := m.taskLines(m.contentWidth())
	if len(lines) != len(layout.rows) || start != bounds.start || end != bounds.end {
		t.Fatal("rendering diverged from the shared visible layout")
	}
	height := m.taskListHeight(m.contentWidth())
	if start < m.scroll || (end-start+1 <= height && end >= m.scroll+height) {
		t.Fatalf("selection %d..%d outside viewport %d..%d", start, end, m.scroll, m.scroll+height-1)
	}
}

func TestHistorySectionsStartCollapsedAndUseEffectiveGroups(t *testing.T) {
	m := sectionFixture()
	want := []visibleEntry{{task: 0}, {section: "ignored"}, {section: "done"}}
	if got := m.overviewLayout().entries; !slices.Equal(got, want) {
		t.Fatalf("entries = %+v, want %+v", got, want)
	}
	lines, _, _ := m.taskLines(100)
	view := ansi.Strip(strings.Join(lines, "\n"))
	for _, label := range []string{"Active work", "▸ Ignored (1)", "▸ Done (2) · last 3 days"} {
		if !strings.Contains(view, label) {
			t.Fatalf("missing %q:\n%s", label, view)
		}
	}
	for _, hidden := range []string{"Ignored work", "Completed work", "Older completed work", "Need immediate attention", "acme/app:7", "ABC-456"} {
		if strings.Contains(view, hidden) {
			t.Fatalf("collapsed/ignored content %q leaked into overview:\n%s", hidden, view)
		}
	}
	positions, count := m.taskRowPositions()
	if len(lines) != 7 || count != len(lines) || len(positions) != 3 || positions[visibleEntry{section: "ignored"}] != 4 || positions[visibleEntry{section: "done"}] != 6 {
		t.Fatalf("collapsed layout: rows=%d, count=%d, positions=%v", len(lines), count, positions)
	}
	if m.tasks[1].Attention != "immediate" || m.tasks[2].Attention != "done" {
		t.Fatal("TUI grouping changed underlying attention/lifecycle")
	}
}

func TestHeadersNavigateAndToggleIndependently(t *testing.T) {
	for _, down := range []string{"j", "down", "ctrl+n"} {
		for _, up := range []string{"k", "up", "ctrl+p"} {
			t.Run(down+"/"+up, func(t *testing.T) {
				m := sectionFixture()
				m = pressSectionKey(t, m, down)
				if m.selectedSection != "ignored" {
					t.Fatal("Ignored header was not selectable")
				}
				m = pressSectionKey(t, m, "enter")
				if m.selectedSection != "ignored" || !m.sectionExpanded("ignored") || m.sectionExpanded("done") {
					t.Fatal("Enter did not expand only Ignored while retaining its selection")
				}
				m = pressSectionKey(t, m, down)
				if task, ok := m.selectedTask(); !ok || task.ID != 2 {
					t.Fatal("expanded child was not selectable")
				}
				m = pressSectionKey(t, m, up)
				m = pressSectionKey(t, m, "enter")
				m = pressSectionKey(t, m, down)
				if m.selectedSection != "done" || m.sectionExpanded("ignored") {
					t.Fatal("collapse left a child in the selectable order")
				}
				m = pressSectionKey(t, m, "enter")
				if m.selectedSection != "done" || !m.sectionExpanded("done") || m.sectionExpanded("ignored") {
					t.Fatal("Done did not expand independently")
				}
				m = pressSectionKey(t, m, down)
				if task, ok := m.selectedTask(); !ok || task.ID != 3 {
					t.Fatal("Done no longer uses newest-completion-first ordering")
				}
				m = pressSectionKey(t, m, "G")
				if task, ok := m.selectedTask(); !ok || task.ID != 4 {
					t.Fatal("end did not select the oldest expanded Done child")
				}
				m = pressSectionKey(t, m, "g")
				if task, ok := m.selectedTask(); !ok || task.ID != 1 {
					t.Fatal("home did not select the first active task")
				}
			})
		}
	}
}

func TestEmptySectionsHideButRetainSessionExpansion(t *testing.T) {
	m := sectionFixture()
	m.selectEntry(visibleEntry{section: "ignored"})
	m = pressSectionKey(t, m, "enter")
	m.selectEntry(visibleEntry{section: "done"})
	m = pressSectionKey(t, m, "enter")
	original := slices.Clone(m.tasks)
	for _, tasks := range [][]protocol.Task{original[:1], {}} {
		m.applyResponse(protocol.Response{Tasks: tasks}, false)
		if strings.Contains(ansi.Strip(m.taskList(100, 100)), "Ignored") || strings.Contains(ansi.Strip(m.taskList(100, 100)), "Done") {
			t.Fatal("empty history sections are visible")
		}
		if !m.sectionExpanded("ignored") || !m.sectionExpanded("done") {
			t.Fatal("temporary emptiness reset session expansion")
		}
		assertVisibleSelection(t, m)
	}
	m.applyResponse(protocol.Response{Tasks: original}, false)
	if !m.sectionExpanded("ignored") || !m.sectionExpanded("done") || len(m.overviewLayout().entries) != 6 {
		t.Fatal("reappearing sections did not retain expansion")
	}
	fresh := newModel("")
	fresh.applyResponse(protocol.Response{Tasks: original}, false)
	if fresh.sectionExpanded("ignored") || fresh.sectionExpanded("done") {
		t.Fatal("expansion escaped the lifetime of this TUI session")
	}
}

func TestSectionHeadersNeverRunTaskActions(t *testing.T) {
	for _, section := range []string{"ignored", "done"} {
		for _, expanded := range []bool{false, true} {
			for _, key := range []string{"m", "d", "D", "p", "o", "i", "right", "w", "x"} {
				t.Run(fmt.Sprintf("%s/expanded=%v/%s", section, expanded, key), func(t *testing.T) {
					m := sectionFixture()
					m.expandedSections = map[string]bool{section: expanded}
					m.selectEntry(visibleEntry{section: section})
					updated, cmd := m.Update(inspectKey(key))
					if cmd != nil || !reflect.DeepEqual(updated.(model), m) {
						t.Fatalf("header key %s changed state or dispatched a task action", key)
					}
					if _, ok := m.selectedTask(); ok {
						t.Fatal("header lookup returned the neighboring task")
					}
				})
			}
		}
	}
}

func TestGlobalActionsRemainAvailableOnHeaders(t *testing.T) {
	for _, key := range []string{"n", "c", "s", "r", "X", "q", "ctrl+c"} {
		t.Run(key, func(t *testing.T) {
			m := sectionFixture()
			m.selectEntry(visibleEntry{section: "ignored"})
			updated, cmd := m.Update(inspectKey(key))
			got := updated.(model)
			switch key {
			case "n":
				if cmd != nil || got.mode != "task_authoring" {
					t.Fatal("new task is not global")
				}
			case "c":
				if cmd != nil || got.mode != "workspace_name" {
					t.Fatal("new workspace is not global")
				}
			case "s":
				if cmd != nil || !got.sourcesExpanded {
					t.Fatal("source expansion is not global")
				}
			default:
				if cmd == nil {
					t.Fatalf("global %s action did not dispatch", key)
				}
			}
		})
	}
}

func TestHeaderExpansionDuringResourceOperationIsClientOnly(t *testing.T) {
	m := sectionFixture()
	m.operation = taskOperation{task: m.tasks[0], kind: "cleanup", label: "Cleaning up…"}
	m.selectEntry(visibleEntry{section: "done"})
	m = pressSectionKey(t, m, "enter")
	if !m.sectionExpanded("done") || m.operation.kind != "cleanup" {
		t.Fatal("header expansion interfered with a running task operation")
	}
	m.selectEntry(visibleEntry{task: 0})
	m = pressSectionKey(t, m, "enter")
	if m.operation.kind != "cleanup" {
		t.Fatal("task Enter replaced an in-flight operation")
	}
}

func TestHeaderSelectionSurvivesRefreshAndMutationSnapshots(t *testing.T) {
	m := sectionFixture()
	m.selectEntry(visibleEntry{section: "done"})
	m = pressSectionKey(t, m, "enter")
	tasks := slices.Clone(m.tasks)
	slices.Reverse(tasks)
	for _, mode := range []string{"", "workspace_edit"} {
		m.mode = mode
		for _, msg := range []tea.Msg{
			watchMsg{response: protocol.Response{Tasks: tasks}},
			fetchMsg{response: protocol.Response{Tasks: tasks}},
			actionMsg{response: &protocol.Response{Tasks: tasks}},
			taskActionMsg{action: actionMsg{response: &protocol.Response{Tasks: tasks}}},
		} {
			updated, _ := m.Update(msg)
			m = updated.(model)
			if m.selectedSection != "done" || !m.sectionExpanded("done") || m.sectionExpanded("ignored") {
				t.Fatalf("%T reset selected header or expansion in mode %q", msg, mode)
			}
		}
	}
}

func TestIgnoreKeepsSelectionAmongActiveTasks(t *testing.T) {
	// Incoming order intentionally differs from the rendered active order.
	original := []protocol.Task{
		{ID: 1, Attention: "low_priority"}, {ID: 2, Attention: "attention"},
		{ID: 3, Attention: "in_progress"}, {ID: 4, Attention: "attention", Ignored: true},
		{ID: 5, Attention: "done"},
	}
	for _, expanded := range []bool{false, true} {
		for _, tt := range []struct{ cursor, wantID int }{{1, 3}, {2, 1}, {0, 3}} {
			t.Run(fmt.Sprintf("cursor=%d/expanded=%v", tt.cursor, expanded), func(t *testing.T) {
				m := model{tasks: original, cursor: tt.cursor, expandedSections: map[string]bool{"ignored": expanded, "done": expanded}}
				tasks := slices.Clone(original)
				tasks[tt.cursor].Ignored = true
				m.applyResponse(protocol.Response{Tasks: tasks}, false)
				selected, ok := m.selectedTask()
				if !ok || selected.ID != tt.wantID || m.sectionExpanded("ignored") != expanded || m.sectionExpanded("done") != expanded {
					t.Fatalf("ignore selected %+v, want active task %d", m.selectedEntry(), tt.wantID)
				}
				assertVisibleSelection(t, m)
			})
		}
	}
}

func TestMovingLastTaskToHistorySelectsHeaderEvenWhenExpanded(t *testing.T) {
	for _, destination := range []string{"ignored", "done"} {
		for _, expanded := range []bool{false, true} {
			for _, originIgnored := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/expanded=%v/originIgnored=%v", destination, expanded, originIgnored), func(t *testing.T) {
					selected := protocol.Task{ID: 1, Attention: "attention", Ignored: originIgnored}
					m := model{tasks: []protocol.Task{selected}, expandedSections: map[string]bool{"ignored": originIgnored, "done": expanded}}
					if destination == "ignored" {
						m.expandedSections["ignored"] = expanded
						selected.Ignored = true
					} else {
						selected.Attention = "done"
					}
					// Already ignored is not a group transition; test Done for that case.
					if originIgnored && destination == "ignored" {
						return
					}
					m.applyResponse(protocol.Response{Tasks: []protocol.Task{selected}}, false)
					if m.selectedSection != destination || m.sectionExpanded(destination) != expanded {
						t.Fatalf("selection %+v, want destination header without changing expansion", m.selectedEntry())
					}
					assertVisibleSelection(t, m)
				})
			}
		}
	}
}

func TestUnignoreFollowsActiveTaskAndPreservesExpansion(t *testing.T) {
	for _, attention := range []string{"immediate", "attention", "in_progress", "low_priority", "done"} {
		t.Run(attention, func(t *testing.T) {
			m := sectionFixture()
			m.expandedSections = map[string]bool{"ignored": true, "done": true}
			m.tasks[1].Attention = attention
			m.selectEntry(visibleEntry{task: 1})
			tasks := slices.Clone(m.tasks)
			tasks[1].Ignored = false
			m.applyResponse(protocol.Response{Tasks: tasks}, false)
			selected, ok := m.selectedTask()
			if !ok || selected.ID != 2 || selected.Attention != attention || selected.Ignored || !m.sectionExpanded("ignored") || !m.sectionExpanded("done") {
				t.Fatal("unignore failed to follow task without changing lifecycle or expansion")
			}
		})
	}
}

func TestIgnoreSelectionSurvivesRegroupedTaskIDs(t *testing.T) {
	selected := protocol.Task{ID: 1, Attention: "attention", SourceRefs: []protocol.SourceRef{{ID: "github:pr:acme/app:7"}}}
	m := model{tasks: []protocol.Task{selected, {ID: 2, Attention: "low_priority"}}}
	selected.ID, selected.Ignored = 3, true
	m.applyResponse(protocol.Response{Tasks: []protocol.Task{{ID: 2, Attention: "low_priority"}, selected}}, false)
	if task, ok := m.selectedTask(); !ok || task.ID != 2 {
		t.Fatal("regrouping followed an ignored task into history")
	}
	m.expandedSections = map[string]bool{"ignored": true}
	m.selectEntry(visibleEntry{task: 1})
	selected.ID, selected.Ignored = 4, false
	m.applyResponse(protocol.Response{Tasks: []protocol.Task{selected, {ID: 2, Attention: "low_priority"}}}, false)
	if task, ok := m.selectedTask(); !ok || task.ID != 4 {
		t.Fatal("unignore lost stable source-ref identity when task ID changed")
	}
}

func TestIgnoreWaitsForSuccessfulPublicationAndDoesNotStealFocus(t *testing.T) {
	for _, navigate := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("navigate=%v/fail=%v", navigate, fail), func(t *testing.T) {
				m := sectionFixture()
				updated, cmd := m.Update(inspectKey("m"))
				m = updated.(model)
				if cmd == nil || m.cursor != 0 || m.selectedSection != "" || m.tasks[0].Ignored {
					t.Fatal("m changed preference or selection before publication")
				}
				if navigate {
					m = pressSectionKey(t, m, "end")
				}
				response := protocol.Response{Revision: 2, Tasks: slices.Clone(m.tasks)}
				response.Tasks[0].Ignored = true
				msg := actionMsg{response: &response}
				if fail {
					msg = actionMsg{err: errors.New("ignore failed")}
				}
				updated, _ = m.Update(msg)
				m = updated.(model)
				if fail {
					if m.tasks[0].Ignored || m.err == nil || (!navigate && m.selectedSection != "") {
						t.Fatal("failed ignore changed state/selection")
					}
				} else if !navigate && m.selectedSection != "ignored" {
					t.Fatal("last active task did not select Ignored header")
				}
				if navigate && m.selectedSection != "done" {
					t.Fatal("ignore response stole focus from user navigation")
				}
				if !fail {
					// Watch/action duplicates cannot jump back to the moved child.
					updated, _ = m.Update(watchMsg{response: response})
					m = updated.(model)
					want := "ignored"
					if navigate {
						want = "done"
					}
					if m.selectedSection != want || m.sectionExpanded(want) {
						t.Fatal("duplicate snapshot changed selection or autoexpanded history")
					}
				}
			})
		}
	}
}

func TestIgnoredInspectPreservesRawFactsAndPinnedIdentity(t *testing.T) {
	for _, group := range []string{"ignored", "done"} {
		m := sectionFixture()
		m = pressSectionKey(t, m, "i")
		selected := m.tasks[0]
		selected.Ignored = true
		if group == "done" {
			selected.Attention = "done"
		}
		tasks := slices.Clone(m.tasks)
		tasks[0] = selected
		m.applyResponse(protocol.Response{Tasks: tasks}, false)
		if !m.detail.available || m.detail.task.ID != 1 || !m.detail.task.Ignored || m.selectedSection != "ignored" {
			t.Fatal("Inspect lost pinned task or retained an invisible overview cursor")
		}
		view := ansi.Strip(m.View())
		if !strings.Contains(view, "Ignored    true") || !strings.Contains(view, "ABC-123") || !strings.Contains(view, selected.Attention) {
			t.Fatalf("Inspect hid preference or actual source facts:\n%s", view)
		}
		m = pressSectionKey(t, m, "esc")
		if m.mode != "" || m.selectedSection != "ignored" || m.sectionExpanded(group) {
			t.Fatal("returning from Inspect autoexpanded or selected a hidden child")
		}
		assertVisibleSelection(t, m)
	}
}

func TestRequestedHistoryTaskMapsToCollapsedHeader(t *testing.T) {
	m := sectionFixture()
	for _, index := range []int{1, 2} {
		m.selectTaskCursor(index)
		if m.selectedSection != m.tasks[index].DisplayGroup() || m.sectionExpanded(m.selectedSection) {
			t.Fatal("startup/current-context selection forced expansion")
		}
	}
	m.cursor, m.selectedSection = 1, ""
	m.ensureVisibleSelection()
	if m.selectedSection != "ignored" {
		t.Fatal("a hidden child cursor was not mapped to its header")
	}
}

func TestHistoryCountsUseServedTasksNotExpansionOrLifecyclePreference(t *testing.T) {
	m := sectionFixture()
	// The daemon may serve older Done tasks with unresolved workspace resources.
	m.tasks[3].DoneAt = "2001-01-01T00:00:00Z"
	m.tasks[3].SourceRefs = []protocol.SourceRef{{ID: "workspace:old", Source: "workspace", Kind: "workspace", CleanupIssues: []string{"unresolved work"}}}
	for _, expanded := range []bool{false, true} {
		m.expandedSections = map[string]bool{"ignored": expanded, "done": expanded}
		view := ansi.Strip(m.taskList(100, 100))
		if !strings.Contains(view, "Ignored (1)") || !strings.Contains(view, "Done (2) · last 3 days") {
			t.Fatal("history counts double-counted ignored+done or imposed different retention")
		}
		if strings.Contains(view, "Older completed work") != expanded {
			t.Fatal("TUI discarded a served unresolved-workspace retention exception")
		}
	}
}

func TestSectionHelpIsContextualAndKeepsViewportHeight(t *testing.T) {
	for _, width := range []int{40, 60, 80, 100, 140, 224} {
		m := sectionFixture()
		activeHelp := m.mainHelp(width)
		for _, group := range []string{"ignored", "done"} {
			m.selectEntry(visibleEntry{section: group})
			for _, expanded := range []bool{false, true} {
				m.expandedSections = map[string]bool{group: expanded}
				help := ansi.Strip(m.mainHelp(width))
				assertNoWideLines(t, help, width)
				if lipgloss.Height(help) != lipgloss.Height(activeHelp) {
					t.Fatal("contextual help changed viewport height")
				}
				for _, forbidden := range []string{"m ignore", "d done", "D delete", "p urgent", "o open", "i inspect", "w workspace", "x cleanup"} {
					if strings.Contains(help, forbidden) {
						t.Fatalf("header help advertises task action %q", forbidden)
					}
				}
				want := "enter expand section"
				if expanded {
					want = "enter collapse section"
				}
				if !strings.Contains(help, want) {
					t.Fatalf("help missing %q:\n%s", want, help)
				}
			}
		}
		m.expandedSections = map[string]bool{"ignored": true, "done": true}
		m.selectEntry(visibleEntry{task: 1})
		if !strings.Contains(ansi.Strip(m.mainHelp(width)), "m unignore") {
			t.Fatal("ignored task help did not offer unignore")
		}
		m.selectEntry(visibleEntry{task: 2})
		if !strings.Contains(ansi.Strip(m.mainHelp(width)), "m unignore") {
			t.Fatal("Done hid its retained ignored preference in action help")
		}
	}
}

func TestSectionLayoutHandlesSmallViewportsPageMovementAndCollapse(t *testing.T) {
	t.Setenv("TMUX", "test")
	m := sectionFixture()
	for i := 0; i < 25; i++ {
		m.tasks = append(m.tasks, protocol.Task{ID: i + 10, Title: fmt.Sprintf("History %d", i), Attention: "attention", Ignored: true, SourceRefs: []protocol.SourceRef{{ID: fmt.Sprintf("github:pr:acme/app:%d", i+10)}, {ID: fmt.Sprintf("jira:issue:ABC-%d", i+10)}}})
	}
	m.width, m.height = 80, 24
	m.selectEntry(visibleEntry{section: "ignored"})
	m = pressSectionKey(t, m, "enter")
	for _, size := range []tea.WindowSizeMsg{{Width: 80, Height: 24}, {Width: 100, Height: 30}, {Width: 60, Height: 24}} {
		updated, _ := m.Update(size)
		m = updated.(model)
		for _, key := range []string{"home", "down", "pgdown", "ctrl+d", "end", "pgup", "ctrl+u", "home"} {
			m = pressSectionKey(t, m, key)
			assertVisibleSelection(t, m)
			view := m.View()
			assertNoWideLines(t, view, size.Width)
			if lipgloss.Height(view) != size.Height {
				t.Fatalf("dashboard height %d exceeds/shrinks viewport %d", lipgloss.Height(view), size.Height)
			}
		}
	}
	m.selectEntry(visibleEntry{section: "ignored"})
	m.syncTaskScroll()
	m = pressSectionKey(t, m, "enter")
	assertVisibleSelection(t, m)
	if len(m.overviewLayout().entries) != 3 {
		t.Fatal("collapse kept history children in page navigation")
	}
	for _, key := range []string{"end", "pgup", "pgdown", "home"} {
		m = pressSectionKey(t, m, key)
		assertVisibleSelection(t, m)
	}
}

func TestSectionsNavigationWithTeatest(t *testing.T) {
	t.Setenv("TMUX", "test")
	m := sectionFixture()
	tm := teatest.NewTestModel(t, staticTUIModel{model: m}, teatest.WithInitialTermSize(80, 24))
	for _, key := range []string{"j", "enter", "j", "i", "esc", "k", "enter", "j", "enter", "j", "q"} {
		tm.Send(inspectKey(key))
	}
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
	final := tm.FinalModel(t).(staticTUIModel).model
	if final.sectionExpanded("ignored") || !final.sectionExpanded("done") {
		t.Fatal("interactive expansion state did not survive Inspect/navigation")
	}
	if task, ok := final.selectedTask(); !ok || task.ID != 3 {
		t.Fatal("interactive Done selection did not select its newest child")
	}
}
