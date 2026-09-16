package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"radar/internal/protocol"
)

func TestTaskLinksIncludesEveryURLAndAction(t *testing.T) {
	for _, kind := range []string{"urls", "actions", "mixed"} {
		t.Run(kind, func(t *testing.T) {
			task := protocol.Task{Title: "Task", URL: "https://example.test/task"}
			for i := 0; i < 45; i++ {
				ref := protocol.SourceRef{Source: "link", Title: fmt.Sprintf("Entry %02d", i), URL: fmt.Sprintf("https://example.test/%d", i)}
				if kind == "actions" || kind == "mixed" && i%2 == 0 {
					ref.Source, ref.Kind = "obsidian", "task"
					ref.URL = fmt.Sprintf("obsidian://open?vault=Test&file=Task%d.md", i)
				}
				task.SourceRefs = append(task.SourceRefs, ref)
			}
			// Duplicate URLs, including action-owned URLs, must stay deduplicated.
			task.SourceRefs = append(task.SourceRefs, protocol.SourceRef{Source: "link", URL: task.SourceRefs[0].URL})
			links := taskLinks(task)
			if len(links) != 46 {
				t.Fatalf("got %d entries, want all 45 refs plus task URL", len(links))
			}
			for i, ref := range task.SourceRefs[:45] {
				if links[i].Label != ref.Title {
					t.Fatalf("entry %d = %+v, want title %q in source order", i, links[i], ref.Title)
				}
				if ref.Source == "obsidian" {
					if links[i].Action != "obsidian_open" || links[i].Ref.URL != ref.URL {
						t.Fatalf("entry %d lost its source action: %+v", i, links[i])
					}
				} else if links[i].URL != ref.URL {
					t.Fatalf("entry %d lost its URL: %+v", i, links[i])
				}
			}
			if links[45].URL != task.URL {
				t.Fatalf("last entry = %+v, want task URL", links[45])
			}
		})
	}
}

func TestTaskLinksAllocatesUnusedSingleKeysThenLeavesBlanks(t *testing.T) {
	for _, source := range []string{"GitHub", "jkq", "9"} {
		t.Run(source, func(t *testing.T) {
			task := protocol.Task{}
			for i := 0; i < 40; i++ {
				task.SourceRefs = append(task.SourceRefs, protocol.SourceRef{Source: source, URL: fmt.Sprintf("https://example.test/%d", i)})
			}
			links := taskLinks(task)
			seen := map[string]bool{"j": true, "k": true, "q": true}
			var digits string
			for i, link := range links {
				if i >= 33 { // 26 letters minus j/k/q, plus ten digits.
					if link.Key != "" {
						t.Fatalf("entry %d has exhausted shortcut %q", i, link.Key)
					}
					continue
				}
				if len(link.Key) != 1 || seen[link.Key] {
					t.Fatalf("entry %d has invalid, duplicate, or reserved key %q", i, link.Key)
				}
				seen[link.Key] = true
				if strings.Contains("0123456789", link.Key) {
					digits += link.Key
				}
				if got, ok := matchingLink(links, link.Key); !ok || got.URL != link.URL {
					t.Fatalf("shortcut %q does not select entry %d", link.Key, i)
				}
			}
			wantDigits := "1234567890"
			if source == "9" {
				wantDigits = "9123456780"
			}
			if digits != wantDigits {
				t.Fatalf("digits = %q, want %q", digits, wantDigits)
			}
			if _, ok := matchingLink(links, ""); ok {
				t.Fatal("blank shortcuts must never match")
			}
		})
	}
}

func openLinkTestModel() model {
	m := model{mode: "open_link", width: 100, height: 24, tasks: []protocol.Task{{Title: "Task"}}}
	for i := 0; i < 40; i++ {
		m.links = append(m.links, linkChoice{Source: "Test", Label: fmt.Sprintf("Entry %02d", i), Detail: fmt.Sprintf("detail-%02d", i), Action: fmt.Sprintf("test-action-%02d", i)})
	}
	return m
}

func TestOpenLinkNavigationScrollsThroughEntriesWithoutShortcuts(t *testing.T) {
	t.Setenv("TMUX", "")
	for _, keys := range [][2]tea.KeyMsg{
		{{Type: tea.KeyRunes, Runes: []rune{'j'}}, {Type: tea.KeyRunes, Runes: []rune{'k'}}},
		{{Type: tea.KeyDown}, {Type: tea.KeyUp}},
		{{Type: tea.KeyCtrlN}, {Type: tea.KeyCtrlP}},
	} {
		t.Run(keys[0].String(), func(t *testing.T) {
			m := openLinkTestModel()
			for i := 0; i < 45; i++ {
				updated, cmd := m.Update(keys[0])
				m = updated.(model)
				if cmd != nil || m.mode != "open_link" || m.cursor != 0 {
					t.Fatalf("navigation opened an entry or moved the task cursor: %+v", m)
				}
				view := ansi.Strip(m.View())
				if !strings.Contains(view, fmt.Sprintf("Entry %02d", m.linkCursor)) || !strings.Contains(view, fmt.Sprintf("detail-%02d", m.linkCursor)) {
					t.Fatalf("selected entry %d is not visible:\n%s", m.linkCursor, view)
				}
				if lipgloss.Height(view) > m.height {
					t.Fatalf("view exceeds terminal height: %d > %d", lipgloss.Height(view), m.height)
				}
			}
			if m.linkCursor != 39 || m.linkScroll == 0 || !strings.Contains(m.View(), "↑ more") || strings.Contains(m.View(), "↓ more") {
				t.Fatalf("last entry not reached: cursor=%d scroll=%d\n%s", m.linkCursor, m.linkScroll, m.View())
			}
			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			got := updated.(model)
			if cmd == nil || !got.loading || got.mode != "" || got.links != nil || got.linkCursor != 0 || got.linkScroll != 0 {
				t.Fatalf("enter did not open and clear selection: %+v", got)
			}
			// Unknown actions return without opening anything, letting us verify the
			// selected target through the real dispatch path without external effects.
			result := cmd().(actionMsg)
			if result.err == nil || !strings.Contains(result.err.Error(), "test-action-39") {
				t.Fatalf("enter dispatched the wrong target: %+v", result)
			}
			for i := 0; i < 45; i++ {
				updated, _ := m.Update(keys[1])
				m = updated.(model)
			}
			if m.linkCursor != 0 || m.linkScroll != 0 || strings.Contains(m.View(), "↑ more") || !strings.Contains(m.View(), "↓ more") {
				t.Fatalf("first entry not reached: cursor=%d scroll=%d", m.linkCursor, m.linkScroll)
			}
		})
	}
}

