package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"radar.nvim/internal/client"
	"radar.nvim/internal/config"
	"radar.nvim/internal/protocol"
	"radar.nvim/internal/workspace"
)

type fetchMsg struct {
	response protocol.Response
	err      error
}

type watchMsg struct {
	response protocol.Response
	err      error
}

type watchRetryMsg struct{}

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

type deletePreviewMsg struct {
	preview deletePreview
	err     error
}

type preparingWorkspaceMsg struct{}

type deletePreview struct {
	Path        string
	Branch      string
	SessionName string
	Dirty       bool
	SessionOnly bool
}

type linkChoice struct {
	Key    string
	Source string
	Label  string
	URL    string
}

type picker struct {
	query   string
	options []string
	cursor  int
	loading bool
}

type createForm struct {
	repo           string
	base           string
	name           string
	forkPiSession  string
	sourceRepoName string
	repoList       picker
	baseList       picker
}

type model struct {
	socketPath          string
	width               int
	height              int
	loading             bool
	err                 error
	summary             protocol.Summary
	tasks               []protocol.Task
	sources             []protocol.SourceStatus
	cursor              int
	selectedCurrentTask bool
	mode                string
	create              createForm
	delete              deletePreview
	links               []linkChoice
	worktrees           []protocol.SourceRef
	worktreeCursor      int
	message             string
	scroll              int
	revision            int64
	watching            bool
}

const (
	creatingWorkspaceMessage  = "Creating Workspace..."
	preparingWorkspaceMessage = "Preparing workspace..."
)

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

	notificationStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("120")).
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("36")).
				Padding(0, 1)
)

func Run(socketPath string) error {
	program := tea.NewProgram(newModel(socketPath), tea.WithAltScreen())
	_, err := program.Run()
	return err
}

func RunCreate(socketPath string) error {
	model := newModel(socketPath)
	model.mode = "create_repo"
	model.create = newCreateForm()
	program := tea.NewProgram(model, tea.WithAltScreen())
	_, err := program.Run()
	return err
}

func RunFork(socketPath string) error {
	form, err := newForkCreateForm()
	if err != nil {
		return err
	}
	model := newModel(socketPath)
	model.mode = "create_base"
	model.create = form
	program := tea.NewProgram(model, tea.WithAltScreen())
	_, err = program.Run()
	return err
}

func newModel(socketPath string) model {
	return model{socketPath: socketPath, loading: true}
}

