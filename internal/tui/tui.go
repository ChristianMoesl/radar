package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"radar/internal/app"
	"radar/internal/client"
	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/openurl"
	"radar/internal/protocol"
	"radar/internal/sourceactions"
	"radar/internal/taskrefs"
	"radar/internal/workspace"
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

type cleanupPreviewMsg struct {
	preview protocol.CleanupPreview
	err     error
}

type preparingWorkspaceMsg struct{}

type linkChoice struct {
	Key    string
	Source string
	Label  string
	Detail string
	URL    string
	Action string
	Ref    protocol.SourceRef
}

type picker struct {
	query   string
	options []string
	cursor  int
	loading bool
}

type createForm struct {
	repo           string
	branchMode     integration.WorkspaceBranchMode
	branch         string
	base           string
	name           string
	forkPiSession  string
	sourceRepoName string
	repoList       picker
	intentList     picker
	branchList     picker
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
	cleanup             protocol.CleanupPreview
	links               []linkChoice
	worktrees           []protocol.SourceRef
	worktreeTask        protocol.Task
	worktreeCursor      int
	message             string
	authoredTitle       string
	scroll              int
	revision            int64
	watching            bool
}

const (
	creatingWorkspaceMessage  = "Creating Workspace..."
	preparingWorkspaceMessage = "Preparing workspace..."
	createIntentExisting      = "Work on an existing branch"
	createIntentNew           = "Create a new branch"
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

	errorNotificationStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("203")).
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("203")).
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
	form.branchMode = integration.WorkspaceBranchNew
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
	if (m.mode == "create_base" || m.mode == "create_branch") && m.create.repo != "" {
		commands = append(commands, m.loadBranches(m.create.repo))
	}
	return tea.Batch(commands...)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.syncTaskScroll()
		return m, nil
	case tea.KeyMsg:
		if m.mode == "task_authoring" {
			switch msg.String() {
			case "esc", "ctrl+c":
				m.mode = ""
				m.authoredTitle = ""
				m.err = nil
				return m, nil
			case "enter":
				title := strings.TrimSpace(m.authoredTitle)
				if title == "" {
					m.err = fmt.Errorf("task title is required")
					return m, nil
				}
				m.mode = ""
				m.authoredTitle = ""
				m.loading = true
				m.err = nil
				return m, m.createAuthoredTask(title)
			case "backspace", "ctrl+h":
				m.authoredTitle = dropLastRune(m.authoredTitle)
				return m, nil
			}
			if value, ok := textInputValue(msg); ok {
				m.authoredTitle += value
			}
			return m, nil
		}
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
				return m, m.openTask(m.tasks[m.cursor], link)
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
				task := m.worktreeTask
				m.mode = ""
				m.worktrees = nil
				m.worktreeTask = protocol.Task{}
				m.worktreeCursor = 0
				m.loading = true
				m.err = nil
				m.message = "Creating tmux session…"
				return m, m.createSessionForWorktree(task, ref)
			default:
				return m, nil
			}
		}
		if m.mode == "cleanup_confirm" {
			switch msg.String() {
			case "y", "Y":
				preview := m.cleanup
				m.mode = ""
				m.loading = true
				m.err = nil
				m.message = "Cleaning up…"
				return m, m.cleanupSelected(preview)
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
		case "n":
			m.mode = "task_authoring"
			m.authoredTitle = ""
			m.err = nil
			m.message = ""
		case "d":
			if len(m.tasks) > 0 {
				task := m.tasks[m.cursor]
				ref, ok := authoredTaskRef(task)
				if !ok {
					m.message = "Selected task does not support authored lifecycle changes"
					return m, nil
				}
				m.loading = true
				m.err = nil
				return m, m.setAuthoredTaskDone(task, ref.Metadata["state"] != "done")
			}
		case "p":
			if len(m.tasks) > 0 {
				task := m.tasks[m.cursor]
				ref, ok := authoredTaskRef(task)
				if !ok {
					m.message = "Selected task does not support authored priority changes"
					return m, nil
				}
				if task.Attention == "done" {
					m.message = "Done tasks cannot change priority"
					return m, nil
				}
				priority := "urgent"
				if ref.Metadata["priority"] == "urgent" {
					priority = "normal"
				}
				m.loading = true
				m.err = nil
				return m, m.setTaskPriority(task, priority)
			}
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
		case "x":
			if len(m.tasks) > 0 {
				m.loading = true
				m.err = nil
				m.message = "Inspecting local resources…"
				return m, m.previewCleanup(m.tasks[m.cursor])
			}
		case "X":
			m.loading = true
			m.err = nil
			m.message = "Garbage collecting…"
			return m, m.garbageCollect()
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
		if m.mode == "create_branch" {
			m.create.branchList.loading = false
			if msg.err == nil {
				m.create.branchList.options = existingBranchNames(msg.branches)
				m.create.branchList.cursor = 0
			}
		} else {
			m.create.baseList.loading = false
			if msg.err == nil {
				m.create.baseList.options = msg.branches
				m.create.baseList.cursor = 0
			}
		}
	case cleanupPreviewMsg:
		m.loading = false
		m.err = msg.err
		m.message = ""
		if msg.err == nil {
			m.cleanup = msg.preview
			m.mode = "cleanup_confirm"
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

var taskGroupKeys = []string{"immediate", "attention", "in_progress", "low_priority", "done"}

func (m *model) moveCursor(delta int) {
	order := m.taskCursorOrder()
	if len(order) == 0 {
		m.syncTaskScroll()
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
		m.syncTaskScroll()
		return
	}

	position = max(0, min(position+delta, len(order)-1))
	m.cursor = order[position]
	m.syncTaskScroll()
}

func (m *model) moveCursorToEdge(last bool) {
	order := m.taskCursorOrder()
	if len(order) == 0 {
		m.syncTaskScroll()
		return
	}
	if last {
		m.cursor = order[len(order)-1]
		m.syncTaskScroll()
		return
	}
	m.cursor = order[0]
	m.syncTaskScroll()
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

	if m.mode == "task_authoring" {
		sections = append(sections, m.taskAuthoringView(contentWidth))
		sections = append(sections, helpStyle.Render("type a title • enter create • esc cancel"))
		return m.renderFrame(strings.Join(sections, "\n\n"), contentWidth)
	}

	if m.mode == "detail" {
		sections = append(sections, m.detailView(contentWidth))
		sections = append(sections, helpStyle.Render("esc/backspace/h back • q quit"))
		return m.renderFrame(strings.Join(sections, "\n\n"), contentWidth)
	}

	if m.mode == "open_link" {
		sections = append(sections, m.openLinkView(contentWidth))
		sections = append(sections, helpStyle.Render("press key to open • esc cancel • q quit"))
		return m.renderFrame(strings.Join(sections, "\n\n"), contentWidth)
	}

	if m.mode == "cleanup_confirm" {
		sections = append(sections, m.cleanupConfirmView(contentWidth))
		sections = append(sections, helpStyle.Render("y clean up • esc/n cancel • q quit"))
		return m.renderFrame(strings.Join(sections, "\n\n"), contentWidth)
	}

	if m.mode == "worktree_session" {
		sections = append(sections, m.worktreeSessionView(contentWidth))
		sections = append(sections, helpStyle.Render("↑/k ↓/j move • enter create session • esc cancel • q quit"))
		return m.renderFrame(strings.Join(sections, "\n\n"), contentWidth)
	}

	if strings.HasPrefix(m.mode, "create_") {
		sections = append(sections, m.createView(contentWidth))
		sections = append(sections, helpStyle.Render("type to filter • ↑/ctrl+p ↓/ctrl+n move • enter select/submit • esc cancel"))
		return m.renderFrame(strings.Join(sections, "\n\n"), contentWidth)
	}

	if m.loading && len(m.tasks) == 0 {
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
	sections = append(sections, truncateLine(helpStyle.Render("↑/k/ctrl+p ↓/j/ctrl+n select • enter switch tmux • n new task • d done/reopen • p urgent/normal • o open link • i inspect • c create workspace • x cleanup • X garbage collect • f config • r refresh • q quit"), width))
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
	message := m.message
	style := notificationStyle
	if m.err != nil {
		message = "Error: " + m.err.Error()
		style = errorNotificationStyle
	}
	if message == "" {
		return frame
	}

	lines := strings.Split(frame, "\n")
	popupLines := strings.Split(style.Render(message), "\n")
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
	return createForm{
		repoList:   picker{loading: true},
		intentList: picker{options: []string{createIntentExisting, createIntentNew}},
	}
}

func newCreateFormForTask(task protocol.Task) createForm {
	form := newCreateForm()
	form.name = workspaceNameForTask(task)
	return form
}

func workspaceNameForTask(task protocol.Task) string {
	return taskrefs.WorkspaceName(task)
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
	case "up", "ctrl+p":
		m.moveCreateCursor(-1)
		return m, nil
	case "down", "ctrl+n":
		m.moveCreateCursor(1)
		return m, nil
	case "enter":
		return m.selectCreateStep()
	case "backspace", "ctrl+h":
		m.backspaceCreateQuery()
		return m, nil
	}

	if value, ok := textInputValue(msg); ok {
		m.appendCreateQuery(value)
	}
	return m, nil
}

func textInputValue(msg tea.KeyMsg) (string, bool) {
	if msg.Type == tea.KeySpace {
		return " ", true
	}
	if msg.Type == tea.KeyRunes {
		return string(msg.Runes), true
	}
	return "", false
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

func existingBranchNames(refs []string) []string {
	branches := make([]string, 0, len(refs))
	seen := map[string]bool{}
	for _, ref := range refs {
		branch := strings.TrimPrefix(strings.TrimSpace(ref), "origin/")
		if branch == "" || seen[branch] {
			continue
		}
		seen[branch] = true
		branches = append(branches, branch)
	}
	return branches
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
		m.mode = "create_intent"
		m.err = nil
		return m, nil
	case "create_intent":
		switch selectedPickerOption(m.create.intentList) {
		case createIntentExisting:
			m.create.branchMode = integration.WorkspaceBranchExisting
			m.create.branchList = picker{loading: true}
			m.mode = "create_branch"
		case createIntentNew:
			m.create.branchMode = integration.WorkspaceBranchNew
			m.create.baseList = picker{loading: true}
			m.mode = "create_base"
		default:
			m.err = fmt.Errorf("select how to create the workspace")
			return m, nil
		}
		m.err = nil
		return m, m.loadBranches(m.create.repo)
	case "create_branch":
		selected := selectedPickerOption(m.create.branchList)
		if selected == "" {
			m.err = fmt.Errorf("select an existing branch")
			return m, nil
		}
		m.create.branch = selected
		m.create.name = selected
		m.err = nil
		return m.submitCreate()
	case "create_base":
		selected := selectedPickerOption(m.create.baseList)
		if selected == "" {
			m.err = fmt.Errorf("select a starting reference")
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
	if strings.TrimSpace(m.create.repo) == "" {
		m.err = fmt.Errorf("repository is required")
		return m, nil
	}
	switch m.create.branchMode {
	case integration.WorkspaceBranchExisting:
		if strings.TrimSpace(m.create.branch) == "" {
			m.err = fmt.Errorf("existing branch is required")
			return m, nil
		}
	case integration.WorkspaceBranchNew:
		if strings.TrimSpace(m.create.base) == "" || strings.TrimSpace(m.create.name) == "" {
			m.err = fmt.Errorf("starting reference and new branch name are required")
			return m, nil
		}
	default:
		m.err = fmt.Errorf("workspace branch mode is required")
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
		options := integration.CreateWorkspaceRequest{
			Repo:                    form.repo,
			BranchMode:              form.branchMode,
			Branch:                  form.branch,
			Base:                    form.base,
			Name:                    form.name,
			Model:                   cfg.Model,
			Thinking:                cfg.Thinking,
			Sandbox:                 cfg.SBX.Enabled,
			SandboxKitName:          cfg.SBX.Kit.Name,
			SandboxKitPath:          cfg.SBX.Kit.Path,
			AdditionalSandboxMounts: cfg.SBX.AdditionalMounts,
			Tmux:                    cfg.Tmux,
			Switch:                  switchAfterCreate,
			ForkPiSession:           form.forkPiSession,
		}
		if form.forkPiSession != "" && form.sourceRepoName != "" {
			root, err := workspace.DefaultRoot()
			if err != nil {
				return actionMsg{err: err}
			}
			options.Path = filepath.Join(root, form.sourceRepoName, workspace.WorktreeName(form.name))
			options.SessionName = workspace.SessionName(form.sourceRepoName, form.name)
		}
		workspaceProvider, err := app.DefaultIntegrations().Workspace()
		if err != nil {
			return actionMsg{err: err}
		}
		created, err := workspaceProvider.Create(context.Background(), options)
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

func (m model) createAuthoredTask(title string) tea.Cmd {
	return func() tea.Msg {
		response, err := client.CreateTask(m.socketPath, title)
		if err != nil {
			return actionMsg{err: err}
		}
		if !response.OK {
			return actionMsg{err: fmt.Errorf("%s", response.Error)}
		}
		return actionMsg{response: &response, message: "Task created"}
	}
}

func (m model) setAuthoredTaskDone(task protocol.Task, complete bool) tea.Cmd {
	return func() tea.Msg {
		var response protocol.Response
		var err error
		message := "Task reopened"
		if complete {
			response, err = client.CompleteTask(m.socketPath, task.ID)
			message = "Task completed"
		} else {
			response, err = client.ReopenTask(m.socketPath, task.ID)
		}
		if err != nil {
			return actionMsg{err: err}
		}
		if !response.OK {
			return actionMsg{err: fmt.Errorf("%s", response.Error)}
		}
		return actionMsg{response: &response, message: message}
	}
}

func (m model) setTaskPriority(task protocol.Task, priority string) tea.Cmd {
	return func() tea.Msg {
		response, err := client.SetTaskPriority(m.socketPath, task.ID, priority)
		if err != nil {
			return actionMsg{err: err}
		}
		if !response.OK {
			return actionMsg{err: fmt.Errorf("%s", response.Error)}
		}
		message := "Task marked urgent"
		if priority == "normal" {
			message = "Task restored to natural priority"
		}
		return actionMsg{response: &response, message: message}
	}
}

func (m model) garbageCollect() tea.Cmd {
	return func() tea.Msg {
		response, err := client.Call(m.socketPath, "gc")
		if err != nil {
			return actionMsg{err: err}
		}
		if !response.OK {
			return actionMsg{err: fmt.Errorf("%s", response.Error)}
		}
		if response.GarbageCollectionResult == nil {
			return actionMsg{err: fmt.Errorf("garbage collection response was empty")}
		}
		return actionMsg{response: &response, message: garbageCollectionMessage(*response.GarbageCollectionResult)}
	}
}

func garbageCollectionMessage(result protocol.GarbageCollectionResult) string {
	if len(result.Deleted) == 0 && len(result.Skipped) == 0 {
		return "No workspaces eligible for garbage collection"
	}
	return fmt.Sprintf("Garbage collection: deleted %d, skipped %d", len(result.Deleted), len(result.Skipped))
}

func (m model) previewCleanup(task protocol.Task) tea.Cmd {
	return func() tea.Msg {
		response, err := client.CallRequest(m.socketPath, protocol.Request{Method: "cleanup-preview", TaskID: task.ID})
		if err != nil {
			return cleanupPreviewMsg{err: err}
		}
		if !response.OK {
			return cleanupPreviewMsg{err: fmt.Errorf("%s", response.Error)}
		}
		if response.CleanupPreview == nil {
			return cleanupPreviewMsg{err: fmt.Errorf("cleanup preview response was empty")}
		}
		return cleanupPreviewMsg{preview: *response.CleanupPreview}
	}
}

func (m model) cleanupSelected(preview protocol.CleanupPreview) tea.Cmd {
	return func() tea.Msg {
		response, err := client.CallRequest(m.socketPath, protocol.Request{Method: "cleanup", Cleanup: &preview})
		if err != nil {
			return actionMsg{err: err}
		}
		if !response.OK {
			return actionMsg{err: fmt.Errorf("%s", response.Error)}
		}
		return actionMsg{message: cleanupSuccessMessage(preview), refresh: true}
	}
}

func (m *model) appendCreateQuery(value string) {
	switch m.mode {
	case "create_repo":
		m.create.repoList.query += value
		m.create.repoList.cursor = 0
	case "create_intent":
		m.create.intentList.query += value
		m.create.intentList.cursor = 0
	case "create_branch":
		m.create.branchList.query += value
		m.create.branchList.cursor = 0
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
	case "create_intent":
		m.create.intentList.query = dropLastRune(m.create.intentList.query)
		m.create.intentList.cursor = 0
	case "create_branch":
		m.create.branchList.query = dropLastRune(m.create.branchList.query)
		m.create.branchList.cursor = 0
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
	case "create_intent":
		return &m.create.intentList
	case "create_branch":
		return &m.create.branchList
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

func (m model) taskAuthoringView(width int) string {
	title := m.authoredTitle
	if title == "" {
		title = subtleStyle.Render("type a task title")
	}
	return strings.Join([]string{
		titleStyle.Render("Create task"),
		selectedStyle.Width(width - 4).Render("› Title  " + title),
	}, "\n")
}

func (m model) createView(width int) string {
	switch m.mode {
	case "create_repo":
		return m.pickerView(width, "Create workspace", "Repository", m.create.repoList)
	case "create_intent":
		return strings.Join([]string{
			subtleStyle.Render("Repository " + shortenPath(m.create.repo)),
			m.pickerView(width, "Create workspace", "Action", m.create.intentList),
		}, "\n")
	case "create_branch":
		return strings.Join([]string{
			subtleStyle.Render("Repository " + shortenPath(m.create.repo)),
			m.pickerView(width, "Create workspace", "Existing branch", m.create.branchList),
		}, "\n")
	case "create_base":
		title := "Create workspace"
		if m.create.forkPiSession != "" {
			title = "Fork workspace"
		}
		return strings.Join([]string{
			subtleStyle.Render("Repository " + shortenPath(m.create.repo)),
			m.pickerView(width, title, "Starting reference", m.create.baseList),
		}, "\n")
	case "create_name":
		name := m.create.name
		if name == "" {
			name = subtleStyle.Render("type a branch name")
		}
		title := "Create workspace"
		lines := []string{
			titleStyle.Render(title),
			subtleStyle.Render("Repository " + shortenPath(m.create.repo)),
			subtleStyle.Render("Start      " + m.create.base),
		}
		if m.create.forkPiSession != "" {
			lines[0] = titleStyle.Render("Fork workspace")
			lines = append(lines, subtleStyle.Render("Pi fork    "+m.create.forkPiSession))
		}
		lines = append(lines, selectedStyle.Width(width-4).Render("› New branch "+name))
		return strings.Join(lines, "\n")
	default:
		return ""
	}
}

func (m model) cleanupConfirmView(width int) string {
	preview := m.cleanup
	dirty := false
	for _, target := range preview.Targets {
		if target.Dirty {
			dirty = true
			break
		}
	}
	warning := "This will remove every local resource linked to the task. Git branches and remote resources are preserved."
	if dirty {
		warning = "This will discard uncommitted changes and remove every local resource linked to the task."
	}
	lines := []string{titleStyle.Render("Clean up local resources?"), warning, ""}
	for _, target := range preview.Targets {
		switch target.Kind {
		case "worktree":
			label := "Worktree " + shortenPath(target.Path)
			if target.Dirty {
				label += " (dirty)"
			}
			lines = append(lines, label)
		case "session":
			lines = append(lines, "Session  "+target.SessionName)
		case "sandbox":
			lines = append(lines, "Sandbox  "+target.SandboxName)
		default:
			lines = append(lines, target.Source+" "+target.Title)
		}
	}
	lines = append(lines, "", errorStyle.Render("Press y to clean up."))
	return lipgloss.NewStyle().Width(width).Render(strings.Join(lines, "\n"))
}

func cleanupSuccessMessage(preview protocol.CleanupPreview) string {
	return fmt.Sprintf("Cleaned up %d local resource(s)", len(preview.Targets))
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
		lines = append(lines, fmt.Sprintf("  %s  %-10s %s", titleStyle.Render(link.Key), link.Source, link.Label))
		if link.Detail != "" {
			lines = append(lines, subtleStyle.Render("               "+link.Detail))
		}
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
	appendDetailLine("Reason", displayTaskReason(task))
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
			appendRefDetail("role", string(ref.Role))
			appendRefDetail("status", ref.Status)
			appendRefDetail("repo", ref.Repo)
			appendRefDetail("path", shortenPath(ref.Path))
			appendRefDetail("branch", ref.Branch)
			metadataKeys := make([]string, 0, len(ref.Metadata))
			for key := range ref.Metadata {
				metadataKeys = append(metadataKeys, key)
			}
			sort.Strings(metadataKeys)
			for _, key := range metadataKeys {
				appendRefDetail(key, ref.Metadata[key])
			}
		}
	}
	return strings.Join(lines, "\n")
}

func (m *model) applyResponse(response protocol.Response, selectCurrentTask bool) {
	var selectedTask *protocol.Task
	selectedPosition := -1
	if response.Tasks != nil && m.cursor >= 0 && m.cursor < len(m.tasks) {
		selected := m.tasks[m.cursor]
		selectedTask = &selected
		selectedPosition = m.cursorPosition()
	}

	if response.Revision > m.revision {
		m.revision = response.Revision
	}
	if response.Summary != nil {
		m.summary = *response.Summary
	}
	if response.Tasks != nil {
		m.tasks = response.Tasks
		m.restoreCursor(selectedTask, selectedPosition)
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
	m.syncTaskScroll()
}

func (m model) cursorPosition() int {
	for position, cursor := range m.taskCursorOrder() {
		if cursor == m.cursor {
			return position
		}
	}
	return -1
}

func (m *model) restoreCursor(selectedTask *protocol.Task, selectedPosition int) {
	if selectedTask != nil {
		if cursor, ok := matchingTaskCursor(m.tasks, *selectedTask); ok {
			m.cursor = cursor
			return
		}
	}

	order := m.taskCursorOrder()
	if len(order) == 0 {
		m.cursor = 0
		return
	}
	if selectedPosition >= 0 {
		m.cursor = order[min(selectedPosition, len(order)-1)]
		return
	}
	if m.cursor >= len(m.tasks) {
		m.cursor = max(0, len(m.tasks)-1)
	}
}

func matchingTaskCursor(tasks []protocol.Task, selected protocol.Task) (int, bool) {
	if selected.ID != 0 {
		for i, task := range tasks {
			if task.ID == selected.ID {
				return i, true
			}
		}
	}

	selectedRefs := make(map[string]struct{}, len(selected.SourceRefs))
	for _, ref := range selected.SourceRefs {
		if ref.ID != "" {
			selectedRefs[ref.ID] = struct{}{}
		}
	}
	if len(selectedRefs) == 0 {
		return 0, false
	}
	for i, task := range tasks {
		for _, ref := range task.SourceRefs {
			if _, ok := selectedRefs[ref.ID]; ok && ref.ID != "" {
				return i, true
			}
		}
	}
	return 0, false
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
	if target := taskrefs.SessionTarget(task); target != "" {
		m.loading = true
		m.err = nil
		return m, m.switchTmuxSession(target)
	}

	if ref, ok := taskrefs.TaskWorkspace(task); ok {
		m.loading = true
		m.err = nil
		m.message = "Creating task session…"
		return m, m.createSessionForAuthoredTask(task, ref)
	}

	worktrees := taskrefs.Worktrees(task)
	switch len(worktrees) {
	case 0:
		if ref, ok := taskrefs.WorkspaceCandidate(task); ok {
			if ref.Repo != "" && ref.Branch != "" {
				m.loading = true
				m.err = nil
				m.message = creatingWorkspaceMessage
				return m, tea.Batch(preparingWorkspaceNotification(), m.createWorkspaceForPullRequest(ref))
			}
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
		return m, m.createSessionForWorktree(task, worktrees[0])
	default:
		m.mode = "worktree_session"
		m.worktrees = worktrees
		m.worktreeTask = task
		m.worktreeCursor = 0
		m.message = ""
		m.err = nil
		return m, nil
	}
}

func (m model) switchTmuxSession(target string) tea.Cmd {
	return func() tea.Msg {
		multiplexer, err := app.DefaultIntegrations().Multiplexer()
		if err != nil {
			return actionMsg{err: err}
		}
		if err := multiplexer.Switch(context.Background(), integration.SessionTarget{Name: target}); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{quit: true}
	}
}

func authoredTaskRef(task protocol.Task) (protocol.SourceRef, bool) {
	for _, ref := range task.SourceRefs {
		if ref.Role == protocol.SourceRefRoleAuthoritative && ref.Lifecycle == protocol.SourceRefLifecycleWorkItem && ref.Authority == protocol.SourceRefAuthorityPrimary && ref.Metadata["authoring"] == "true" {
			return ref, true
		}
	}
	return protocol.SourceRef{}, false
}

func (m model) createSessionForAuthoredTask(task protocol.Task, workspaceRef protocol.SourceRef) tea.Cmd {
	return func() tea.Msg {
		authoredRef, ok := authoredTaskRef(task)
		if !ok {
			return actionMsg{err: fmt.Errorf("task has no authored source ref")}
		}
		id := strings.TrimSpace(authoredRef.Metadata["radar_id"])
		if id == "" {
			return actionMsg{err: fmt.Errorf("authored task has no stable identity")}
		}
		compactID := strings.ReplaceAll(strings.ToLower(id), "-", "")
		if len(compactID) > 12 {
			compactID = compactID[:12]
		}
		sessionName := workspace.WorktreeName("radar-" + compactID)
		cfg, err := config.Load()
		if err != nil {
			return actionMsg{err: err}
		}
		notePath := authoredRef.Metadata["note_path"]
		prompt := "Read task.md, work toward the desired outcome, and keep Working notes and Outcome links current."
		created, err := workspace.CreateSessionWithOptions(context.Background(), workspace.ExecRunner{}, workspace.CreateSessionOptions{
			Path: workspaceRef.Path, SessionName: sessionName, PiSessionID: "obsidian-" + id, InitialPrompt: prompt,
			Environment: map[string]string{"RADAR_TASK_ID": id, "RADAR_TASK_NOTE": notePath},
			Model:       cfg.Model, Thinking: cfg.Thinking, Sandbox: cfg.SBX.Enabled,
			SandboxKitName: cfg.SBX.Kit.Name, SandboxKitPath: cfg.SBX.Kit.Path,
			AdditionalSandboxMounts: cfg.SBX.AdditionalMounts,
			SandboxName:             workspace.SandboxName("obsidian", id), Tmux: cfg.Tmux, Switch: os.Getenv("TMUX") != "",
		})
		if err != nil {
			return actionMsg{err: err}
		}
		switchAfterCreate := os.Getenv("TMUX") != ""
		return actionMsg{message: "Created " + created.SessionName, refresh: !switchAfterCreate, quit: switchAfterCreate}
	}
}

func (m model) createSessionForWorktree(task protocol.Task, ref protocol.SourceRef) tea.Cmd {
	return func() tea.Msg {
		switchAfterCreate := os.Getenv("TMUX") != ""
		sessionName := workspace.SessionName(filepath.Base(filepath.Dir(ref.Path)), filepath.Base(ref.Path))
		cfg, err := config.Load()
		if err != nil {
			return actionMsg{err: err}
		}
		created, err := workspace.CreateSessionWithOptions(context.Background(), workspace.ExecRunner{}, workspace.CreateSessionOptions{
			Path:                    ref.Path,
			SessionName:             sessionName,
			Model:                   cfg.Model,
			Thinking:                cfg.Thinking,
			Sandbox:                 cfg.SBX.Enabled,
			SandboxKitName:          cfg.SBX.Kit.Name,
			SandboxKitPath:          cfg.SBX.Kit.Path,
			AdditionalSandboxMounts: cfg.SBX.AdditionalMounts,
			SandboxName:             sandboxNameForWorktree(task, ref.Path),
			Tmux:                    cfg.Tmux,
			Switch:                  switchAfterCreate,
		})
		if err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{message: "Created " + created.SessionName, refresh: !switchAfterCreate, quit: switchAfterCreate}
	}
}

func sandboxNameForWorktree(task protocol.Task, path string) string {
	for _, ref := range task.SourceRefs {
		if ref.Source != "sbx" || ref.Kind != "sandbox" || !samePath(ref.Path, path) {
			continue
		}
		if ref.Metadata != nil && strings.TrimSpace(ref.Metadata["name"]) != "" {
			return ref.Metadata["name"]
		}
		if strings.TrimSpace(ref.Title) != "" {
			return ref.Title
		}
		return strings.TrimPrefix(ref.ID, "sbx:sandbox:")
	}
	return ""
}

func samePath(left string, right string) bool {
	return strings.TrimSpace(left) != "" && strings.TrimSpace(right) != "" && filepath.Clean(left) == filepath.Clean(right)
}

func (m model) createWorkspaceForPullRequest(ref protocol.SourceRef) tea.Cmd {
	return func() tea.Msg {
		repo, err := localRepoForPullRequest(ref)
		if err != nil {
			return actionMsg{err: err}
		}
		runner := workspace.ExecRunner{}
		name := strings.TrimSpace(ref.Presentation.WorkspaceName)
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
		workspaceProvider, err := app.DefaultIntegrations().Workspace()
		if err != nil {
			return actionMsg{err: err}
		}
		created, err := workspaceProvider.Create(context.Background(), integration.CreateWorkspaceRequest{
			Repo:                    repo,
			BranchMode:              integration.WorkspaceBranchExisting,
			Name:                    name,
			Branch:                  name,
			Model:                   cfg.Model,
			Thinking:                cfg.Thinking,
			Sandbox:                 cfg.SBX.Enabled,
			SandboxKitName:          cfg.SBX.Kit.Name,
			SandboxKitPath:          cfg.SBX.Kit.Path,
			AdditionalSandboxMounts: cfg.SBX.AdditionalMounts,
			Tmux:                    cfg.Tmux,
			Switch:                  switchAfterCreate,
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
	branch = strings.TrimSpace(branch)
	branch = strings.TrimPrefix(branch, "refs/remotes/")
	branch = strings.TrimPrefix(branch, "origin/")
	return strings.TrimPrefix(branch, "refs/heads/"), nil
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

func (m model) openTask(task protocol.Task, choice linkChoice) tea.Cmd {
	if choice.Action != "" {
		return m.openSourceAction(task, choice)
	}
	return m.openTaskURL(task, choice.URL)
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

func (m model) openSourceAction(task protocol.Task, choice linkChoice) tea.Cmd {
	return func() tea.Msg {
		result, err := sourceactions.Open(context.Background(), task, choice.Action, choice.Ref)
		if err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{message: result.Message, refresh: result.Refresh, quit: result.Quit}
	}
}

func currentTaskCursor(tasks []protocol.Task) (int, bool) {
	return taskrefs.TaskCursorForCurrent(tasks, taskrefs.DetectCurrentContext())
}

func taskLinks(task protocol.Task) []linkChoice {
	seen := map[string]bool{}
	var links []linkChoice
	usedKeys := map[string]bool{}
	reserveKey := func(preferred string, source string) string {
		key := strings.ToLower(strings.TrimSpace(preferred))
		if key != "" && !usedKeys[key] {
			usedKeys[key] = true
			return key
		}
		key = linkMnemonic(source, usedKeys)
		if key == "" {
			key = fmt.Sprint(len(links) + 1)
		}
		usedKeys[key] = true
		return key
	}
	addURL := func(source string, label string, url string) {
		if url == "" || seen[url] || len(links) >= 9 {
			return
		}
		seen[url] = true
		links = append(links, linkChoice{Key: reserveKey("", source), Source: source, Label: label, Detail: url, URL: url})
	}
	addAction := func(preferredKey string, source string, label string, detail string, action string, ref protocol.SourceRef) {
		if action == "" || len(links) >= 9 {
			return
		}
		if ref.URL != "" {
			seen[ref.URL] = true
		}
		links = append(links, linkChoice{Key: reserveKey(preferredKey, source), Source: source, Label: label, Detail: detail, Action: action, Ref: ref})
	}

	for _, ref := range task.SourceRefs {
		label := sourceRefLabel(ref)
		actions := sourceactions.SourceRefActions(ref, label)
		for _, action := range actions {
			addAction(action.PreferredKey, action.Source, action.Label, action.Detail, action.ID, action.Ref)
		}
		if len(actions) == 0 {
			addURL(sourceRefSourceLabel(ref), label, ref.URL)
		}
	}
	addURL("link", task.Title, task.URL)
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

func linkMnemonic(label string, used map[string]bool) string {
	for _, r := range label {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			continue
		}
		key := strings.ToLower(string(r))
		if !used[key] {
			return key
		}
	}
	return ""
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
	return openurl.Open(context.Background(), url)
}

func (m model) header(width int) string {
	counts := strings.Join([]string{
		urgentStyle.Render(fmt.Sprintf("🚨 %d urgent", m.summary.Immediate)),
		attentionStyle.Render(fmt.Sprintf("👀 %d attention", m.summary.Attention)),
		progressStyle.Render(fmt.Sprintf("⏳ %d progress", m.summary.InProgress)),
		lowStyle.Render(fmt.Sprintf("🔇 %d low", m.summary.LowPriority)),
		doneStyle.Render(fmt.Sprintf("✅ %d done", m.summary.Done)),
	}, "  ")

	return truncateLine(lipgloss.JoinHorizontal(lipgloss.Top, titleStyle.Render("Radar"), "  ", counts), width)
}

func (m model) taskList(width int, height int) string {
	lines, selectedStart, selectedEnd := m.taskLines(width)
	scroll := adjustedTaskScroll(lines, selectedStart, selectedEnd, m.scroll, height)
	return scrolledLines(lines, selectedStart, selectedEnd, scroll, height)
}

func (m *model) syncTaskScroll() {
	if len(m.tasks) == 0 {
		m.scroll = 0
		return
	}
	width := m.contentWidth()
	lines, selectedStart, selectedEnd := m.taskLines(width)
	m.scroll = adjustedTaskScroll(lines, selectedStart, selectedEnd, m.scroll, m.taskListHeight(width))
}

func (m model) taskListHeight(width int) int {
	before := []string{m.header(width)}
	return m.availableTaskRows(before, m.afterTaskSections(width))
}

func adjustedTaskScroll(lines []string, selectedStart int, selectedEnd int, scroll int, height int) int {
	if height <= 0 || len(lines) <= height {
		return 0
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
	return max(0, min(scroll, len(lines)-height))
}

func scrolledLines(lines []string, selectedStart int, selectedEnd int, scroll int, height int) string {
	if height <= 0 || len(lines) <= height {
		return strings.Join(lines, "\n")
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
		{key: "low_priority", title: "🔇 Low priority", style: lowStyle},
		{key: "done", title: "✅ Done (last 3 days)", style: doneStyle},
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
	if reason := displayTaskReason(task); reason != "" {
		if !selected {
			reason = subtleStyle.Render(reason)
		}
		title = fmt.Sprintf("%s  %s", title, reason)
	}
	return title
}

func displayTaskReason(task protocol.Task) string {
	return task.Reason
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
