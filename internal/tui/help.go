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
		{"enter", "switch tmux"},
		{"n", "new task"},
		{"d", "done/reopen"},
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
		{"r", "refresh"},
		{"q", "quit"},
	},
}

// Wrap between hints rather than truncating the remaining shortcuts. Keep task
// actions and workspace/system actions on separate lines even in wide terminals.
func mainHelp(width int) string {
	width = max(1, width)
	lines := []string{separatorStyle.Render(strings.Repeat("─", width))}
	for _, group := range mainKeyHints {
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
