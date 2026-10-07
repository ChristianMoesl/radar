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

	"radar/internal/integration"
	"radar/internal/protocol"
)

func navigationModel(mode string) model {
	m := model{mode: mode, width: 100, height: 24, create: newCreateForm()}
	m.create.repo = "/repo/example"
	if list := m.activePicker(); list != nil {
		list.options = nil
	}
	for i := 0; i < 40; i++ {
		label := fmt.Sprintf("item-%02d-", i) + strings.Repeat("界", 80)
		if list := m.activePicker(); list != nil {
			list.options = append(list.options, label)
			list.loading = false
		}
		m.worktrees = append(m.worktrees, protocol.SourceRef{Path: "/repo/" + label, Branch: "feature"})
		m.editor.desired.Worktrees = append(m.editor.desired.Worktrees, integration.DesiredWorkspaceWorktree{Repository: "/repo/" + label, Branch: "feature"})
		m.editor.plan.Warnings = append(m.editor.plan.Warnings, fmt.Sprintf("Warning %02d: %s", i, label))
	}
	return m
}

func navigationKey(t *testing.T, m model, key string) model {
	t.Helper()
	updated, cmd := m.Update(inspectKey(key))
	if cmd != nil {
		t.Fatalf("navigation %q unexpectedly dispatched an action", key)
	}
	return updated.(model)
}

func TestPickerNavigationStopsAtEdgesAndPreservesSearch(t *testing.T) {
	for _, mode := range []string{"create_repo", "create_intent", "create_branch", "create_base", "fork_member"} {
		t.Run(mode, func(t *testing.T) {
			m := model{mode: mode}
			m.activePicker().options = []string{"alpha", "jacket", "joke"}
			for _, key := range []string{"up", "ctrl+p"} {
				m = navigationKey(t, m, key)
				if m.activePicker().cursor != 0 {
					t.Fatalf("%s wrapped at top", key)
				}
			}
			for range 6 {
				m = navigationKey(t, m, "down")
			}
			if m.activePicker().cursor != 2 {
				t.Fatal("down wrapped at bottom")
			}
			m = navigationKey(t, m, "j")
			m = navigationKey(t, m, "k")
			list := m.activePicker()
			if list.query != "jk" || list.cursor != 0 || len(filteredOptions(*list)) != 2 {
				t.Fatalf("search changed: %+v", list)
			}
			m = navigationKey(t, m, "ctrl+n")
			m = navigationKey(t, m, "down")
			if m.activePicker().cursor != 1 {
				t.Fatal("filtered list wrapped")
			}
			m = navigationKey(t, m, "z")
			for _, key := range []string{"up", "down", "ctrl+n", "ctrl+p"} {
				m = navigationKey(t, m, key)
			}
			if m.activePicker().cursor != 0 || len(filteredOptions(*m.activePicker())) != 0 {
				t.Fatal("empty search moved selection")
			}
		})
	}
}

func TestSelectionListsFitAndRetainSelectionOnResize(t *testing.T) {
	for _, tmux := range []string{"", "test"} {
		for _, mode := range []string{"create_repo", "create_intent", "create_branch", "create_base", "fork_member", "worktree_session", "workspace_edit"} {
			t.Run(mode+"/tmux="+tmux, func(t *testing.T) {
				t.Setenv("TMUX", tmux)
				m := navigationModel(mode)
				for _, size := range []tea.WindowSizeMsg{{Width: 100, Height: 15}, {Width: 32, Height: 20}, {Width: 60, Height: 24}, {Width: 160, Height: 40}, {Width: 80, Height: 20}} {
					updated, _ := m.Update(size)
					m = updated.(model)
					for range 45 {
						m = navigationKey(t, m, "down")
						assertSelectionFits(t, m, size)
					}
					view := ansi.Strip(m.View())
					if !strings.Contains(view, "item-39-") || !strings.Contains(view, "↑ more") || strings.Contains(view, "↓ more") {
						t.Fatalf("last item/position missing:\n%s", view)
					}
				}
				for range 45 {
					m = navigationKey(t, m, "up")
					assertSelectionFits(t, m, tea.WindowSizeMsg{Width: m.width, Height: m.height})
				}
				if view := ansi.Strip(m.View()); !strings.Contains(view, "item-00-") || strings.Contains(view, "↑ more") {
					t.Fatalf("did not return to first item:\n%s", view)
				}
			})
		}
	}
}

func assertSelectionFits(t *testing.T, m model, size tea.WindowSizeMsg) {
	t.Helper()
	view := m.View()
	if lipgloss.Height(view) > size.Height {
		t.Fatalf("%s overflowed %dx%d (%d rows):\n%s", m.mode, size.Width, size.Height, lipgloss.Height(view), ansi.Strip(view))
	}
	assertNoWideLines(t, view, size.Width)
	if !strings.Contains(ansi.Strip(view), "› ") || !strings.Contains(view, "esc") {
		t.Fatalf("selection/help missing:\n%s", ansi.Strip(view))
	}
}

