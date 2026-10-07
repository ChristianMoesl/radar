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
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"radar/internal/app"
	"radar/internal/cleanup"
	"radar/internal/client"
	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/openurl"
	"radar/internal/pathdisplay"
	"radar/internal/protocol"
	"radar/internal/sourceactions"
	"radar/internal/taskrefs"
	"radar/internal/update"
	"radar/internal/version"
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
	session  string
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
	warning  string
	err      error
}

type cleanupPreviewMsg struct {
	preview protocol.CleanupPreview
	err     error
}

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
	scroll  int
	loading bool
}

type forkMember struct {
	repository string
	path       string
	branch     string
}

type createForm struct {
	repo           string
	branchMode     integration.WorkspaceBranchMode
	branch         string
	base           string
	name           string
	taskLinkingKey string
	notePath       string
	forkPiSession  string
	repoList       picker
	intentList     picker
	branchList     picker
	baseList       picker
	memberList     picker
	forkMembers    []forkMember
}

type model struct {
	releaseNotice       update.Notice
	relaunch            bool
	creations           *creationTracker
	operation           taskOperation
	operationGeneration uint64
	operationFrame      int
	taskFailures        []taskFailure
	cleanupTask         protocol.Task
	deletion            taskDeletionState
	editor              workspaceEditor
	socketPath          string
	width               int
	height              int
	loading             bool
	err                 error
	summary             protocol.Summary
	tasks               []protocol.Task
	sources             []protocol.SourceStatus
	sourcesExpanded     bool
	cursor              int
	selectedSection     string
	expandedSections    map[string]bool
	selectedCurrentTask bool
	mode                string
	detail              detailState
	create              createForm
	cleanup             protocol.CleanupPreview
	cleanupDetails      bool
	cleanupScroll       int
	links               []linkChoice
	linkTask            protocol.Task
	linkCursor          int
	linkScroll          int
	worktrees           []protocol.SourceRef
	worktreeTask        protocol.Task
	worktreeCursor      int
	worktreeScroll      int
	message             string
	authoredTitle       string
	scroll              int
	revision            int64
	watching            bool
}

const (
	createIntentExisting = "Work on an existing branch"
	createIntentNew      = "Create a new branch"
)

func Run(socketPath string) error {
	return runModel(newModel(socketPath))
}

func RunCreate(socketPath string) error {
	model := newModel(socketPath)
	model.mode = "workspace_name"
	model.editor = workspaceEditor{active: true, desired: integration.DesiredWorkspaceDescription{Worktrees: []integration.DesiredWorkspaceWorktree{}}}
	return runModel(model)
}

func RunFork(socketPath string) error {
	form, err := newForkCreateForm()
	if err != nil {
		return err
	}
	model := newModel(socketPath)
	model.mode = "create_base"
	if len(form.forkMembers) > 1 {
		model.mode = "fork_member"
	}
	form.branchMode = integration.WorkspaceBranchNew
	model.create = form
	return runModel(model)
}

func newModel(socketPath string) model {
	return model{socketPath: socketPath, loading: true, creations: &creationTracker{}}
}

