package onboarding

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestChoicePromptUsesDefaultAndReturnsSelectedValue(t *testing.T) {
	q := question{
		Title: "Choose a policy", Initial: "balanced",
		Choices: []choice{{Value: "deny-all", Label: "Block outbound traffic"}, {Value: "balanced", Label: "Typical development traffic"}, {Value: "allow-all", Label: "Allow all outbound traffic"}},
	}
	for _, tc := range []struct {
		name string
		keys []tea.KeyMsg
		want int
	}{
		{"default", nil, 1},
		{"up", []tea.KeyMsg{{Type: tea.KeyUp}}, 0},
		{"down", []tea.KeyMsg{{Type: tea.KeyDown}}, 2},
		{"tab", []tea.KeyMsg{{Type: tea.KeyTab}}, 2},
		{"first boundary", []tea.KeyMsg{{Type: tea.KeyUp}, {Type: tea.KeyUp}}, 0},
		{"last boundary", []tea.KeyMsg{{Type: tea.KeyDown}, {Type: tea.KeyDown}}, 2},
		{"typing ignored", []tea.KeyMsg{{Type: tea.KeyRunes, Runes: []rune("unexpected")}}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newPrompt(q)
			for _, option := range q.Choices {
				if !strings.Contains(m.View(), option.Label) {
					t.Fatal("missing choice label")
				}
			}
			for _, key := range append(tc.keys, tea.KeyMsg{Type: tea.KeyEnter}) {
				updated, _ := m.Update(key)
				m = updated.(promptModel)
			}
			if !m.accepted || m.selected != tc.want || m.input.Value() != q.Choices[tc.want].Value {
				t.Fatalf("selected=%d value=%q accepted=%t", m.selected, m.input.Value(), m.accepted)
			}
			if !strings.Contains(m.View(), q.Choices[tc.want].Label) {
				t.Fatal("selected label missing from transcript")
			}
		})
	}
}

func TestChoicePromptCancellationDoesNotAcceptDefault(t *testing.T) {
	for _, key := range []tea.KeyType{tea.KeyEsc, tea.KeyCtrlC, tea.KeyCtrlD} {
		m := newPrompt(question{Title: "Choose", Initial: "balanced", Choices: []choice{{Value: "balanced", Label: "Balanced"}}})
		updated, _ := m.Update(tea.KeyMsg{Type: key})
		result := updated.(promptModel)
		if !result.aborted || result.accepted {
			t.Fatal("cancellation accepted policy")
		}
	}
}