func TestWorkspaceConfirmationPageAndEdgeAliases(t *testing.T) {
	t.Setenv("TMUX", "")
	m := navigationModel("workspace_confirm")
	for _, pair := range [][2]string{{"down", "j"}, {"up", "k"}, {"pgdown", "ctrl+d"}, {"pgup", "ctrl+u"}} {
		m.editor.scroll = 20
		a := navigationKey(t, m, pair[0])
		b := navigationKey(t, m, pair[1])
		if a.editor.scroll != b.editor.scroll || a.editor.scroll == m.editor.scroll {
			t.Fatalf("%v: offsets %d/%d from %d", pair, a.editor.scroll, b.editor.scroll, m.editor.scroll)
		}
	}
	m = navigationKey(t, m, "end")
	if !strings.Contains(m.View(), "Warning 39:") {
		t.Fatal("End did not reach last warning")
	}
	for _, size := range []tea.WindowSizeMsg{{Width: 60, Height: 20}, {Width: 100, Height: 40}} {
		updated, _ := m.Update(size)
		m = updated.(model)
		m = navigationKey(t, m, "end")
		if lipgloss.Height(m.View()) > size.Height {
			t.Fatalf("confirmation overflowed after resize:\n%s", ansi.Strip(m.View()))
		}
		if !strings.Contains(m.View(), "y/enter apply") || !strings.Contains(m.View(), "Warning 39:") {
			t.Fatal("confirmation hid its final warning or actions")
		}
	}
	m = navigationKey(t, m, "home")
	if m.editor.scroll != 0 || !strings.Contains(m.View(), "Apply workspace changes?") {
		t.Fatal("Home did not return to start")
	}
}

func TestNavigationHelpMatchesContext(t *testing.T) {
	for _, mode := range []string{"workspace_edit", "workspace_confirm", "cleanup_confirm", "task_delete_confirm"} {
		m := navigationModel(mode)
		m.height = 18
		m.cleanup = cleanupFixture()
		m.deletion.preview.Description = strings.Repeat("detail\n", 40)
		view := ansi.Strip(m.View())
		for _, hint := range []string{"↑/k", "↓/j"} {
			if !strings.Contains(view, hint) {
				t.Errorf("%s help missing %q", mode, hint)
			}
		}
	}
	m := model{mode: "create_name", width: 100}
	view := ansi.Strip(m.View())
	if strings.Contains(view, "filter") || strings.Contains(view, "move") || !strings.Contains(view, "type a branch name") {
		t.Fatalf("branch name shows picker help:\n%s", view)
	}
	if !strings.Contains(ansi.Strip(mainHelp(140)), "PgUp/PgDn") {
		t.Fatal("dashboard omits page keys")
	}
}

func selectionState(m model) (cursor, scroll int) {
	if list := m.activePicker(); list != nil {
		return list.cursor, list.scroll
	}
	if m.mode == "worktree_session" {
		return m.worktreeCursor, m.worktreeScroll
	}
	return m.editor.cursor, m.editor.listScroll
}

func TestSelectionScrollOnlyMovesAtViewportEdges(t *testing.T) {
	for _, mode := range []string{"create_repo", "worktree_session", "workspace_edit"} {
		t.Run(mode, func(t *testing.T) {
			m := navigationModel(mode)
			for range 25 {
				m = navigationKey(t, m, "down")
			}
			_, scroll := selectionState(m)
			if scroll == 0 {
				t.Fatal("selection did not scroll")
			}
			footerRow := renderedLineIndex(m.View(), "esc")
			m = navigationKey(t, m, "up")
			cursor, gotScroll := selectionState(m)
			if gotScroll != scroll || cursor != 24 || renderedLineIndex(m.View(), "esc") != footerRow {
				t.Fatal("moving within the viewport moved the viewport or help")
			}
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
			m = updated.(model)
			if cursor, _ := selectionState(m); cursor != 24 {
				t.Fatal("resize changed selection")
			}
			assertSelectionFits(t, m, tea.WindowSizeMsg{Width: 80, Height: 20})
		})
	}
}

func TestPickerViewportLoadingEmptyAndLongQuery(t *testing.T) {
	t.Setenv("TMUX", "")
	m := navigationModel("create_repo")
	m.width, m.height = 60, 20
	for range 25 {
		m = navigationKey(t, m, "down")
	}
	m = navigationKey(t, m, "item-03")
	if m.create.repoList.cursor != 0 || m.create.repoList.scroll != 0 || !strings.Contains(m.View(), "item-03-") {
		t.Fatal("filter did not reset the viewport")
	}
	query := strings.Repeat("界", 80) + "jk-tail"
	m.create.repoList.query = ""
	m = navigationKey(t, m, query)
	if m.create.repoList.query != query {
		t.Fatal("display truncation modified the query")
	}
	for _, loading := range []bool{false, true} {
		m.create.repoList.loading = loading
		view := m.View()
		want := "No matches"
		if loading {
			want = "Loading…"
		}
		if !strings.Contains(view, want) || !strings.Contains(view, "jk-tail") || !strings.Contains(view, "esc cancel") {
			t.Fatalf("missing query, state or help:\n%s", ansi.Strip(view))
		}
		assertNoWideLines(t, view, m.width)
		if lipgloss.Height(view) > m.height {
			t.Fatal("loading/empty picker exceeded terminal height")
		}
	}
}

