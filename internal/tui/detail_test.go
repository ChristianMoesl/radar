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

func inspectFixture() model {
	task := protocol.Task{ID: 1, Title: "Inspected task", Attention: "attention", Metadata: map[string]string{}}
	for i := 0; i < 60; i++ {
		task.Metadata[fmt.Sprintf("field-%02d", i)] = fmt.Sprintf("value-%02d", i)
	}
	task.SourceRefs = []protocol.SourceRef{{ID: "git:worktree:/repo/one", Source: "git", Kind: "worktree", Path: "/repo/one"}}
	m := model{width: 80, height: 24, tasks: []protocol.Task{task, {ID: 2, Title: "Other task", Attention: "attention"}}}
	updated, _ := m.Update(inspectKey("i"))
	return updated.(model)
}

func inspectKey(key string) tea.KeyMsg {
	types := map[string]tea.KeyType{
		"down": tea.KeyDown, "up": tea.KeyUp, "right": tea.KeyRight,
		"ctrl+n": tea.KeyCtrlN, "ctrl+p": tea.KeyCtrlP,
		"pgdown": tea.KeyPgDown, "pgup": tea.KeyPgUp,
		"ctrl+d": tea.KeyCtrlD, "ctrl+u": tea.KeyCtrlU,
		"home": tea.KeyHome, "end": tea.KeyEnd, "esc": tea.KeyEsc,
		"backspace": tea.KeyBackspace, "enter": tea.KeyEnter, "ctrl+c": tea.KeyCtrlC,
	}
	if kind, ok := types[key]; ok {
		return tea.KeyMsg{Type: kind}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
}

func TestInspectNavigationOnlyScrollsDetails(t *testing.T) {
	for _, key := range []string{"j", "down", "ctrl+n", "k", "up", "ctrl+p", "pgdown", "ctrl+d", "pgup", "ctrl+u", "home", "g", "end", "G"} {
		t.Run(key, func(t *testing.T) {
			m := inspectFixture()
			m.detail.scroll = 20
			lines, _, _, rows := m.detailViewport()
			limit := len(lines) - rows
			want := 20
			switch key {
			case "j", "down", "ctrl+n":
				want++
			case "k", "up", "ctrl+p":
				want--
			case "pgdown", "ctrl+d":
				want += rows
			case "pgup", "ctrl+u":
				want -= rows
			case "home", "g":
				want = 0
			case "end", "G":
				want = limit
			}
			updated, cmd := m.Update(inspectKey(key))
			got := updated.(model)
			if cmd != nil || got.detail.scroll != max(0, min(want, limit)) {
				t.Fatalf("%s: scroll=%d want=%d, command=%v", key, got.detail.scroll, want, cmd)
			}
			got.detail.scroll = m.detail.scroll
			if !reflect.DeepEqual(got, m) {
				t.Fatalf("%s changed state outside the detail offset", key)
			}
		})
	}
}

func TestInspectNavigationClampsAtBothEdges(t *testing.T) {
	for _, short := range []bool{false, true} {
		m := inspectFixture()
		if short {
			m.detail.task.Metadata = nil
			m.detail.task.SourceRefs = nil
		}
		for _, key := range []string{"end", "j", "pgdown", "home", "k", "pgup"} {
			updated, cmd := m.Update(inspectKey(key))
			m = updated.(model)
			lines, _, _, rows := m.detailViewport()
			want := max(0, len(lines)-rows)
			if key == "home" || key == "k" || key == "pgup" {
				want = 0
			}
			if cmd != nil || m.detail.scroll != want || m.cursor != 0 || m.scroll != 0 {
				t.Fatalf("short=%v key=%s: detail=%d cursor=%d overview=%d", short, key, m.detail.scroll, m.cursor, m.scroll)
			}
		}
	}
}

func TestInspectIsReadOnly(t *testing.T) {
	m := inspectFixture()
	for _, key := range []string{"enter", "n", "d", "p", "c", "w", "f", "o", "x", "X", "r", "i", "right", "h", "l"} {
		updated, cmd := m.Update(inspectKey(key))
		if cmd != nil || !reflect.DeepEqual(updated.(model), m) {
			t.Fatalf("%s triggered an overview action", key)
		}
	}
	for _, key := range []string{"q", "ctrl+c"} {
		_, cmd := m.Update(inspectKey(key))
		if cmd == nil {
			t.Fatalf("%s did not quit", key)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatalf("%s did not return QuitMsg", key)
		}
	}
}

func TestInspectBackPreservesOverviewAndReopeningStartsAtTop(t *testing.T) {
	for _, back := range []string{"esc", "backspace"} {
		for _, open := range []string{"i", "right"} {
			m := model{width: 80, height: 24, tasks: longTaskListFixture(), cursor: 20}
			m.syncTaskScroll()
			cursor, scroll := m.cursor, m.scroll
			for _, key := range []string{open, "end", back} {
				updated, cmd := m.Update(inspectKey(key))
				m = updated.(model)
				if cmd != nil || m.cursor != cursor || m.scroll != scroll {
					t.Fatalf("%s changed the overview position", key)
				}
			}
			if m.mode != "" || !reflect.DeepEqual(m.detail, detailState{}) {
				t.Fatal("back did not leave inspect and clear its state")
			}
			updated, _ := m.Update(inspectKey("j"))
			m = updated.(model)
			if m.cursor == cursor {
				t.Fatal("overview navigation no longer works")
			}
			updated, _ = m.Update(inspectKey(open))
			m = updated.(model)
			if m.detail.scroll != 0 || !reflect.DeepEqual(m.detail.task, m.tasks[m.cursor]) {
				t.Fatal("reopening did not start at the top of the new task")
			}
		}
	}
}

func TestInspectRefreshPinsIdentityAndHandlesDisappearance(t *testing.T) {
	m := inspectFixture()
	m.detail.scroll = 10
	selected, other := m.tasks[0], m.tasks[1]
	selected.Title = "Updated inspected task"
	m.applyResponse(protocol.Response{Tasks: []protocol.Task{other, selected}}, false)
	if m.cursor != 1 || m.detail.task.Title != selected.Title || m.detail.scroll != 10 {
		t.Fatal("refresh lost the inspected task or its offset")
	}
	m.applyResponse(protocol.Response{Tasks: []protocol.Task{other}}, false)
	if m.detail.available || m.detail.scroll != 0 || m.detail.task.ID != selected.ID {
		t.Fatal("missing task was replaced or scroll was not clamped")
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "Task no longer available.") || strings.Contains(view, other.Title) {
		t.Fatalf("missing task silently switched details:\n%s", view)
	}
	m.applyResponse(protocol.Response{Tasks: []protocol.Task{}}, false)
	m.applyResponse(protocol.Response{Tasks: []protocol.Task{other, selected}}, false)
	if !m.detail.available || m.cursor != 1 || m.detail.task.ID != selected.ID {
		t.Fatal("returning task was not restored by identity")
	}
	// Regrouping can replace task IDs; retain the existing source-ref identity rule.
	selected.ID = 3
	m.applyResponse(protocol.Response{Tasks: []protocol.Task{selected, other}}, false)
	if !m.detail.available || m.detail.task.ID != 3 || m.cursor != 0 {
		t.Fatal("source-ref identity was not preserved across regrouping")
	}
	selected.Metadata = nil
	selected.SourceRefs = nil
	m.detail.scroll = 100
	m.applyResponse(protocol.Response{Tasks: []protocol.Task{selected}}, false)
	if m.detail.scroll != 0 {
		t.Fatal("shortened details did not clamp the offset")
	}
}

func TestInspectFitsTerminalAndPreservesLongIdentifiers(t *testing.T) {
	for _, tmux := range []string{"", "test"} {
		t.Run("tmux="+tmux, func(t *testing.T) {
			t.Setenv("TMUX", tmux)
			m := inspectFixture()
			path := "/repo/" + strings.Repeat("very-long-世界-path/", 30)
			url := "https://example.com/" + strings.Repeat("long-segment/", 30)
			title := strings.Repeat("Long 世界 title ", 20)
			m.detail.task.Title = title
			m.detail.task.SourceRefs[0].Path = path
			m.detail.task.URL = url
			for _, size := range []struct{ width, height int }{{32, 20}, {60, 24}, {100, 30}, {200, 40}} {
				m.detail.scroll = 9999
				updated, _ := m.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
				m = updated.(model)
				lines, _, _, rows := m.detailViewport()
				if m.detail.scroll != max(0, len(lines)-rows) {
					t.Fatal("resize did not clamp the stored offset")
				}
				full := strings.Join(strings.Fields(ansi.Strip(strings.Join(lines, "\n"))), "")
				if !strings.Contains(full, path) || !strings.Contains(full, url) || !strings.Contains(full, strings.Join(strings.Fields(title), "")) {
					t.Fatal("wrapping truncated a long title or identifier")
				}
				helpRow := renderedLineIndex(m.View(), "esc/backspace")
				for _, key := range []string{"home", "pgdown", "end", "pgup", "home"} {
					updated, _ := m.Update(inspectKey(key))
					m = updated.(model)
					view := m.View()
					if lipgloss.Width(view) > size.width || lipgloss.Height(view) != size.height {
						t.Fatalf("view %dx%d does not fit %dx%d:\n%s", lipgloss.Width(view), lipgloss.Height(view), size.width, size.height, view)
					}
					plain := ansi.Strip(view)
					if renderedLineIndex(view, "esc/backspace") != helpRow {
						t.Fatal("scroll moved the footer")
					}
					if key == "end" && !strings.Contains(plain, ansi.Strip(lines[len(lines)-1])) {
						t.Fatal("end did not reveal the last detail row")
					}
					if !strings.Contains(plain, "Inspect") || !strings.Contains(plain, "esc/backspace") || !strings.Contains(plain, "quit") {
						t.Fatalf("scroll hid title or footer:\n%s", plain)
					}
				}
			}
		})
	}
}

func TestInspectMetadataOrderIsStable(t *testing.T) {
	m := inspectFixture()
	before := taskDetailView(m.detail.task, 80)
	for i := 0; i < 30; i++ {
		if view := taskDetailView(m.detail.task, 80); view != before {
			t.Fatal("metadata rows changed between renders")
		}
	}
	if strings.Index(before, "field-00") > strings.Index(before, "field-59") {
		t.Fatal("metadata is not sorted")
	}
}

func TestInspectScrollRoundTripWithTeatest(t *testing.T) {
	t.Setenv("TMUX", "test")
	m := inspectFixture()
	tm := teatest.NewTestModel(t, staticTUIModel{model: m}, teatest.WithInitialTermSize(80, 24))
	for _, key := range []string{"j", "pgdown", "end", "enter", "x", "home", "k", "q"} {
		tm.Send(inspectKey(key))
	}
	final := tm.FinalModel(t, teatest.WithFinalTimeout(time.Second)).(staticTUIModel).model
	if final.mode != "detail" || final.cursor != 0 || final.detail.scroll != 0 || final.detail.task.ID != 1 {
		t.Fatalf("interactive navigation escaped inspect: %+v", final.detail)
	}
}

func TestInspectSectionOrderAndAllIssues(t *testing.T) {
	task := resourceBadgeFixture()
	task.Repo = "acme/app"
	task.URL = "https://example.test/task"
	task.Metadata = map[string]string{"context": "task metadata"}
	task.SourceRefs[0].CleanupIssues = []string{"uncommitted changes will be discarded", "branch commits were not found remotely"}
	task.SourceRefs = append(task.SourceRefs,
		protocol.SourceRef{ID: "workspace:extra", Source: "workspace", Kind: "workspace", Path: "/workspaces/extra", ProvidesWorkspace: true, CleanupIssues: []string{"workspace anchor contains unknown files: /workspaces/extra/test.sh"}},
		protocol.SourceRef{ID: "tmux:extra", Source: "tmux", Kind: "session", Path: "/workspaces/extra", InUse: true},
	)
	before := taskDetailView(task, 140)
	view := ansi.Strip(before)
	previous := -1
	for _, label := range []string{"Title", "Status", "Activity", "Repo", "URL", "Metadata", "task metadata", "Unresolved", "Source refs"} {
		position := strings.Index(view, label)
		if position <= previous {
			t.Fatalf("%q is missing or out of order; expected task details, Unresolved, Source refs:\n%s", label, view)
		}
		previous = position
	}
	for _, want := range []string{"git worktree · /repo/one", "uncommitted changes will be discarded", "branch commits were not found remotely", "workspace · /workspaces/extra", "/workspaces/extra/test.sh", "tmux session · /workspaces/extra", "a related local resource is in use", "Source refs"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q:\n%s", want, view)
		}
	}
	// Rendering must not consume or mutate issue snapshots.
	if got := taskDetailView(task, 140); got != before {
		t.Fatal("rendering mutated issues")
	}
	for i := range task.SourceRefs {
		task.SourceRefs[i].CleanupIssues = nil
		task.SourceRefs[i].InUse = false
	}
	if view := taskDetailView(task, 140); strings.Contains(view, "Unresolved") {
		t.Fatalf("empty section should be hidden:\n%s", view)
	}
}

