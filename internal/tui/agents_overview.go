package tui

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"radar/internal/protocol"
)

// Both overviews are session-local. Workspaces keeps the existing cursor,
// scroll, section and query fields; Agents never borrows those task indices.
type agentsOverview struct {
	rows           []protocol.SourceRef
	selected       string
	cursor, scroll int
	query, problem string
}

func durableAgentRef(ref protocol.SourceRef) bool {
	return ref.Source == "agent" && ref.Kind == "agent" && ref.AgentID != ""
}

func agentTitle(ref protocol.SourceRef) string {
	if title := strings.TrimSpace(ansi.Strip(ref.Title)); title != "" {
		return title
	}
	return ref.AgentID
}

func agentNeedsAttention(ref protocol.SourceRef) bool {
	return ref.Metadata["attention"] == "true" || ref.Metadata["runtime_error"] != ""
}

// Pure presentation policy. All original source facts stay on the task and in
// rawTasks for detail/resource operations; no server lifecycle state is changed.
func workspaceTasks(tasks []protocol.Task, agents []protocol.SourceRef) []protocol.Task {
	var result []protocol.Task
	for _, task := range tasks {
		ignored := func(ref protocol.SourceRef) bool {
			if durableAgentRef(ref) {
				return true
			}
			for _, agent := range agents {
				owned := agent.Metadata["owned_workspace_id"]
				if owned == "" {
					continue
				}
				if ref.WorkspaceID == owned && slices.Contains([]string{"workspace", "git", "tmux", "sbx"}, ref.Source) {
					return true
				}
				key := agent.Metadata["owned_note_key"]
				if ref.Authored && key != "" && ref.WorkspaceAnchorPath == agent.Metadata["owned_note_path"] && (ref.CanonicalKey == key || slices.Contains(ref.LinkingKeys, key)) {
					return true
				}
			}
			return false
		}
		if !slices.ContainsFunc(task.SourceRefs, ignored) {
			result = append(result, task)
			continue
		}
		projected, visible := protocol.ProjectAttention(task, func(ref protocol.SourceRef) protocol.ContributionAction {
			if ignored(ref) {
				return protocol.ContributionMute
			}
			return protocol.ContributionKeep
		})
		if !visible {
			continue
		}
		// An independently linked note/provider remains ordinary work, even if
		// the original title came from the now-separated agent contribution.
		if slices.ContainsFunc(task.SourceRefs, func(ref protocol.SourceRef) bool { return ignored(ref) && ref.Title == task.Title }) {
			for _, ref := range task.SourceRefs {
				if !ignored(ref) && ref.Role == protocol.SourceRefRoleAuthoritative && ref.Title != "" {
					projected.Title = ref.Title
					break
				}
			}
		}
		result = append(result, projected)
	}
	return result
}

func (m *model) refreshAgents() {
	problem := ""
	for _, source := range m.sources {
		if source.Name == "agent" && (source.Status == "error" || source.Status == "partial") {
			problem = source.Detail
			if problem == "" {
				problem = "Agent collection unavailable"
			}
		}
	}
	byID := map[string]protocol.SourceRef{}
	// Incomplete collection cannot prove that a registration disappeared.
	if problem != "" {
		for _, ref := range m.agents.rows {
			byID[ref.AgentID] = ref
		}
	}
	for _, task := range m.rawTasks {
		for _, ref := range task.SourceRefs {
			if durableAgentRef(ref) {
				byID[ref.AgentID] = ref
			}
		}
	}
	m.agents.rows = nil
	for _, ref := range byID {
		m.agents.rows = append(m.agents.rows, ref)
	}
	sort.Slice(m.agents.rows, func(i, j int) bool {
		a, b := m.agents.rows[i], m.agents.rows[j]
		if agentNeedsAttention(a) != agentNeedsAttention(b) {
			return agentNeedsAttention(a)
		}
		left, right := strings.ToLower(agentTitle(a)), strings.ToLower(agentTitle(b))
		if left != right {
			return left < right
		}
		return a.AgentID < b.AgentID
	})
	m.agents.problem = problem
	m.syncAgentsSelection()
	if m.mode == "agent" || m.detailReturn == "agent" {
		_, found := byID[m.agent.id]
		if found {
			m.agent.unavailable = false
		} else if problem == "" {
			m.agent.unavailable = true
		}
	}
}

