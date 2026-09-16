package tui

import (
	"errors"
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

func garbageCollectionFixture() protocol.GarbageCollectionResult {
	return protocol.GarbageCollectionResult{
		Deleted: []protocol.GarbageCollectionItem{{TaskID: 1, Path: "/workspaces/finished"}},
		Skipped: []protocol.GarbageCollectionItem{
			{TaskID: 2, Path: "/workspaces/local-edits", Reason: "uncommitted changes will be discarded"},
			{TaskID: 3, Path: "/workspaces/unknown-files", Reason: "workspace anchor contains unknown files: /workspaces/unknown-files/test.sh"},
			{TaskID: 4, Path: "/workspaces/branch", Reason: "branch commits were not found remotely"},
			{TaskID: 5, Path: "/workspaces/offline", Reason: "branch publication could not be verified"},
			{TaskID: 6, Path: "/workspaces/attached", Reason: "a related local resource is in use"},
			{TaskID: 7, Path: "/outside/workspace", Reason: "workspace is outside configured workspace root"},
		},
	}
}

func TestGarbageCollectionResponseOpensResultAndPreservesLiveUpdates(t *testing.T) {
	result := garbageCollectionFixture()
	m := model{loading: true, gcScroll: 99, message: "Garbage collecting…"}
	updated, cmd := m.Update(actionMsg{response: &protocol.Response{
		OK: true, Revision: 10, GarbageCollectionResult: &result,
		Tasks: []protocol.Task{{ID: 20, Title: "Remaining task", Attention: "in_progress"}},
	}, message: garbageCollectionMessage(result)})
	m = updated.(model)
	if cmd != nil || m.loading || m.mode != "gc_result" || m.gcScroll != 0 || m.message != "" || m.revision != 10 || len(m.tasks) != 1 {
		t.Fatalf("unexpected result state: %+v", m)
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{"Garbage collection", "Workspaces: 1 deleted, 6 skipped", "SKIPPED", "DELETED", "Task #7", "[Enter/Esc] Back"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q:\n%s", want, view)
		}
	}
	for _, item := range result.Skipped {
		if !strings.Contains(view, item.Path) || !strings.Contains(view, item.Reason) {
			t.Fatalf("skipped workspace or reason missing: %+v\n%s", item, view)
		}
	}
	if strings.Index(view, "SKIPPED") > strings.Index(view, "DELETED") {
		t.Fatal("skipped workspaces should appear first")
	}
	updated, _ = m.Update(watchMsg{response: protocol.Response{OK: true, Revision: 11, Tasks: []protocol.Task{}}})
	m = updated.(model)
	if len(m.tasks) != 0 || m.mode != "gc_result" || !reflect.DeepEqual(m.gcResult, result) {
		t.Fatalf("live refresh lost the run's result: %+v", m)
	}
}

func TestGarbageCollectionEmptyAndDeletedOnlyResults(t *testing.T) {
	for _, result := range []protocol.GarbageCollectionResult{{}, {Deleted: []protocol.GarbageCollectionItem{{Path: "/workspaces/finished", TaskID: 8}}}} {
		updated, _ := (model{}).Update(actionMsg{response: &protocol.Response{OK: true, GarbageCollectionResult: &result}})
		m := updated.(model)
		view := ansi.Strip(m.View())
		if m.mode != "gc_result" || strings.Contains(view, "SKIPPED") {
			t.Fatalf("unexpected successful result: %s", view)
		}
		want := "No workspaces eligible for garbage collection."
		if len(result.Deleted) > 0 {
			want = "/workspaces/finished"
		}
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q: %s", want, view)
		}
	}
}

func TestGarbageCollectionFailureDoesNotOpenResult(t *testing.T) {
	updated, _ := (model{loading: true}).Update(actionMsg{err: errors.New("collection failed")})
	m := updated.(model)
	if m.mode != "" || m.loading || m.err == nil || !strings.Contains(m.View(), "collection failed") {
		t.Fatalf("failure was not shown: %+v", m)
	}
}

