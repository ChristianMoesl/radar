package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type keyHint struct {
	keys   string
	action string
}

var mainKeyHints = [][]keyHint{
	{
		{"↑/k/ctrl+p ↓/j/ctrl+n", "select"},
		{"ctrl+u/d", "page"},
		{"enter", "open"},
		{"n", "new task"},
		{"d", "done/reopen"},
		{"m", "ignore/unignore"},
		{"D", "delete task"},
		{"p", "urgent/normal"},
		{"o", "open link"},
		{"i", "inspect"},
	},
	{
		{"c", "create workspace"},
		{"w", "workspace resources"},
		{"x", "cleanup"},
		{"X", "garbage collect"},
		{"f", "config"},
		{"s", "sources"},
		{"r", "refresh"},
		{"q", "quit"},
	},
}

// Wrap between hints rather than truncating the remaining shortcuts. Keep task
// actions and workspace/system actions on separate lines even in wide terminals.
func mainHelp(width int) string {
	return renderKeyHints(width, mainKeyHints)
}

func (m model) mainHelp(width int) string {
	groups := make([][]keyHint, len(mainKeyHints))
	for i, hints := range mainKeyHints {
		groups[i] = append([]keyHint(nil), hints...)
	}
	if m.selectedSection != "" {
		action := "expand section"
		if m.sectionExpanded(m.selectedSection) {
			action = "collapse section"
		}
		groups[0] = []keyHint{groups[0][0], groups[0][1], {"enter", action}}
		// Workspace creation and system actions are genuinely global.
		groups[1] = nil
		for _, hint := range mainKeyHints[1] {
			if hint.keys != "w" && hint.keys != "x" {
				groups[1] = append(groups[1], hint)
			}
		}
		groups[0] = append(groups[0], keyHint{"n", "new task"})
	} else if task, ok := m.selectedTask(); ok {
		for i, hint := range groups[0] {
			if hint.keys == "m" {
				groups[0][i].action = "ignore"
				if task.Ignored {
					groups[0][i].action = "unignore"
				}
			}
		}
	}
	// Context changes must not move the viewport or the footer. Reserve the
	// same shortcut rows as the complete help, even on a section header.
	return lipgloss.NewStyle().Height(lipgloss.Height(mainHelp(width))).Render(renderKeyHints(width, groups))
}

func renderKeyHints(width int, groups [][]keyHint) string {
	width = max(1, width)
	lines := []string{separatorStyle.Render(strings.Repeat("─", width))}
	for _, group := range groups {
		line := ""
		for _, hint := range group {
			item := helpKeyStyle.Render(hint.keys) + helpStyle.Render(" "+hint.action)
			if line != "" && lipgloss.Width(line)+3+lipgloss.Width(item) > width {
				lines = append(lines, line)
				line = ""
			}
			if lipgloss.Width(item) > width {
				lines = append(lines, ansi.Hardwrap(item, width, false))
				continue
			}
			if line != "" {
				line += "   "
			}
			line += item
		}
		if line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}
