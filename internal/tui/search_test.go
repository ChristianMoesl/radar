package tui

import (
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

func TestTaskSearchMatchesTermsAcrossGenericFields(t *testing.T) {
	task := protocol.Task{Title: "Fix VÄULT access", Repo: "acme/app", SourceRefs: []protocol.SourceRef{{
		ID: "issue:ABC-123", Title: "Rotate credentials", EntityID: "12345", Branch: "feature/rotation",
		Path: "/work/secret-tools", Repo: "acme/service", LinkingKeys: []string{"mark:ABC-456"},
		Presentation: protocol.SourceRefPresentation{Label: "Infrastructure", WorkspaceName: "Security workspace"},
		Metadata:     map[string]string{"body": "not-indexed"},
	}}}
	for _, tc := range []struct {
		query string
		want  bool
	}{
		{"", true}, {" \t ", true}, {"väult ABC-123", true}, {"ACCESS\tcredentials", true},
		{"app service rotation secret-tools", true}, {"infrastructure security", true},
		{"ABC-456 12345", true}, {"väult missing", false}, {"not-indexed", false}, {"fixaccess", false},
	} {
		t.Run(tc.query, func(t *testing.T) {
			m := model{searchQuery: tc.query}
			if got := matchesTaskSearch(task, m.searchTerms()); got != tc.want {
				t.Fatalf("matches = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSearchInputDoesNotDispatchDashboardActions(t *testing.T) {
	m := navigationKey(t, sectionFixture(), "/")
	for _, key := range []string{"j", "k", "d", "D", "m", "p", "q", "i", "o", "n", "c", "w", "f", "r", "s", "x", "X"} {
		m = navigationKey(t, m, key)
	}
	if m.mode != "search" || m.searchQuery != "jkdDmpqioncwfrsxX" {
		t.Fatalf("input was interpreted as actions: mode=%q query=%q", m.mode, m.searchQuery)
	}
	m = navigationKey(t, m, "backspace")
	m = navigationKey(t, m, "界")
	m = navigationKey(t, m, "backspace")
	if m.searchQuery != "jkdDmpqioncwfrsx" {
		t.Fatal("backspace did not remove one rune")
	}
	m = navigationKey(t, m, "esc")
	m = navigationKey(t, m, "/")
	updated, cmd := m.Update(inspectKey("ctrl+c"))
	if updated.(model).mode != "search" || cmd == nil {
		t.Fatal("Ctrl+C did not remain available")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("Ctrl+C did not quit")
	}
}

func TestSearchRevealsHistoryWithoutChangingExpansion(t *testing.T) {
	for _, expanded := range []bool{false, true} {
		m := sectionFixture()
		m.expandedSections = map[string]bool{"muted": expanded, "done": !expanded}
		original := map[string]bool{"muted": expanded, "done": !expanded}
		m = navigationKey(t, m, "/")
		m = navigationKey(t, m, "WORK")
		want := []visibleEntry{{task: 0}, {task: 1}, {task: 2}, {task: 3}}
		if got := m.overviewLayout().entries; !slices.Equal(got, want) {
			t.Fatalf("filtered order = %+v, want %+v", got, want)
		}
		for _, key := range []string{"down", "ctrl+n", "up", "ctrl+p"} {
			m = navigationKey(t, m, key)
			assertVisibleSelection(t, m)
		}
		m = navigationKey(t, m, "enter")
		if m.mode != "" || m.searchQuery != "WORK" {
			t.Fatal("Enter did not retain the filter for normal navigation")
		}
		m = navigationKey(t, m, "G")
		if task, ok := m.selectedTask(); !ok || task.ID != 4 {
			t.Fatal("normal navigation did not reach matching Done work")
		}
		m.toggleSection("done")
		if !reflect.DeepEqual(m.expandedSections, original) {
			t.Fatal("search changed stored expansion choices")
		}
		m = navigationKey(t, m, "esc")
		if m.mode != "" || m.searchQuery != "" || !reflect.DeepEqual(m.expandedSections, original) {
			t.Fatal("Esc did not clear search and restore expansion")
		}
		assertVisibleSelection(t, m)
	}
}

func TestSearchEscapeExitsWithoutQuitting(t *testing.T) {
	for _, query := range []string{"", "work", "no results"} {
		for _, accept := range []bool{false, true} {
			m := navigationKey(t, sectionFixture(), "/")
			if query != "" {
				m = navigationKey(t, m, query)
			}
			if accept && query != "" {
				m = navigationKey(t, m, "enter")
				m = navigationKey(t, m, "/") // Re-edit the retained query.
				if m.searchQuery != query {
					t.Fatal("re-editing lost the query")
				}
				m = navigationKey(t, m, "enter")
			}
			m = navigationKey(t, m, "esc")
			if m.mode != "" || m.searchQuery != "" {
				t.Fatal("Esc did not exit and clear search")
			}
			_, cmd := m.Update(inspectKey("esc"))
			if cmd == nil {
				t.Fatal("second Esc did not quit")
			}
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Fatal("second Esc returned a different action")
			}
		}
	}
}

func TestSearchNoResultsCannotActOnHiddenTasks(t *testing.T) {
	m := navigationKey(t, sectionFixture(), "/")
	m = navigationKey(t, m, "missing")
	m = navigationKey(t, m, "enter")
	if _, ok := m.selectedTask(); ok || len(m.overviewLayout().entries) != 0 {
		t.Fatal("no-results search retained a hidden task selection")
	}
	for _, key := range []string{"d", "D", "m", "p", "i", "o", "w", "x", "enter", "j", "k", "G", "g", "pgdown", "pgup"} {
		m = navigationKey(t, m, key)
		assertVisibleSelection(t, m)
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "No matching tasks.") || !strings.Contains(view, "0/4") {
		t.Fatalf("missing empty-search feedback:\n%s", view)
	}
	m = navigationKey(t, m, "esc")
	if _, ok := m.selectedTask(); !ok {
		t.Fatal("clearing search did not restore a valid task")
	}
}

func TestSearchRefreshPreservesMatchingIdentityAndFallsBackSafely(t *testing.T) {
	m := navigationKey(t, sectionFixture(), "/")
	m = navigationKey(t, m, "work")
	m = navigationKey(t, m, "down") // Muted task ID 2.
	tasks := slices.Clone(m.tasks)
	slices.Reverse(tasks)
	m.applyResponse(protocol.Response{Revision: 1, Tasks: tasks}, true)
	if task, ok := m.selectedTask(); !ok || task.ID != 2 || m.searchQuery != "work" || m.mode != "search" {
		t.Fatal("refresh lost selected identity or search")
	}
	for i := range tasks {
		if tasks[i].ID == 2 {
			tasks[i].Title = "No longer matches"
		}
	}
	m.applyResponse(protocol.Response{Revision: 2, Tasks: tasks}, false)
	if task, ok := m.selectedTask(); !ok || task.ID != 1 {
		t.Fatal("refresh left selection on a nonmatching task")
	}
	assertVisibleSelection(t, m)
	m.applyResponse(protocol.Response{Revision: 3, Tasks: []protocol.Task{}}, false)
	assertVisibleSelection(t, m)
	if m.searchQuery != "work" {
		t.Fatal("empty snapshot cleared the query")
	}
}

func TestSearchKeepsMatchingSelectionThroughCompletion(t *testing.T) {
	m := navigationKey(t, sectionFixture(), "/")
	m = navigationKey(t, m, "active")
	tasks := slices.Clone(m.tasks)
	tasks[0].Attention = "done"
	m.applyResponse(protocol.Response{Tasks: tasks}, false)
	if task, ok := m.selectedTask(); !ok || task.ID != 1 || task.Attention != "done" {
		t.Fatal("completion moved away from the only matching task")
	}
	assertVisibleSelection(t, m)
	m = navigationKey(t, m, "esc")
	if m.selectedSection != "done" || m.sectionExpanded("done") {
		t.Fatal("clear search retained an invisible child or expanded Done")
	}
}

func TestSearchPersistsAcrossInspectAndOpenLinks(t *testing.T) {
	for _, key := range []string{"i", "o"} {
		m := sectionFixture()
		m.tasks[0].URL = "https://example.test/task"
		m = navigationKey(t, m, "/")
		m = navigationKey(t, m, "active")
		m = navigationKey(t, m, "enter")
		m = navigationKey(t, m, key)
		if m.mode == "" {
			t.Fatalf("%s did not open its view", key)
		}
		m = navigationKey(t, m, "esc")
		if m.mode != "" || m.searchQuery != "active" {
			t.Fatal("returning from a view lost search")
		}
		if task, ok := m.selectedTask(); !ok || task.ID != 1 {
			t.Fatal("returning from a view lost selection")
		}
	}
}

func TestSearchFitsCompactAndRailLayouts(t *testing.T) {
	for _, tmux := range []string{"", "test"} {
		t.Setenv("TMUX", tmux)
		for _, size := range []tea.WindowSizeMsg{{Width: 224, Height: 49}, {Width: 80, Height: 24}, {Width: 180, Height: 50}, {Width: 180, Height: 28}, {Width: 140, Height: 45}} {
			m := railFixture()
			m.tasks = longTaskListFixture()
			m = navigationKey(t, m, "/")
			m = navigationKey(t, m, "task")
			updated, _ := m.Update(size)
			m = updated.(model)
			for range 60 {
				m = navigationKey(t, m, "down")
			}
			for _, query := range []string{"task", strings.Repeat("界", 200)} {
				m.searchQuery = query
				m.syncTaskScroll()
				view := ansi.Strip(m.View())
				assertNoWideLines(t, view, size.Width)
				if lipgloss.Height(view) != size.Height {
					t.Fatalf("search overflowed %dx%d: height=%d\n%s", size.Width, size.Height, lipgloss.Height(view), view)
				}
				if strings.Contains(view, "Mark done") || strings.Contains(view, "j/ctrl+n") {
					t.Fatal("input help advertised dashboard action keys")
				}
				assertVisibleSelection(t, m)
			}
		}
	}
}

func TestSearchAvailableDuringResourceOperation(t *testing.T) {
	m := sectionFixture()
	m.operation = taskOperation{task: m.tasks[0], kind: "cleanup", label: "Working"}
	m = navigationKey(t, m, "/")
	m = navigationKey(t, m, "muted")
	if task, ok := m.selectedTask(); !ok || task.ID != 2 || m.operation.kind != "cleanup" {
		t.Fatal("operation blocked search or search interfered with operation")
	}
	m = navigationKey(t, m, "enter")
	m = navigationKey(t, m, "esc")
	if m.searchQuery != "" || m.operation.kind != "cleanup" {
		t.Fatal("operation prevented clearing search")
	}
}

func TestSearchInteractionWithTeatest(t *testing.T) {
	t.Setenv("TMUX", "test")
	m := sectionFixture()
	m.width, m.height = 100, 30
	tm := teatest.NewTestModel(t, staticTUIModel{model: m}, teatest.WithInitialTermSize(100, 30))
	for _, key := range []string{"/", "ABC-456", "enter", "i", "esc", "/", "esc", "q"} {
		tm.Send(inspectKey(key))
	}
	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second)).(staticTUIModel).model
	if final.searchQuery != "" || final.mode != "" || final.sectionExpanded("done") {
		t.Fatal("interactive search did not clear or restore collapsed history")
	}
	assertVisibleSelection(t, final)
}
