package onboarding

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"radar/internal/setup"
)

func workspaceQuestion() setup.MultiSelectQuestion {
	return setup.MultiSelectQuestion{
		Title: "Terraform workspaces", Initial: []string{"beta", "unavailable"},
		Choices: []choice{{Value: "alpha", Label: "Alpha"}, {Value: "beta", Label: "Beta"}, {Value: "gamma", Label: "Gamma"}},
		Validate: func(values []string) error {
			if len(values) == 0 {
				return fmt.Errorf("select at least one workspace")
			}
			return nil
		},
	}
}

func TestMultiSelectPromptPreselectsAndToggles(t *testing.T) {
	m := newMultiSelectPrompt(workspaceQuestion())
	if !reflect.DeepEqual(m.selectedValues(), []string{"beta"}) || !strings.Contains(m.View(), "[x] Beta") {
		t.Fatal("available initial selection not checked", m.selectedValues(), m.View())
	}
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyUp}, // Stop at the first choice.
		{Type: tea.KeySpace},
		{Type: tea.KeyDown}, {Type: tea.KeySpace}, // Deselect the initial choice.
		{Type: tea.KeyTab}, {Type: tea.KeyDown}, // Stop at the last choice.
		{Type: tea.KeyRunes, Runes: []rune("ignored")},
		{Type: tea.KeySpace}, {Type: tea.KeyEnter},
	} {
		updated, _ := m.Update(key)
		m = updated.(promptModel)
	}
	if !m.accepted || !reflect.DeepEqual(m.selectedValues(), []string{"alpha", "gamma"}) {
		t.Fatal("wrong selected values", m.selectedValues(), m.accepted)
	}
	if view := m.View(); !strings.Contains(view, "Alpha, Gamma") || strings.Contains(view, "Beta") {
		t.Fatal("transcript did not show selected labels", view)
	}
}

func TestMultiSelectPromptValidatesBeforeAccepting(t *testing.T) {
	q := workspaceQuestion()
	q.Initial = nil
	m := newMultiSelectPrompt(q)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(promptModel)
	if m.accepted || !strings.Contains(m.View(), "select at least one workspace") {
		t.Fatal("empty selection accepted or validation not shown", m.View())
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = updated.(promptModel)
	if m.validation != "" {
		t.Fatal("validation did not clear after a change")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !updated.(promptModel).accepted {
		t.Fatal("valid selection not accepted")
	}
}

func TestMultiSelectPromptCancellationDoesNotAcceptInitial(t *testing.T) {
	for _, key := range []tea.KeyType{tea.KeyEsc, tea.KeyCtrlC, tea.KeyCtrlD} {
		m := newMultiSelectPrompt(workspaceQuestion())
		updated, _ := m.Update(tea.KeyMsg{Type: key})
		m = updated.(promptModel)
		if !m.aborted || m.accepted {
			t.Fatal("cancellation accepted choices")
		}
	}
}

func TestMultiSelectPromptKeepsCursorVisibleWhenScrollingAndResizing(t *testing.T) {
	q := workspaceQuestion()
	q.Choices = nil
	for i := 0; i < 100; i++ {
		name := fmt.Sprintf("workspace-%03d-with-a-long-name", i)
		q.Choices = append(q.Choices, choice{Value: name, Label: name})
	}
	m := newMultiSelectPrompt(q)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 30, Height: 12})
	m = updated.(promptModel)
	for i := 0; i < 95; i++ {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(promptModel)
	}
	for _, size := range []tea.WindowSizeMsg{{Width: 30, Height: 12}, {Width: 60, Height: 18}} {
		updated, _ = m.Update(size)
		m = updated.(promptModel)
		view := ansi.Strip(m.View())
		if !strings.Contains(view, "> [ ] workspace-095") || !strings.Contains(view, "96/100") || strings.Count(view, "\n") > size.Height {
			t.Fatal("selection not visible within terminal", view)
		}
		for _, line := range strings.Split(view, "\n") {
			if strings.Contains(line, "workspace-") && ansi.StringWidth(line) > size.Width {
				t.Fatal("choice wrapped beyond terminal width", line)
			}
		}
	}
}
