package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// These modal lists have one terminal row per item. Reserve the actual wrapped
// help, heading, Radar header, position row and two blank section boundaries.
func (m model) modalListRows(width int, heading, footer string) int {
	if m.height <= 0 {
		return 10
	}
	used := m.frameHeight() + lipgloss.Height(m.header(width)) + lipgloss.Height(footer) + 3
	if heading != "" {
		used += lipgloss.Height(heading)
	}
	return max(1, m.height-used)
}

func selectionWindow(cursor, count, scroll, rows int) (start, end int) {
	rows = max(1, rows)
	cursor = max(0, min(cursor, count-1))
	start = max(0, min(scroll, count-rows))
	if cursor < start {
		start = cursor
	} else if cursor >= start+rows {
		start = cursor - rows + 1
	}
	return start, min(count, start+rows)
}

func singleLine(text string, width int) string {
	return truncateLine(strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(text), max(1, width))
}

func inputTail(text string, width int) string {
	if ansi.StringWidth(text) <= width {
		return text
	}
	return ansi.TruncateLeft(text, ansi.StringWidth(text)-width+1, "…")
}

func selectionView(labels []string, cursor, scroll, rows, width int, empty string) string {
	start, end := selectionWindow(cursor, len(labels), scroll, rows)
	var lines []string
	for i := start; i < end; i++ {
		prefix := "  "
		if i == cursor {
			prefix = "› "
		}
		line := truncateLine(prefix+singleLine(labels[i], width), width)
		if i == cursor {
			line = selectedStyle.Width(width).Render(line)
		}
		lines = append(lines, line)
	}
	if len(labels) == 0 {
		lines = append(lines, subtleStyle.Render(singleLine(empty, width)))
	}
	position := 0
	if len(labels) > 0 {
		position = cursor + 1
	}
	status := fmt.Sprintf("%d/%d", position, len(labels))
	if start > 0 {
		status += " • ↑ more"
	}
	if end < len(labels) {
		status += " • ↓ more"
	}
	return lipgloss.NewStyle().Height(rows).Render(strings.Join(lines, "\n")) + "\n" + subtleStyle.Render(truncateLine(status, width))
}

// Store the viewport independently of selection, so moving upward within the
// visible window does not scroll. The same clamp is used after terminal resize.
func (m *model) syncSelectionScroll() {
	width := m.contentWidth()
	if list := m.activePicker(); list != nil {
		rows := m.modalListRows(width, m.createPickerHeading(width), m.createHelp(width))
		list.scroll, _ = selectionWindow(list.cursor, len(filteredOptions(*list)), list.scroll, rows)
	} else if m.mode == "worktree_session" {
		rows := m.modalListRows(width, worktreeSessionHeading(width), worktreeSessionHelp(width))
		m.worktreeScroll, _ = selectionWindow(m.worktreeCursor, len(m.worktrees), m.worktreeScroll, rows)
	} else if m.mode == "workspace_edit" {
		rows := m.modalListRows(width, m.workspaceListHeading(width), m.workspaceListFooter(width))
		m.editor.listScroll, _ = selectionWindow(m.editor.cursor, len(m.editor.desired.Worktrees), m.editor.listScroll, rows)
	} else if m.mode == "workspace_confirm" {
		m.editor.scroll = max(0, min(m.editor.scroll, len(m.workspaceConfirmationLines(width))-m.workspacePageRows()))
	}
}
