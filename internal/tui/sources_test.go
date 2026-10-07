package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest"

	"radar/internal/protocol"
)

func TestSourcesStartCollapsed(t *testing.T) {
	m := newModel("")
	m.sources = allSourceStatusesFixture()
	if m.sourcesExpanded {
		t.Fatal("new dashboard should start with sources collapsed")
	}
	view := ansi.Strip(m.sourceList(100))
	if want := "Sources: ✓ 8 healthy  [s] details"; view != want {
		t.Fatalf("source summary = %q, want %q", view, want)
	}
	if strings.Contains(m.View(), "refs") {
		t.Fatal("collapsed dashboard still shows source diagnostics")
	}
}

func TestSourceSummaryDistinguishesHealthStates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sources []protocol.SourceStatus
		want    string
	}{
		{"healthy", []protocol.SourceStatus{{Name: "git", Status: "ok"}}, "✓ 1 healthy"},
		{"failure", []protocol.SourceStatus{{Name: "github", Status: "error"}}, "⚠ github failed"},
		{"disabled", []protocol.SourceStatus{{Name: "jira", Status: "disabled"}}, "1 disabled"},
		{"partial", []protocol.SourceStatus{{Name: "git", Status: "partial"}}, "⚠ git partial"},
		{"paused", []protocol.SourceStatus{{Name: "github", Status: "paused"}}, "⚠ github paused"},
		{"unknown", []protocol.SourceStatus{{Name: "git"}}, "⚠ git unknown"},
		{"other", []protocol.SourceStatus{{Name: "git", Status: "unavailable"}}, "⚠ git unavailable"},
		{"mixed", []protocol.SourceStatus{
			{Name: "obsidian", Status: "ok"},
			{Name: "git", Status: "partial"},
			{Name: "jira", Status: "disabled"},
			{Name: "github", Status: "error", Detail: "request failed"},
		}, "⚠ github failed · ⚠ git partial · ✓ 1 healthy · 1 disabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := model{sources: tc.sources}
			got := ansi.Strip(m.sourceList(140))
			want := "Sources: " + tc.want + "  [s] details"
			if got != want {
				t.Fatalf("source summary = %q, want %q", got, want)
			}
		})
	}
}

func TestSourceSummaryFitsOneLineAndKeepsToggleHint(t *testing.T) {
	m := model{sources: []protocol.SourceStatus{
		{Name: "obsidian", Status: "ok"},
		{Name: "github", Status: "error", Detail: "long diagnostics should only appear when expanded"},
		{Name: "another-long-source-name", Status: "partial"},
		{Name: "日本語", Status: "paused"},
	}}
	for _, width := range []int{1, 10, 20, 40, 60, 80, 140} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			view := ansi.Strip(m.sourceList(width))
			assertNoWideLines(t, view, width)
			if lipgloss.Height(view) != 1 {
				t.Fatalf("summary is not one line: %q", view)
			}
			if width >= 20 && !strings.HasSuffix(view, "[s] details") {
				t.Fatalf("toggle hint disappeared: %q", view)
			}
			if width >= 40 && !strings.Contains(view, "⚠ github failed") {
				t.Fatalf("healthy sources hid the failure: %q", view)
			}
		})
	}
}

func TestSourceToggleReclaimsRowsAndKeepsSelectionVisible(t *testing.T) {
	for _, tc := range []struct {
		width, height int
		tmux          bool
	}{
		{224, 49, true}, {100, 35, true}, {80, 24, true}, {60, 35, true},
		{180, 50, false}, {100, 35, false}, {80, 32, false},
	} {
		t.Run(fmt.Sprintf("%dx%d/tmux=%v", tc.width, tc.height, tc.tmux), func(t *testing.T) {
			t.Setenv("TMUX", "")
			if tc.tmux {
				t.Setenv("TMUX", "test")
			}
			for _, cursor := range []int{0, 29, 59} {
				m := model{width: tc.width, height: tc.height, tasks: longTaskListFixture(), sources: allSourceStatusesFixture(), cursor: cursor}
				m.syncTaskScroll()
				collapsedRows := m.taskListHeight(m.contentWidth())
				anchor := mainNavigationAnchor(m)
				footerLine := renderedLineIndex(m.View(), anchor)
				for _, expanded := range []bool{true, false} {
					updated, cmd := m.Update(runeKey('s'))
					m = updated.(model)
					if cmd != nil || m.sourcesExpanded != expanded || m.cursor != cursor {
						t.Fatalf("toggle changed selection or dispatched a command: cursor=%d expanded=%v cmd=%v", m.cursor, m.sourcesExpanded, cmd)
					}
					wantRows := collapsedRows
					if expanded {
						wantRows -= len(m.sources)
					}
					if got := m.taskListHeight(m.contentWidth()); got != wantRows {
						t.Fatalf("task rows = %d, want %d", got, wantRows)
					}
					view := ansi.Strip(m.View())
					assertNoWideLines(t, view, tc.width)
					if lipgloss.Height(view) != tc.height || !strings.Contains(view, "› ") {
						t.Fatalf("toggle overflowed or hid selection:\n%s", view)
					}
					if got := renderedLineIndex(view, anchor); got != footerLine {
						t.Fatalf("toggle moved footer from %d to %d", footerLine, got)
					}
					if strings.Contains(view, "1 refs") != expanded || strings.Contains(view, "[s] hide") != expanded {
						t.Fatalf("source details do not match expansion state:\n%s", view)
					}
					lines, start, end := m.taskLines(m.contentWidth())
					if want := adjustedTaskScroll(lines, start, end, m.scroll, wantRows); m.scroll != want {
						t.Fatalf("scroll was not synchronized: got %d want %d", m.scroll, want)
					}
				}
			}
		})
	}
}

