package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"radar.nvim/internal/client"
	"radar.nvim/internal/filters"
	"radar.nvim/internal/protocol"
	"radar.nvim/internal/workstream"
)

type fetchMsg struct {
	response protocol.Response
	err      error
}

type actionMsg struct {
	response *protocol.Response
	err      error
	quit     bool
	refresh  bool
	message  string
}

type reposMsg struct {
	repos []string
	err   error
}

type branchesMsg struct {
	branches []string
	err      error
}

type picker struct {
	query   string
	options []string
	cursor  int
	loading bool
}

type createForm struct {
	repo     string
	base     string
	name     string
	repoList picker
	baseList picker
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
	mode       string
	create     createForm
	message    string
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
		if strings.HasPrefix(m.mode, "create_") {
			return m.updateCreate(msg)
		}
		if m.mode == "detail" {
			switch msg.String() {
			case "esc", "backspace", "h":
				m.mode = ""
				return m, nil
			case "q", "ctrl+c":
				return m, tea.Quit
			}
		}
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "c":
			m.mode = "create_repo"
			m.err = nil
			m.message = ""
			m.create = newCreateForm()
			return m, m.loadRepos()
		case "f":
			return m, m.openFilters()
		case "i", "right", "l":
			if len(m.tasks) > 0 {
				m.mode = "detail"
			}
		case "R":
			m.loading = true
			m.err = nil
			m.message = "Resetting…"
			return m, m.fetch("reset")
		case "r":
			m.loading = true
			m.err = nil
			m.message = "Refreshing…"
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
			m.message = ""
			if msg.response.Summary != nil {
				m.summary = *msg.response.Summary
			}
			m.tasks = msg.response.Tasks
			m.sources = msg.response.Sources
			if m.cursor >= len(m.tasks) {
				m.cursor = max(0, len(m.tasks)-1)
			}
		}
	case reposMsg:
		m.err = msg.err
		m.create.repoList.loading = false
		if msg.err == nil {
			m.create.repoList.options = msg.repos
			m.create.repoList.cursor = 0
		}
	case branchesMsg:
		m.err = msg.err
		m.create.baseList.loading = false
		if msg.err == nil {
			m.create.baseList.options = msg.branches
			m.create.baseList.cursor = 0
		}
	case actionMsg:
		m.loading = false
		m.err = msg.err
		m.message = msg.message
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
		if msg.refresh && msg.err == nil {
			m.loading = true
			return m, m.fetch("refresh")
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

	if m.mode == "detail" {
		sections = append(sections, m.detailView(contentWidth))
		sections = append(sections, helpStyle.Render("esc/backspace/h back • enter open/switch • q quit"))
		panel := panelStyle.Width(contentWidth).Render(strings.Join(sections, "\n\n"))
		return appStyle.Render(panel)
	}

	if strings.HasPrefix(m.mode, "create_") {
		sections = append(sections, m.createView(contentWidth))
		if m.err != nil {
			sections = append(sections, errorStyle.Render(m.err.Error()))
		}
		sections = append(sections, helpStyle.Render("type to filter • ↑/k ↓/j move • enter select/submit • esc cancel"))
		panel := panelStyle.Width(contentWidth).Render(strings.Join(sections, "\n\n"))
		return appStyle.Render(panel)
	}

	if m.message != "" {
		sections = append(sections, doneStyle.Render(m.message))
	}

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

	sections = append(sections, helpStyle.Render("↑/k ↓/j select • enter open/switch • i inspect • c create • f filters • r refresh • R reset • q quit"))

	panel := panelStyle.Width(contentWidth).Render(strings.Join(sections, "\n\n"))
	return appStyle.Render(panel)
}

func newCreateForm() createForm {
	return createForm{repoList: picker{loading: true}}
}

func (m model) updateCreate(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.mode = ""
		m.err = nil
		return m, nil
	case "up", "k":
		m.moveCreateCursor(-1)
		return m, nil
	case "down", "j":
		m.moveCreateCursor(1)
		return m, nil
	case "enter":
		return m.selectCreateStep()
	case "backspace", "ctrl+h":
		m.backspaceCreateQuery()
		return m, nil
	}

	if msg.Type == tea.KeyRunes {
		m.appendCreateQuery(string(msg.Runes))
	}
	return m, nil
}

