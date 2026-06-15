package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest"

	"radar.nvim/internal/protocol"
)

type staticTUIModel struct {
	model
}

func (m staticTUIModel) Init() tea.Cmd { return nil }

func (m staticTUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	updated, cmd := m.model.Update(msg)
	if next, ok := updated.(model); ok {
		m.model = next
		return m, cmd
	}
	return updated, cmd
}

func (m staticTUIModel) View() string { return m.model.View() }

func TestCategoryHeadersKeepOrderWhileScrollingWithTeatest(t *testing.T) {
	t.Setenv("TMUX", "test")
	view := renderedViewAfterKeys(t, repeatRunes('j', 8))
	attentionLine := renderedLineIndex(view, "Need attention")
	progressLine := renderedLineIndex(view, "In progress")
	if attentionLine >= 0 && progressLine >= 0 && progressLine < attentionLine {
		t.Fatalf("category headers rendered out of order: attention=%d progress=%d\n%s", attentionLine, progressLine, view)
	}
}

func TestCounterHeaderStaysFixedWhileScrollingWithTeatest(t *testing.T) {
	t.Setenv("TMUX", "test")
	before := staticTUIModel{model: model{width: 100, height: 35, tasks: longTaskListFixture()}}.View()
	beforeLine := renderedLineIndex(before, "Radar")
	beforeColumn := renderedColumnIndex(before, "Radar")
	if beforeLine < 0 || beforeColumn < 0 {
		t.Fatalf("initial view missing counter header:\n%s", before)
	}

	cases := []struct {
		name string
		keys []rune
	}{
		{name: "part way down", keys: repeatRunes('j', 8)},
		{name: "bottom", keys: repeatRunes('j', 80)},
		{name: "back to top", keys: append(repeatRunes('j', 80), repeatRunes('k', 80)...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			view := renderedViewAfterKeys(t, tc.keys)
			line := renderedLineIndex(view, "Radar")
			column := renderedColumnIndex(view, "Radar")
			if line != beforeLine || column != beforeColumn {
				t.Fatalf("counter header moved from line %d/column %d to line %d/column %d:\n%s", beforeLine, beforeColumn, line, column, view)
			}
		})
	}
}

func TestLongTaskListScrollRoundTripWithTeatest(t *testing.T) {
	t.Setenv("TMUX", "test")
	m := staticTUIModel{model: model{width: 100, height: 35, tasks: longTaskListFixture()}}
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 35))

	for i := 0; i < 80; i++ {
		tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	}
	for i := 0; i < 80; i++ {
		tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	}
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})

	final := tm.FinalModel(t, teatest.WithFinalTimeout(time.Second)).(staticTUIModel).model
	if final.cursor != 0 {
		t.Fatalf("cursor after scroll round trip = %d, want 0", final.cursor)
	}
	view := final.View()
	for _, want := range []string{"Radar", "Need attention", "attention task"} {
		if !strings.Contains(view, want) {
			t.Fatalf("final view missing %q:\n%s", want, view)
		}
	}
	assertNoWideLines(t, view, final.contentWidth())
}

func renderedViewAfterKeys(t *testing.T, keys []rune) string {
	t.Helper()
	m := staticTUIModel{model: model{width: 100, height: 35, tasks: longTaskListFixture()}}
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 35))
	for _, key := range keys {
		tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
	}
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	return tm.FinalModel(t, teatest.WithFinalTimeout(time.Second)).(staticTUIModel).model.View()
}

func repeatRunes(r rune, count int) []rune {
	out := make([]rune, count)
	for i := range out {
		out[i] = r
	}
	return out
}

func longTaskListFixture() []protocol.Task {
	tasks := make([]protocol.Task, 0, 60)
	for i := 0; i < 30; i++ {
		tasks = append(tasks, protocol.Task{
			Title:     "attention task with a very very very long title that should not wrap",
			Repo:      "redbullmediahouse/rb3ca-experience-center",
			Reason:    "2 unresolved review thread(s), 1 new PR comment(s)",
			Attention: "attention",
			SourceRefs: []protocol.SourceRef{{
				ID:     "git:worktree:/very/very/very/very/very/very/very/long/path/that/would/wrap",
				Source: "git",
				Kind:   "worktree",
				Path:   "/very/very/very/very/very/very/very/long/path/that/would/wrap",
			}},
		})
	}
	for i := 0; i < 30; i++ {
		tasks = append(tasks, protocol.Task{
			Title:     "progress task",
			Attention: "in_progress",
			SourceRefs: []protocol.SourceRef{{
				ID:     "git:worktree:/repo/progress",
				Source: "git",
				Kind:   "worktree",
				Path:   "/repo/progress",
			}},
		})
	}
	return tasks
}

func renderedLineIndex(view string, needle string) int {
	for i, line := range strings.Split(view, "\n") {
		if strings.Contains(ansi.Strip(line), needle) {
			return i
		}
	}
	return -1
}

func renderedColumnIndex(view string, needle string) int {
	for _, line := range strings.Split(view, "\n") {
		stripped := ansi.Strip(line)
		if index := strings.Index(stripped, needle); index >= 0 {
			return lipgloss.Width(stripped[:index])
		}
	}
	return -1
}

func assertNoWideLines(t *testing.T, view string, width int) {
	t.Helper()
	for _, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(ansi.Strip(line)); got > width {
			t.Fatalf("line width = %d, want <= %d for %q:\n%s", got, width, ansi.Strip(line), view)
		}
	}
}
