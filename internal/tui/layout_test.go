package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"radar/internal/protocol"
)

func TestTaskBlocksHaveOneBlankRowBetweenThem(t *testing.T) {
	m := model{cursor: 1, tasks: []protocol.Task{
		{Title: "First task", Attention: "attention", SourceRefs: []protocol.SourceRef{{ID: "jira:issue:ABC-123"}, {ID: "github:pr:owner/repo:1"}}},
		{Title: "Second task", Attention: "attention", SourceRefs: []protocol.SourceRef{{ID: "jira:issue:ABC-456"}}},
		{Title: "Third task", Attention: "in_progress"},
		{Title: "Fourth task", Attention: "in_progress"},
	}}
	lines, start, end := m.taskLines(100)
	plain := ansi.Strip(strings.Join(lines, "\n"))
	want := "👀 Need attention\n" +
		"    First task\n    ↳ jira:issue:ABC-123\n    ↳ github:pr:owner/repo:1\n\n" +
		"›   Second task\n    ↳ jira:issue:ABC-456\n\n" +
		"⏳ In progress\n    Third task\n\n    Fourth task"
	if plain != want {
		t.Fatalf("task blocks:\n%s\nwant:\n%s", plain, want)
	}
	if start != 5 || end != 6 {
		t.Fatalf("selected block = %d..%d, want 5..6 without the gap", start, end)
	}
	positions, count := m.taskRowPositions()
	if count != len(lines) {
		t.Fatalf("navigation counts %d rows, rendered %d", count, len(lines))
	}
	for i, task := range m.tasks {
		if !strings.Contains(ansi.Strip(lines[positions[i]]), task.Title) {
			t.Fatalf("task %q has wrong navigation position %d", task.Title, positions[i])
		}
	}
}

func TestMainHelpKeepsEveryShortcutWithoutOverflow(t *testing.T) {
	for _, width := range []int{40, 60, 80, 100, 140, 224} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			help := ansi.Strip(mainHelp(width))
			assertNoWideLines(t, help, width)
			for _, group := range mainKeyHints {
				for _, hint := range group {
					if !strings.Contains(help, hint.keys+" "+hint.action) {
						t.Fatalf("help lost %q:\n%s", hint.keys+" "+hint.action, help)
					}
				}
			}
		})
	}
	if got := lipgloss.Height(mainHelp(224)); got != 3 {
		t.Fatalf("wide help has %d rows, want separator and two shortcut rows", got)
	}
}

func allSourceStatusesFixture() []protocol.SourceStatus {
	var sources []protocol.SourceStatus
	for _, name := range []string{"obsidian", "github", "jira", "datadog", "workspace", "git", "tmux", "sbx"} {
		sources = append(sources, protocol.SourceStatus{Name: name, Status: "ok", SourceRefCount: 1})
	}
	return sources
}

func TestDashboardFitsTerminalAndKeepsChromeFixed(t *testing.T) {
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
			m := model{width: tc.width, height: tc.height, tasks: longTaskListFixture(), sources: allSourceStatusesFixture()}
			initial := m.View()
			for _, cursor := range []int{0, 1, 29, 30, 59} {
				m.cursor = cursor
				m.syncTaskScroll()
				view := m.View()
				assertNoWideLines(t, view, tc.width)
				if got := lipgloss.Height(view); got != tc.height {
					t.Fatalf("height = %d, want %d including the outer margins:\n%s", got, tc.height, ansi.Strip(view))
				}
				if !strings.Contains(ansi.Strip(view), "› ") {
					t.Fatalf("selected task disappeared:\n%s", ansi.Strip(view))
				}
				for _, anchor := range []string{"Radar", "Sources", "↑/k/ctrl+p"} {
					if renderedLineIndex(view, anchor) != renderedLineIndex(initial, anchor) {
						t.Fatalf("%s moved while scrolling:\n%s", anchor, ansi.Strip(view))
					}
				}
			}
		})
	}
}

