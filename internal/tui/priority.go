package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"radar/internal/protocol"
)

var priorityChoices = []string{"low", "normal", "urgent"}

func prioritySupported(task protocol.Task) bool {
	for _, ref := range task.SourceRefs {
		if ref.Role == protocol.SourceRefRoleAuthoritative {
			return true
		}
	}
	return false
}

func (m model) updatePriority(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "backspace", "q", "ctrl+c":
		m.mode, m.priorityTask = "", protocol.Task{}
	case "j", "down", "ctrl+n":
		m.priorityChoice = min(len(priorityChoices)-1, m.priorityChoice+1)
	case "k", "up", "ctrl+p":
		m.priorityChoice = max(0, m.priorityChoice-1)
	case "enter":
		cursor, ok := matchingTaskCursor(m.tasks, m.priorityTask)
		m.mode, m.priorityTask = "", protocol.Task{}
		if !ok || m.tasks[cursor].Attention == "done" || !prioritySupported(m.tasks[cursor]) {
			m.message = "Task unavailable or done\nRefresh before changing priority"
			return m, nil
		}
		m.loading, m.err = true, nil
		return m, m.setTaskPriority(m.tasks[cursor], priorityChoices[m.priorityChoice])
	}
	return m, nil
}

func (m model) priorityScreen() string {
	width := m.contentWidth()
	title := titleStyle.Render(truncateLine("Task priority · "+m.priorityTask.Title, width))
	intro := ansi.Wrap("Low defers known activity. New actionable requests can temporarily resurface the task without clearing Low. Choose Low again to defer outstanding requests.", width, "")
	footer := wrappedHelp("↑/k ↓/j select • enter apply • esc cancel", width)
	rows := len(priorityChoices)
	sections := []string{title}
	if m.height <= 0 || m.height-m.frameHeight()-lipgloss.Height(title)-lipgloss.Height(footer)-3 >= rows+lipgloss.Height(intro)+1 {
		sections = append(sections, intro)
	} else {
		rows = max(1, min(rows, m.height-m.frameHeight()-lipgloss.Height(title)-lipgloss.Height(footer)-3))
	}
	choices := selectionView([]string{"Low — defer current activity", "Normal — use source priority", "Urgent"}, m.priorityChoice, 0, rows, width, "")
	sections = append(sections, choices, footer)
	return m.renderFrame(strings.Join(sections, "\n\n"), width)
}
