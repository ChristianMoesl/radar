// Package onboarding owns foreground setup. No collector or daemon may prompt,
// install software, or turn a missing config into a completed setup.
package onboarding

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/term"
)

var ErrAborted = errors.New("setup cancelled — no Radar configuration was written")

type question struct {
	title, hint, initial string
	secret               bool
	validate             func(string) error
}

type prompter interface {
	input(question) (string, error)
	confirm(string) (bool, error)
	print(string, ...any)
}

type terminalUI struct {
	in  *os.File
	out io.Writer
}

func (u terminalUI) print(format string, args ...any) { fmt.Fprintf(u.out, format, args...) }
func (u terminalUI) input(q question) (string, error) {
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
func (u terminalUI) confirm(title string) (bool, error) {
	m := newPrompt(question{title: title})
	m.confirm = true
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

func Run() error {
	if !term.IsTerminal(os.Stdin.Fd()) || !term.IsTerminal(os.Stdout.Fd()) {
		return fmt.Errorf("first-time setup needs an interactive terminal — run `radar init` in a terminal")
	}
	return newWizard(terminalUI{in: os.Stdin, out: os.Stdout}, realSystem{}).run()
}

var heading = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
var muted = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

type promptModel struct {
	q                               question
	input                           textinput.Model
	confirm, yes, accepted, aborted bool
	validation                      string
}

func newPrompt(q question) promptModel {
	input := textinput.New()
	input.Prompt = "> "
	input.Width = 70
	input.CharLimit = 8192
	input.SetValue(q.initial)
	input.Focus()
	if q.secret {
		input.EchoMode = textinput.EchoPassword
		input.EchoCharacter = '•'
	}
	return promptModel{q: q, input: input}
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
		if msg.Type == tea.KeyEnter {
			value := strings.TrimSpace(m.input.Value())
			if m.q.validate != nil {
				if err := m.q.validate(value); err != nil {
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
		if m.q.secret {
			answer = "[hidden]"
		}
		if m.confirm {
			answer = "no"
			if m.yes {
				answer = "yes"
			}
		}
		return fmt.Sprintf("✓ %s: %s\n", m.q.title, answer)
	}
	view := heading.Render(m.q.title) + "\n"
	if m.q.hint != "" {
		view += muted.Render(m.q.hint) + "\n"
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
	view += m.input.View() + "\n"
	if m.validation != "" {
		view += m.validation + "\n"
	}
	return view + muted.Render("enter accept · esc cancel") + "\n"
}