func (m model) loadRepos() tea.Cmd {
	return func() tea.Msg {
		cwd, err := os.Getwd()
		if err != nil {
			return reposMsg{err: err}
		}
		return reposMsg{repos: workstream.DiscoverRepos(context.Background(), workstream.ExecRunner{}, cwd)}
	}
}

func (m model) loadBranches(repo string) tea.Cmd {
	return func() tea.Msg {
		branches, err := workstream.Branches(context.Background(), workstream.ExecRunner{}, repo)
		return branchesMsg{branches: branches, err: err}
	}
}

func (m *model) moveCreateCursor(delta int) {
	list := m.activePicker()
	if list == nil {
		return
	}
	matches := filteredOptions(*list)
	if len(matches) == 0 {
		list.cursor = 0
		return
	}
	list.cursor = (list.cursor + delta + len(matches)) % len(matches)
}

func (m model) selectCreateStep() (tea.Model, tea.Cmd) {
	switch m.mode {
	case "create_repo":
		selected := selectedPickerOption(m.create.repoList)
		if selected == "" {
			m.err = fmt.Errorf("select a repository")
			return m, nil
		}
		m.create.repo = selected
		m.create.baseList = picker{loading: true}
		m.mode = "create_base"
		m.err = nil
		return m, m.loadBranches(selected)
	case "create_base":
		selected := selectedPickerOption(m.create.baseList)
		if selected == "" {
			m.err = fmt.Errorf("select a base branch")
			return m, nil
		}
		m.create.base = selected
		m.mode = "create_name"
		m.err = nil
		return m, nil
	case "create_name":
		return m.submitCreate()
	}
	return m, nil
}

func (m model) submitCreate() (tea.Model, tea.Cmd) {
	if strings.TrimSpace(m.create.repo) == "" || strings.TrimSpace(m.create.base) == "" || strings.TrimSpace(m.create.name) == "" {
		m.err = fmt.Errorf("repo, base, and name are required")
		return m, nil
	}
	form := m.create
	m.mode = ""
	m.loading = true
	m.err = nil
	m.message = "Creating workstream…"
	return m, func() tea.Msg {
		created, err := workstream.Create(context.Background(), workstream.ExecRunner{}, workstream.CreateOptions{
			Repo:   form.repo,
			Base:   form.base,
			Name:   form.name,
			Switch: os.Getenv("TMUX") != "",
		})
		if err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{message: "Created " + created.SessionName, refresh: true}
	}
}

func (m *model) appendCreateQuery(value string) {
	switch m.mode {
	case "create_repo":
		m.create.repoList.query += value
		m.create.repoList.cursor = 0
	case "create_base":
		m.create.baseList.query += value
		m.create.baseList.cursor = 0
	case "create_name":
		m.create.name += value
	}
}

func (m *model) backspaceCreateQuery() {
	switch m.mode {
	case "create_repo":
		m.create.repoList.query = dropLastRune(m.create.repoList.query)
		m.create.repoList.cursor = 0
	case "create_base":
		m.create.baseList.query = dropLastRune(m.create.baseList.query)
		m.create.baseList.cursor = 0
	case "create_name":
		m.create.name = dropLastRune(m.create.name)
	}
}

func (m *model) activePicker() *picker {
	switch m.mode {
	case "create_repo":
		return &m.create.repoList
	case "create_base":
		return &m.create.baseList
	default:
		return nil
	}
}

func dropLastRune(value string) string {
	if value == "" {
		return ""
	}
	runes := []rune(value)
	return string(runes[:len(runes)-1])
}

func selectedPickerOption(list picker) string {
	matches := filteredOptions(list)
	if len(matches) == 0 {
		return ""
	}
	if list.cursor >= len(matches) {
		return matches[len(matches)-1]
	}
	return matches[list.cursor]
}

