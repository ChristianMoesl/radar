package tui

import (
	"sort"
	"time"

	"github.com/charmbracelet/lipgloss"

	"radar/internal/protocol"
)

// An entry is either a real task index or a selectable section header. Headers
// never masquerade as tasks, and active section headings are not entries.
type visibleEntry struct {
	section string
	task    int
}

type taskSection struct {
	key         string
	title       string
	style       lipgloss.Style
	collapsible bool
}

var taskSections = []taskSection{
	{key: "immediate", title: "🚨 Need immediate attention", style: urgentStyle},
	{key: "attention", title: "👀 Need attention", style: attentionStyle},
	{key: "in_progress", title: "⏳ In progress", style: progressStyle},
	{key: "low_priority", title: "🔇 Low priority", style: lowStyle},
	{key: "muted", title: "Muted", style: lowStyle, collapsible: true},
	{key: "done", title: "Done", style: doneStyle, collapsible: true},
}

type overviewRow struct {
	section *taskSection
	count   int
	entry   visibleEntry
	ref     *protocol.SourceRef
	blank   bool
}

type entryBounds struct {
	line       int
	start, end int
}

type overviewLayout struct {
	rows    []overviewRow
	entries []visibleEntry
	bounds  map[visibleEntry]entryBounds
}

func historyGroup(group string) bool { return group == "muted" || group == "done" }

func (m model) sectionExpanded(group string) bool { return m.expandedSections[group] }

func (m *model) toggleSection(group string) {
	if m.expandedSections == nil {
		m.expandedSections = make(map[string]bool)
	}
	m.expandedSections[group] = !m.expandedSections[group]
	m.syncTaskScroll()
}

// Sorting is independent from expansion and never mutates the served view.
// Retention (including unresolved workspace exceptions) remains server-owned.
func (m model) taskGroupOrder(key string) []int {
	var order []int
	for i, task := range m.tasks {
		if task.DisplayGroup() == key {
			order = append(order, i)
		}
	}
	if key == "done" {
		sort.SliceStable(order, func(i, j int) bool {
			left, leftErr := time.Parse(time.RFC3339, m.tasks[order[i]].DoneAt)
			right, rightErr := time.Parse(time.RFC3339, m.tasks[order[j]].DoneAt)
			if leftErr != nil {
				return false
			}
			return rightErr != nil || left.After(right)
		})
	}
	return order
}

// Rendering, navigation, page movement and scrolling consume this one layout.
// Collapsed child tasks and refs are never allocated rows or selectable entries.
func (m model) overviewLayout() overviewLayout {
	layout := overviewLayout{bounds: make(map[visibleEntry]entryBounds)}
	for _, section := range taskSections {
		order := m.taskGroupOrder(section.key)
		if len(order) == 0 {
			continue
		}
		if len(layout.rows) > 0 {
			layout.rows = append(layout.rows, overviewRow{blank: true})
		}
		headerLine := len(layout.rows)
		header := visibleEntry{section: section.key}
		layout.rows = append(layout.rows, overviewRow{section: &section, count: len(order), entry: header})
		if section.collapsible {
			layout.entries = append(layout.entries, header)
			layout.bounds[header] = entryBounds{line: headerLine, start: headerLine, end: headerLine}
			if !m.sectionExpanded(section.key) {
				continue
			}
		}
		for position, index := range order {
			if position > 0 {
				layout.rows = append(layout.rows, overviewRow{blank: true})
			}
			entry := visibleEntry{task: index}
			line := len(layout.rows)
			layout.rows = append(layout.rows, overviewRow{entry: entry})
			for _, ref := range overviewSourceRefs(m.tasks[index]) {
				layout.rows = append(layout.rows, overviewRow{ref: &ref, entry: entry})
			}
			start := line
			if position == 0 && !section.collapsible {
				start = headerLine // Keep the active heading with its first task.
			}
			layout.entries = append(layout.entries, entry)
			layout.bounds[entry] = entryBounds{line: line, start: start, end: len(layout.rows) - 1}
		}
	}
	return layout
}

