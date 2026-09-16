package tui

import (
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

func cleanupFixture() protocol.CleanupPreview {
	return protocol.CleanupPreview{TaskID: 7, TaskTitle: "ABC-123 · Inspector intermittently fails", Targets: []protocol.CleanupTarget{
		{Source: "tmux", Kind: "session", ResourceID: "$11", Description: "tmux session $11",
			Presentation: protocol.CleanupPresentation{Singular: "terminal session", Plural: "terminal sessions"}},
		{Source: "sbx", Kind: "sandbox", ResourceID: "small-fix-12345678", Description: "SBX sandbox small-fix-12345678",
			Presentation: protocol.CleanupPresentation{Singular: "sandbox", Plural: "sandboxes"}},
		{Source: "git", Kind: "worktree", Path: "/repo/worktrees/inspector--small-fix", Branch: "small-fix",
			Description:  "worktree /repo/worktrees/inspector--small-fix (deletes local branch small-fix)",
			Presentation: protocol.CleanupPresentation{Singular: "worktree", Plural: "worktrees", Label: "inspector", Detail: "small-fix"},
			Safety: []protocol.CleanupSafety{
				{Kind: "deletes_local_data", Summary: "deletes local branch", Message: "deletes local branch small-fix"},
				{Kind: "unpublished_data", Message: "local branch has commits not verified as published or merged", BlocksAutomatic: true},
			}},
		{Source: "git", Kind: "worktree", Path: "/repo/worktrees/frontend--main", Branch: "main",
			Presentation: protocol.CleanupPresentation{Singular: "worktree", Plural: "worktrees", Label: "frontend", Detail: "main (branch kept)"}},
		{Source: "workspace", Kind: "workspace", Path: "/repo/worktrees", Description: "workspace anchor /repo/worktrees",
			Presentation: protocol.CleanupPresentation{Singular: "workspace directory", Plural: "workspace directories"}},
	}}
}

func TestCleanupSummaryEmphasizesConsequencesNotPaths(t *testing.T) {
	m := model{mode: "cleanup_confirm", cleanup: cleanupFixture()}
	view := ansi.Strip(m.View())
	for _, want := range []string{
		"Clean up local resources?", "ABC-123 · Inspector intermittently fails",
		"⚠ Local branch has commits not verified as published or merged.", "REMOVE", "2 worktrees",
		"inspector · small-fix", "deletes local branch · ⚠ remote backup unverified",
		"frontend · main (branch kept)", "1 terminal session", "1 sandbox", "1 workspace directory",
		"KEEP", "Remote resources remain unchanged.", "[d] Show full paths and branch names",
		"[y] Clean up", "[Esc/n] Cancel", "[q] Quit",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q:\n%s", want, view)
		}
	}
	for _, unwanted := range []string{"/repo/worktrees", "small-fix-12345678", "$11", "Press y", " urgent", " attention", " progress", " done"} {
		if strings.Contains(view, unwanted) {
			t.Fatalf("unexpected %q in summary:\n%s", unwanted, view)
		}
	}
	if strings.Index(view, "⚠ Branch") > strings.Index(view, "REMOVE") || strings.Count(view, "[y] Clean up") != 1 {
		t.Fatalf("warning must lead and action must appear once:\n%s", view)
	}
}

