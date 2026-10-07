package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest"

	"radar/internal/protocol"
	"radar/internal/update"
)

func railFixture() model {
	task := authoredTaskForTUITest("open", "normal", "in_progress")
	task.Title = "Selected example"
	task.URL = "https://example.test/task"
	return model{width: 180, height: 49, tasks: []protocol.Task{task}, sources: allSourceStatusesFixture()}
}

func TestDashboardShowsActionFirstRightRail(t *testing.T) {
	for _, tmux := range []string{"", "test"} {
		t.Run("tmux="+tmux, func(t *testing.T) {
			t.Setenv("TMUX", tmux)
			m := railFixture()
			view := ansi.Strip(m.View())
			for _, want := range []string{"KEYBOARD", "Navigate", "Selected task", "Global", "OTHER BINDINGS", "Mark done", "Make urgent", "Ctrl+P/N", "Ctrl+U/D"} {
				if !strings.Contains(view, want) {
					t.Fatalf("missing rail text %q:\n%s", want, view)
				}
			}
			if strings.Contains(view, "↑/k/ctrl+p ↓/j/ctrl+n") {
				t.Fatal("dashboard retained its keymap footer")
			}
			if renderedColumnIndex(view, "KEYBOARD") <= renderedColumnIndex(view, "Selected example") {
				t.Fatal("keymap is not to the right of the task list")
			}
			assertNoWideLines(t, view, m.width)
			if lipgloss.Height(view) != m.height {
				t.Fatal("rail changed the frame height")
			}
		})
	}
}

func TestRailResizeRetainsSelectionAndSourceSpace(t *testing.T) {
	t.Setenv("TMUX", "test")
	m := railFixture()
	m.tasks = longTaskListFixture()
	m.cursor = 42
	for _, size := range []tea.WindowSizeMsg{{Width: 224, Height: 49}, {Width: 80, Height: 24}, {Width: 180, Height: 50}, {Width: 180, Height: 28}, {Width: 140, Height: 45}} {
		updated, _ := m.Update(size)
		m = updated.(model)
		if m.cursor != 42 {
			t.Fatal("layout change moved selection")
		}
		view := ansi.Strip(m.View())
		assertNoWideLines(t, view, size.Width)
		if lipgloss.Height(view) != size.Height || !strings.Contains(view, "›   progress task") {
			t.Fatalf("resize overflowed or hid selection at %dx%d:\n%s", size.Width, size.Height, view)
		}
		if got, want := strings.Contains(view, "KEYBOARD"), size.Width >= 140 && size.Height >= 45; got != want {
			t.Fatalf("rail present=%v, want=%v", got, want)
		}
	}
}

func TestRailKeepsGlobalSectionStableAcrossContexts(t *testing.T) {
	t.Setenv("TMUX", "test")
	m := railFixture()
	m.tasks = append(m.tasks, protocol.Task{ID: 8, Title: "Completed", Attention: "done"})
	initial := m.View()
	for _, state := range []string{"section", "busy", "empty"} {
		other := m
		switch state {
		case "section":
			other.selectedSection = "done"
		case "busy":
			other.operation = taskOperation{kind: "cleanup", task: m.tasks[0], label: "Working"}
		case "empty":
			other.tasks = nil
		}
		view := ansi.Strip(other.View())
		for _, anchor := range []string{"Navigate", "Global", "OTHER BINDINGS", "Sources:"} {
			if renderedLineIndex(view, anchor) != renderedLineIndex(initial, anchor) {
				t.Fatalf("%s moved in %s state", anchor, state)
			}
		}
		for _, forbidden := range []string{"Mark done", "Make urgent", "Delete task"} {
			if strings.Contains(view, forbidden) {
				t.Fatalf("%s advertises unavailable %s", state, forbidden)
			}
		}
		if state == "section" && !strings.Contains(view, "Expand section") {
			t.Fatal("section action missing")
		}
	}
}