func (m model) Init() tea.Cmd {
	commands := []tea.Cmd{m.fetch("tasks")}
	if runtime.GOOS == "darwin" {
		commands = append(commands, checkReleaseNotice)
	}
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
	case releaseNoticeMsg:
		m.releaseNotice = update.Notice(msg)
		m.syncTaskScroll()
		return m, nil
	case maintenanceMsg:
		if version.CheckInstalled() != nil {
			m.relaunch = true
			return m, tea.Quit
		}
		m.err = msg.err
		return m, m.fetch("tasks")
	case operationTickMsg:
		if m.operation.kind == "" || uint64(msg) != m.operationGeneration {
			return m, nil
		}
		m.operationFrame++
		return m, m.operationTick()
	case taskActionMsg:
		action := msg.action
		if m.operation.kind == "session" || m.operation.kind == "cleanup" {
			action.message = ""
		}
		m.finishOperation(action.err, true)
		m.message = action.message
		if action.response != nil {
			m.applyResponse(*action.response, false)
		}
		if action.session != "" && action.err == nil {
			return m, m.switchTmuxSession(action.session)
		}
		if action.quit && action.err == nil {
			return m, tea.Quit
		}
		if action.refresh && action.err == nil {
			m.loading = true
			return m, m.fetch("refresh")
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.syncTaskScroll()
		m.clampDetailScroll()
		m.syncSelectionScroll()
		if m.mode == "open_link" {
			m.syncLinkScroll()
		}
		return m, nil
	case tea.KeyMsg:
		if m.operation.kind != "" && (!operationNavigationKey(msg.String()) || (msg.String() == "enter" && (m.mode != "" || m.selectedSection == ""))) {
			return m, nil
		}
		if strings.HasPrefix(m.mode, "workspace_") && !m.operationOnRow() {
			return m.updateWorkspace(msg)
		}
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
		if strings.HasPrefix(m.mode, "create_") || m.mode == "fork_member" {
			return m.updateCreate(msg)
		}
		if m.mode == "detail" {
			return m.updateDetail(msg)
		}
		if m.mode == "open_link" {
			return m.updateOpenLink(msg)
		}
		if m.mode == "worktree_session" {
			switch msg.String() {
			case "esc", "backspace":
				m.mode = ""
				m.worktrees = nil
				m.worktreeCursor, m.worktreeScroll = 0, 0
				return m, nil
			case "q", "ctrl+c":
				return m, tea.Quit
			case "j", "down", "ctrl+n":
				if m.worktreeCursor < len(m.worktrees)-1 {
					m.worktreeCursor++
				}
				m.syncSelectionScroll()
				return m, nil
			case "k", "up", "ctrl+p":
				if m.worktreeCursor > 0 {
					m.worktreeCursor--
				}
				m.syncSelectionScroll()
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
				m.worktreeCursor, m.worktreeScroll = 0, 0
				cmd := m.startOperation(task, "session", "Starting session…", "Session start failed", taskAction(m.createSessionForWorktree(task, ref)))
				return m, cmd
			default:
				return m, nil
			}
		}
		if m.mode == "task_delete_confirm" {
			return m.updateTaskDeletion(msg)
		}
		if m.mode == "cleanup_confirm" {
			switch msg.String() {
			case "d":
				m.cleanupDetails = !m.cleanupDetails
				m.cleanupScroll = 0
				return m, nil
			case "j", "down", "ctrl+n", "k", "up", "ctrl+p", "pgdown", "pgup", "ctrl+d", "ctrl+u", "home", "end":
				m.scrollCleanup(msg.String())
				return m, nil
			case "y", "Y":
				if len(m.cleanup.Targets) == 0 {
					return m, nil
				}
				preview := m.cleanup
				m.mode = ""
				cmd := m.startOperation(m.cleanupTask, "cleanup", "Cleaning up…", "Cleanup failed", taskAction(m.cleanupSelected(preview)))
				return m, cmd
			case "esc", "backspace", "n", "N":
				m.mode = ""
				m.err = nil
				return m, nil
			case "q", "ctrl+c":
				return m, tea.Quit
			}
			// A confirmation is modal: Enter and main-view shortcuts do nothing.
			return m, nil
		}
		m.ensureVisibleSelection()
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "n":
			m.mode = "task_authoring"
			m.authoredTitle = ""
			m.err = nil
			m.message = ""
		case "d":
			if task, ok := m.selectedTask(); ok {
				ref, ok := authoredTaskRef(task)
				if !ok {
					m.message = "Selected task does not support authored lifecycle changes"
					return m, nil
				}
				m.loading = true
				m.err = nil
				return m, m.setAuthoredTaskDone(task, ref.Metadata["state"] != "done")
			}
		case "m":
			if task, ok := m.selectedTask(); ok {
				m.loading = true
				m.err = nil
				return m, m.setTaskMuted(task, !task.Muted)
			}
		case "D":
			if task, ok := m.selectedTask(); ok {
				if _, ok := authoredTaskRef(task); !ok {
					m.message = "Selected task has no authored note to delete"
					return m, nil
				}
				m.deletion = taskDeletionState{task: task}
				cmd := m.startOperation(task, "task-delete-check", "Checking task deletion…", "Task deletion check failed", m.previewTaskDeletion(task))
				return m, cmd
			}
		case "p":
			if task, ok := m.selectedTask(); ok {
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
			return m.newWorkspace()
		case "w":
			if task, ok := m.selectedTask(); ok {
				return m.editWorkspace(task)
			}
		case "u":
			if runtime.GOOS == "darwin" {
				return m, runMaintenance("upgrade", "--from-tui")
			}
		case "N":
			if runtime.GOOS == "darwin" {
				return m, runMaintenance("setup", "notifications")
			}
		case "f":
			return m, m.openConfig()
		case "i", "right":
			if task, ok := m.selectedTask(); ok {
				m.mode = "detail"
				m.detail = detailState{task: task, available: true}
			}
		case "o":
			if task, ok := m.selectedTask(); ok {
				m.links = taskLinks(task)
				m.linkCursor, m.linkScroll = 0, 0
				if len(m.links) == 0 {
					m.message = "No link on selected task"
					return m, nil
				}
				m.linkTask = task
				m.mode = "open_link"
				m.message = ""
			}
		case "x":
			if task, ok := m.selectedTask(); ok {
				m.cleanupTask = task
				cmd := m.startOperation(m.cleanupTask, "cleanup-check", "Checking local resources…", "Resource check failed", m.previewCleanup(m.cleanupTask))
				return m, cmd
			}
		case "X":
			m.loading = true
			m.err = nil
			m.message = "Garbage collecting…"
			return m, m.garbageCollect()
		case "s":
			m.sourcesExpanded = !m.sourcesExpanded
			m.syncTaskScroll()
		case "r":
			m.loading = true
			m.err = nil
			m.message = "Refreshing…"
			return m, m.fetch("refresh")
		case "j", "down", "ctrl+n":
			m.moveCursor(1)
		case "k", "up", "ctrl+p":
			m.moveCursor(-1)
		case "ctrl+d", "pgdown":
			if m.mode == "" || m.operationOnRow() {
				m.moveCursorPage(1)
			}
		case "ctrl+u", "pgup":
			if m.mode == "" || m.operationOnRow() {
				m.moveCursorPage(-1)
			}
		case "g", "home":
			m.moveCursorToEdge(false)
		case "G", "end":
			m.moveCursorToEdge(true)
		case "enter":
			return m.activateSelected()
		}
	case workspaceStateMsg:
		m.message = ""
		m.finishOperation(msg.err, true)
		if msg.err != nil {
			m.mode = ""
			m.editor = workspaceEditor{}
			return m, nil
		}
		m.editor, m.mode = msg.editor, "workspace_edit"
		if m.editor.state.Path == "" && m.editor.create.Name == "" {
			m.mode = "workspace_name"
		}
	case workspacePlanMsg:
		m.mode, m.message = "workspace_edit", ""
		m.finishOperation(msg.err, true)
		if msg.err == nil {
			m.editor.plan = msg.plan
			if msg.plan.Note != nil {
				m.editor.desired.Note = msg.plan.Note
			}
			m.editor.scroll = 0
			if len(msg.plan.Changes) == 0 {
				m.message = "No workspace changes"
			} else if m.editor.createsWorkspace() {
				return m.applyWorkspace()
			} else {
				m.mode = "workspace_confirm"
			}
		}
	case workspaceAppliedMsg:
		m.message = ""
		if msg.err == nil && msg.result.ReconfirmRequired && msg.result.Plan != nil {
			m.finishOperation(nil, false)
			m.editor.plan, m.mode = *msg.result.Plan, "workspace_confirm"
			m.editor.scroll = 0
			m.message = "Workspace plan changed; review it again"
			return m, nil
		}
		problem := msg.err
		if problem == nil && msg.created.Path == "" && !msg.result.OK {
			problem = fmt.Errorf("workspace changes did not finish: %s; reopen the editor to inspect completed work", msg.result.Error)
		}
		// Never keep submitting a stale draft after a partial apply.
		if m.mode != "detail" {
			m.mode = ""
		}
		m.editor = workspaceEditor{}
		if problem == nil && msg.created.Path != "" && (canSwitchMultiplexer() || (msg.created.SessionName != "" && msg.created.Warning == "")) {
			m.finishOperation(nil, true)
			if canSwitchMultiplexer() {
				return m, tea.Quit
			}
			return m, m.switchTmuxSession(msg.created.SessionName)
		}
		var notices []string
		if msg.result.WorktreesAdded > 0 || msg.result.WorktreesRemoved > 0 {
			notices = append(notices, "Run /radar-reload-workspace-resources in Pi to refresh member skills")
		}
		if msg.result.Warning != "" {
			notices = append(notices, msg.result.Warning)
		}
		if msg.created.Warning != "" {
			notices = append(notices, msg.created.Warning)
		}
		m.message = strings.Join(notices, ". ")
		if problem != nil {
			m.finishOperation(problem, false)
			// Refresh partial work without erasing the operation's explanation.
			return m, m.refreshWorkspaceResult(m.message, m.err)
		}
		return m, taskAction(m.refreshWorkspaceResult(m.message, nil))
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
		if msg.err == nil && msg.response.Version != "" && msg.response.Version != version.Current() && version.CheckInstalled() != nil {
			m.relaunch = true
			return m, tea.Quit
		}
		m.watching = false
		if msg.err != nil {
			// Revisions are local to a daemon process. Reconnect from zero so
			// a restarted daemon's snapshots are not mistaken for stale replies.
			m.revision = 0
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
			m.create.repoList.cursor, m.create.repoList.scroll = 0, 0
		}
	case branchesMsg:
		m.err = msg.err
		if msg.err == nil {
			m.message = msg.warning
		}
		if m.mode == "create_branch" {
			m.create.branchList.loading = false
			if msg.err == nil {
				m.create.branchList.options = existingBranchNames(msg.branches)
				m.create.branchList.cursor, m.create.branchList.scroll = 0, 0
			}
		} else {
			m.create.baseList.loading = false
			if msg.err == nil {
				m.create.baseList.options = msg.branches
				m.create.baseList.cursor, m.create.baseList.scroll = 0, 0
			}
		}
	case taskDeletionPreviewMsg:
		m.finishOperation(msg.err, true)
		m.message = ""
		if msg.err == nil {
			m.deletion.preview, m.deletion.scroll = msg.preview, 0
			m.mode = "task_delete_confirm"
		}
	case cleanupPreviewMsg:
		m.finishOperation(msg.err, true)
		m.message = ""
		if msg.err == nil {
			m.cleanup = msg.preview
			m.cleanupDetails = false
			m.cleanupScroll = 0
			m.mode = "cleanup_confirm"
		}
	case actionMsg:
		m.loading = false
		m.err = msg.err
		m.message = msg.message
		if msg.response != nil {
			m.applyResponse(*msg.response, false)
		}
		if msg.session != "" && msg.err == nil {
			return m, m.switchTmuxSession(msg.session)
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

func (m model) View() string {
	if m.mode == "task_delete_confirm" {
		return m.taskDeletionScreen()
	}
	if m.mode == "cleanup_confirm" {
		return m.cleanupScreen()
	}
	contentWidth := m.contentWidth()

	var sections []string
	sections = append(sections, m.header(contentWidth))

	if strings.HasPrefix(m.mode, "workspace_") && !m.operationOnRow() {
		sections = append(sections, m.workspaceView(contentWidth))
		return m.renderFrame(strings.Join(sections, "\n\n"), contentWidth)
	}
	if m.mode == "task_authoring" {
		sections = append(sections, m.taskAuthoringView(contentWidth))
		sections = append(sections, helpStyle.Render("type a title • enter create • esc cancel"))
		return m.renderFrame(strings.Join(sections, "\n\n"), contentWidth)
	}

	if m.mode == "detail" {
		return m.detailScreen()
	}

	if m.mode == "open_link" {
		sections = append(sections, m.openLinkView(contentWidth))
		sections = append(sections, openLinkHelp(contentWidth))
		return m.renderFrame(strings.Join(sections, "\n\n"), contentWidth)
	}

	if m.mode == "worktree_session" {
		sections = append(sections, m.worktreeSessionView(contentWidth))
		sections = append(sections, worktreeSessionHelp(contentWidth))
		return m.renderFrame(strings.Join(sections, "\n\n"), contentWidth)
	}

	if strings.HasPrefix(m.mode, "create_") || m.mode == "fork_member" {
		sections = append(sections, m.createView(contentWidth))
		sections = append(sections, m.createHelp(contentWidth))
		return m.renderFrame(strings.Join(sections, "\n\n"), contentWidth)
	}

	afterTaskSections := m.afterTaskSections(contentWidth)
	taskRows := m.availableTaskRows(sections, afterTaskSections)
	var tasks string
	if m.loading && len(m.tasks) == 0 {
		tasks = subtleStyle.Render("Loading tasks…")
	} else if len(m.tasks) == 0 {
		tasks = subtleStyle.Render("No tasks need your attention.")
	} else {
		tasks = m.taskList(contentWidth, taskRows)
	}
	if m.height > 0 {
		// Reserve the whole list viewport even for short or empty lists. Sources
		// and shortcuts stay at the bottom rather than following the last task.
		tasks = lipgloss.NewStyle().Height(taskRows).Render(tasks)
	}
	sections = append(sections, tasks)
	sections = append(sections, afterTaskSections...)
	return m.renderFrame(strings.Join(sections, "\n\n"), contentWidth)
}

func (m model) afterTaskSections(width int) []string {
	sections := []string{}
	if m.operation.kind != "" && !m.operationOnRow() {
		label := m.operation.label
		if m.operation.task.Title != "" {
			label = m.operation.task.Title + " · " + label
		}
		sections = append(sections, truncateLine(m.operationMarker()+" "+label, width))
	}
	if len(m.sources) > 0 {
		sections = append(sections, m.sourceList(width))
	}
	sections = append(sections, m.mainHelp(width))
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
	// Joining sections with two newlines adds one blank row per boundary;
	// each section's own rows are already included in its height above.
	used += len(before) + len(after)
	return max(3, m.height-used)
}

// Reduce the outer padding on small terminals without changing task spacing.
func (m model) frameStyle() lipgloss.Style {
	style := appStyle
	if m.width > 0 && m.width < 100 {
		style = style.PaddingLeft(2).PaddingRight(2)
	}
	if m.height > 0 && m.height < 30 {
		style = style.PaddingTop(1).PaddingBottom(1)
	}
	if m.height > 0 && m.height < 20 {
		style = style.PaddingTop(0).PaddingBottom(0)
	}
	return style
}

func (m model) frameHeight() int {
	height := m.frameStyle().GetVerticalFrameSize()
	if !canSwitchMultiplexer() {
		height += panelStyle.GetVerticalFrameSize()
	}
	return height
}

func (m model) contentWidth() int {
	if m.width <= 0 {
		if canSwitchMultiplexer() {
			return 80
		}
		return maxContentWidth
	}
	frameWidth := m.frameStyle().GetHorizontalFrameSize()
	if !canSwitchMultiplexer() {
		frameWidth += panelStyle.GetHorizontalFrameSize()
	}
	width := max(1, m.width-frameWidth)
	if !canSwitchMultiplexer() {
		width = min(width, maxContentWidth)
	}
	return width
}

func (m model) renderFrame(content string, width int) string {
	style := m.frameStyle()
	if !canSwitchMultiplexer() {
		content = panelStyle.Width(width + panelStyle.GetHorizontalPadding()).Render(content)
		width += panelStyle.GetHorizontalFrameSize()
	}
	// Lip Gloss Width includes padding, but not borders.
	frame := style.Width(width + style.GetHorizontalPadding()).Render(content)
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
		lineWidth := lipgloss.Width(lines[target])
		col := max(0, lineWidth-popupWidth-2)
		prefix := ansi.Cut(lines[target], 0, col)
		rest := ansi.Cut(lines[target], col+lipgloss.Width(popupLine), lineWidth)
		lines[target] = prefix + popupLine + rest
	}
	return strings.Join(lines, "\n")
}

func newCreateForm() createForm {
	return createForm{
		repoList:   picker{loading: true},
		intentList: picker{options: []string{createIntentNew, createIntentExisting}},
	}
}

func workspaceNameForTask(task protocol.Task) string {
	return taskrefs.WorkspaceName(task)
}

func newForkCreateForm() (createForm, error) {
	integrations := app.DefaultIntegrations()
	multiplexer, err := integrations.Multiplexer()
	if err != nil {
		return createForm{}, err
	}
	session, active, err := multiplexer.Current(context.Background())
	if err != nil {
		return createForm{}, err
	}
	if !active || strings.TrimSpace(session.Name) == "" {
		return createForm{}, fmt.Errorf("radar fork requires an active multiplexer session")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return createForm{}, err
	}
	manager, err := integrations.WorkspaceManager()
	if err != nil {
		return createForm{}, err
	}
	group, registered, err := manager.RegisteredWorkspace(cwd)
	if err != nil {
		return createForm{}, err
	}
	form := createForm{forkPiSession: session.Name}
	if registered {
		if len(group.Members) == 0 {
			return createForm{}, fmt.Errorf("workspace has no Git member to fork")
		}
		for _, member := range group.Members {
			form.forkMembers = append(form.forkMembers, forkMember{repository: member.Repository, path: member.Path, branch: member.Branch})
			form.memberList.options = append(form.memberList.options, member.Path)
		}
		if len(form.forkMembers) == 1 {
			configureForkMember(&form, form.forkMembers[0])
		}
		return form, nil
	}
	workspaceProvider, err := integrations.Workspace()
	if err != nil {
		return createForm{}, err
	}
	current, found, err := workspaceProvider.Current(context.Background(), cwd)
	if err != nil {
		return createForm{}, err
	}
	if !found {
		return createForm{}, fmt.Errorf("current directory is not a code workspace")
	}
	configureForkMember(&form, forkMember{repository: current.Repo, path: current.Path, branch: current.Branch})
	return form, nil
}

func configureForkMember(form *createForm, member forkMember) {
	form.repo = member.repository
	form.baseList = picker{loading: true, query: member.branch}
}

func (m model) updateCreate(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.mode = ""
		if m.editor.active {
			m.mode = "workspace_edit"
		}
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
		manager, err := app.DefaultIntegrations().WorkspaceManager()
		if err != nil {
			return reposMsg{err: err}
		}
		repos, err := manager.DiscoverRepositories(context.Background(), cwd)
		return reposMsg{repos: repos, err: err}
	}
}

func (m model) loadBranches(repo string) tea.Cmd {
	return func() tea.Msg {
		manager, err := app.DefaultIntegrations().WorkspaceManager()
		if err != nil {
			return branchesMsg{err: err}
		}
		return loadBranchOptions(context.Background(), manager, repo)
	}
}

type repositoryBranchProvider interface {
	RepositoryBranches(ctx context.Context, repository string) ([]string, string, error)
}

func loadBranchOptions(ctx context.Context, manager repositoryBranchProvider, repo string) branchesMsg {
	branches, warning, err := manager.RepositoryBranches(ctx, repo)
	return branchesMsg{branches: branches, warning: warning, err: err}
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
		list.cursor, list.scroll = 0, 0
		return
	}
	list.cursor = max(0, min(list.cursor+delta, len(matches)-1))
	m.syncSelectionScroll()
}