func TestCleanupDetailsTogglePreservesPreviewAndWarnings(t *testing.T) {
	m := model{mode: "cleanup_confirm", cleanup: cleanupFixture()}
	before := m.cleanup
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m = updated.(model)
	if cmd != nil || !m.cleanupDetails || m.mode != "cleanup_confirm" || !reflect.DeepEqual(before, m.cleanup) {
		t.Fatalf("details toggle mutated the cleanup plan or started an action: %+v", m)
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{"/repo/worktrees/inspector--small-fix", "deletes local branch small-fix", "small-fix-12345678", "$11", "Branch: main", "⚠ Local branch has commits not verified as published or merged.", "[d] Hide details"} {
		if !strings.Contains(view, want) {
			t.Fatalf("details missing %q:\n%s", want, view)
		}
	}
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m = updated.(model)
	if cmd != nil || m.cleanupDetails || strings.Contains(m.View(), "/repo/worktrees") {
		t.Fatalf("second toggle did not restore compact view:\n%s", m.View())
	}
}

func TestCleanupWarningsAreDeduplicatedAndUnknownRisksRemainVisible(t *testing.T) {
	m := model{mode: "cleanup_confirm", cleanup: cleanupFixture()}
	m.cleanup.Targets[3].Safety = []protocol.CleanupSafety{
		{Kind: "unpublished_data", Message: "local branch has commits not verified as published or merged", BlocksAutomatic: true},
		{Kind: "local_changes", Message: "uncommitted changes will be discarded", BlocksAutomatic: true},
		{Kind: "safety_check_unavailable", Message: "branch publication or merge could not be verified", BlocksAutomatic: true},
		{Kind: "future_risk", Message: "a custom safety warning", BlocksAutomatic: true},
		{Kind: "future_effect", Message: "custom local data will be removed"},
	}
	for _, details := range []bool{false, true} {
		m.cleanupDetails = details
		view := ansi.Strip(m.View())
		for _, warning := range []string{"⚠ Local branch has commits not verified as published or merged.", "⚠ Uncommitted changes will be discarded.", "⚠ Branch publication or merge could not be verified.", "⚠ A custom safety warning."} {
			if strings.Count(view, warning) != 1 || strings.Index(view, warning) > strings.Index(view, "REMOVE") {
				t.Fatalf("warning %q must occur once before REMOVE:\n%s", warning, view)
			}
		}
		if !strings.Contains(strings.ToLower(view), "custom local data will be removed") {
			t.Fatalf("nonblocking effect was hidden:\n%s", view)
		}
	}
}

func TestCleanupKeysAreModal(t *testing.T) {
	m := model{mode: "cleanup_confirm", cleanup: cleanupFixture(), tasks: []protocol.Task{{Title: "Other task"}}}
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEnter}, {Type: tea.KeyRunes, Runes: []rune{'x'}},
		{Type: tea.KeyRunes, Runes: []rune{'X'}}, {Type: tea.KeyRunes, Runes: []rune{'c'}},
		{Type: tea.KeyRunes, Runes: []rune{'r'}}, {Type: tea.KeyRunes, Runes: []rune{'p'}},
		{Type: tea.KeyRunes, Runes: []rune{'o'}}, {Type: tea.KeyRunes, Runes: []rune{'i'}},
	} {
		updated, cmd := m.Update(key)
		if cmd != nil || !reflect.DeepEqual(updated.(model), m) {
			t.Fatalf("%s escaped confirmation or triggered an unrelated action", key.String())
		}
	}
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEsc}, {Type: tea.KeyBackspace}, {Type: tea.KeyRunes, Runes: []rune{'n'}}, {Type: tea.KeyRunes, Runes: []rune{'N'}}} {
		updated, cmd := m.Update(key)
		if cmd != nil || updated.(model).mode != "" || updated.(model).loading {
			t.Fatalf("%s did not cancel safely", key.String())
		}
	}
	for _, key := range []rune{'y', 'Y'} {
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
		got := updated.(model)
		if cmd == nil || got.mode != "" || !got.loading || got.message != "Cleaning up…" || !reflect.DeepEqual(got.cleanup, m.cleanup) {
			t.Fatalf("%c did not submit the unchanged preview", key)
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

func TestCleanupPreviewResetsDetailsAndScroll(t *testing.T) {
	m := model{cleanupDetails: true, cleanupScroll: 99}
	updated, _ := m.Update(cleanupPreviewMsg{preview: cleanupFixture()})
	got := updated.(model)
	if got.cleanupDetails || got.cleanupScroll != 0 || got.mode != "cleanup_confirm" {
		t.Fatalf("new preview should start compact at the top: %+v", got)
	}
}

func TestEmptyCleanupCannotBeConfirmed(t *testing.T) {
	m := model{mode: "cleanup_confirm"}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd != nil || updated.(model).loading || strings.Contains(m.View(), "[y]") || !strings.Contains(m.View(), "No local resources") {
		t.Fatalf("empty preview allowed cleanup:\n%s", m.View())
	}
}

func TestCleanupSummaryGroupsAnyProviderAndPluralizes(t *testing.T) {
	m := model{mode: "cleanup_confirm", cleanup: protocol.CleanupPreview{Targets: []protocol.CleanupTarget{
		{Source: "custom", Presentation: protocol.CleanupPresentation{Singular: "local volume", Plural: "local volumes"}},
		{Source: "custom", Presentation: protocol.CleanupPresentation{Singular: "local volume", Plural: "local volumes"}},
	}}}
	if view := m.View(); !strings.Contains(view, "2 local volumes") || strings.Contains(view, "worktree") {
		t.Fatalf("provider-neutral summary incorrect:\n%s", view)
	}
	m.cleanup.Targets = m.cleanup.Targets[:1]
	if view := m.View(); !strings.Contains(view, "1 local volume") || strings.Contains(view, "1 local volumes") {
		t.Fatalf("singular summary incorrect:\n%s", view)
	}
}

func TestCleanupFitsTerminalAndLongDetailsCanBeScrolled(t *testing.T) {
	for _, tmux := range []string{"", "test"} {
		t.Run("tmux="+tmux, func(t *testing.T) {
			t.Setenv("TMUX", tmux)
			for _, size := range []struct{ width, height int }{{32, 20}, {60, 24}, {100, 30}, {200, 40}} {
				m := model{mode: "cleanup_confirm", width: size.width, height: size.height, cleanup: cleanupFixture()}
				m.cleanup.Targets[2].Path += strings.Repeat("/very-long-世界-path", 30)
				m.cleanup.Targets[2].Branch = strings.Repeat("very-long-世界-branch", 20)
				m.cleanup.Targets[2].Presentation.Detail = m.cleanup.Targets[2].Branch
				m.cleanup.Targets[2].Description = ""
				for _, details := range []bool{false, true} {
					m.cleanupDetails = details
					if details {
						full := strings.Join(strings.Fields(ansi.Strip(m.cleanupConfirmView(m.cleanupWidth()))), "")
						for _, value := range []string{m.cleanup.Targets[2].Path, m.cleanup.Targets[2].Branch} {
							if !strings.Contains(full, value) {
								t.Fatalf("details truncated an identifier: %s", value)
							}
						}
					}
					for _, key := range []string{"home", "pgdown", "end", "pgup", "home"} {
						m.scrollCleanup(key)
						view := m.View()
						if lipgloss.Width(view) > size.width || lipgloss.Height(view) > size.height {
							t.Fatalf("view %dx%d exceeds terminal %dx%d (tmux=%q, details=%v):\n%s", lipgloss.Width(view), lipgloss.Height(view), size.width, size.height, tmux, details, view)
						}
						if !strings.Contains(ansi.Strip(view), "[y] Clean up") {
							t.Fatalf("scroll hid action footer:\n%s", view)
						}
						if key == "end" && !strings.Contains(view, "KEEP") {
							t.Fatalf("end did not reach bottom:\n%s", view)
						}
					}
					if m.cleanupScroll != 0 {
						t.Fatal("home did not reset scroll")
					}
				}
				m.cleanupScroll = 9999
				if view := m.View(); !strings.Contains(view, "KEEP") {
					t.Fatalf("stale offset after resize was not clamped:\n%s", view)
				}
			}
		})
	}
}

func TestCleanupToggleAndScrollWithTeatest(t *testing.T) {
	t.Setenv("TMUX", "test")
	m := staticTUIModel{model: model{mode: "cleanup_confirm", width: 70, height: 24, cleanup: cleanupFixture()}}
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(70, 24))
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	tm.Send(tea.KeyMsg{Type: tea.KeyEnd})
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	final := tm.FinalModel(t, teatest.WithFinalTimeout(time.Second)).(staticTUIModel).model
	if !final.cleanupDetails || final.cleanupScroll == 0 || !strings.Contains(final.View(), "KEEP") {
		t.Fatalf("details were not reachable interactively:\n%s", final.View())
	}
}
