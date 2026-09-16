package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"radar/internal/pathdisplay"
	"radar/internal/protocol"
)

// The result is a snapshot of this run, independent of the live task list.
// In particular, completed tasks may no longer be visible in that list.
func (m model) updateGarbageCollectionResult(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "backspace", "enter":
		m.mode = ""
		m.message = garbageCollectionMessage(m.gcResult)
		m.gcResult = protocol.GarbageCollectionResult{}
		m.gcScroll = 0
	case "q", "ctrl+c":
		return m, tea.Quit
	default:
		lines, _, _, rows := m.garbageCollectionViewport()
		limit := max(0, len(lines)-rows)
		m.gcScroll = min(m.gcScroll, limit)
		switch msg.String() {
		case "j", "down", "ctrl+n":
			m.gcScroll++
		case "k", "up", "ctrl+p":
			m.gcScroll--
		case "pgdown", "ctrl+d":
			m.gcScroll += rows
		case "pgup", "ctrl+u":
			m.gcScroll -= rows
		case "home":
			m.gcScroll = 0
		case "end":
			m.gcScroll = limit
		}
		m.gcScroll = max(0, min(m.gcScroll, limit))
	}
	return m, nil
}

func (m model) garbageCollectionLines(width int) []string {
	var lines []string
	if len(m.gcResult.Skipped) > 0 {
		lines = append(lines, attentionStyle.Render("SKIPPED"))
		for _, item := range m.gcResult.Skipped {
			lines = append(lines, "", pathdisplay.HomeRelative(item.Path), fmt.Sprintf("  Task #%d", item.TaskID), "  Reason: "+item.Reason)
		}
	}
	if len(m.gcResult.Deleted) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, titleStyle.Render("DELETED"))
		for _, item := range m.gcResult.Deleted {
			lines = append(lines, "", pathdisplay.HomeRelative(item.Path), fmt.Sprintf("  Task #%d", item.TaskID))
		}
	}
	if len(lines) == 0 {
		lines = append(lines, "No workspaces eligible for garbage collection.")
	}
	// Wrap rather than truncate: complete workspace paths and provider reasons
	// must remain reachable, even on narrow terminals.
	return strings.Split(ansi.Wrap(strings.Join(lines, "\n"), max(1, width), ""), "\n")
}

func (m model) garbageCollectionViewport() (lines []string, header, footer string, rows int) {
	width := m.contentWidth()
	lines = m.garbageCollectionLines(width)
	header = titleStyle.Render("Garbage collection") + "\n" + fmt.Sprintf("Workspaces: %d deleted, %d skipped", len(m.gcResult.Deleted), len(m.gcResult.Skipped))
	header = ansi.Wrap(header, width, "")
	footer = helpStyle.Render(ansi.Wrap("[Enter/Esc] Back · [q] Quit", width, ""))
	rows = len(lines)
	if m.height > 0 {
		rows = max(1, m.height-m.frameHeight()-lipgloss.Height(header)-lipgloss.Height(footer)-2)
	}
	if len(lines) > rows {
		footer = helpStyle.Render(ansi.Wrap("↑/↓ Scroll · PgUp/PgDn Page · Home/End", width, "")) + "\n" + footer
		if m.height > 0 {
			rows = max(1, m.height-m.frameHeight()-lipgloss.Height(header)-lipgloss.Height(footer)-2)
		}
	}
	return lines, header, footer, rows
}

func (m model) garbageCollectionScreen() string {
	lines, header, footer, rows := m.garbageCollectionViewport()
	offset := max(0, min(m.gcScroll, max(0, len(lines)-rows)))
	body := strings.Join(lines[offset:min(len(lines), offset+rows)], "\n")
	return m.renderFrame(header+"\n\n"+body+"\n\n"+footer, m.contentWidth())
}
