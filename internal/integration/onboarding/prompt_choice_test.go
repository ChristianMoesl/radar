package onboarding

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestChoicePromptUsesDefaultAndReturnsSelectedValue(t *testing.T) {
	q := question{
		title: "Choose a policy", initial: "balanced",
		choices: []choice{{"deny-all", "Block outbound traffic"}, {"balanced", "Typical development traffic"}, {"allow-all", "Allow all outbound traffic"}},
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
			for _, option := range q.choices {
				if !strings.Contains(m.View(), option.label) {
					t.Fatal("missing choice label")
				}
			}
			for _, key := range append(tc.keys, tea.KeyMsg{Type: tea.KeyEnter}) {
				updated, _ := m.Update(key)
				m = updated.(promptModel)
			}
			if !m.accepted || m.selected != tc.want || m.input.Value() != q.choices[tc.want].value {
				t.Fatalf("selected=%d value=%q accepted=%t", m.selected, m.input.Value(), m.accepted)
			}
			if !strings.Contains(m.View(), q.choices[tc.want].label) {
				t.Fatal("selected label missing from transcript")
			}
		})
	}
}

func TestChoicePromptCancellationDoesNotAcceptDefault(t *testing.T) {
	for _, key := range []tea.KeyType{tea.KeyEsc, tea.KeyCtrlC, tea.KeyCtrlD} {
		m := newPrompt(question{title: "Choose", initial: "balanced", choices: []choice{{"balanced", "Balanced"}}})
		updated, _ := m.Update(tea.KeyMsg{Type: key})
		result := updated.(promptModel)
		if !result.aborted || result.accepted {
			t.Fatal("cancellation accepted policy")
		}
	}
}