func TestFrameMarginsMatchContentBudget(t *testing.T) {
	t.Setenv("TMUX", "test")
	m := model{width: 100, height: 35}
	view := m.View()
	if column, row := renderedColumnIndex(view, "Radar"), renderedLineIndex(view, "Radar"); column != 4 || row != 2 {
		t.Fatalf("header at column %d/row %d, want 4/2", column, row)
	}
	if m.contentWidth() != 92 || m.frameHeight() != 4 {
		t.Fatalf("content width = %d, frame height = %d", m.contentWidth(), m.frameHeight())
	}
	m.width, m.height = 80, 24
	view = m.View()
	if column, row := renderedColumnIndex(view, "Radar"), renderedLineIndex(view, "Radar"); column != 2 || row != 1 {
		t.Fatalf("compact header at column %d/row %d, want 2/1", column, row)
	}
}

func TestTaskSpacingSurvivesResizeAndPageNavigation(t *testing.T) {
	t.Setenv("TMUX", "test")
	m := model{width: 140, height: 45, tasks: longTaskListFixture(), sources: allSourceStatusesFixture(), cursor: 40}
	for _, size := range []tea.WindowSizeMsg{{Width: 100, Height: 35}, {Width: 140, Height: 45}} {
		updated, _ := m.Update(size)
		m = updated.(model)
		if m.cursor != 40 {
			t.Fatal("resize changed the selected task")
		}
		view := m.View()
		if !strings.Contains(ansi.Strip(view), "›   progress task") {
			t.Fatalf("resize hid selected task:\n%s", view)
		}
		assertNoWideLines(t, view, size.Width)
		if lipgloss.Height(view) > size.Height {
			t.Fatal("resize overflowed the terminal height")
		}
	}
	for range 100 {
		m.moveCursorPage(-1)
	}
	if m.cursor != 0 || m.scroll != 0 {
		t.Fatalf("page-up did not return to top: cursor=%d scroll=%d", m.cursor, m.scroll)
	}
}

func TestCatppuccinStylesAndSelectedTaskHierarchy(t *testing.T) {
	originalProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(originalProfile) })

	if referenceStyle.GetForeground() != mochaOverlay2 || subtleStyle.GetForeground() != mochaSubtext0 || selectedStyle.GetBackground() != mochaSurface0 {
		t.Fatal("reference, metadata, and selection styles do not use the Mocha palette")
	}
	task := resourceBadgeFixture()
	task.Repo = "owner/repo"
	task.Reason = "review requested"
	line := taskLine(task, true, 140)
	for _, segment := range []string{
		selectedStyle.Bold(true).Render(task.Title),
		subtleStyle.Background(mochaSurface0).Render("  " + task.Repo),
		subtleStyle.Background(mochaSurface0).Render("  " + task.Reason),
		progressStyle.Background(mochaSurface0).Render("● busy  "),
		attentionStyle.Render(unresolvedIcon + " unresolved"),
	} {
		if !strings.Contains(line, segment) {
			t.Fatalf("selected task is missing styled segment %q: %q", segment, line)
		}
	}
	task.Activity = protocol.ActivityWaiting
	line = taskLine(task, true, 140)
	if !strings.Contains(line, attentionStyle.Background(mochaSurface0).Render("! waiting  ")) {
		t.Fatalf("selected waiting badge loses warning style/background: %q", line)
	}
	for _, profile := range []termenv.Profile{termenv.ANSI256, termenv.Ascii} {
		lipgloss.SetColorProfile(profile)
		line := taskLine(task, true, 140)
		if strings.Contains(line, "38;2;") || strings.Contains(line, "48;2;") {
			t.Fatal("forced truecolor in a terminal without truecolor support")
		}
		if profile == termenv.Ascii && strings.Contains(line, "\x1b[") {
			t.Fatal("color escapes remain with colors disabled")
		}
	}
}

func TestNotificationPreservesColorsOutsideTheToast(t *testing.T) {
	t.Setenv("TMUX", "test")
	originalProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(originalProfile) })
	m := model{width: 120, height: 35, message: "Saved", tasks: longTaskListFixture()}
	view := m.View()
	if !strings.Contains(view, titleStyle.Render("Radar")) {
		t.Fatalf("toast stripped the header colors: %q", view)
	}
	assertNoWideLines(t, view, m.width)
}