func TestSourceRefreshPreservesExpansionAndUpdatesSummary(t *testing.T) {
	for _, expanded := range []bool{false, true} {
		for _, watch := range []bool{false, true} {
			t.Run(fmt.Sprintf("expanded=%v/watch=%v", expanded, watch), func(t *testing.T) {
				m := model{sourcesExpanded: expanded, watching: true, selectedCurrentTask: true, cursor: 2, tasks: longTaskListFixture(), sources: allSourceStatusesFixture()}
				for _, status := range []string{"error", "ok"} {
					response := protocol.Response{OK: true, Sources: []protocol.SourceStatus{{Name: "github", Status: status, SourceRefCount: 6, Detail: "full diagnostics"}}}
					var msg tea.Msg = fetchMsg{response: response}
					if watch {
						msg = watchMsg{response: response}
					}
					updated, _ := m.Update(msg)
					m = updated.(model)
					if m.sourcesExpanded != expanded || m.cursor != 2 || !reflect.DeepEqual(m.sources, response.Sources) {
						t.Fatal("refresh changed expansion/selection or failed to update sources")
					}
					view := ansi.Strip(m.sourceList(100))
					want := "✓ 1 healthy"
					if status == "error" {
						want = "⚠ github failed"
					}
					if !strings.Contains(view, want) || strings.Contains(view, "full diagnostics") != expanded {
						t.Fatalf("refresh did not update source display: %q", view)
					}
				}
			})
		}
	}
}

func TestSourceToggleWithNoSourcesOrTasks(t *testing.T) {
	m := model{width: 100, height: 35}
	for range 2 {
		updated, cmd := m.Update(runeKey('s'))
		m = updated.(model)
		if cmd != nil || m.cursor != 0 || m.scroll != 0 || strings.Contains(m.View(), "Sources:") {
			t.Fatal("empty dashboard toggle should be harmless and omit the empty summary")
		}
	}
}

func TestSourceToggleRemainsAvailableDuringTaskOperation(t *testing.T) {
	m := model{tasks: []protocol.Task{{ID: 1, Title: "Task", Attention: "in_progress"}}, sources: allSourceStatusesFixture()}
	m.startOperation(m.tasks[0], "cleanup", "Cleaning up…", "Cleanup failed", nil)
	updated, cmd := m.Update(runeKey('s'))
	got := updated.(model)
	if cmd != nil || !got.sourcesExpanded || !reflect.DeepEqual(got.operation, m.operation) {
		t.Fatal("source toggle should remain available without interrupting the operation")
	}
}

func TestSourceToggleIsScopedToDashboard(t *testing.T) {
	for _, mode := range []string{"detail", "cleanup_confirm", "open_link", "worktree_session", "task_authoring", "create_name", "workspace_name"} {
		t.Run(mode, func(t *testing.T) {
			m := model{mode: mode}
			updated, _ := m.Update(runeKey('s'))
			got := updated.(model)
			if got.sourcesExpanded {
				t.Fatal("s toggled sources outside the dashboard")
			}
			if mode == "task_authoring" && got.authoredTitle != "s" {
				t.Fatal("source shortcut intercepted task title input")
			}
		})
	}
}

func TestSourceToggleWithTeatest(t *testing.T) {
	t.Setenv("TMUX", "test")
	for _, toggles := range []int{1, 2} {
		m := staticTUIModel{model: model{tasks: longTaskListFixture(), sources: allSourceStatusesFixture()}}
		tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 35))
		tm.Send(runeKey('G'))
		for range toggles {
			tm.Send(runeKey('s'))
		}
		tm.Send(runeKey('q'))
		final := tm.FinalModel(t, teatest.WithFinalTimeout(time.Second)).(staticTUIModel).model
		if final.sourcesExpanded != (toggles == 1) || final.cursor != len(m.tasks)-1 {
			t.Fatal("event loop lost source expansion or task selection")
		}
		if view := ansi.Strip(final.View()); !strings.Contains(view, "›   progress task") {
			t.Fatalf("event loop hid selected task:\n%s", view)
		}
	}
}
