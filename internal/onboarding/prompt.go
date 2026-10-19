// Package onboarding owns foreground setup. No collector or daemon may prompt,
// install software, or turn a missing config into a completed setup.
package onboarding

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/term"
	"github.com/muesli/termenv"

	"radar/internal/hostsetup"
	"radar/internal/integration"
	"radar/internal/setup"
)

var ErrAborted = setup.ErrAborted
var ErrToolsNotReady = hostsetup.ErrToolsNotReady

type choice = setup.Choice
type question = setup.Question
type prompter interface {
	setup.UI
	review(setupReview) error
}

type terminalUI struct {
	in  *os.File
	out io.Writer
}

func (u terminalUI) Print(format string, args ...any) { fmt.Fprintf(u.out, format, args...) }

// Bind styling to the actual output, not the process-global renderer. Never
// emit ANSI for redirected output or override an explicit NO_COLOR setting.
func (u terminalUI) Style() lipgloss.Style {
	r := lipgloss.NewRenderer(u.out)
	out, ok := u.out.(*os.File)
	if !ok || !term.IsTerminal(out.Fd()) || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		r.SetColorProfile(termenv.Ascii)
	}
	return r.NewStyle()
}

func (u terminalUI) Input(q question) (string, error) {
	m := newPrompt(q)
	final, err := tea.NewProgram(m, tea.WithInput(u.in), tea.WithOutput(u.out)).Run()
	if err != nil {
		return "", err
	}
	result := final.(promptModel)
	if !result.accepted {
		return "", ErrAborted
	}
	return strings.TrimSpace(result.input.Value()), nil
}
func (u terminalUI) Confirm(title string, initial bool) (bool, error) {
	m := newPrompt(question{Title: title})
	m.confirm, m.yes = true, initial
	final, err := tea.NewProgram(m, tea.WithInput(u.in), tea.WithOutput(u.out)).Run()
	if err != nil {
		return false, err
	}
	result := final.(promptModel)
	if !result.accepted {
		return false, ErrAborted
	}
	return result.yes, nil
}

func Run(registry integration.Registry) error {
	if !term.IsTerminal(os.Stdin.Fd()) || !term.IsTerminal(os.Stdout.Fd()) {
		return fmt.Errorf("setup needs an interactive terminal — run `radar setup` in a terminal")
	}
	w := newWizard(terminalUI{in: os.Stdin, out: os.Stdout}, setup.RealSystem{}, registry)
	return w.run()
}

var heading = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
var muted = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

type promptModel struct {
	q                               question
	input                           textinput.Model
	confirm, yes, accepted, aborted bool
	validation                      string
	selected                        int
}

func newPrompt(q question) promptModel {
	input := textinput.New()
	input.Prompt = "> "
	input.Width = 70
	input.CharLimit = 8192
	input.SetValue(q.Initial)
	input.Focus()
	if q.Secret {
		input.EchoMode = textinput.EchoPassword
		input.EchoCharacter = '•'
	}
	m := promptModel{q: q, input: input}
	for i, option := range q.Choices {
		if option.Value == q.Initial {
			m.selected = i
		}
	}
	return m
}
func (m promptModel) Init() tea.Cmd { return textinput.Blink }
func (m promptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.input.Width = max(10, msg.Width-6)
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "ctrl+d":
			m.aborted = true
			return m, tea.Quit
		}
		if m.confirm {
			switch msg.String() {
			case "left", "right", "tab":
				m.yes = !m.yes
			case "y":
				m.yes, m.accepted = true, true
			case "n":
				m.yes, m.accepted = false, true
			case "enter":
				m.accepted = true
			}
			if m.accepted {
				return m, tea.Quit
			}
			return m, nil
		}
		if len(m.q.Choices) > 0 {
			switch msg.String() {
			case "up":
				m.selected = max(0, m.selected-1)
			case "down", "tab":
				m.selected = min(len(m.q.Choices)-1, m.selected+1)
			case "enter":
				m.input.SetValue(m.q.Choices[m.selected].Value)
				m.accepted = true
				return m, tea.Quit
			}
			return m, nil
		}
		if msg.Type == tea.KeyEnter {
			value := strings.TrimSpace(m.input.Value())
			if m.q.Validate != nil {
				if err := m.q.Validate(value); err != nil {
					m.validation = err.Error()
					return m, nil
				}
			}
			m.accepted = true
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}
func (m promptModel) View() string {
	if m.aborted {
		return "Setup cancelled.\n"
	}
	if m.accepted {
		answer := strings.TrimSpace(m.input.Value())
		if m.q.Secret {
			answer = "[hidden]"
		}
		if len(m.q.Choices) > 0 {
			answer = m.q.Choices[m.selected].Label
		}
		if m.confirm {
			answer = "no"
			if m.yes {
				answer = "yes"
			}
		}
		return fmt.Sprintf("✓ %s: %s\n", m.q.Title, answer)
	}
	view := heading.Render(m.q.Title) + "\n"
	if m.q.Hint != "" {
		view += muted.Render(m.q.Hint) + "\n"
	}
	if m.confirm {
		yes, no := " yes ", " no "
		if m.yes {
			yes = heading.Reverse(true).Render(yes)
		} else {
			no = heading.Reverse(true).Render(no)
		}
		return view + yes + "  " + no + "\n" + muted.Render("←/→ choose · enter accept · esc cancel") + "\n"
	}
	if len(m.q.Choices) > 0 {
		for i, option := range m.q.Choices {
			line := "  " + option.Label
			if i == m.selected {
				line = heading.Reverse(true).Render("> " + option.Label)
			}
			view += line + "\n"
		}
		return view + muted.Render("↑/↓ choose · enter accept · esc cancel") + "\n"
	}
	view += m.input.View() + "\n"
	if m.validation != "" {
		view += m.validation + "\n"
	}
	return view + muted.Render("enter accept · esc cancel") + "\n"
}
