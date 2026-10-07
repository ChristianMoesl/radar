package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const (
	mainRailWidth    = 32
	mainRailGap      = 3 // One space either side of the vertical separator.
	minTaskListWidth = 56
)

type railGroup struct {
	title string
	hints []keyHint
}

// Keep the rail's order and row slots stable, even when an action is unavailable.
// Global commands come from the common hint set, including platform additions.
func mainRailGroups() []railGroup {
	byKey := map[string]keyHint{}
	for _, group := range mainKeyHints {
		for _, hint := range group {
			byKey[hint.keys] = hint
		}
	}
	groups := []railGroup{
		{title: "Navigate", hints: []keyHint{{"↑/k ↓/j", "Select"}, {"PgUp/PgDn", "Page"}, {"Home/End", "First / last"}}},
		{title: "Selected task"},
		{title: "Global", hints: []keyHint{byKey["n"]}},
	}
	for _, key := range []string{"enter", "o", "i", "d", "m", "p", "w", "x", "D"} {
		groups[1].hints = append(groups[1].hints, byKey[key])
	}
	for _, hint := range mainKeyHints[1] {
		if hint.keys != "w" && hint.keys != "x" {
			groups[2].hints = append(groups[2].hints, hint)
		}
	}
	return groups
}

func mainRailMinHeight() int {
	rows := 2 + 4 // Title and gap; separator, alias heading and two alias rows.
	for _, group := range mainRailGroups() {
		rows += 2 + len(group.hints) // Heading, stable command slots and gap.
	}
	return rows
}

func (m model) mainRailHeight() int {
	return m.height - m.frameHeight() - lipgloss.Height(m.header(m.contentWidth())) - 1
}

func (m model) showsMainRail() bool {
	return m.contentWidth() >= minTaskListWidth+mainRailGap+mainRailWidth && m.mainRailHeight() >= mainRailMinHeight()
}

func (m model) taskListWidth() int {
	width := m.contentWidth()
	if m.showsMainRail() {
		width -= mainRailWidth + mainRailGap
	}
	return width
}

func railHintLine(hint keyHint) string {
	if hint.keys == "" {
		return ""
	}
	key := hint.keys
	if key == "enter" {
		key = "Enter"
	}
	action := singleLine(hint.action, mainRailWidth-lipgloss.Width(key)-2)
	padding := strings.Repeat(" ", mainRailWidth-lipgloss.Width(action)-lipgloss.Width(key))
	return helpStyle.Render(action) + padding + helpKeyStyle.Render(key)
}

func (m model) mainRail(height int) string {
	available := map[string]keyHint{}
	for _, group := range m.mainHelpGroups() {
		for _, hint := range group {
			available[hint.keys] = hint
		}
	}
	lines := []string{referenceStyle.Render("KEYBOARD"), ""}
	for index, group := range mainRailGroups() {
		if index == 1 && m.selectedSection != "" {
			group.title = "Selected section"
		}
		lines = append(lines, titleStyle.Bold(false).Render(group.title))
		for _, hint := range group.hints {
			if index != 0 {
				hint = available[hint.keys]
				if hint.action != "" {
					hint.action = strings.ToUpper(hint.action[:1]) + hint.action[1:]
				}
				switch hint.keys {
				case "c":
					hint.action = "New workspace"
				case "w":
					hint.action = "Resources"
				}
			}
			lines = append(lines, railHintLine(hint))
		}
		lines = append(lines, "")
	}
	aliases := strings.Join([]string{
		separatorStyle.Render(strings.Repeat("─", mainRailWidth)),
		referenceStyle.Render("OTHER BINDINGS"),
		referenceStyle.Render("Ctrl+P/N select · Ctrl+U/D page"),
		referenceStyle.Render("g/G first/last · → inspect"),
	}, "\n")
	body := lipgloss.NewStyle().Width(mainRailWidth).Height(height - 4).Render(strings.Join(lines, "\n"))
	return body + "\n" + aliases
}

func (m model) dashboardColumns(body string) string {
	height := m.mainRailHeight()
	left := lipgloss.NewStyle().Width(m.taskListWidth()).Height(height).Render(body)
	separator := separatorStyle.Render(strings.TrimSuffix(strings.Repeat(" │ \n", height), "\n"))
	return lipgloss.JoinHorizontal(lipgloss.Top, left, separator, m.mainRail(height))
}