func TestDashboardUsesFullHeightRegardlessOfTaskCount(t *testing.T) {
	for _, tmux := range []bool{true, false} {
		for _, size := range []struct{ width, height int }{{180, 49}, {100, 35}, {80, 28}} {
			for _, withSources := range []bool{true, false} {
				t.Run(fmt.Sprintf("%dx%d/tmux=%v/sources=%v", size.width, size.height, tmux, withSources), func(t *testing.T) {
					t.Setenv("TMUX", "")
					if tmux {
						t.Setenv("TMUX", "test")
					}
					m := model{width: size.width, height: size.height, tasks: longTaskListFixture()}
					if withSources {
						m.sources = allSourceStatusesFixture()
					}
					full := m.View()
					for _, count := range []int{0, 1, 3, len(m.tasks)} {
						for _, loading := range []bool{false, true} {
							short := m
							short.tasks = m.tasks[:count]
							short.loading = loading
							view := short.View()
							if got := lipgloss.Height(view); got != size.height {
								t.Fatalf("count=%d loading=%v: height=%d, want %d:\n%s", count, loading, got, size.height, ansi.Strip(view))
							}
							assertNoWideLines(t, view, size.width)
							for _, anchor := range []string{"Radar", "Sources", "↑/k/ctrl+p"} {
								if renderedLineIndex(view, anchor) != renderedLineIndex(full, anchor) {
									t.Fatalf("count=%d loading=%v: %s moved:\n%s", count, loading, anchor, ansi.Strip(view))
								}
							}
							if count == 0 {
								want := "No tasks need your attention."
								if loading {
									want = "Loading tasks…"
								}
								if !strings.Contains(ansi.Strip(view), want) {
									t.Fatalf("empty/loading state missing %q", want)
								}
							}
						}
					}
				})
			}
		}
	}
}

func TestTallerPopupGivesExtraRowsToTasks(t *testing.T) {
	t.Setenv("TMUX", "test")
	m := model{width: 160, height: 35, tasks: longTaskListFixture(), sources: allSourceStatusesFixture()}
	before := ansi.Strip(m.View())
	beforeRows := m.taskListHeight(m.contentWidth())
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	m = updated.(model)
	after := ansi.Strip(m.View())
	if rows := m.taskListHeight(m.contentWidth()); rows != beforeRows+15 {
		t.Fatalf("task area grew by %d rows, want 15", rows-beforeRows)
	}
	if strings.Count(after, "attention task") <= strings.Count(before, "attention task") {
		t.Fatalf("taller popup did not display more tasks:\n%s", after)
	}
	for _, anchor := range []string{"Sources", "↑/k/ctrl+p"} {
		if renderedLineIndex(after, anchor) != renderedLineIndex(before, anchor)+15 {
			t.Fatalf("%s did not follow the bottom of the resized popup", anchor)
		}
	}
	if lipgloss.Height(after) != 50 {
		t.Fatalf("resized frame height = %d, want 50", lipgloss.Height(after))
	}
}

func TestSourceColumnsAlignAfterTheLongestName(t *testing.T) {
	for _, names := range [][]string{
		{"obsidian", "github", "jira", "datadog", "workspace", "git", "tmux", "sbx"},
		{"git", "workspace", "custom-provider", "日本語"},
	} {
		m := model{}
		nameWidth := 8
		for _, name := range names {
			nameWidth = max(nameWidth, lipgloss.Width(name))
			m.sources = append(m.sources, protocol.SourceStatus{Name: name, Status: "ok", SourceRefCount: 1, Detail: "source details"})
		}
		lines := strings.Split(m.sourceList(100), "\n")[1:]
		for _, line := range lines {
			if column := renderedColumnIndex(line, "ok"); column != 2+nameWidth+1 {
				t.Fatalf("status column = %d, want %d: %q", column, 2+nameWidth+1, ansi.Strip(line))
			}
			for _, anchor := range []string{"1 refs", "source details"} {
				if column := renderedColumnIndex(line, anchor); column != renderedColumnIndex(lines[0], anchor) {
					t.Fatalf("%q columns do not align: %q", anchor, ansi.Strip(line))
				}
			}
		}
		assertNoWideLines(t, m.sourceList(40), 40)
	}
}