func (m model) selectCreateStep() (tea.Model, tea.Cmd) {
	switch m.mode {
	case "fork_member":
		selected := selectedPickerOption(m.create.memberList)
		for _, member := range m.create.forkMembers {
			if member.path == selected {
				configureForkMember(&m.create, member)
				m.mode = "create_base"
				m.err = nil
				return m, m.loadBranches(member.repository)
			}
		}
		m.err = fmt.Errorf("select a workspace member")
		return m, nil
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
		if strings.TrimSpace(m.create.name) == "" {
			m.create.name = selected
		}
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
	if m.editor.active {
		member := integration.DesiredWorkspaceWorktree{Repository: m.create.repo, BranchMode: m.create.branchMode}
		if member.BranchMode == integration.WorkspaceBranchNew {
			member.Name, member.Base = m.create.name, m.create.base
		} else {
			member.Branch = m.create.branch
		}
		for _, current := range m.editor.desired.Worktrees {
			if current.Repository == member.Repository && current.BranchMode == member.BranchMode && current.Branch == member.Branch && current.Name == member.Name {
				m.err = fmt.Errorf("repository branch is already in this workspace draft")
				return m, nil
			}
		}
		m.editor.desired.Worktrees = append(m.editor.desired.Worktrees, member)
		m.editor.cursor = len(m.editor.desired.Worktrees) - 1
		m.mode, m.err = "workspace_edit", nil
		return m, nil
	}
	form := m.create
	m.editor = workspaceEditor{active: true, create: integration.ManagedWorkspaceRequest{
		Repo: form.repo, BranchMode: form.branchMode, Branch: form.branch, Base: form.base, Name: form.name,
		ForkPiSession: form.forkPiSession, TaskLinkingKey: form.taskLinkingKey, NotePath: form.notePath,
	}}
	return m.previewWorkspace()
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

func (m model) setTaskMuted(task protocol.Task, muted bool) tea.Cmd {
	return func() tea.Msg {
		var response protocol.Response
		var err error
		message := "Task unmuted"
		if muted {
			response, err = client.MuteTask(m.socketPath, task.ID)
			message = "Task muted"
		} else {
			response, err = client.UnmuteTask(m.socketPath, task.ID)
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
		return actionMsg{response: &response, message: cleanupSuccessMessage(preview)}
	}
}

func (m *model) appendCreateQuery(value string) {
	switch m.mode {
	case "fork_member":
		m.create.memberList.query += value
		m.create.memberList.cursor, m.create.memberList.scroll = 0, 0
	case "create_repo":
		m.create.repoList.query += value
		m.create.repoList.cursor, m.create.repoList.scroll = 0, 0
	case "create_intent":
		m.create.intentList.query += value
		m.create.intentList.cursor, m.create.intentList.scroll = 0, 0
	case "create_branch":
		m.create.branchList.query += value
		m.create.branchList.cursor, m.create.branchList.scroll = 0, 0
	case "create_base":
		m.create.baseList.query += value
		m.create.baseList.cursor, m.create.baseList.scroll = 0, 0
	case "create_name":
		m.create.name += value
	}
}

func (m *model) backspaceCreateQuery() {
	switch m.mode {
	case "fork_member":
		m.create.memberList.query = dropLastRune(m.create.memberList.query)
		m.create.memberList.cursor, m.create.memberList.scroll = 0, 0
	case "create_repo":
		m.create.repoList.query = dropLastRune(m.create.repoList.query)
		m.create.repoList.cursor, m.create.repoList.scroll = 0, 0
	case "create_intent":
		m.create.intentList.query = dropLastRune(m.create.intentList.query)
		m.create.intentList.cursor, m.create.intentList.scroll = 0, 0
	case "create_branch":
		m.create.branchList.query = dropLastRune(m.create.branchList.query)
		m.create.branchList.cursor, m.create.branchList.scroll = 0, 0
	case "create_base":
		m.create.baseList.query = dropLastRune(m.create.baseList.query)
		m.create.baseList.cursor, m.create.baseList.scroll = 0, 0
	case "create_name":
		m.create.name = dropLastRune(m.create.name)
	}
}

func (m *model) activePicker() *picker {
	switch m.mode {
	case "fork_member":
		return &m.create.memberList
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

func (m model) createPickerHeading(width int) string {
	title, label := "Create workspace", "Repository"
	switch m.mode {
	case "fork_member":
		title, label = "Fork workspace", "Member worktree"
	case "create_intent":
		label = "Action"
	case "create_branch":
		label = "Existing branch"
	case "create_base":
		label = "Starting reference"
		if m.create.forkPiSession != "" {
			title = "Fork workspace"
		}
	}
	lines := []string{}
	if m.mode == "create_intent" || m.mode == "create_branch" || m.mode == "create_base" {
		lines = append(lines, subtleStyle.Render(singleLine("Repository "+shortenPath(m.create.repo), width)))
	}
	lines = append(lines, titleStyle.Render(singleLine(title, width)))
	if list := m.activePicker(); list != nil {
		// Keep the most recently typed query characters visible without wrapping.
		query := strings.Join(strings.Fields(label+": "+list.query), " ")
		lines = append(lines, inputTail(query, width))
	}
	return strings.Join(lines, "\n")
}

func (m model) createView(width int) string {
	if list := m.activePicker(); list != nil {
		heading := m.createPickerHeading(width)
		rows := m.modalListRows(width, heading, m.createHelp(width))
		matches, empty := filteredOptions(*list), "No matches"
		if list.loading {
			matches, empty = nil, "Loading…"
		}
		labels := make([]string, len(matches))
		for i, match := range matches {
			labels[i] = shortenPath(match)
		}
		return heading + "\n" + selectionView(labels, list.cursor, list.scroll, rows, width, empty)
	}
	if m.mode == "create_name" {
		name := m.create.name
		if name == "" {
			name = subtleStyle.Render("type a branch name")
		}
		lines := []string{
			titleStyle.Render("Create workspace"),
			subtleStyle.Render(singleLine("Repository "+shortenPath(m.create.repo), width)),
			subtleStyle.Render(singleLine("Start      "+m.create.base, width)),
		}
		if m.create.forkPiSession != "" {
			lines[0] = titleStyle.Render("Fork workspace")
			lines = append(lines, subtleStyle.Render(singleLine("Pi fork    "+m.create.forkPiSession, width)))
		}
		lines = append(lines, selectedStyle.Width(width).Render(inputTail("› New branch "+name, width)))
		return strings.Join(lines, "\n")
	}
	return ""
}

func cleanupSafetyMessage(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	message = strings.ToUpper(message[:1]) + message[1:]
	if !strings.HasSuffix(message, ".") {
		message += "."
	}
	return message
}

func cleanupSuccessMessage(preview protocol.CleanupPreview) string {
	return fmt.Sprintf("Cleaned up %d local resource(s)", len(preview.Targets))
}

func worktreeSessionHeading(width int) string {
	return titleStyle.Render(singleLine("Create tmux session for worktree", width))
}

func (m model) worktreeSessionView(width int) string {
	heading := worktreeSessionHeading(width)
	rows := m.modalListRows(width, heading, worktreeSessionHelp(width))
	labels := make([]string, len(m.worktrees))
	for i, ref := range m.worktrees {
		label := sourceRefLabel(ref)
		if ref.Path != "" {
			label = shortenPath(ref.Path)
		}
		if ref.Branch != "" {
			label += "  " + ref.Branch
		}
		labels[i] = label
	}
	return heading + "\n" + selectionView(labels, m.worktreeCursor, m.worktreeScroll, rows, width, "No git worktrees on selected task.")
}

func shortenPath(path string) string {
	return pathdisplay.HomeRelative(path)
}

func (m *model) applyResponse(response protocol.Response, selectCurrentTask bool) {
	// An in-flight watch/fetch can arrive after a mutation's newer snapshot.
	// In particular, it must not put a deleted task back on the dashboard.
	if response.Revision > 0 && response.Revision < m.revision {
		return
	}
	var selectedTask *protocol.Task
	selectedSection := m.selectedSection
	selectedPosition, activePosition := m.cursorPosition(), -1
	if selected, ok := m.selectedTask(); response.Tasks != nil && ok {
		selectedTask = &selected
		activePosition = 0
		for _, entry := range m.overviewLayout().entries {
			if entry == m.selectedEntry() {
				break
			}
			if entry.section == "" && !historyGroup(m.tasks[entry.task].DisplayGroup()) {
				activePosition++
			}
		}
	}

	if response.Revision > m.revision {
		m.revision = response.Revision
	}
	if response.Summary != nil {
		m.summary = *response.Summary
	}
	if response.Tasks != nil {
		m.tasks = response.Tasks
		m.restoreCursor(selectedTask, selectedSection, selectedPosition, activePosition)
		if m.mode == "detail" {
			wasAvailable := m.detail.available
			cursor, ok := matchingTaskCursor(m.tasks, m.detail.task)
			m.detail.available = ok
			if ok {
				m.detail.task = m.tasks[cursor]
				// Keep Inspect pinned independently of the overview fallback.
				// A returning task regains selection, but a completed/muted
				// task must not pull the overview back into history on refresh.
				if !wasAvailable {
					m.selectTaskCursor(cursor)
				}
			}
		}
	}
	if response.Sources != nil {
		m.sources = response.Sources
	}
	if selectCurrentTask && !m.selectedCurrentTask && m.mode != "detail" {
		if cursor, ok := currentTaskCursor(m.tasks); ok {
			m.selectTaskCursor(cursor)
		}
		m.selectedCurrentTask = true
	}
	m.ensureVisibleSelection()
	m.syncTaskScroll()
	m.clampDetailScroll()
}

// Restore the visible selection, not a hidden task index. Task identity follows
// regrouping; section identity and expansion live independently of snapshots.
func (m *model) restoreCursor(selectedTask *protocol.Task, selectedSection string, selectedPosition, activePosition int) {
	layout := m.overviewLayout()
	if selectedSection != "" {
		entry := visibleEntry{section: selectedSection}
		if _, ok := layout.bounds[entry]; ok {
			m.selectEntry(entry)
			return
		}
	}
	if selectedTask != nil {
		if cursor, ok := matchingTaskCursor(m.tasks, *selectedTask); ok {
			oldGroup, newGroup := selectedTask.DisplayGroup(), m.tasks[cursor].DisplayGroup()
			if oldGroup != newGroup && historyGroup(newGroup) {
				// Prefer the next active task at the vacated active position, then
				// the previous one, even when a history section is already open.
				var active []visibleEntry
				for _, entry := range layout.entries {
					if entry.section == "" && !historyGroup(m.tasks[entry.task].DisplayGroup()) {
						active = append(active, entry)
					}
				}
				if len(active) > 0 {
					m.selectEntry(active[max(0, min(activePosition, len(active)-1))])
					return
				}
				// No active work: select a header, never follow the moved child.
				for _, entry := range layout.entries {
					if entry.section != "" {
						m.selectEntry(entry)
						return
					}
				}
			} else {
				// Unmute/reopen follows the selected task into active work.
				// A refresh hiding a child maps to its collapsed header.
				m.selectTaskCursor(cursor)
				return
			}
		}
	}
	if len(layout.entries) == 0 {
		m.cursor, m.selectedSection, m.scroll = 0, "", 0
		return
	}
	m.selectEntry(layout.entries[max(0, min(selectedPosition, len(layout.entries)-1))])
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
	if m.selectedSection != "" {
		m.toggleSection(m.selectedSection)
		return m, nil
	}
	task, ok := m.selectedTask()
	if !ok {
		return m, nil
	}
	multiplexer, _ := app.DefaultIntegrations().Multiplexer()
	if target := taskrefs.SessionTarget(task, multiplexer); target != "" {
		m.loading = true
		m.err = nil
		return m, m.switchTmuxSession(target)
	}

	if anchor, ok := workspaceAnchorRef(task); ok {
		cmd := m.startOperation(task, "session", "Starting session…", "Session start failed", taskAction(m.openRegisteredWorkspace(anchor)))
		return m, cmd
	}

	worktrees := taskrefs.Worktrees(task)
	switch len(worktrees) {
	case 0:
		return m.editWorkspace(task)
	case 1:
		cmd := m.startOperation(task, "session", "Starting session…", "Session start failed", taskAction(m.createSessionForWorktree(task, worktrees[0])))
		return m, cmd
	default:
		m.mode = "worktree_session"
		m.worktrees = worktrees
		m.worktreeTask = task
		m.worktreeCursor, m.worktreeScroll = 0, 0
		m.message = ""
		m.err = nil
		return m, nil
	}
}

func (m model) switchTmuxSession(target string) tea.Cmd {
	multiplexer, err := app.DefaultIntegrations().Multiplexer()
	if err != nil {
		return func() tea.Msg { return actionMsg{err: err} }
	}
	if !multiplexer.ClientActive() {
		command, err := multiplexer.AttachCommand(integration.SessionTarget{Name: target})
		if err != nil {
			return func() tea.Msg { return actionMsg{err: err} }
		}
		return tea.ExecProcess(command, func(err error) tea.Msg {
			return actionMsg{err: err, refresh: err == nil, message: "Returned from workspace"}
		})
	}
	return func() tea.Msg {
		if err := multiplexer.Switch(context.Background(), integration.SessionTarget{Name: target}); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{quit: true}
	}
}

func workspaceAnchorRef(task protocol.Task) (protocol.SourceRef, bool) {
	for _, ref := range task.SourceRefs {
		if ref.WorkspaceEntry && strings.TrimSpace(ref.Path) != "" {
			return ref, true
		}
	}
	return protocol.SourceRef{}, false
}

func (m model) openRegisteredWorkspace(ref protocol.SourceRef) tea.Cmd {
	return func() tea.Msg {
		switchAfterCreate := canSwitchMultiplexer()
		manager, err := app.DefaultIntegrations().WorkspaceManager()
		if err != nil {
			return actionMsg{err: err}
		}
		created, err := manager.OpenWorkspace(context.Background(), ref.Path, switchAfterCreate)
		if err != nil {
			return actionMsg{err: err}
		}
		if switchAfterCreate {
			return actionMsg{quit: true}
		}
		return actionMsg{session: created.SessionName}
	}
}

func authoredTaskRef(task protocol.Task) (protocol.SourceRef, bool) {
	for _, ref := range task.SourceRefs {
		if ref.Authored {
			return ref, true
		}
	}
	return protocol.SourceRef{}, false
}

func (m model) createSessionForWorktree(task protocol.Task, ref protocol.SourceRef) tea.Cmd {
	return func() tea.Msg {
		switchAfterCreate := canSwitchMultiplexer()
		manager, err := app.DefaultIntegrations().WorkspaceManager()
		if err != nil {
			return actionMsg{err: err}
		}
		sessionName := manager.SessionName(filepath.Base(filepath.Dir(ref.Path)), filepath.Base(ref.Path))
		created, err := manager.CreateSession(context.Background(), integration.CreateSessionRequest{
			Path: ref.Path, SessionName: sessionName, TaskLinkingKey: taskrefs.TaskLinkingKey(task),
			RuntimeResourceID: sandboxNameForWorktree(task, ref.Path), Switch: switchAfterCreate,
		})
		if err != nil {
			return actionMsg{err: err}
		}
		if switchAfterCreate {
			return actionMsg{quit: true}
		}
		return actionMsg{session: created.SessionName}
	}
}

func sandboxNameForWorktree(task protocol.Task, path string) string {
	integrations := app.DefaultIntegrations()
	for _, ref := range task.SourceRefs {
		if !samePath(ref.Path, path) {
			continue
		}
		if name, ok := integrations.RuntimeResourceName(ref); ok {
			return name
		}
	}
	return ""
}

func samePath(left string, right string) bool {
	return strings.TrimSpace(left) != "" && strings.TrimSpace(right) != "" && filepath.Clean(left) == filepath.Clean(right)
}

func workspaceCreationMessage(created integration.Workspace) string {
	message := "Created " + created.SessionName
	if created.Warning != "" {
		message += ". Warning: " + created.Warning
	}
	return message
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

func canSwitchMultiplexer() bool {
	multiplexer, err := app.DefaultIntegrations().Multiplexer()
	return err == nil && multiplexer.ClientActive()
}

func currentTaskCursor(tasks []protocol.Task) (int, bool) {
	integrations := app.DefaultIntegrations()
	workspaceProvider, _ := integrations.Workspace()
	multiplexer, _ := integrations.Multiplexer()
	current := taskrefs.DetectCurrentContext(context.Background(), workspaceProvider, multiplexer)
	return taskrefs.TaskCursorForCurrent(tasks, current, multiplexer)
}

func taskLinks(task protocol.Task) []linkChoice {
	seen := map[string]bool{}
	var links []linkChoice
	usedKeys := map[string]bool{"j": true, "k": true, "q": true}
	reserveKey := func(preferred string, source string) string {
		key := strings.ToLower(strings.TrimSpace(preferred))
		if len([]rune(key)) != 1 || linkMnemonic(key, usedKeys) == "" {
			key = linkMnemonic(source+"abcdefghijklmnopqrstuvwxyz1234567890", usedKeys)
		}
		if key != "" {
			usedKeys[key] = true
		}
		return key
	}
	addURL := func(source string, label string, url string) {
		if url == "" || seen[url] {
			return
		}
		seen[url] = true
		links = append(links, linkChoice{Key: reserveKey("", source), Source: source, Label: label, Detail: url, URL: url})
	}
	addAction := func(preferredKey string, source string, label string, detail string, action string, ref protocol.SourceRef) {
		if action == "" {
			return
		}
		if ref.URL != "" {
			seen[ref.URL] = true
		}
		links = append(links, linkChoice{Key: reserveKey(preferredKey, source), Source: source, Label: label, Detail: detail, Action: action, Ref: ref})
	}

	for _, ref := range task.SourceRefs {
		label := sourceRefOpenLabel(ref)
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
		if link.Key != "" && link.Key == key {
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
		lowStyle.Render(fmt.Sprintf("%d muted", m.summary.Muted)),
		doneStyle.Render(fmt.Sprintf("✅ %d done", m.summary.Done)),
	}, "  ")

	header := truncateLine(lipgloss.JoinHorizontal(lipgloss.Top, titleStyle.Render("Radar"), "  ", counts), width)
	if m.releaseNotice.Version != "" {
		header += "\n" + truncateLine("Release "+m.releaseNotice.Version+" available — u to review", width)
	}
	return header
}

func (m model) taskList(width int, height int) string {
	lines, selectedStart, selectedEnd := m.taskLines(width)
	scroll := adjustedTaskScroll(lines, selectedStart, selectedEnd, m.scroll, height)
	return scrolledLines(lines, selectedStart, selectedEnd, scroll, height)
}

func (m *model) syncTaskScroll() {
	m.ensureVisibleSelection()
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
	layout := m.overviewLayout()
	lines := make([]string, 0, len(layout.rows))
	for _, row := range layout.rows {
		switch {
		case row.blank:
			lines = append(lines, "")
		case row.section != nil:
			section := row.section
			label := section.title
			if section.collapsible {
				marker := "▸"
				if m.sectionExpanded(section.key) {
					marker = "▾"
				}
				label = fmt.Sprintf("%s %s (%d)", marker, label, row.count)
				if section.key == "done" {
					label += " · last 3 days"
				}
				if row.entry == m.selectedEntry() {
					label = selectedStyle.Render("› " + label)
				} else {
					label = section.style.Render("  " + label)
				}
			} else {
				label = section.style.Render(label)
			}
			lines = append(lines, truncateLine(label, width))
		case row.ref != nil:
			lines = append(lines, taskSourceRefLine(*row.ref, width))
		default:
			task := m.tasks[row.entry.task]
			selected := row.entry == m.selectedEntry()
			lineWidth := max(1, width)
			marker, status := m.taskOperationStatus(task)
			statusWidth := 0
			if status != "" {
				statusWidth = min(lipgloss.Width(status)+2, max(0, (lineWidth-4)/2))
			}
			line := marker + " " + taskLine(task, selected, max(1, lineWidth-4-statusWidth))
			if statusWidth > 0 {
				line += "  " + truncateLine(status, max(1, statusWidth-2))
			}
			if selected {
				line = selectedStyle.Render("› " + line)
			} else {
				line = "  " + line
			}
			lines = append(lines, truncateLine(line, lineWidth))
		}
	}
	bounds := layout.bounds[m.selectedEntry()]
	return lines, bounds.start, bounds.end
}

func truncateLine(line string, width int) string {
	if width <= 0 {
		return line
	}
	return ansi.Truncate(line, width, "…")
}

func taskActivity(task protocol.Task) protocol.Activity {
	if task.Attention == "done" {
		return protocol.ActivityIdle
	}
	return protocol.MergeActivity(protocol.ActivityIdle, task.Activity)
}

func taskLine(task protocol.Task, selected bool, width int) string {
	activity := taskActivity(task)
	activityStyle, activityMarker := progressStyle, "● "
	if activity == protocol.ActivityWaiting {
		activityStyle, activityMarker = attentionStyle, "! "
	}
	titleStyle, metadataStyle := textStyle, subtleStyle
	if selected {
		titleStyle = selectedStyle.Bold(true)
		metadataStyle = metadataStyle.Background(mochaSurface0)
		activityStyle = activityStyle.Background(mochaSurface0)
	}
	title := titleStyle.Render(task.Title)
	if activity != protocol.ActivityIdle {
		title = activityStyle.Render(activityMarker+activity.String()+"  ") + title
	}
	if task.Repo != "" {
		title += metadataStyle.Render("  " + task.Repo)
	}
	if reason := displayTaskReason(task); reason != "" {
		title += metadataStyle.Render("  " + reason)
	}
	badges := taskResourceBadges(task)
	if badges == "" {
		return truncateLine(title, width)
	}
	// Keep resource counts and the unresolved warning visible even for long titles.
	title = truncateLine(title, max(1, width-lipgloss.Width(badges)-2))
	badgeStyle := textStyle
	if selected {
		badgeStyle = selectedStyle
	}
	return title + badgeStyle.Render("  "+badges)
}

const (
	gitWorktreeIcon = "🌿"
	sandboxIcon     = "🐳"
	tmuxIcon        = "📟"
	obsidianIcon    = "📝"
	unresolvedIcon  = "⚠️"
)

func resourceIcon(ref protocol.SourceRef) string {
	switch {
	case ref.Source == "git" && ref.Kind == "worktree":
		return gitWorktreeIcon
	case ref.Source == "sbx" && ref.Kind == "sandbox":
		return sandboxIcon
	case ref.Source == "tmux" && ref.Kind == "session":
		return tmuxIcon
	case ref.Source == "obsidian" && ref.Kind == "task":
		return obsidianIcon
	default:
		return ""
	}
}

func overviewSourceRefs(task protocol.Task) []protocol.SourceRef {
	var refs []protocol.SourceRef
	for _, ref := range task.SourceRefs {
		if ref.Source == "workspace" && ref.Kind == "workspace" {
			continue
		}
		if resourceIcon(ref) == "" {
			refs = append(refs, ref)
		}
	}
	return refs
}

func taskResourceBadges(task protocol.Task) string {
	counts := make(map[string]int)
	for _, ref := range task.SourceRefs {
		icon := resourceIcon(ref)
		if icon == "" {
			continue
		}
		counts[icon]++
	}
	var badges []string
	for _, icon := range []string{gitWorktreeIcon, sandboxIcon, tmuxIcon, obsidianIcon} {
		if count := counts[icon]; count > 0 {
			badges = append(badges, fmt.Sprintf("%s %d", icon, count))
		}
	}
	if len(cleanup.Unresolved(task)) > 0 {
		badges = append(badges, attentionStyle.Render(unresolvedIcon+" unresolved"))
	}
	return strings.Join(badges, " ")
}

func displayTaskReason(task protocol.Task) string {
	return task.Reason
}

func taskSourceRefLine(ref protocol.SourceRef, width int) string {
	const prefix = "    ↳ "
	label := sourceRefLabel(ref)
	status := ""
	if ref.ProvidesWorkspace && ref.Status != "" && ref.Status != "clean" {
		status = ref.Status
	}
	if status == "" {
		return truncateLine(referenceStyle.Render(prefix+label), width)
	}

	separator := "  "
	labelWidth := max(1, width-lipgloss.Width(prefix)-lipgloss.Width(separator)-lipgloss.Width(status))
	label = truncateLine(label, labelWidth)
	line := referenceStyle.Render(prefix+label) + separator + attentionStyle.Render(status)
	return truncateLine(line, width)
}

func sourceRefOpenLabel(ref protocol.SourceRef) string {
	if title := strings.TrimSpace(ref.Title); title != "" {
		return title
	}
	return sourceRefLabel(ref)
}

func sourceRefLabel(ref protocol.SourceRef) string {
	if ref.Presentation.Label != "" {
		return ref.Presentation.Label
	}
	if ref.ID != "" {
		return pathdisplay.HomeRelativeSuffix(ref.ID, ref.Path)
	}
	for _, value := range []string{ref.Title, ref.Repo} {
		if value != "" {
			return value
		}
	}
	if ref.Path != "" {
		return shortenPath(ref.Path)
	}
	if ref.Branch != "" {
		return ref.Branch
	}
	return ref.Source + ":" + ref.Kind
}

func (m model) sourceList(width int) string {
	lines := []string{m.sourceSummary(width)}
	if !m.sourcesExpanded {
		return lines[0]
	}
	nameWidth := 8
	for _, source := range m.sources {
		nameWidth = max(nameWidth, lipgloss.Width(source.Name))
	}
	for _, source := range m.sources {
		statusStyle := sourceStatusStyle(source.Status)
		name := source.Name + strings.Repeat(" ", nameWidth-lipgloss.Width(source.Name))
		line := textStyle.Render("  "+name+" ") +
			statusStyle.Render(fmt.Sprintf("%-8s", source.Status)) +
			subtleStyle.Render(fmt.Sprintf("  %4d refs", source.SourceRefCount))
		if source.Detail != "" {
			line += "  " + subtleStyle.Render(source.Detail)
		}
		lines = append(lines, truncateLine(line, width))
	}
	return strings.Join(lines, "\n")
}

func (m model) sourceSummary(width int) string {
	var failures, warnings []string
	healthy, disabled := 0, 0
	for _, source := range m.sources {
		switch source.Status {
		case "ok":
			healthy++
		case "disabled":
			disabled++
		case "error":
			failures = append(failures, urgentStyle.Render("⚠ "+source.Name+" failed"))
		default:
			status := source.Status
			if status == "" {
				status = "unknown"
			}
			warnings = append(warnings, attentionStyle.Render("⚠ "+source.Name+" "+status))
		}
	}
	// Put failures first so healthy or disabled sources cannot hide them on
	// narrow terminals. The full diagnostics remain available with s.
	parts := append(failures, warnings...)
	if healthy > 0 {
		parts = append(parts, doneStyle.Render(fmt.Sprintf("✓ %d healthy", healthy)))
	}
	if disabled > 0 {
		parts = append(parts, subtleStyle.Render(fmt.Sprintf("%d disabled", disabled)))
	}
	label := "details"
	if m.sourcesExpanded {
		label = "hide"
	}
	hint := "  " + helpKeyStyle.Render("[s]") + helpStyle.Render(" "+label)
	summary := titleStyle.Render("Sources: ") + strings.Join(parts, subtleStyle.Render(" · "))
	return truncateLine(truncateLine(summary, max(1, width-lipgloss.Width(hint)))+hint, width)
}

func sourceStatusStyle(status string) lipgloss.Style {
	switch status {
	case "ok":
		return doneStyle
	case "paused", "partial":
		return attentionStyle
	case "error":
		return urgentStyle
	default:
		return subtleStyle
	}
}
