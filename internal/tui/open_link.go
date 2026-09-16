package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func (m model) updateOpenLink(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "backspace":
		m.closeOpenLink()
	case "q", "ctrl+c":
		return m, tea.Quit
	case "j", "down", "ctrl+n":
		m.linkCursor = min(m.linkCursor+1, max(0, len(m.links)-1))
		m.syncLinkScroll()
	case "k", "up", "ctrl+p":
		m.linkCursor = max(0, m.linkCursor-1)
		m.syncLinkScroll()
	case "enter":
		if m.linkCursor >= 0 && m.linkCursor < len(m.links) {
			return m.openChosenLink(m.links[m.linkCursor])
		}
	default:
		if link, ok := matchingLink(m.links, msg.String()); ok {
			return m.openChosenLink(link)
		}
	}
	return m, nil
}

func (m *model) closeOpenLink() {
	m.mode = ""
	m.links = nil
	m.linkCursor, m.linkScroll = 0, 0
}

func (m model) openChosenLink(link linkChoice) (tea.Model, tea.Cmd) {
	if m.cursor < 0 || m.cursor >= len(m.tasks) {
		return m, nil
	}
	m.closeOpenLink()
	m.loading = true
	m.err = nil
	return m, m.openTask(m.tasks[m.cursor], link)
}

func openLinkHelp(width int) string {
	return helpStyle.Render(ansi.Wrap("↑/k ↓/j move • enter/key open • esc/backspace back • q quit", max(1, width), ""))
}

func (m model) openLinkHeight(width int, lineCount int) int {
	if m.height <= 0 {
		return max(1, lineCount)
	}
	// Header, help, two blank section boundaries, plus the list title and status.
	used := m.frameHeight() + lipgloss.Height(m.header(width)) + lipgloss.Height(openLinkHelp(width)) + 4 + 2
	return max(1, m.height-used)
}

func (m *model) syncLinkScroll() {
	if len(m.links) == 0 {
		m.linkCursor, m.linkScroll = 0, 0
		return
	}
	m.linkCursor = max(0, min(m.linkCursor, len(m.links)-1))
	width := m.contentWidth()
	lines, start, end := m.openLinkLines(width)
	m.linkScroll = adjustedTaskScroll(lines, start, end, m.linkScroll, m.openLinkHeight(width, len(lines)))
}

func (m model) openLinkView(width int) string {
	if len(m.links) == 0 {
		return subtleStyle.Render("No links on selected task.")
	}
	lines, start, end := m.openLinkLines(width)
	height := m.openLinkHeight(width, len(lines))
	scroll := adjustedTaskScroll(lines, start, end, m.linkScroll, height)
	bottom := min(len(lines), scroll+height)
	status := fmt.Sprintf("%d/%d", m.linkCursor+1, len(m.links))
	if scroll > 0 {
		status += " • ↑ more"
	}
	if bottom < len(lines) {
		status += " • ↓ more"
	}
	return strings.Join([]string{
		titleStyle.Render(truncateLine("Open link", width)),
		lipgloss.NewStyle().Height(height).Render(strings.Join(lines[scroll:bottom], "\n")),
		subtleStyle.Render(truncateLine(status, width)),
	}, "\n")
}

func (m model) openLinkLines(width int) ([]string, int, int) {
	var lines []string
	selectedStart, selectedEnd := 0, 0
	// Each entry uses one label row and, optionally, one detail row. Bound both
	// rows to the viewport so long URLs and multiline labels cannot hide entries.
	oneLine := func(text string) string { return strings.Join(strings.Fields(text), " ") }
	for i, link := range m.links {
		prefix := "  "
		if i == m.linkCursor {
			prefix = "› "
			selectedStart = len(lines)
		}
		line := truncateLine(fmt.Sprintf("%s%-1s  %-10s %s", prefix, link.Key, oneLine(link.Source), oneLine(link.Label)), width)
		if i == m.linkCursor {
			line = selectedStyle.Width(width).Render(line)
		}
		lines = append(lines, line)
		if link.Detail != "" {
			lines = append(lines, subtleStyle.Render(truncateLine("     "+oneLine(link.Detail), width)))
		}
		if i == m.linkCursor {
			selectedEnd = len(lines) - 1
		}
	}
	return lines, selectedStart, selectedEnd
}