func filteredOptions(list picker) []string {
	if list.query == "" {
		return list.options
	}
	matches := make([]string, 0, len(list.options))
	query := strings.ToLower(list.query)
	for _, option := range list.options {
		if fuzzyMatch(strings.ToLower(option), query) {
			matches = append(matches, option)
		}
	}
	return matches
}

func fuzzyMatch(value string, query string) bool {
	for _, r := range query {
		index := strings.IndexRune(value, r)
		if index < 0 {
			return false
		}
		value = value[index+len(string(r)):]
	}
	return true
}

func (m model) createView(width int) string {
	switch m.mode {
	case "create_repo":
		return m.pickerView(width, "Create workstream", "Repository", m.create.repoList)
	case "create_base":
		return strings.Join([]string{
			subtleStyle.Render("Repository " + shortenPath(m.create.repo)),
			m.pickerView(width, "Create workstream", "Base branch", m.create.baseList),
		}, "\n")
	case "create_name":
		name := m.create.name
		if name == "" {
			name = subtleStyle.Render("type a workstream name")
		}
		return strings.Join([]string{
			titleStyle.Render("Create workstream"),
			subtleStyle.Render("Repository " + shortenPath(m.create.repo)),
			subtleStyle.Render("Base       " + m.create.base),
			selectedStyle.Width(width - 4).Render("› Name       " + name),
		}, "\n")
	default:
		return ""
	}
}

func (m model) pickerView(width int, title string, label string, list picker) string {
	lines := []string{titleStyle.Render(title), label + ": " + list.query}
	if list.loading {
		lines = append(lines, subtleStyle.Render("Loading…"))
		return strings.Join(lines, "\n")
	}
	matches := filteredOptions(list)
	if len(matches) == 0 {
		lines = append(lines, subtleStyle.Render("No matches"))
		return strings.Join(lines, "\n")
	}
	limit := min(len(matches), 10)
	start := 0
	if list.cursor >= limit {
		start = list.cursor - limit + 1
	}
	for i := start; i < start+limit; i++ {
		line := shortenPath(matches[i])
		if i == list.cursor {
			line = selectedStyle.Width(width - 4).Render("› " + line)
		} else {
			line = "  " + line
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func shortenPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+string(os.PathSeparator)) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

func (m model) detailView(width int) string {
	if len(m.tasks) == 0 {
		return subtleStyle.Render("No task selected.")
	}
	task := m.tasks[m.cursor]
	lines := []string{titleStyle.Render(task.Title)}
	appendDetailLine := func(label string, value string) {
		if value != "" {
			lines = append(lines, fmt.Sprintf("%-10s %s", label, value))
		}
	}
	appendDetailLine("Status", task.Attention)
	appendDetailLine("Reason", task.Reason)
	appendDetailLine("Repo", task.Repo)
	appendDetailLine("URL", task.URL)
	if len(task.Metadata) > 0 {
		lines = append(lines, "", titleStyle.Render("Metadata"))
		for key, value := range task.Metadata {
			appendDetailLine(key, value)
		}
	}
	if len(task.SourceRefs) > 0 {
		lines = append(lines, "", titleStyle.Render("Source refs"))
		for _, ref := range task.SourceRefs {
			line := "  " + sourceRefLabel(ref)
			if ref.URL != "" {
				line += "  " + subtleStyle.Render(ref.URL)
			}
			lines = append(lines, line)
			appendRefDetail := func(label string, value string) {
				if value != "" {
					lines = append(lines, subtleStyle.Render(fmt.Sprintf("    %-8s %s", label, value)))
				}
			}
			appendRefDetail("source", ref.Source)
			appendRefDetail("kind", ref.Kind)
			appendRefDetail("repo", ref.Repo)
			appendRefDetail("path", shortenPath(ref.Path))
			appendRefDetail("branch", ref.Branch)
		}
	}
	return strings.Join(lines, "\n")
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

func (m model) openFilters() tea.Cmd {
	path, err := filters.EnsureFile()
	if err != nil {
		return func() tea.Msg { return actionMsg{err: err} }
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	return tea.ExecProcess(exec.Command(editor, path), func(err error) tea.Msg {
		if err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{message: "Filters saved", refresh: true}
	})
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