func TestInspectUnresolvedUpdatesWithTheTaskAndWrapsWithoutTruncation(t *testing.T) {
	m := inspectFixture()
	updatedTask := m.detail.task
	updatedTask.Metadata = nil
	updatedTask.SourceRefs = []protocol.SourceRef{{ID: "git:one", Source: "git", Kind: "worktree", Path: "/workspaces/" + strings.Repeat("long-世界-path/", 20), CleanupIssues: []string{"branch publication could not be verified: " + strings.Repeat("long-error-", 30)}}}
	updated, _ := m.Update(watchMsg{response: protocol.Response{Tasks: []protocol.Task{updatedTask}}})
	m = updated.(model)
	if !strings.Contains(m.View(), "Unresolved") {
		t.Fatalf("live issue missing:\n%s", m.View())
	}
	lines, _, _, _ := m.detailViewport()
	full := strings.Join(strings.Fields(ansi.Strip(strings.Join(lines, "\n"))), "")
	for _, text := range []string{updatedTask.SourceRefs[0].Path, updatedTask.SourceRefs[0].CleanupIssues[0]} {
		if !strings.Contains(full, strings.Join(strings.Fields(text), "")) {
			t.Fatalf("truncated %q", text)
		}
	}
	for _, key := range []string{"end", "pgup", "home"} {
		updated, cmd := m.Update(inspectKey(key))
		m = updated.(model)
		if cmd != nil || lipgloss.Width(m.View()) > m.width || lipgloss.Height(m.View()) > m.height {
			t.Fatalf("unresolved section broke viewport:\n%s", m.View())
		}
	}
	updatedTask.SourceRefs[0].CleanupIssues = nil
	updated, _ = m.Update(watchMsg{response: protocol.Response{Tasks: []protocol.Task{updatedTask}}})
	m = updated.(model)
	if strings.Contains(m.View(), "Unresolved") || strings.Contains(taskResourceBadges(m.detail.task), "unresolved") {
		t.Fatalf("cleared issues remain:\n%s", m.View())
	}
}

func TestUnresolvedSectionWithTeatest(t *testing.T) {
	t.Setenv("TMUX", "test")
	m := inspectFixture()
	m.detail.task.Metadata = nil
	m.detail.task.SourceRefs[0].CleanupIssues = []string{"uncommitted changes will be discarded", "branch commits were not found remotely"}
	tm := teatest.NewTestModel(t, staticTUIModel{model: m}, teatest.WithInitialTermSize(80, 24))
	tm.Send(inspectKey("end"))
	tm.Send(inspectKey("home"))
	tm.Send(inspectKey("q"))
	final := tm.FinalModel(t, teatest.WithFinalTimeout(time.Second)).(staticTUIModel).model
	if final.detail.scroll != 0 || !strings.Contains(final.View(), "Unresolved") || !strings.Contains(final.View(), "branch commits were not found remotely") {
		t.Fatalf("issues below task details were not reachable interactively:\n%s", final.View())
	}
}