func TestTruncatedSelectionKeepsFullActionValue(t *testing.T) {
	m := navigationModel("create_repo")
	m = navigationKey(t, m, "down")
	want := m.create.repoList.options[1]
	m = navigationKey(t, m, "enter")
	if m.mode != "create_intent" || m.create.repo != want {
		t.Fatal("selection used the truncated label as a repository path")
	}
}

func TestTextFieldsKeepNavigationLetters(t *testing.T) {
	for _, mode := range []string{"task_authoring", "workspace_name", "create_name"} {
		m := model{mode: mode}
		for _, key := range []string{"j", "k", "g", "G", "home", "end"} {
			m = navigationKey(t, m, key)
		}
		if got := m.authoredTitle + m.editor.create.Name + m.create.name; got != "jkgG" {
			t.Fatalf("%s text=%q", mode, got)
		}
	}
}

func TestListNavigationAliasesAndBoundaries(t *testing.T) {
	for _, mode := range []string{"worktree_session", "workspace_edit"} {
		for _, pair := range [][2]string{{"j", "k"}, {"down", "up"}, {"ctrl+n", "ctrl+p"}} {
			m := navigationModel(mode)
			for range 50 {
				m = navigationKey(t, m, pair[0])
			}
			if cursor, _ := selectionState(m); cursor != 39 {
				t.Fatalf("%s/%v did not stop at last item", mode, pair)
			}
			for range 50 {
				m = navigationKey(t, m, pair[1])
			}
			if cursor, scroll := selectionState(m); cursor != 0 || scroll != 0 {
				t.Fatalf("%s/%v did not stop at first item", mode, pair)
			}
		}
	}
}

func TestWorkspaceViewportReservesSandboxAndDirtyState(t *testing.T) {
	t.Setenv("TMUX", "")
	m := editorModel()
	m.width, m.height = 100, 15
	m.editor.state.Members[0].Dirty = true
	m.editor.desired.Worktrees[0].Branch = strings.Repeat("long-branch-", 30)
	m.editor.state.Members[0].Branch = m.editor.desired.Worktrees[0].Branch
	assertSelectionFits(t, m, tea.WindowSizeMsg{Width: m.width, Height: m.height})
	for _, text := range []string{"[dirty]", "Sandbox:", "enter review"} {
		if !strings.Contains(m.View(), text) {
			t.Fatalf("missing %s", text)
		}
	}
	m = navigationKey(t, m, "x")
	if len(m.editor.desired.Worktrees) != 1 || m.err == nil {
		t.Fatal("dirty-member removal protection changed")
	}
}

func TestPickerResizeAndNavigationWithTeatest(t *testing.T) {
	t.Setenv("TMUX", "test")
	for _, mode := range []string{"create_repo", "worktree_session", "workspace_edit"} {
		t.Run(mode, func(t *testing.T) {
			m := navigationModel(mode)
			tm := teatest.NewTestModel(t, staticTUIModel{model: m}, teatest.WithInitialTermSize(100, 24))
			for range 25 {
				tm.Send(inspectKey("down"))
			}
			tm.Send(tea.WindowSizeMsg{Width: 80, Height: 20})
			for range 3 {
				tm.Send(inspectKey("up"))
			}
			tm.Send(tea.Quit())
			final := tm.FinalModel(t, teatest.WithFinalTimeout(time.Second)).(staticTUIModel).model
			if cursor, _ := selectionState(final); cursor != 22 || final.mode != mode {
				t.Fatal("event loop lost the selection or escaped the picker")
			}
			assertSelectionFits(t, final, tea.WindowSizeMsg{Width: 80, Height: 20})
		})
	}
}

func TestDashboardPageAliasesDuringOperations(t *testing.T) {
	for _, busy := range []bool{false, true} {
		m := model{width: 100, height: 35, tasks: longTaskListFixture()}
		if busy {
			m.operation = taskOperation{kind: "cleanup", task: m.tasks[0]}
		}
		for _, pair := range [][2]string{{"pgdown", "ctrl+d"}, {"pgup", "ctrl+u"}} {
			m = navigationKey(t, m, "pgdown")
			a := navigationKey(t, m, pair[0])
			b := navigationKey(t, m, pair[1])
			if a.selectedEntry() != b.selectedEntry() || a.scroll != b.scroll || a.selectedEntry() == m.selectedEntry() {
				t.Fatalf("busy=%v: %v are not equivalent page movements", busy, pair)
			}
		}
	}
}
