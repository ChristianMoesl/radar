package onboarding

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"radar/internal/config"
	"radar/internal/setup"
)

func TestPromptKeepsSecretsMaskedOnInputAndAcceptance(t *testing.T) {
	m := newPrompt(question{Title: "API token", Secret: true, Validate: setup.Required})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("secret-fixture")})
	m = updated.(promptModel)
	if strings.Contains(m.View(), "secret-fixture") {
		t.Fatal("secret visible during entry")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(promptModel)
	if !m.accepted || strings.Contains(m.View(), "secret-fixture") || !strings.Contains(m.View(), "[hidden]") {
		t.Fatal("secret visible in transcript")
	}
}
func TestPromptValidationAndCancellation(t *testing.T) {
	m := newPrompt(question{Title: "Required", Validate: setup.Required})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(promptModel)
	if m.accepted || m.validation == "" {
		t.Fatal("empty input accepted")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(promptModel)
	if !m.aborted || m.accepted {
		t.Fatal("ctrl-c did not abort")
	}
}
func TestConfirmationHonorsDefaultAndSupportsKeyboard(t *testing.T) {
	for _, initial := range []bool{false, true} {
		m := newPrompt(question{Title: "Confirmation"})
		m.confirm, m.yes = true, initial
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		result := updated.(promptModel)
		if !result.accepted || result.yes != initial {
			t.Fatal("Enter did not accept the chosen default")
		}
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
		result = updated.(promptModel)
		updated, _ = result.Update(tea.KeyMsg{Type: tea.KeyEnter})
		result = updated.(promptModel)
		if !result.accepted || result.yes == initial {
			t.Fatal("arrow/Enter did not change selection")
		}
	}
}

func TestRepeatSetupWithSBXDisabledDoesNotRequirePiSBX(t *testing.T) {
	w, ui, sys, home := fixture(t)
	delete(sys.missing, "sbx")
	cfg := config.Default()
	no := false
	cfg.SBX.Enabled = &no
	cfg.RepositoryDirs = []string{filepath.Join(home, "repos")}
	cfg.Workspace.RootDir = filepath.Join(home, "workspaces")
	cfg.Obsidian.VaultPath = filepath.Join(home, "notes")
	if err := config.Create(cfg); err != nil {
		t.Fatal(err)
	}
	ui.decisions = []bool{false, false, false, true}
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ui.transcript.String(), "pi-sbx") {
		t.Fatal("disabled sandboxing still required pi-sbx")
	}
	saved, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.SBX.Enabled == nil || *saved.SBX.Enabled {
		t.Fatal("setup changed explicit sandbox disablement")
	}
}