func (m model) visibleAgents() []protocol.SourceRef {
	var rows []protocol.SourceRef
	terms := strings.Fields(strings.ToLower(m.agents.query))
	for _, ref := range m.agents.rows {
		text := strings.ToLower(ref.AgentID + "\n" + agentTitle(ref) + "\n" + ref.Metadata["summary"])
		if slices.ContainsFunc(terms, func(term string) bool { return !strings.Contains(text, term) }) {
			continue
		}
		rows = append(rows, ref)
	}
	return rows
}

func (m *model) syncAgentsSelection() {
	rows := m.visibleAgents()
	for i, ref := range rows {
		if ref.AgentID == m.agents.selected {
			m.agents.cursor = i
			break
		}
	}
	m.agents.cursor = max(0, min(m.agents.cursor, len(rows)-1))
	if len(rows) > 0 {
		m.agents.selected = rows[m.agents.cursor].AgentID
	} else {
		m.agents.selected = ""
	}
	m.agents.scroll, _ = selectionWindow(m.agents.cursor, len(rows), m.agents.scroll, m.agentListPageSize())
}

func (m model) overviewHeader(width int) string {
	return m.header(width) + "\n" + m.overviewNavigation(width)
}

func (m model) overviewNavigation(width int) string {
	label := fmt.Sprintf("Agents %d", len(m.agents.rows))
	count := 0
	for _, ref := range m.agents.rows {
		if agentNeedsAttention(ref) {
			count++
		}
	}
	if count > 0 {
		label += fmt.Sprintf(" · !%d", count)
	}
	if m.agents.problem != "" {
		if len(m.agents.rows) == 0 {
			label = "Agents ? · !"
		} else {
			label += " · ?"
		}
	}
	workspaces := "Workspaces"
	if m.activeView == "agents" {
		label = "[" + label + "]"
	} else {
		workspaces = "[" + workspaces + "]"
	}
	text := workspaces + "   " + label
	hint := "Tab: switch view"
	if ansi.StringWidth(text)+3+len(hint) <= width {
		return helpStyle.Render(text + "   " + hint)
	}
	return wrappedHelp(text, width) + "\n" + wrappedHelp(hint, width)
}

func (m model) agentOverviewHelp(width int) string {
	if m.mode == "search" && m.activeView == "agents" {
		return renderKeyHints(width, searchHelpGroups())
	}
	groups := [][]keyHint{{{"↑/k ↓/j", "select"}, {"enter/i", "agent details"}, {"/", "search agents"}}}
	var global []keyHint
	for _, hint := range mainKeyHints[1] {
		if !slices.Contains([]string{"w", "x", "/", "esc"}, hint.keys) {
			global = append(global, hint)
		}
	}
	if m.agents.query != "" {
		global = append(global, keyHint{"esc", "clear search"})
	}
	groups = append(groups, global, []keyHint{{"n", "new task"}})
	return renderKeyHints(width, groups)
}

func (m model) agentOverviewSections() ([]string, []string) {
	width := m.contentWidth()
	before := []string{m.overviewHeader(width)}
	if m.agents.problem != "" {
		before = append(before, wrappedHelp("Agent collection failed (last-known information): "+singleLine(m.agents.problem, width), width))
	}
	if (m.mode == "search" && m.activeView == "agents") || m.agents.query != "" {
		query := m.agents.query
		if m.mode == "search" && m.activeView == "agents" {
			query += "▏"
		}
		before = append(before, singleLine("/ "+query, width))
	}
	if m.err != nil {
		before = append(before, singleLine(m.err.Error(), width))
	}
	if m.message != "" {
		before = append(before, singleLine(m.message, width))
	}
	after := []string{}
	if len(m.sources) > 0 {
		after = append(after, m.sourceList(width))
	}
	after = append(after, m.agentOverviewHelp(width))
	return before, after
}

func (m model) agentListPageSize() int {
	before, after := m.agentOverviewSections()
	return max(1, m.availableTaskRows(before, after)/2)
}

