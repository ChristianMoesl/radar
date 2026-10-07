package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"radar/internal/update"
)

func TestMaintenanceHasNoDashboardShortcuts(t *testing.T) {
	for _, key := range []string{"u", "N"} {
		t.Run(key, func(t *testing.T) {
			m := newModel("unused.sock")
			next, command := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
			if command != nil {
				t.Fatal("maintenance shortcut launched a command")
			}
			if next.(model).mode != "" || next.(model).relaunch {
				t.Fatal("maintenance shortcut changed the dashboard")
			}
			for _, group := range mainKeyHints {
				for _, hint := range group {
					if hint.keys == key {
						t.Fatal("maintenance shortcut still advertised")
					}
				}
			}
		})
	}
	if !strings.Contains(ansi.Strip(mainHelp(100)), "ctrl+u/d") {
		t.Fatal("removed existing navigation shortcuts")
	}
}

func TestReleaseNoticePointsToUpdateCommand(t *testing.T) {
	m := newModel("unused.sock")
	if strings.Contains(ansi.Strip(m.header(100)), "radar update") {
		t.Fatal("notice shown without an available release")
	}
	next, command := m.Update(releaseNoticeMsg(update.Notice{Version: "v0.2.0"}))
	if command != nil {
		t.Fatal("informational notice started an action")
	}
	for _, width := range []int{30, 60, 100} {
		header := ansi.Strip(next.(model).header(width))
		if !strings.Contains(header, "radar update") {
			t.Fatalf("width %d lost update command: %s", width, header)
		}
		if strings.Contains(header, "u to review") {
			t.Fatal("notice advertised removed shortcut")
		}
	}
}