func TestGarbageCollectionResultKeysAreReadOnlyAndModal(t *testing.T) {
	m := model{mode: "gc_result", gcResult: garbageCollectionFixture()}
	for _, r := range "xXydprcoiw" {
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		if cmd != nil || !reflect.DeepEqual(updated.(model), m) {
			t.Fatalf("%c changed the result or triggered an action", r)
		}
	}
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEsc}, {Type: tea.KeyEnter}, {Type: tea.KeyBackspace}} {
		updated, cmd := m.Update(key)
		got := updated.(model)
		if cmd != nil || got.mode != "" || got.loading || got.gcScroll != 0 || !reflect.DeepEqual(got.gcResult, protocol.GarbageCollectionResult{}) {
			t.Fatalf("%s did not return safely: %+v", key.String(), got)
		}
	}
	for _, key := range []tea.KeyMsg{{Type: tea.KeyCtrlC}, {Type: tea.KeyRunes, Runes: []rune{'q'}}} {
		_, cmd := m.Update(key)
		if cmd == nil {
			t.Fatalf("%s did not quit", key.String())
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatalf("%s did not return QuitMsg", key.String())
		}
	}
}

func TestGarbageCollectionResultWrapsAndScrollsAllDetails(t *testing.T) {
	for _, tmux := range []string{"", "test"} {
		t.Run("tmux="+tmux, func(t *testing.T) {
			t.Setenv("TMUX", tmux)
			for _, size := range []struct{ width, height int }{{32, 20}, {60, 24}, {100, 30}, {200, 40}} {
				m := model{mode: "gc_result", width: size.width, height: size.height, gcResult: garbageCollectionFixture()}
				m.gcResult.Skipped[0].Path += strings.Repeat("/世界-long-path", 30)
				m.gcResult.Skipped[0].Reason += ": " + strings.Repeat("long-error-without-spaces-", 20)
				full := strings.Join(strings.Fields(ansi.Strip(strings.Join(m.garbageCollectionLines(m.contentWidth()), "\n"))), "")
				for _, item := range m.gcResult.Skipped {
					for _, value := range []string{item.Path, item.Reason} {
						if !strings.Contains(full, strings.Join(strings.Fields(value), "")) {
							t.Fatalf("result truncated %q", value)
						}
					}
				}
				for _, key := range []tea.KeyType{tea.KeyHome, tea.KeyPgDown, tea.KeyEnd, tea.KeyPgUp, tea.KeyHome} {
					updated, cmd := m.Update(tea.KeyMsg{Type: key})
					m = updated.(model)
					view := m.View()
					if cmd != nil || lipgloss.Width(view) > size.width || lipgloss.Height(view) > size.height {
						t.Fatalf("view %dx%d exceeds %dx%d:\n%s", lipgloss.Width(view), lipgloss.Height(view), size.width, size.height, view)
					}
					if !strings.Contains(view, "Garbage collection") || !strings.Contains(view, "[Enter/Esc] Back") {
						t.Fatalf("scroll hid header/footer:\n%s", view)
					}
					if key == tea.KeyEnd && !strings.Contains(view, "Task #1") {
						t.Fatalf("end did not reach final workspace:\n%s", view)
					}
				}
				if m.gcScroll != 0 {
					t.Fatal("home did not reset scroll")
				}
				m.gcScroll = 9999
				updated, _ := m.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height + 2})
				if view := updated.(model).View(); !strings.Contains(view, "Task #1") {
					t.Fatalf("resize did not clamp viewport:\n%s", view)
				}
			}
		})
	}
}

func TestGarbageCollectionPathsAreHomeRelative(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	m := model{gcResult: protocol.GarbageCollectionResult{Skipped: []protocol.GarbageCollectionItem{{Path: "/home/test/radar/feature", TaskID: 42, Reason: "a related local resource is in use"}}}}
	if view := strings.Join(m.garbageCollectionLines(100), "\n"); !strings.Contains(view, "~/radar/feature") {
		t.Fatalf("missing home-relative path: %s", view)
	}
}

func TestGarbageCollectionResultWithTeatest(t *testing.T) {
	t.Setenv("TMUX", "test")
	m := staticTUIModel{model: model{width: 70, height: 24}}
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(70, 24))
	result := garbageCollectionFixture()
	for i := 0; i < 20; i++ {
		result.Skipped = append(result.Skipped, protocol.GarbageCollectionItem{Path: fmt.Sprintf("/workspaces/feature-%d", i), Reason: "a related local resource is in use"})
	}
	tm.Send(actionMsg{response: &protocol.Response{OK: true, GarbageCollectionResult: &result}})
	tm.Send(tea.KeyMsg{Type: tea.KeyEnd})
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	final := tm.FinalModel(t, teatest.WithFinalTimeout(time.Second)).(staticTUIModel).model
	if final.mode != "gc_result" || final.gcScroll == 0 || !strings.Contains(final.View(), "/workspaces/finished") {
		t.Fatalf("results were not reachable interactively:\n%s", final.View())
	}
}