func TestOpenLinkResizeKeepsSelectionVisible(t *testing.T) {
	t.Setenv("TMUX", "")
	m := openLinkTestModel()
	m.linkCursor = 25
	m.links[25].Detail = strings.Repeat("long URL ", 40) + "\nsecond line"
	m.links[25].Label += "\n" + strings.Repeat("long label ", 40)
	for _, size := range []tea.WindowSizeMsg{{Width: 100, Height: 24}, {Width: 50, Height: 18}, {Width: 140, Height: 40}, {Width: 80, Height: 16}} {
		updated, _ := m.Update(size)
		m = updated.(model)
		view := ansi.Strip(m.View())
		if m.linkCursor != 25 || !strings.Contains(view, "Entry 25") || !strings.Contains(view, "long URL") {
			t.Fatalf("resize lost selection:\n%s", view)
		}
		if lipgloss.Height(view) > size.Height || lipgloss.Width(view) > size.Width {
			t.Fatalf("view %dx%d exceeds terminal %dx%d:\n%s", lipgloss.Width(view), lipgloss.Height(view), size.Width, size.Height, view)
		}
		if !strings.Contains(view, "↑ more") || !strings.Contains(view, "↓ more") {
			t.Fatalf("missing scroll indicators:\n%s", view)
		}
	}
}

func TestOpenLinkCancelAndReopenResetSelection(t *testing.T) {
	for _, key := range []tea.KeyType{tea.KeyEsc, tea.KeyBackspace} {
		m := openLinkTestModel()
		m.tasks[0].URL = "https://example.test/task"
		m.linkCursor, m.linkScroll = 20, 30
		updated, cmd := m.Update(tea.KeyMsg{Type: key})
		m = updated.(model)
		if cmd != nil || m.mode != "" || m.links != nil || m.linkCursor != 0 || m.linkScroll != 0 {
			t.Fatalf("cancel did not reset selection: %+v", m)
		}
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
		m = updated.(model)
		if m.mode != "open_link" || len(m.links) != 1 || m.linkCursor != 0 || m.linkScroll != 0 {
			t.Fatalf("reopen did not reset selection: %+v", m)
		}
	}
}

func TestOpenLinkEmptyListDoesNotActivate(t *testing.T) {
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyDown}, {Type: tea.KeyUp}} {
		updated, cmd := (model{mode: "open_link"}).Update(key)
		m := updated.(model)
		if cmd != nil || m.mode != "open_link" || m.linkCursor != 0 || m.linkScroll != 0 {
			t.Fatalf("empty list key %s changed state: %+v", key, m)
		}
	}
}

func TestOpenLinkRendersBlankShortcutAndOptionalDetail(t *testing.T) {
	m := model{links: []linkChoice{
		{Key: "a", Source: "Test", Label: "First"},
		{Source: "Test", Label: "No shortcut", Detail: "Last detail"},
	}, linkCursor: 1}
	lines, start, end := m.openLinkLines(60)
	if len(lines) != 3 || start != 1 || end != 2 {
		t.Fatalf("rows=%d selection=%d..%d, want 3 rows and 1..2", len(lines), start, end)
	}
	if got := ansi.Strip(lines[1]); !strings.HasPrefix(got, "›    Test") || !strings.Contains(got, "No shortcut") {
		t.Fatalf("missing entry or shortcut column is not blank: %q", got)
	}
	if view := m.openLinkView(60); !strings.Contains(view, "First") || !strings.Contains(view, "No shortcut") || !strings.Contains(view, "Last detail") {
		t.Fatalf("unbounded view omits entries: %s", view)
	}
}

func TestOpenLinkShortcutOpensOffscreenEntry(t *testing.T) {
	m := openLinkTestModel()
	m.links[0].Key = "a"
	m.linkCursor = 39
	m.syncLinkScroll()
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	got := updated.(model)
	if cmd == nil || !got.loading || got.mode != "" || got.links != nil || got.linkCursor != 0 || got.linkScroll != 0 {
		t.Fatalf("shortcut did not open and reset selection: %+v", got)
	}
	result := cmd().(actionMsg)
	if result.err == nil || !strings.Contains(result.err.Error(), "test-action-00") {
		t.Fatalf("shortcut dispatched the wrong target: %+v", result)
	}
}

func TestOpenLinkQuitKeysStillQuit(t *testing.T) {
	for _, key := range []tea.KeyMsg{{Type: tea.KeyRunes, Runes: []rune{'q'}}, {Type: tea.KeyCtrlC}} {
		_, cmd := openLinkTestModel().Update(key)
		if cmd == nil {
			t.Fatalf("%s did not quit", key)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatalf("%s did not return QuitMsg", key)
		}
	}
}
