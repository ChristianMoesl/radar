package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"radar/internal/protocol"
)

func (m model) filteringTasks() bool {
	return strings.TrimSpace(m.searchQuery) != ""
}

// Search the existing projection, not providers or arbitrary source metadata.
// Terms may match different fields; no source-specific identifier parsing is needed.
func matchesTaskSearch(task protocol.Task, terms []string) bool {
	if len(terms) == 0 {
		return true
	}
	fields := []string{task.Title, task.Repo}
	for _, ref := range task.SourceRefs {
		fields = append(fields, ref.Title, ref.ID, ref.EntityID, ref.Repo, ref.Branch,
			ref.Path, ref.Presentation.Label, ref.Presentation.WorkspaceName)
		fields = append(fields, ref.LinkingKeys...)
	}
	text := strings.ToLower(strings.Join(fields, "\n"))
	for _, term := range terms {
		if !strings.Contains(text, term) {
			return false
		}
	}
	return true
}

func (m model) searchTerms() []string {
	return strings.Fields(strings.ToLower(m.searchQuery))
}

func (m model) updateSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.mode, m.searchQuery = "", ""
	case "enter":
		m.mode = ""
		m.searchQuery = strings.TrimSpace(m.searchQuery)
	case "up", "ctrl+p":
		m.moveCursor(-1)
		return m, nil
	case "down", "ctrl+n":
		m.moveCursor(1)
		return m, nil
	case "backspace", "ctrl+h":
		m.searchQuery = dropLastRune(m.searchQuery)
	default:
		if value, ok := textInputValue(msg); ok {
			m.searchQuery += value
		}
	}
	m.syncTaskScroll()
	return m, nil
}

func (m model) searchBar(width int) string {
	if m.mode != "search" && m.searchQuery == "" {
		return ""
	}
	matches := 0
	terms := m.searchTerms()
	for _, task := range m.tasks {
		if matchesTaskSearch(task, terms) {
			matches++
		}
	}
	count := fmt.Sprintf("  %d/%d", matches, len(m.tasks))
	query := m.searchQuery
	if m.mode == "search" {
		query += "▏"
	}
	query = strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(query)
	return truncateLine(helpKeyStyle.Render("/ ")+inputTail(query, max(1, width-2-lipgloss.Width(count)))+subtleStyle.Render(count), width)
}

func (m model) beforeTaskSections(width int) []string {
	sections := []string{m.header(m.contentWidth())}
	if bar := m.searchBar(width); bar != "" {
		sections = append(sections, bar)
	}
	return sections
}

func searchHelpGroups() [][]keyHint {
	return [][]keyHint{{
		{"↑/ctrl+p ↓/ctrl+n", "select"},
		{"enter", "navigate results"},
		{"esc", "clear search"},
		{"ctrl+c", "quit"},
	}}
}

func (m model) searchRail(height int) string {
	lines := []string{referenceStyle.Render("KEYBOARD"), "", titleStyle.Bold(false).Render("Search")}
	for _, hint := range []keyHint{{"↑ ↓", "Select"}, {"enter", "Navigate results"}, {"Esc", "Clear search"}, {"Ctrl+C", "Quit"}} {
		lines = append(lines, railHintLine(hint))
	}
	lines = append(lines, "", referenceStyle.Render("Type to filter · Backspace erase"), referenceStyle.Render("Ctrl+P/N select"))
	return lipgloss.NewStyle().Width(mainRailWidth).Height(height).Render(strings.Join(lines, "\n"))
}
