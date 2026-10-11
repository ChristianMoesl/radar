package onboarding

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"radar/internal/pathdisplay"
)

func (u terminalUI) review(report setupReview) error {
	final, err := tea.NewProgram(newReviewModel(report, u.style()), tea.WithInput(u.in), tea.WithOutput(u.out)).Run()
	if err != nil {
		return err
	}
	if !final.(reviewModel).accepted {
		return ErrAborted
	}
	return nil
}

type reviewScreen int

const (
	reviewSummary reviewScreen = iota
	reviewFiles
	reviewDetails
)

type reviewModel struct {
	report            setupReview
	style             lipgloss.Style
	screen            reviewScreen
	selected          int
	viewport          viewport.Model
	accepted, aborted bool
}

func newReviewModel(report setupReview, style lipgloss.Style) reviewModel {
	return reviewModel{report: report, style: style, viewport: viewport.New(80, 18)}
}

func (m reviewModel) Init() tea.Cmd { return nil }

func (m reviewModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.viewport.Width = max(10, msg.Width)
		m.viewport.Height = max(3, msg.Height-6)
		if m.screen == reviewDetails {
			m.setDetails()
		}
	case tea.KeyMsg:
		key := msg.String()
		if key == "c" || key == "ctrl+c" || key == "ctrl+d" {
			m.aborted = true
			return m, tea.Quit
		}
		switch m.screen {
		case reviewSummary:
			switch key {
			case "enter":
				m.accepted = true
				return m, tea.Quit
			case "v":
				if len(m.report.files) > 0 {
					m.screen = reviewFiles
				}
			case "esc":
				m.aborted = true
				return m, tea.Quit
			}
		case reviewFiles:
			switch key {
			case "esc":
				m.screen = reviewSummary
			case "up":
				m.selected = max(0, m.selected-1)
			case "down":
				m.selected = min(len(m.report.files)-1, m.selected+1)
			case "enter":
				m.screen = reviewDetails
				m.setDetails()
				m.viewport.GotoTop()
			}
		case reviewDetails:
			if key == "esc" {
				m.screen = reviewFiles
				return m, nil
			}
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func (m *reviewModel) setDetails() {
	// Keep requested details scrollable rather than dumping another long file
	// into the transcript. Wrapping preserves content on narrow terminals.
	body := ansi.Strip(m.report.files[m.selected].details)
	m.viewport.SetContent(ansi.Hardwrap(body, m.viewport.Width, true))
}

func (m reviewModel) View() string {
	if m.aborted {
		return "Setup cancelled — no Radar configuration was written.\n"
	}
	if m.accepted {
		return m.report.summary(m.style) + "\n✓ Changes approved\n"
	}
	switch m.screen {
	case reviewFiles:
		var out strings.Builder
		fmt.Fprintln(&out, m.style.Bold(true).Render("View file details"))
		fmt.Fprintln(&out)
		for i, file := range m.report.files {
			marker := "  "
			if i == m.selected {
				marker = "> "
			}
			fmt.Fprintf(&out, "%s%-9s  %s\n", marker, file.action, pathdisplay.HomeRelative(file.path))
		}
		fmt.Fprintln(&out, "\n↑/↓ Choose file · Enter View · Esc Back · c Cancel")
		return out.String()
	case reviewDetails:
		file := m.report.files[m.selected]
		header := m.style.Bold(true).Render(file.purpose+" · "+file.action) + "\n" + pathdisplay.HomeRelative(file.path)
		return header + "\n\n" + m.viewport.View() + "\n↑/↓ Scroll · Esc Back · c Cancel\n"
	default:
		return m.report.summary(m.style) + "\n" + m.style.Bold(true).Render("[Enter] Save") + "   [v] View details   [c] Cancel\n"
	}
}
