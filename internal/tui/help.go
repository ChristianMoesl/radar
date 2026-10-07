package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"radar/internal/protocol"
)

func confirmationScrollHelp(width int) string {
	// Keep actions and useful body rows visible on narrow confirmation screens.
	if width < 60 {
		return "↑/k ↓/j scroll • PgUp/PgDn page"
	}
	return "↑/k ↓/j scroll • PgUp/PgDn/ctrl+u/d page • Home/End top/bottom"
}

func wrappedHelp(text string, width int) string {
	return helpStyle.Render(ansi.Wrap(text, max(1, width), ""))
}

func (m model) createHelp(width int) string {
	if m.mode == "create_name" {
		return wrappedHelp("type a branch name • enter submit • esc cancel", width)
	}
	return wrappedHelp("type to filter • ↑/ctrl+p ↓/ctrl+n move • enter select • esc cancel", width)
}

func worktreeSessionHelp(width int) string {
	return wrappedHelp("↑/k ↓/j move • enter create session • esc/backspace back • q quit", width)
}

type keyHint struct {
	keys   string
	action string
}

var mainKeyHints = [][]keyHint{
	{
		{"↑/k/ctrl+p ↓/j/ctrl+n", "select"},
		{"PgUp/PgDn/ctrl+u/d", "page"},
		{"enter", "open"},
		{"n", "new task"},
		{"d", "done/reopen"},
		{"m", "mute/unmute"},
		{"D", "delete task"},
		{"p", "urgent/normal"},
		{"o", "open link"},
		{"i", "inspect"},
	},
	{
		{"c", "create workspace"},
		{"w", "workspace resources"},
		{"x", "cleanup"},
		{"s", "sources"},
		{"r", "refresh"},
		{"f", "config"},
		{"X", "garbage collect"},
		{"q", "quit"},
	},
}

// Wrap between hints rather than truncating the remaining shortcuts. Keep task
// actions and workspace/system actions on separate lines even in wide terminals.
func mainHelp(width int) string {
	return renderKeyHints(width, mainKeyHints)
}

func (m model) mainHelp(width int) string {
	// Context changes must not move the compact viewport or footer.
	return lipgloss.NewStyle().Height(lipgloss.Height(mainHelp(width))).Render(renderKeyHints(width, m.mainHelpGroups()))
}

// The compact footer and the right-hand rail share contextual labels and
// availability. Neither changes the key handlers or adds a new interaction.
func (m model) mainHelpGroups() [][]keyHint {
	task, selected := m.selectedTask()
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
	} else if selected {
		ref, _ := authoredTaskRef(task)
		for i, hint := range groups[0] {
			switch hint.keys {
			case "m":
				groups[0][i].action = "mute"
				if task.Muted {
					groups[0][i].action = "unmute"
				}
			case "d":
				groups[0][i].action = "mark done"
				if ref.Metadata["state"] == "done" {
					groups[0][i].action = "reopen"
				}
			case "p":
				groups[0][i].action = "make urgent"
				if ref.Metadata["priority"] == "urgent" {
					groups[0][i].action = "make normal"
				}
			}
		}
	}
	for i, group := range groups {
		groups[i] = nil
		for _, hint := range group {
			if m.mainHintAvailable(hint.keys, task, selected) {
				groups[i] = append(groups[i], hint)
			}
		}
	}
	return groups
}

func (m model) mainHintAvailable(key string, task protocol.Task, selected bool) bool {
	// These hints describe several aliases rather than a single input event.
	if key == mainKeyHints[0][0].keys || key == mainKeyHints[0][1].keys {
		return true
	}
	if m.operation.kind != "" && (!operationNavigationKey(key) || (key == "enter" && (m.mode != "" || m.selectedSection == ""))) {
		return false
	}
	if key == "enter" && m.selectedSection != "" {
		return true
	}
	switch key {
	case "enter", "i", "w", "x", "m":
		return selected
	case "o":
		return selected && len(taskLinks(task)) > 0
	case "d", "D", "p":
		_, authored := authoredTaskRef(task)
		return selected && authored && (key != "p" || task.Attention != "done")
	default:
		return true
	}
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