func (m model) selectedEntry() visibleEntry {
	if m.selectedSection != "" {
		return visibleEntry{section: m.selectedSection}
	}
	return visibleEntry{task: m.cursor}
}

func (m *model) selectEntry(entry visibleEntry) {
	m.selectedSection = entry.section
	if entry.section == "" {
		m.cursor = entry.task
	}
}

// The only overview task lookup. A header or hidden child cannot trigger task
// actions, even while an asynchronous response is regrouping the task list.
func (m model) selectedTask() (protocol.Task, bool) {
	entry := m.selectedEntry()
	if entry.section != "" || entry.task < 0 || entry.task >= len(m.tasks) {
		return protocol.Task{}, false
	}
	if _, ok := m.overviewLayout().bounds[entry]; !ok {
		return protocol.Task{}, false
	}
	return m.tasks[entry.task], true
}

func (m *model) selectTaskCursor(cursor int) {
	group := m.tasks[cursor].DisplayGroup()
	if historyGroup(group) && !m.sectionExpanded(group) {
		m.selectEntry(visibleEntry{section: group})
	} else {
		m.selectEntry(visibleEntry{task: cursor})
	}
}

func (m *model) ensureVisibleSelection() {
	layout := m.overviewLayout()
	if _, ok := layout.bounds[m.selectedEntry()]; ok {
		return
	}
	if m.selectedSection == "" && m.cursor >= 0 && m.cursor < len(m.tasks) {
		m.selectTaskCursor(m.cursor)
		if _, ok := layout.bounds[m.selectedEntry()]; ok {
			return
		}
	}
	if len(layout.entries) > 0 {
		m.selectEntry(layout.entries[0])
	} else {
		m.cursor, m.selectedSection, m.scroll = 0, "", 0
	}
}

func (m *model) moveCursor(delta int) {
	layout := m.overviewLayout()
	if len(layout.entries) == 0 {
		m.ensureVisibleSelection()
		return
	}
	position := m.cursorPosition()
	if position < 0 {
		position = 0
	} else {
		position = max(0, min(position+delta, len(layout.entries)-1))
	}
	m.selectEntry(layout.entries[position])
	m.syncTaskScroll()
}

func (m *model) moveCursorToEdge(last bool) {
	entries := m.overviewLayout().entries
	if len(entries) == 0 {
		m.ensureVisibleSelection()
		return
	}
	position := 0
	if last {
		position = len(entries) - 1
	}
	m.selectEntry(entries[position])
	m.syncTaskScroll()
}

func (m *model) moveCursorPage(direction int) {
	layout := m.overviewLayout()
	current, ok := layout.bounds[m.selectedEntry()]
	if !ok || direction == 0 {
		m.ensureVisibleSelection()
		m.syncTaskScroll()
		return
	}
	height := m.taskListHeight(m.taskListWidth())
	targetLine := max(0, min(current.line+direction*height, len(layout.rows)-1))
	best := m.selectedEntry()
	bestDistance := len(layout.rows) + height
	for _, entry := range layout.entries {
		line := layout.bounds[entry].line
		if (direction < 0 && line >= current.line) || (direction > 0 && line <= current.line) {
			continue
		}
		distance := line - targetLine
		if distance < 0 {
			distance = -distance
		}
		if distance < bestDistance {
			best, bestDistance = entry, distance
		}
	}
	if best == m.selectedEntry() {
		return
	}
	m.selectEntry(best)
	m.scroll = max(0, min(m.scroll+direction*height, max(0, len(layout.rows)-height)))
	m.syncTaskScroll()
}

func (m model) cursorPosition() int {
	for position, entry := range m.overviewLayout().entries {
		if entry == m.selectedEntry() {
			return position
		}
	}
	return -1
}

func (m model) taskRowPositions() (map[visibleEntry]int, int) {
	layout := m.overviewLayout()
	positions := make(map[visibleEntry]int, len(layout.entries))
	for entry, bounds := range layout.bounds {
		positions[entry] = bounds.line
	}
	return positions, len(layout.rows)
}