func (m model) agentsOverviewView() string {
	width := m.contentWidth()
	before, after := m.agentOverviewSections()
	rows := m.visibleAgents()
	height := m.availableTaskRows(before, after)
	start, end := selectionWindow(m.agents.cursor, len(rows), m.agents.scroll, max(1, height/2))
	var lines []string
	for i := start; i < end; i++ {
		ref := rows[i]
		status := "Status unavailable"
		switch ref.Metadata["running"] {
		case "true":
			status = "Running"
		case "false":
			status = "Stopped"
		}
		if ref.Metadata["attention"] == "true" {
			status += " · Attention"
		}
		if ref.Metadata["runtime_error"] != "" {
			status += " · Error"
		}
		prefix := "  "
		if agentNeedsAttention(ref) {
			prefix = "! "
		}
		if i == m.agents.cursor {
			prefix = "›" + prefix
		}
		// Essential status wins over the title and the optional summary.
		title := singleLine(agentTitle(ref), max(1, width-ansi.StringWidth(prefix)-ansi.StringWidth(status)-3))
		line := singleLine(prefix+title+" · "+status, width)
		compactStatus := ansi.StringWidth(prefix)+ansi.StringWidth(status)+4 > width
		if compactStatus {
			line = singleLine(prefix+agentTitle(ref), width)
		}
		if i == m.agents.cursor {
			line = selectedStyle.Render(line)
		}
		lines = append(lines, line)
		if compactStatus && height > 1 {
			lines = append(lines, singleLine(status, width))
			continue
		}
		if height > 1 {
			summary := strings.TrimSpace(ansi.Strip(ref.Metadata["summary"]))
			if summary == "" {
				summary = "No summary available"
			}
			lines = append(lines, singleLine("  "+summary, width))
		}
	}
	if len(rows) == 0 {
		empty := "No durable agents registered"
		if m.agents.problem != "" {
			empty = "Agent collection unavailable"
		} else if len(m.agents.rows) > 0 {
			empty = "No agents match your search"
		}
		lines = append(lines, subtleStyle.Render(singleLine(empty, width)))
	}
	list := strings.Join(lines, "\n")
	if m.height > 0 {
		list = lipgloss.NewStyle().Height(height).Render(list)
	}
	sections := append(before, list)
	sections = append(sections, after...)
	return m.renderFrame(strings.Join(sections, "\n\n"), width)
}

func (m model) updateAgentsOverview(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := m.visibleAgents()
	switch msg.String() {
	case "/":
		m.mode = "search"
	case "esc":
		if m.agents.query != "" {
			m.agents.query = ""
		} else {
			return m, tea.Quit
		}
	case "up", "k", "ctrl+p":
		m.agents.cursor--
		m.agents.selected = ""
	case "down", "j", "ctrl+n":
		m.agents.cursor++
		m.agents.selected = ""
	case "ctrl+u", "pgup":
		m.agents.cursor -= m.agentListPageSize()
		m.agents.selected = ""
	case "ctrl+d", "pgdown":
		m.agents.cursor += m.agentListPageSize()
		m.agents.selected = ""
	case "home", "g":
		m.agents.cursor = 0
		m.agents.selected = ""
	case "end", "G":
		m.agents.cursor = len(rows) - 1
		m.agents.selected = ""
	case "enter", "i", "right":
		if len(rows) > 0 {
			return m.openAgent(rows[m.agents.cursor].AgentID)
		}
	}
	m.syncAgentsSelection()
	return m, nil
}

func (m model) updateAgentsSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.mode = ""
		m.agents.query = ""
	case "enter":
		m.mode = ""
		m.agents.query = strings.TrimSpace(m.agents.query)
	case "up", "ctrl+p", "down", "ctrl+n":
		return m.updateAgentsOverview(msg)
	case "backspace", "ctrl+h":
		m.agents.query = dropLastRune(m.agents.query)
	default:
		if value, ok := textInputValue(msg); ok {
			m.agents.query += value
		}
	}
	m.syncAgentsSelection()
	return m, nil
}

func (m model) associatedWorkspaceTask() (protocol.Task, bool) {
	for _, task := range m.rawTasks {
		for _, ref := range task.SourceRefs {
			if ref.Source == "workspace" && ref.WorkspaceID == m.agent.status.Workspace.ID && ref.Path == m.agent.status.Workspace.Path && ref.WorkspaceID != "" {
				return task, true
			}
		}
	}
	return protocol.Task{}, false
}