func TestRailReservesReleaseNoticeAndCompactSourceRows(t *testing.T) {
	t.Setenv("TMUX", "test")
	m := railFixture()
	m.releaseNotice = update.Notice{Version: "v1.2.3"}
	view := ansi.Strip(m.View())
	for _, want := range []string{"KEYBOARD", "Run radar update", "v1.2.3 available"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if lipgloss.Height(view) != m.height {
		t.Fatal("release notice overflowed frame")
	}
	m.width, m.height = 80, 23
	m.releaseNotice = update.Notice{}
	m.sourcesExpanded = true
	m.tasks = longTaskListFixture()
	m.cursor = len(m.tasks) - 1
	m.syncTaskScroll()
	view = ansi.Strip(m.View())
	if lipgloss.Height(view) != m.height || !strings.Contains(view, "›   progress task") {
		t.Fatalf("compact layout overflowed or hid selection:\n%s", view)
	}
	assertNoWideLines(t, view, m.width)
}

func TestRailUsesFullTaskHeightAndFixedWidth(t *testing.T) {
	t.Setenv("TMUX", "test")
	for _, width := range []int{140, 180, 224} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m := railFixture()
			m.width = width
			view := ansi.Strip(m.View())
			rightMargin := width - renderedColumnIndex(view, "KEYBOARD")
			if rightMargin != 36 {
				t.Fatalf("rail isn't 32 columns wide: right margin=%d", rightMargin)
			}
			wantRows := m.height - m.frameHeight() - lipgloss.Height(m.header(m.contentWidth())) - lipgloss.Height(m.sourceList(m.contentWidth())) - 2
			if rows := m.taskListHeight(m.contentWidth()); rows != wantRows {
				t.Fatalf("keymap used task rows: got %d, want %d", rows, wantRows)
			}
		})
	}
}

func mainNavigationAnchor(m model) string {
	if m.showsMainRail() {
		return "↑/k ↓/j"
	}
	return "↑/k/ctrl+p"
}

func TestRailKeysAlignAtTheRightEdge(t *testing.T) {
	t.Setenv("TMUX", "test")
	m := railFixture()
	lines := strings.Split(ansi.Strip(m.mainRail(m.mainRailHeight())), "\n")
	for _, group := range mainRailGroups() {
		for _, hint := range group.hints {
			key := hint.keys
			if key == "enter" {
				key = "Enter"
			}
			found := false
			for _, line := range lines {
				if strings.HasSuffix(line, "  "+key) && lipgloss.Width(line) == mainRailWidth {
					found = true
				}
			}
			if !found {
				t.Fatalf("key %q not right aligned in rail:\n%s", key, strings.Join(lines, "\n"))
			}
		}
	}
	assertNoWideLines(t, strings.Join(lines, "\n"), mainRailWidth)
}

func TestRailUsesCurrentTaskLabels(t *testing.T) {
	t.Setenv("TMUX", "test")
	for _, state := range []string{"open", "done"} {
		m := railFixture()
		m.tasks[0] = authoredTaskForTUITest(state, "urgent", "in_progress")
		m.tasks[0].Muted = true
		m.expandedSections = map[string]bool{"muted": true}
		want := "Mark done"
		if state == "done" {
			want = "Reopen"
		}
		view := m.mainRail(m.mainRailHeight())
		for _, label := range []string{want, "Unmute", "Make normal"} {
			if !strings.Contains(view, label) {
				t.Fatalf("%s state missing %s", state, label)
			}
		}
		m.tasks[0].Attention = "done"
		m.expandedSections["done"] = true
		if strings.Contains(m.mainRail(m.mainRailHeight()), "Make normal") {
			t.Fatal("done task advertises priority mutation")
		}
	}
}

func TestRailContextDoesNotChangeGeometry(t *testing.T) {
	t.Setenv("TMUX", "test")
	m := railFixture()
	m.tasks = longTaskListFixture()
	m.cursor = 42
	m.syncTaskScroll()
	rows := m.taskListHeight(m.taskListWidth())
	before := m.View()
	for _, expanded := range []bool{true, false} {
		m = navigationKey(t, m, "s")
		want := rows
		if expanded {
			want -= len(m.sources)
		}
		if got := m.taskListHeight(m.taskListWidth()); got != want {
			t.Fatalf("source rows: got %d want %d", got, want)
		}
		if renderedLineIndex(m.View(), "Global") != renderedLineIndex(before, "Global") {
			t.Fatal("sources toggle moved rail")
		}
		assertVisibleSelection(t, m)
	}
}