func (m model) Init() tea.Cmd {
	commands := []tea.Cmd{m.fetch("tasks")}
	if m.mode == "create_repo" {
		commands = append(commands, m.loadRepos())
	}
	if m.mode == "create_base" && m.create.repo != "" {
		commands = append(commands, m.loadBranches(m.create.repo))
	}
	return tea.Batch(commands...)
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
		if m.mode == "open_link" {
			switch msg.String() {
			case "esc", "backspace", "h":
				m.mode = ""
				m.links = nil
				return m, nil
			case "q", "ctrl+c":
				return m, tea.Quit
			default:
				link, ok := matchingLink(m.links, msg.String())
				if !ok {
					return m, nil
				}
				m.mode = ""
				m.links = nil
				m.loading = true
				m.err = nil
				return m, m.openTaskURL(m.tasks[m.cursor], link.URL)
			}
		}
		if m.mode == "worktree_session" {
			switch msg.String() {
			case "esc", "backspace", "h":
				m.mode = ""
				m.worktrees = nil
				m.worktreeCursor = 0
				return m, nil
			case "q", "ctrl+c":
				return m, tea.Quit
			case "j", "down", "ctrl+n":
				if m.worktreeCursor < len(m.worktrees)-1 {
					m.worktreeCursor++
				}
				return m, nil
			case "k", "up", "ctrl+p":
				if m.worktreeCursor > 0 {
					m.worktreeCursor--
				}
				return m, nil
			case "enter":
				if len(m.worktrees) == 0 {
					return m, nil
				}
				ref := m.worktrees[m.worktreeCursor]
				m.mode = ""
				m.worktrees = nil
				m.worktreeCursor = 0
				m.loading = true
				m.err = nil
				m.message = "Creating tmux session…"
				return m, m.createSessionForWorktree(ref)
			default:
				return m, nil
			}
		}
		if m.mode == "delete_confirm" {
			switch msg.String() {
			case "y", "Y":
				preview := m.delete
				m.mode = ""
				m.loading = true
				m.err = nil
				m.message = "Deleting…"
				return m, m.deleteSelected(preview)
			case "esc", "backspace", "h", "n", "N":
				m.mode = ""
				m.err = nil
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
			return m, m.openConfig()
		case "i", "right", "l":
			if len(m.tasks) > 0 {
				m.mode = "detail"
			}
		case "o":
			if len(m.tasks) > 0 {
				m.links = taskLinks(m.tasks[m.cursor])
				if len(m.links) == 0 {
					m.message = "No link on selected task"
					return m, nil
				}
				m.mode = "open_link"
				m.message = ""
			}
		case "d":
			if len(m.tasks) > 0 {
				m.loading = true
				m.err = nil
				m.message = "Inspecting delete target…"
				return m, m.previewDelete(m.tasks[m.cursor])
			}
		case "D":
			if len(m.tasks) > 0 {
				hints := detectCurrentTaskHints()
				cursor, ok := taskCursorForHints(m.tasks, hints)
				if !ok {
					m.message = "No current workspace tracked by Radar"
					return m, nil
				}
				m.loading = true
				m.err = nil
				m.message = "Inspecting current workspace…"
				return m, m.previewCurrentWorkspaceDelete(m.tasks[cursor], hints)
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
		case "j", "down", "ctrl+n":
			m.moveCursor(1)
		case "k", "up", "ctrl+p":
			m.moveCursor(-1)
		case "g", "home":
			m.moveCursorToEdge(false)
		case "G", "end":
			m.moveCursorToEdge(true)
		case "enter":
			return m.activateSelected()
		}
	case fetchMsg:
		m.loading = false
		m.err = msg.err
		if msg.err == nil {
			m.message = ""
			m.applyResponse(msg.response, true)
			if !m.watching {
				m.watching = true
				return m, m.watch(m.revision)
			}
		}
	case watchMsg:
		m.watching = false
		if msg.err != nil {
			m.err = msg.err
			return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return watchRetryMsg{} })
		}
		m.err = nil
		m.applyResponse(msg.response, false)
		m.watching = true
		return m, m.watch(m.revision)
	case watchRetryMsg:
		if !m.watching {
			m.watching = true
			return m, m.watch(m.revision)
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
	case deletePreviewMsg:
		m.loading = false
		m.err = msg.err
		m.message = ""
		if msg.err == nil {
			m.delete = msg.preview
			m.mode = "delete_confirm"
		}
	case preparingWorkspaceMsg:
		if m.loading && m.message == creatingWorkspaceMessage {
			m.message = preparingWorkspaceMessage
		}
	case actionMsg:
		m.loading = false
		m.err = msg.err
		m.message = msg.message
		if msg.response != nil {
			m.applyResponse(*msg.response, false)
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

const maxContentWidth = 140

var taskGroupKeys = []string{"immediate", "attention", "in_progress", "done", "low_priority"}

func (m *model) moveCursor(delta int) {
	order := m.taskCursorOrder()
	if len(order) == 0 {
		return
	}

	position := -1
	for i, index := range order {
		if index == m.cursor {
			position = i
			break
		}
	}
	if position == -1 {
		m.cursor = order[0]
		return
	}

	position = max(0, min(position+delta, len(order)-1))
	m.cursor = order[position]
}

func (m *model) moveCursorToEdge(last bool) {
	order := m.taskCursorOrder()
	if len(order) == 0 {
		return
	}
	if last {
		m.cursor = order[len(order)-1]
		return
	}
	m.cursor = order[0]
}

func (m model) taskCursorOrder() []int {
	order := make([]int, 0, len(m.tasks))
	for _, key := range taskGroupKeys {
		for i, task := range m.tasks {
			if task.Attention == key {
				order = append(order, i)
			}
		}
	}
	return order
}

func (m model) View() string {
	contentWidth := m.contentWidth()

	var sections []string
	sections = append(sections, m.header(contentWidth))

	if m.mode == "detail" {
		sections = append(sections, m.detailView(contentWidth))
		sections = append(sections, helpStyle.Render("esc/backspace/h back • q quit"))
		return m.renderFrame(strings.Join(sections, "\n\n"), contentWidth)
	}

	if m.mode == "open_link" {
		sections = append(sections, m.openLinkView(contentWidth))
		if m.err != nil {
			sections = append(sections, errorStyle.Render(m.err.Error()))
		}
		sections = append(sections, helpStyle.Render("g GitHub • j Jira • esc cancel • q quit"))
		return m.renderFrame(strings.Join(sections, "\n\n"), contentWidth)
	}

	if m.mode == "delete_confirm" {
		sections = append(sections, m.deleteConfirmView(contentWidth))
		if m.err != nil {
			sections = append(sections, errorStyle.Render(m.err.Error()))
		}
		sections = append(sections, helpStyle.Render("y delete • esc/n cancel • q quit"))
		return m.renderFrame(strings.Join(sections, "\n\n"), contentWidth)
	}

	if m.mode == "worktree_session" {
		sections = append(sections, m.worktreeSessionView(contentWidth))
		if m.err != nil {
			sections = append(sections, errorStyle.Render(m.err.Error()))
		}
		sections = append(sections, helpStyle.Render("↑/k ↓/j move • enter create session • esc cancel • q quit"))
		return m.renderFrame(strings.Join(sections, "\n\n"), contentWidth)
	}

	if strings.HasPrefix(m.mode, "create_") {
		sections = append(sections, m.createView(contentWidth))
		if m.err != nil {
			sections = append(sections, errorStyle.Render(m.err.Error()))
		}
		sections = append(sections, helpStyle.Render("type to filter • ↑/k ↓/j move • enter select/submit • esc cancel"))
		return m.renderFrame(strings.Join(sections, "\n\n"), contentWidth)
	}

	if m.err != nil {
		sections = append(sections, errorStyle.Render("Could not load Radar tasks: "+m.err.Error()))
	} else if m.loading && len(m.tasks) == 0 {
		sections = append(sections, subtleStyle.Render("Loading tasks…"))
	} else if len(m.tasks) == 0 {
		sections = append(sections, subtleStyle.Render("No tasks need your attention."))
	} else {
		afterTaskSections := m.afterTaskSections(contentWidth)
		sections = append(sections, m.taskList(contentWidth, m.availableTaskRows(sections, afterTaskSections)))
		sections = append(sections, afterTaskSections...)
		return m.renderFrame(strings.Join(sections, "\n\n"), contentWidth)
	}

	sections = append(sections, m.afterTaskSections(contentWidth)...)
	return m.renderFrame(strings.Join(sections, "\n\n"), contentWidth)
}

func (m model) afterTaskSections(width int) []string {
	sections := []string{}
	if len(m.sources) > 0 {
		sections = append(sections, m.sourceList(width))
	}
	sections = append(sections, truncateLine(helpStyle.Render("↑/k/ctrl+p ↓/j/ctrl+n select • enter switch tmux • o open link • i inspect • c create • d delete • D delete current • f config • r refresh • R reset • q quit"), width))
	return sections
}

func (m model) availableTaskRows(before []string, after []string) int {
	if m.height <= 0 {
		return 20
	}
	used := m.frameHeight()
	for _, section := range before {
		used += lipgloss.Height(section)
	}
	for _, section := range after {
		used += lipgloss.Height(section)
	}
	used += max(0, len(before)+len(after))
	return max(3, m.height-used)
}

func (m model) frameHeight() int {
	if os.Getenv("TMUX") != "" {
		return 2
	}
	return 6
}

func (m model) contentWidth() int {
	if os.Getenv("TMUX") != "" {
		width := m.width - 4
		if width <= 0 {
			width = 80
		}
		return max(width, 60)
	}

	width := m.width - 8
	if width <= 0 {
		width = maxContentWidth
	}
	width = min(width, maxContentWidth)
	return max(width, 60)
}

func (m model) renderFrame(content string, width int) string {
	frame := appStyle.Width(width).Render(content)
	if os.Getenv("TMUX") == "" {
		frame = appStyle.Render(panelStyle.Width(width).Render(content))
	}
	return m.overlayNotification(frame)
}

func (m model) overlayNotification(frame string) string {
	if m.message == "" {
		return frame
	}

	lines := strings.Split(frame, "\n")
	popupLines := strings.Split(notificationStyle.Render(m.message), "\n")
	row := 1
	popupWidth := 0
	for _, popupLine := range popupLines {
		popupWidth = max(popupWidth, lipgloss.Width(popupLine))
	}
	for i, popupLine := range popupLines {
		target := row + i
		if target >= len(lines) {
			break
		}
		plainLine := ansi.Strip(lines[target])
		col := max(0, lipgloss.Width(plainLine)-popupWidth-2)
		prefix := takeCells(plainLine, col)
		rest := dropCells(plainLine, col+lipgloss.Width(popupLine))
		lines[target] = prefix + popupLine + rest
	}
	return strings.Join(lines, "\n")
}

func takeCells(s string, cells int) string {
	var out strings.Builder
	used := 0
	for _, r := range s {
		w := lipgloss.Width(string(r))
		if used+w > cells {
			break
		}
		out.WriteRune(r)
		used += w
	}
	return out.String()
}

func dropCells(s string, cells int) string {
	used := 0
	for i, r := range s {
		w := lipgloss.Width(string(r))
		if used+w > cells {
			return s[i:]
		}
		used += w
	}
	return ""
}

func newCreateForm() createForm {
	return createForm{repoList: picker{loading: true}}
}

func newCreateFormForTask(task protocol.Task) createForm {
	form := newCreateForm()
	form.name = workspaceNameForTask(task)
	return form
}

func workspaceNameForTask(task protocol.Task) string {
	if ref, ok := jiraIssueRef(task); ok {
		if title := strings.TrimSpace(ref.Title); title != "" {
			return title
		}
		if key := metadataValue(ref.Metadata, "key"); key != "" {
			return key
		}
		if key, ok := strings.CutPrefix(ref.ID, "jira:issue:"); ok {
			return key
		}
	}
	if ref, ok := githubPullRequestRef(task); ok {
		if name := pullRequestWorkspaceName(ref); name != "" {
			return name
		}
	}
	return strings.TrimSpace(task.Title)
}

func newForkCreateForm() (createForm, error) {
	if os.Getenv("TMUX") == "" {
		return createForm{}, fmt.Errorf("radar fork must run inside tmux")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return createForm{}, err
	}
	runner := workspace.ExecRunner{}
	repo, err := runner.Run(context.Background(), cwd, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return createForm{}, err
	}
	sessionName, err := runner.Run(context.Background(), cwd, "tmux", "display-message", "-p", "#{session_name}")
	if err != nil {
		return createForm{}, err
	}
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return createForm{}, fmt.Errorf("could not detect current tmux session")
	}
	currentBranch, _ := runner.Run(context.Background(), repo, "git", "branch", "--show-current")
	currentBranch = strings.TrimSpace(currentBranch)
	sourceRepoName := filepath.Base(repo)
	if root, err := workspace.DefaultRoot(); err == nil {
		if rel, err := filepath.Rel(root, repo); err == nil && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != ".." {
			parts := strings.Split(rel, string(os.PathSeparator))
			if len(parts) >= 2 && parts[0] != "." && parts[0] != "" {
				sourceRepoName = parts[0]
			}
		}
	}
	return createForm{
		repo:           repo,
		forkPiSession:  sessionName,
		sourceRepoName: sourceRepoName,
		baseList:       picker{loading: true, query: currentBranch},
	}, nil
}

func (m model) updateCreate(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.mode = ""
		m.err = nil
		return m, nil
	case "up", "k", "ctrl+p":
		m.moveCreateCursor(-1)
		return m, nil
	case "down", "j", "ctrl+n":
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
		repos, err := workspace.DiscoverRepos(context.Background(), workspace.ExecRunner{}, cwd)
		return reposMsg{repos: repos, err: err}
	}
}

func (m model) loadBranches(repo string) tea.Cmd {
	return func() tea.Msg {
		runner := workspace.ExecRunner{}
		if err := workspace.FetchBranches(context.Background(), runner, repo); err != nil {
			return branchesMsg{err: err}
		}
		branches, err := workspace.Branches(context.Background(), runner, repo)
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
	m.message = creatingWorkspaceMessage
	if form.forkPiSession != "" {
		m.message = "Forking workspace…"
	}
	cmd := func() tea.Msg {
		cfg, err := config.Load()
		if err != nil {
			return actionMsg{err: err}
		}
		switchAfterCreate := os.Getenv("TMUX") != ""
		options := workspace.CreateOptions{
			Repo:          form.repo,
			Base:          form.base,
			Name:          form.name,
			Model:         cfg.Model,
			Thinking:      cfg.Thinking,
			Switch:        switchAfterCreate,
			ForkPiSession: form.forkPiSession,
		}
		if form.forkPiSession != "" && form.sourceRepoName != "" {
			root, err := workspace.DefaultRoot()
			if err != nil {
				return actionMsg{err: err}
			}
			options.Path = filepath.Join(root, form.sourceRepoName, workspace.WorktreeName(form.name))
			options.SessionName = workspace.SessionName(form.sourceRepoName, form.name)
		}
		created, err := workspace.Create(context.Background(), workspace.ExecRunner{}, options)
		if err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{message: "Created " + created.SessionName, refresh: !switchAfterCreate, quit: switchAfterCreate}
	}
	if form.forkPiSession != "" {
		return m, cmd
	}
	return m, tea.Batch(preparingWorkspaceNotification(), cmd)
}

func preparingWorkspaceNotification() tea.Cmd {
	return tea.Tick(800*time.Millisecond, func(time.Time) tea.Msg {
		return preparingWorkspaceMsg{}
	})
}

func (m model) previewDelete(task protocol.Task) tea.Cmd {
	return func() tea.Msg {
		ref, ok := worktreeRef(task)
		if !ok || strings.TrimSpace(ref.Path) == "" {
			sessionName := tmuxSessionTarget(task)
			if sessionName == "" {
				return deletePreviewMsg{err: fmt.Errorf("selected task is not backed by a git worktree or tmux session")}
			}
			return deletePreviewMsg{preview: deletePreview{SessionName: sessionName, SessionOnly: true}}
		}
		return previewWorkspaceDelete(task, ref)
	}
}

func (m model) previewCurrentWorkspaceDelete(task protocol.Task, hints currentTaskHints) tea.Cmd {
	return func() tea.Msg {
		ref, ok := currentWorktreeRef(task, hints)
		if !ok || strings.TrimSpace(ref.Path) == "" {
			return deletePreviewMsg{err: fmt.Errorf("current task is not backed by the current git worktree")}
		}
		return previewWorkspaceDelete(task, ref)
	}
}

func previewWorkspaceDelete(task protocol.Task, ref protocol.SourceRef) tea.Msg {
	status, err := workspace.ExecRunner{}.Run(context.Background(), "", "git", "-C", ref.Path, "status", "--porcelain")
	if err != nil {
		return deletePreviewMsg{err: err}
	}
	preview := deletePreview{
		Path:        ref.Path,
		Branch:      ref.Branch,
		SessionName: tmuxSessionTarget(task),
		Dirty:       strings.TrimSpace(status) != "",
	}
	return deletePreviewMsg{preview: preview}
}

func (m model) deleteSelected(preview deletePreview) tea.Cmd {
	return func() tea.Msg {
		if preview.SessionOnly {
			deleted, err := workspace.DeleteSession(context.Background(), workspace.ExecRunner{}, preview.SessionName)
			if err != nil {
				return actionMsg{err: err}
			}
			return actionMsg{message: "Deleted session " + deleted.SessionName, refresh: true}
		}
		deleted, err := workspace.Delete(context.Background(), workspace.ExecRunner{}, preview.Path, preview.SessionName, true)
		if err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{message: "Deleted " + deleted.Path, refresh: true}
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
		return m.pickerView(width, "Create workspace", "Repository", m.create.repoList)
	case "create_base":
		title := "Create workspace"
		if m.create.forkPiSession != "" {
			title = "Fork workspace"
		}
		return strings.Join([]string{
			subtleStyle.Render("Repository " + shortenPath(m.create.repo)),
			m.pickerView(width, title, "Base branch", m.create.baseList),
		}, "\n")
	case "create_name":
		name := m.create.name
		if name == "" {
			name = subtleStyle.Render("type a workspace name")
		}
		title := "Create workspace"
		lines := []string{
			titleStyle.Render(title),
			subtleStyle.Render("Repository " + shortenPath(m.create.repo)),
			subtleStyle.Render("Base       " + m.create.base),
		}
		if m.create.forkPiSession != "" {
			lines[0] = titleStyle.Render("Fork workspace")
			lines = append(lines, subtleStyle.Render("Pi fork    "+m.create.forkPiSession))
		}
		lines = append(lines, selectedStyle.Width(width-4).Render("› Name       "+name))
		return strings.Join(lines, "\n")
	default:
		return ""
	}
}

func (m model) deleteConfirmView(width int) string {
	preview := m.delete
	title := "Delete workspace?"
	warning := "This will remove the git worktree."
	if preview.SessionOnly {
		title = "Delete tmux session?"
		warning = "This will kill only the tmux session."
	} else if preview.Dirty {
		title = "Delete dirty workspace?"
		warning = "This worktree has uncommitted changes. Deleting will permanently discard them."
	}

	lines := []string{
		titleStyle.Render(title),
		warning,
		"",
	}
	if preview.Path != "" {
		lines = append(lines, "Path    "+shortenPath(preview.Path))
	}
	if preview.Branch != "" {
		lines = append(lines, "Branch  "+preview.Branch)
	}
	if preview.SessionName != "" {
		lines = append(lines, "Session "+preview.SessionName)
	}
	lines = append(lines, "", errorStyle.Render("Press y to delete."))
	return lipgloss.NewStyle().Width(width).Render(strings.Join(lines, "\n"))
}

func (m model) worktreeSessionView(width int) string {
	if len(m.worktrees) == 0 {
		return subtleStyle.Render("No git worktrees on selected task.")
	}
	lines := []string{titleStyle.Render("Create tmux session for worktree")}
	for i, ref := range m.worktrees {
		label := sourceRefLabel(ref)
		if ref.Path != "" {
			label = shortenPath(ref.Path)
		}
		if ref.Branch != "" {
			label += "  " + subtleStyle.Render(ref.Branch)
		}
		if i == m.worktreeCursor {
			label = selectedStyle.Width(width - 4).Render("› " + label)
		} else {
			label = "  " + label
		}
		lines = append(lines, label)
	}
	return strings.Join(lines, "\n")
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

func (m model) openLinkView(width int) string {
	if len(m.links) == 0 {
		return subtleStyle.Render("No links on selected task.")
	}
	lines := []string{titleStyle.Render("Open link")}
	for _, link := range m.links {
		lines = append(lines, fmt.Sprintf("  %s  %-6s %s", titleStyle.Render(link.Key), link.Source, link.Label))
		lines = append(lines, subtleStyle.Render("           "+link.URL))
	}
	return strings.Join(lines, "\n")
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

func (m *model) applyResponse(response protocol.Response, selectCurrentTask bool) {
	if response.Revision > m.revision {
		m.revision = response.Revision
	}
	if response.Summary != nil {
		m.summary = *response.Summary
	}
	if response.Tasks != nil {
		m.tasks = response.Tasks
	}
	if response.Sources != nil {
		m.sources = response.Sources
	}
	if selectCurrentTask && !m.selectedCurrentTask {
		if cursor, ok := currentTaskCursor(m.tasks); ok {
			m.cursor = cursor
		}
		m.selectedCurrentTask = true
	}
	if m.cursor >= len(m.tasks) {
		m.cursor = max(0, len(m.tasks)-1)
	}
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

func (m model) watch(revision int64) tea.Cmd {
	return func() tea.Msg {
		res, err := client.Call(m.socketPath, fmt.Sprintf("watch:%d", revision))
		if err != nil {
			return watchMsg{err: err}
		}
		if !res.OK {
			return watchMsg{err: fmt.Errorf("%s", res.Error)}
		}
		return watchMsg{response: res}
	}
}

func (m model) openConfig() tea.Cmd {
	path, err := config.EnsureFile()
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
		return actionMsg{message: "Config saved", refresh: true}
	})
}

func (m model) activateSelected() (tea.Model, tea.Cmd) {
	if len(m.tasks) == 0 {
		return m, nil
	}
	task := m.tasks[m.cursor]
	if target := tmuxSessionTarget(task); target != "" {
		m.loading = true
		m.err = nil
		return m, m.switchTmuxSession(target)
	}

	worktrees := gitWorktreeRefs(task)
	switch len(worktrees) {
	case 0:
		if ref, ok := githubPullRequestRef(task); ok {
			m.loading = true
			m.err = nil
			m.message = creatingWorkspaceMessage
			return m, tea.Batch(preparingWorkspaceNotification(), m.createWorkspaceForPullRequest(ref))
		}
		if _, ok := jiraIssueRef(task); ok {
			m.mode = "create_repo"
			m.create = newCreateFormForTask(task)
			m.message = ""
			m.err = nil
			return m, m.loadRepos()
		}
		m.message = "No tmux session or git worktree on selected task"
		return m, nil
	case 1:
		m.loading = true
		m.err = nil
		m.message = "Creating tmux session…"
		return m, m.createSessionForWorktree(worktrees[0])
	default:
		m.mode = "worktree_session"
		m.worktrees = worktrees
		m.worktreeCursor = 0
		m.message = ""
		m.err = nil
		return m, nil
	}
}

func (m model) switchTmuxSession(target string) tea.Cmd {
	return func() tea.Msg {
		if err := exec.Command("tmux", "switch-client", "-t", target).Run(); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{quit: true}
	}
}

func (m model) createSessionForWorktree(ref protocol.SourceRef) tea.Cmd {
	return func() tea.Msg {
		switchAfterCreate := os.Getenv("TMUX") != ""
		created, err := workspace.CreateSession(context.Background(), workspace.ExecRunner{}, ref.Path, "", switchAfterCreate)
		if err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{message: "Created " + created.SessionName, refresh: !switchAfterCreate, quit: switchAfterCreate}
	}
}

func (m model) createWorkspaceForPullRequest(ref protocol.SourceRef) tea.Cmd {
	return func() tea.Msg {
		repo, err := localRepoForPullRequest(ref)
		if err != nil {
			return actionMsg{err: err}
		}
		runner := workspace.ExecRunner{}
		name := pullRequestWorkspaceName(ref)
		if name == "" {
			name, err = fetchPullRequestHeadBranch(context.Background(), runner, ref)
			if err != nil {
				return actionMsg{err: err}
			}
		}
		if name == "" {
			return actionMsg{err: fmt.Errorf("github pull request has no origin branch")}
		}
		if err := workspace.FetchBranches(context.Background(), runner, repo); err != nil {
			return actionMsg{err: err}
		}
		cfg, err := config.Load()
		if err != nil {
			return actionMsg{err: err}
		}
		switchAfterCreate := os.Getenv("TMUX") != ""
		created, err := workspace.Create(context.Background(), runner, workspace.CreateOptions{
			Repo:     repo,
			Base:     "origin/" + name,
			Name:     name,
			Model:    cfg.Model,
			Thinking: cfg.Thinking,
			Switch:   switchAfterCreate,
		})
		if err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{message: "Created " + created.SessionName, refresh: !switchAfterCreate, quit: switchAfterCreate}
	}
}

func localRepoForPullRequest(ref protocol.SourceRef) (string, error) {
	repos, err := workspace.DiscoverRepos(context.Background(), workspace.ExecRunner{}, "")
	if err != nil {
		return "", err
	}
	wantRepo := githubPullRequestRepo(ref)
	if wantRepo == "" {
		return "", fmt.Errorf("github pull request has no repository")
	}
	matches := make([]string, 0)
	for _, repo := range repos {
		if localRepoMatchesGitHubRepo(repo, wantRepo) {
			matches = append(matches, repo)
		}
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no local repository found for %s", wantRepo)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("multiple local repositories found for %s", wantRepo)
	}
	return matches[0], nil
}

func localRepoMatchesGitHubRepo(repo string, wantRepo string) bool {
	remote, err := workspace.ExecRunner{}.Run(context.Background(), repo, "git", "remote", "get-url", "origin")
	if err == nil {
		return normalizeGitHubRepo(remote) == normalizeGitHubRepo(wantRepo)
	}
	return filepath.Base(repo) == filepath.Base(wantRepo)
}

func fetchPullRequestHeadBranch(ctx context.Context, runner workspace.Runner, ref protocol.SourceRef) (string, error) {
	if err := runner.LookPath("gh"); err != nil {
		return "", fmt.Errorf("github pull request branch lookup requires %q: %w", "gh", err)
	}
	repo := githubPullRequestRepo(ref)
	number := githubPullRequestNumber(ref.ID)
	if repo == "" || number == "" {
		return "", fmt.Errorf("github pull request has no repository or number")
	}
	branch, err := runner.Run(ctx, "", "gh", "pr", "view", number, "--repo", repo, "--json", "headRefName", "--jq", ".headRefName")
	if err != nil {
		return "", err
	}
	return pullRequestWorkspaceName(protocol.SourceRef{Branch: branch}), nil
}

func githubPullRequestRepo(ref protocol.SourceRef) string {
	if repo := strings.TrimSpace(ref.Repo); repo != "" {
		return repo
	}
	value, ok := strings.CutPrefix(ref.ID, "github:pr:")
	if !ok {
		return ""
	}
	index := strings.LastIndex(value, ":")
	if index <= 0 {
		return ""
	}
	return value[:index]
}

func githubPullRequestNumber(id string) string {
	value, ok := strings.CutPrefix(id, "github:pr:")
	if !ok {
		return ""
	}
	index := strings.LastIndex(value, ":")
	if index < 0 || index == len(value)-1 {
		return ""
	}
	return value[index+1:]
}

func normalizeGitHubRepo(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimSuffix(value, ".git")
	value = strings.TrimPrefix(value, "https://github.com/")
	value = strings.TrimPrefix(value, "http://github.com/")
	value = strings.TrimPrefix(value, "git@github.com:")
	return value
}

func (m model) openTaskURL(task protocol.Task, url string) tea.Cmd {
	return func() tea.Msg {
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

		if err := openURL(url); err != nil {
			return actionMsg{response: response, err: err}
		}
		return actionMsg{response: response}
	}
}

func worktreeRef(task protocol.Task) (protocol.SourceRef, bool) {
	refs := gitWorktreeRefs(task)
	if len(refs) == 0 {
		return protocol.SourceRef{}, false
	}
	return refs[0], true
}

func currentWorktreeRef(task protocol.Task, hints currentTaskHints) (protocol.SourceRef, bool) {
	for _, ref := range gitWorktreeRefs(task) {
		if currentPathMatches(ref.Path, hints) {
			return ref, true
		}
	}
	return protocol.SourceRef{}, false
}

func gitWorktreeRefs(task protocol.Task) []protocol.SourceRef {
	refs := make([]protocol.SourceRef, 0)
	for _, ref := range task.SourceRefs {
		if ref.Source == "git" && ref.Kind == "worktree" && ref.Path != "" {
			refs = append(refs, ref)
		}
	}
	return refs
}

func jiraIssueRef(task protocol.Task) (protocol.SourceRef, bool) {
	for _, ref := range task.SourceRefs {
		if ref.Source == "jira" && ref.Kind == "issue" {
			return ref, true
		}
	}
	return protocol.SourceRef{}, false
}

func githubPullRequestRef(task protocol.Task) (protocol.SourceRef, bool) {
	for _, ref := range task.SourceRefs {
		if ref.Source == "github" && ref.Kind == "pull_request" {
			return ref, true
		}
	}
	return protocol.SourceRef{}, false
}

func pullRequestWorkspaceName(ref protocol.SourceRef) string {
	branch := strings.TrimSpace(ref.Branch)
	branch = strings.TrimPrefix(branch, "refs/remotes/")
	branch = strings.TrimPrefix(branch, "origin/")
	branch = strings.TrimPrefix(branch, "refs/heads/")
	return branch
}

type currentTaskHints struct {
	cwd         string
	worktree    string
	sessionName string
	sessionID   string
}

func currentTaskCursor(tasks []protocol.Task) (int, bool) {
	return taskCursorForHints(tasks, detectCurrentTaskHints())
}

func detectCurrentTaskHints() currentTaskHints {
	hints := currentTaskHints{}
	if cwd, err := os.Getwd(); err == nil {
		hints.cwd = filepath.Clean(cwd)
		runner := workspace.ExecRunner{}
		if worktree, err := runner.Run(context.Background(), cwd, "git", "rev-parse", "--show-toplevel"); err == nil {
			hints.worktree = filepath.Clean(worktree)
		}
	}
	if os.Getenv("TMUX") != "" {
		runner := workspace.ExecRunner{}
		if output, err := runner.Run(context.Background(), hints.cwd, "tmux", "display-message", "-p", "#{session_name}\t#{session_id}"); err == nil {
			fields := strings.Split(output, "\t")
			if len(fields) > 0 {
				hints.sessionName = strings.TrimSpace(fields[0])
			}
			if len(fields) > 1 {
				hints.sessionID = strings.TrimSpace(fields[1])
			}
		}
	}
	return hints
}

func taskCursorForHints(tasks []protocol.Task, hints currentTaskHints) (int, bool) {
	if hints.worktree != "" || hints.cwd != "" {
		for i, task := range tasks {
			for _, ref := range task.SourceRefs {
				if ref.Source == "git" && ref.Kind == "worktree" && ref.Path != "" && currentPathMatches(ref.Path, hints) {
					return i, true
				}
			}
		}
	}
	if hints.sessionName != "" || hints.sessionID != "" {
		for i, task := range tasks {
			if task.Kind == "session" && metadataMatchesSession(task.Metadata, hints) {
				return i, true
			}
			for _, ref := range task.SourceRefs {
				if ref.Source == "tmux" && ref.Kind == "session" && tmuxRefMatchesSession(ref, hints) {
					return i, true
				}
			}
		}
	}
	return 0, false
}

func currentPathMatches(refPath string, hints currentTaskHints) bool {
	refPath = filepath.Clean(refPath)
	return samePath(hints.worktree, refPath) || sameOrDescendant(hints.cwd, refPath)
}

func tmuxRefMatchesSession(ref protocol.SourceRef, hints currentTaskHints) bool {
	return metadataMatchesSession(ref.Metadata, hints) || ref.Title == hints.sessionName
}

func metadataMatchesSession(metadata map[string]string, hints currentTaskHints) bool {
	for _, key := range []string{"switch_target", "session_id", "session"} {
		value := metadata[key]
		if value != "" && (value == hints.sessionID || value == hints.sessionName) {
			return true
		}
	}
	return false
}

func samePath(left string, right string) bool {
	return left != "" && right != "" && filepath.Clean(left) == filepath.Clean(right)
}

func sameOrDescendant(path string, root string) bool {
	if path == "" || root == "" {
		return false
	}
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
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

func taskLinks(task protocol.Task) []linkChoice {
	seen := map[string]bool{}
	var links []linkChoice
	add := func(source string, label string, url string) {
		if url == "" || seen[url] || len(links) >= 9 {
			return
		}
		seen[url] = true
		links = append(links, linkChoice{Key: fmt.Sprint(len(links) + 1), Source: source, Label: label, URL: url})
	}

	for _, ref := range task.SourceRefs {
		add(sourceRefSourceLabel(ref), sourceRefLabel(ref), ref.URL)
	}
	add("link", task.Title, task.URL)
	return links
}

func matchingLink(links []linkChoice, key string) (linkChoice, bool) {
	for _, link := range links {
		if link.Key == key {
			return link, true
		}
	}
	return linkChoice{}, false
}

func sourceRefSourceLabel(ref protocol.SourceRef) string {
	if ref.SourceLabel != "" {
		return ref.SourceLabel
	}
	if ref.Source != "" {
		return ref.Source
	}
	return "link"
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

	left := lipgloss.JoinHorizontal(lipgloss.Top, titleStyle.Render("Radar"), "  ", counts)
	if status == "" {
		return truncateLine(left, width)
	}
	available := max(0, width-lipgloss.Width(status))
	left = truncateLine(left, available)
	gap := strings.Repeat(" ", max(0, width-lipgloss.Width(left)-lipgloss.Width(status)))
	return lipgloss.JoinHorizontal(lipgloss.Top, left, gap, status)
}

func (m model) taskList(width int, height int) string {
	lines, selectedStart, selectedEnd := m.taskLines(width)
	return scrolledLines(lines, selectedStart, selectedEnd, m.scroll, height)
}

func scrolledLines(lines []string, selectedStart int, selectedEnd int, scroll int, height int) string {
	if height <= 0 || len(lines) <= height {
		return strings.Join(lines, "\n")
	}
	if selectedStart < scroll {
		scroll = selectedStart
	}
	if selectedEnd >= scroll+height {
		selectedHeight := selectedEnd - selectedStart + 1
		if selectedHeight >= height {
			scroll = selectedStart
		} else {
			scroll = selectedEnd - height + 1
		}
	}
	scroll = max(0, min(scroll, len(lines)-height))
	visible := append([]string{}, lines[scroll:scroll+height]...)
	if scroll > 0 && selectedStart != scroll {
		visible[0] = subtleStyle.Render("↑ more")
	}
	if scroll+height < len(lines) && selectedEnd != scroll+height-1 {
		visible[len(visible)-1] = subtleStyle.Render("↓ more")
	}
	return strings.Join(visible, "\n")
}

func (m model) taskLines(width int) ([]string, int, int) {
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

	selectedStart := 0
	selectedEnd := 0
	var lines []string
	for _, group := range groups {
		var groupLines []string
		groupHeaderIndex := len(lines)
		if len(lines) > 0 {
			groupHeaderIndex++
		}
		for i, task := range m.tasks {
			if task.Attention != group.key {
				continue
			}
			line := taskLine(task, i == m.cursor)
			if i == m.cursor {
				line = selectedStyle.Render("› " + line)
			} else {
				line = "  " + line
			}
			lineWidth := max(20, width-20)
			block := []string{truncateLine(line, lineWidth)}
			for _, ref := range task.SourceRefs {
				block = append(block, truncateLine(subtleStyle.Render("    ↳ "+sourceRefLabel(ref)), lineWidth))
			}
			if i == m.cursor {
				groupStart := groupHeaderIndex
				taskStart := groupStart + len(groupLines) + 1
				selectedStart = taskStart
				if len(groupLines) == 0 {
					selectedStart = groupStart
				}
				selectedEnd = taskStart + len(block) - 1
			}
			groupLines = append(groupLines, block...)
		}
		if len(groupLines) > 0 {
			if len(lines) > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, group.style.Render(group.title))
			lines = append(lines, groupLines...)
		}
	}
	return lines, selectedStart, selectedEnd
}

func truncateLine(line string, width int) string {
	if width <= 0 {
		return line
	}
	return ansi.Truncate(line, width, "…")
}

func taskLine(task protocol.Task, selected bool) string {
	title := task.Title
	if task.Repo != "" {
		repo := task.Repo
		if !selected {
			repo = subtleStyle.Render(repo)
		}
		title = fmt.Sprintf("%s  %s", title, repo)
	}
	if task.Reason != "" {
		reason := task.Reason
		if !selected {
			reason = subtleStyle.Render(reason)
		}
		title = fmt.Sprintf("%s  %s", title, reason)
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
		lines = append(lines, truncateLine(line, width))
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
