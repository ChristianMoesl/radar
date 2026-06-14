package tui

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"radar.nvim/internal/client"
	"radar.nvim/internal/protocol"
)

type fetchMsg struct {
	response protocol.Response
	err      error
}

type actionMsg struct {
	response *protocol.Response
	err      error
	quit     bool
}

type model struct {
	socketPath string
	width      int
	height     int
	loading    bool
	err        error
	summary    protocol.Summary
	tasks      []protocol.Task
	sources    []protocol.SourceStatus
	cursor     int
}

var (
	appStyle = lipgloss.NewStyle().Padding(1, 2)

	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("63")).
			Padding(1, 2)

	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("228"))

	subtleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
	helpStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))

	urgentStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
	attentionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	progressStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("75")).Bold(true)
	doneStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("120")).Bold(true)
	lowStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Bold(true)

	selectedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("230")).
			Background(lipgloss.Color("63")).
			Bold(true)
)

func Run(socketPath string) error {
	program := tea.NewProgram(newModel(socketPath), tea.WithAltScreen())
	_, err := program.Run()
	return err
}

func newModel(socketPath string) model {
	return model{socketPath: socketPath, loading: true}
}

func (m model) Init() tea.Cmd {
	return m.fetch("tasks")
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "r":
			m.loading = true
			m.err = nil
			return m, m.fetch("refresh")
		case "j", "down":
			if m.cursor < len(m.tasks)-1 {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "g", "home":
			m.cursor = 0
		case "G", "end":
			if len(m.tasks) > 0 {
				m.cursor = len(m.tasks) - 1
			}
		case "enter":
			if len(m.tasks) > 0 {
				m.loading = true
				m.err = nil
				return m, m.openSelected()
			}
		}
	case fetchMsg:
		m.loading = false
		m.err = msg.err
		if msg.err == nil {
			if msg.response.Summary != nil {
				m.summary = *msg.response.Summary
			}
			m.tasks = msg.response.Tasks
			m.sources = msg.response.Sources
			if m.cursor >= len(m.tasks) {
				m.cursor = max(0, len(m.tasks)-1)
			}
		}
	case actionMsg:
		m.loading = false
		m.err = msg.err
		if msg.response != nil {
			if msg.response.Summary != nil {
				m.summary = *msg.response.Summary
			}
			m.tasks = msg.response.Tasks
			m.sources = msg.response.Sources
			if m.cursor >= len(m.tasks) {
				m.cursor = max(0, len(m.tasks)-1)
			}
		}
		if msg.quit && msg.err == nil {
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m model) View() string {
	contentWidth := m.width - 8
	if contentWidth < 60 {
		contentWidth = 60
	}

	var sections []string
	sections = append(sections, m.header(contentWidth))

	if m.err != nil {
		sections = append(sections, errorStyle.Render("Could not load Radar tasks: "+m.err.Error()))
	} else if m.loading && len(m.tasks) == 0 {
		sections = append(sections, subtleStyle.Render("Loading tasks…"))
	} else if len(m.tasks) == 0 {
		sections = append(sections, subtleStyle.Render("No tasks need your attention."))
	} else {
		sections = append(sections, m.taskList(contentWidth))
	}

	if len(m.sources) > 0 {
		sections = append(sections, m.sourceList(contentWidth))
	}

	sections = append(sections, helpStyle.Render("↑/k ↓/j select • enter open/switch • r refresh • q quit"))

	panel := panelStyle.Width(contentWidth).Render(strings.Join(sections, "\n\n"))
	return appStyle.Render(panel)
}

func (m model) fetch(method string) tea.Cmd {
	return func() tea.Msg {
		res, err := client.Call(m.socketPath, method)
		if err != nil {
			return fetchMsg{err: err}
		}
		if !res.OK {
			return fetchMsg{err: fmt.Errorf("%s", res.Error)}
		}
		return fetchMsg{response: res}
	}
}

func (m model) openSelected() tea.Cmd {
	task := m.tasks[m.cursor]
	return func() tea.Msg {
		if target := tmuxSessionTarget(task); target != "" {
			if err := exec.Command("tmux", "switch-client", "-t", target).Run(); err != nil {
				return actionMsg{err: err}
			}
			return actionMsg{quit: true}
		}

		var response *protocol.Response
		if task.ID != 0 {
			res, err := client.Call(m.socketPath, "ack:"+fmt.Sprint(task.ID))
			if err != nil {
				return actionMsg{err: err}
			}
			if !res.OK {
				return actionMsg{err: fmt.Errorf("%s", res.Error)}
			}
			response = &res
		}

		if task.URL != "" {
			if err := openURL(task.URL); err != nil {
				return actionMsg{response: response, err: err}
			}
		}
		return actionMsg{response: response}
	}
}

func tmuxSessionTarget(task protocol.Task) string {
	if task.Kind == "session" {
		if target := metadataValue(task.Metadata, "switch_target", "session_id", "session"); target != "" {
			return target
		}
	}
	for _, ref := range task.SourceRefs {
		if ref.Source == "tmux" && ref.Kind == "session" {
			if target := metadataValue(ref.Metadata, "switch_target", "session_id", "session"); target != "" {
				return target
			}
			return ref.Title
		}
	}
	return ""
}

func metadataValue(metadata map[string]string, keys ...string) string {
	for _, key := range keys {
		if metadata[key] != "" {
			return metadata[key]
		}
	}
	return ""
}

func openURL(url string) error {
	command := "xdg-open"
	if runtime.GOOS == "darwin" {
		command = "open"
	}
	return exec.Command(command, url).Start()
}

func (m model) header(width int) string {
	counts := strings.Join([]string{
		urgentStyle.Render(fmt.Sprintf("🚨 %d urgent", m.summary.Immediate)),
		attentionStyle.Render(fmt.Sprintf("👀 %d attention", m.summary.Attention)),
		progressStyle.Render(fmt.Sprintf("⏳ %d progress", m.summary.InProgress)),
		doneStyle.Render(fmt.Sprintf("✅ %d done", m.summary.Done)),
		lowStyle.Render(fmt.Sprintf("🔇 %d low", m.summary.LowPriority)),
	}, "  ")

	status := ""
	if m.loading {
		status = subtleStyle.Render("refreshing…")
	}

	return lipgloss.JoinHorizontal(lipgloss.Top,
		titleStyle.Render("Radar"),
		"  ",
		counts,
		strings.Repeat(" ", max(0, width-lipgloss.Width("Radar  "+counts+status)-4)),
		status,
	)
}

func (m model) taskList(width int) string {
	groups := []struct {
		key   string
		title string
		style lipgloss.Style
	}{
		{key: "immediate", title: "🚨 Need immediate attention", style: urgentStyle},
		{key: "attention", title: "👀 Need attention", style: attentionStyle},
		{key: "in_progress", title: "⏳ In progress", style: progressStyle},
		{key: "done", title: "✅ Done today", style: doneStyle},
		{key: "low_priority", title: "🔇 Low priority", style: lowStyle},
	}

	var lines []string
	for _, group := range groups {
		var groupLines []string
		for i, task := range m.tasks {
			if task.Attention != group.key {
				continue
			}
			line := taskLine(task)
			if i == m.cursor {
				line = selectedStyle.Width(width - 4).Render("› " + line)
			} else {
				line = "  " + line
			}
			groupLines = append(groupLines, line)

			for _, ref := range task.SourceRefs {
				groupLines = append(groupLines, subtleStyle.Render("    ↳ "+sourceRefLabel(ref)))
			}
		}
		if len(groupLines) > 0 {
			if len(lines) > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, group.style.Render(group.title))
			lines = append(lines, groupLines...)
		}
	}
	return strings.Join(lines, "\n")
}