func TestRailLeavesModalsUnchanged(t *testing.T) {
	t.Setenv("TMUX", "test")
	for _, mode := range []string{"create_repo", "create_name", "workspace_edit", "workspace_confirm", "worktree_session", "detail", "open_link", "task_authoring", "cleanup_confirm", "task_delete_confirm"} {
		m := navigationModel(mode)
		m.width, m.height = 180, 49
		if strings.Contains(m.View(), "KEYBOARD") {
			t.Fatalf("dashboard shortcuts leaked into %s", mode)
		}
	}
}

func TestRailResizeAndPagingWithTeatest(t *testing.T) {
	t.Setenv("TMUX", "test")
	m := railFixture()
	m.tasks = longTaskListFixture()
	tm := teatest.NewTestModel(t, staticTUIModel{model: m}, teatest.WithInitialTermSize(180, 49))
	for _, key := range []string{"pgdown", "ctrl+d", "end"} {
		tm.Send(inspectKey(key))
	}
	tm.Send(tea.WindowSizeMsg{Width: 80, Height: 24})
	tm.Send(inspectKey("s"))
	tm.Send(tea.WindowSizeMsg{Width: 180, Height: 49})
	tm.Send(inspectKey("q"))
	final := tm.FinalModel(t, teatest.WithFinalTimeout(time.Second)).(staticTUIModel).model
	if final.cursor != len(m.tasks)-1 || !final.sourcesExpanded || !final.showsMainRail() {
		t.Fatal("event loop lost selection, sources or rail")
	}
	assertVisibleSelection(t, final)
	if view := ansi.Strip(final.View()); !strings.Contains(view, "›   progress task") || lipgloss.Height(view) != final.height {
		t.Fatalf("event loop hid selection or overflowed:\n%s", view)
	}
}

func TestRailLayoutThresholdsDoNotClip(t *testing.T) {
	for _, tmux := range []string{"", "test"} {
		t.Setenv("TMUX", tmux)
		for _, width := range []int{92, 94, 95, 99, 100, 104, 105, 112} {
			for height := 30; height <= 44; height++ {
				m := railFixture()
				m.width, m.height = width, height
				view := m.View()
				assertNoWideLines(t, view, width)
				if lipgloss.Height(view) != height || !strings.Contains(view, "Selected example") {
					t.Fatalf("layout clipped at %dx%d/tmux=%s", width, height, tmux)
				}
				if m.showsMainRail() && (m.taskListWidth() < minTaskListWidth || !strings.Contains(view, "OTHER BINDINGS")) {
					t.Fatal("rail clipped aliases or squeezed the task list")
				}
			}
		}
	}
}

func TestRailTruncatesTaskLabelsBeforeSeparator(t *testing.T) {
	t.Setenv("TMUX", "test")
	m := railFixture()
	m.tasks[0].Title = strings.Repeat("界", 100)
	m.tasks[0].Repo = strings.Repeat("long-repository/", 20)
	m.tasks[0].SourceRefs[0].ID = strings.Repeat("界", 100)
	view := ansi.Strip(m.View())
	assertNoWideLines(t, view, m.width)
	column := renderedColumnIndex(view, "KEYBOARD") - 2
	for _, line := range strings.Split(view, "\n")[renderedLineIndex(view, "KEYBOARD") : m.height-2] {
		if lipgloss.Width(ansi.Truncate(line, column+1, "")) != column+1 || !strings.HasSuffix(ansi.Truncate(line, column+1, ""), "│") {
			t.Fatalf("task text displaced the rail separator: %q", line)
		}
	}
}

func TestCompactViewportKeepsTitleWhenOnlyTwoRowsFit(t *testing.T) {
	m := railFixture()
	m.tasks[0].SourceRefs = append(m.tasks[0].SourceRefs, protocol.SourceRef{ID: "jira:issue:ABC-123"})
	m.tasks = append(m.tasks, protocol.Task{ID: 8, Title: "Next task", Attention: "in_progress"})
	for _, rows := range []int{1, 2} {
		view := ansi.Strip(m.taskList(60, rows))
		if lipgloss.Height(view) != rows || !strings.Contains(view, "›   Selected example") {
			t.Fatalf("%d rows hid selection behind a heading or scroll indicator:\n%s", rows, view)
		}
	}
}