func taskLine(task protocol.Task) string {
	title := task.Title
	if task.Repo != "" {
		title = fmt.Sprintf("%s  %s", title, subtleStyle.Render(task.Repo))
	}
	if task.Reason != "" {
		title = fmt.Sprintf("%s  %s", title, subtleStyle.Render(task.Reason))
	}
	return title
}

func sourceRefLabel(ref protocol.SourceRef) string {
	for _, value := range []string{ref.ID, ref.Title, ref.Repo, ref.Path, ref.Branch} {
		if value != "" {
			return value
		}
	}
	return ref.Source + ":" + ref.Kind
}

func (m model) sourceList(width int) string {
	var lines []string
	lines = append(lines, titleStyle.Render("Sources"))
	for _, source := range m.sources {
		statusStyle := sourceStatusStyle(source.Status)
		line := fmt.Sprintf("  %-8s %s  %4d refs", source.Name, statusStyle.Render(fmt.Sprintf("%-8s", source.Status)), source.SourceRefCount)
		if source.Detail != "" {
			line += "  " + subtleStyle.Render(source.Detail)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func sourceStatusStyle(status string) lipgloss.Style {
	switch status {
	case "ok":
		return doneStyle
	case "paused":
		return attentionStyle
	case "error":
		return urgentStyle
	default:
		return subtleStyle
	}
}
