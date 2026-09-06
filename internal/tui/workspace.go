package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"radar/internal/app"
	"radar/internal/client"
	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/logging"
	"radar/internal/protocol"
	"radar/internal/taskrefs"
)

type workspaceEditor struct {
	active  bool
	task    protocol.Task
	state   integration.WorkspaceState
	create  integration.ManagedWorkspaceRequest
	desired integration.DesiredWorkspaceDescription
	plan    integration.WorkspaceReconcilePlan
	cursor  int
	scroll  int
}

type workspaceStateMsg struct {
	editor workspaceEditor
	err    error
}
type workspacePlanMsg struct {
	plan integration.WorkspaceReconcilePlan
	err  error
}
type workspaceNoteMsg struct {
	note integration.DesiredWorkspaceNote
	err  error
}
type workspaceAppliedMsg struct {
	result  integration.WorkspaceReconcileResult
	created integration.Workspace
	err     error
}

func (m model) editWorkspace(task protocol.Task) (tea.Model, tea.Cmd) {
	m.editor = workspaceEditor{active: true, task: task}
	m.mode, m.err, m.message = "workspace_loading", nil, "Inspecting workspace..."
	return m, func() tea.Msg {
		manager, err := app.DefaultIntegrations().WorkspaceManager()
		if err != nil {
			return workspaceStateMsg{err: err}
		}
		editor := workspaceEditor{active: true, task: task, desired: integration.DesiredWorkspaceDescription{Worktrees: []integration.DesiredWorkspaceWorktree{}}}
		if anchor, ok := workspaceAnchorRef(task); ok {
			state, err := manager.WorkspaceState(context.Background(), anchor.Path)
			editor.state, editor.desired = state, state.Desired
			return workspaceStateMsg{editor: editor, err: err}
		}
		// Unmanaged worktrees are observed, not adopted implicitly by this editor.
		if len(taskrefs.Worktrees(task)) > 0 {
			return workspaceStateMsg{err: fmt.Errorf("this task has unmanaged worktrees; open them with Enter or create a separate managed workspace with c")}
		}
		editor.create = integration.ManagedWorkspaceRequest{Name: workspaceNameForTask(task), TaskLinkingKey: taskrefs.TaskLinkingKey(task)}
		if path := notePathForTask(task); path != "" {
			editor.desired.Note = &integration.DesiredWorkspaceNote{Path: path, LinkingKey: taskrefs.TaskLinkingKey(task)}
		}
		if ref, ok := taskrefs.WorkspaceCandidate(task); ok {
			if seeder, found := app.DefaultIntegrations().WorkspaceSeeder(ref); found {
				seed, err := seeder.PrepareWorkspaceSeed(context.Background(), ref)
				if err != nil {
					return workspaceStateMsg{err: err}
				}
				if editor.create.Name == "" {
					editor.create.Name = seed.Name
				}
				if seed.Repo != "" {
					editor.desired.Worktrees = append(editor.desired.Worktrees, integration.DesiredWorkspaceWorktree{Repository: seed.Repo, BranchMode: seed.BranchMode, Branch: seed.Branch})
				}
			}
		}
		return workspaceStateMsg{editor: editor}
	}
}

func (m model) newWorkspace() (tea.Model, tea.Cmd) {
	m.editor = workspaceEditor{active: true, desired: integration.DesiredWorkspaceDescription{Worktrees: []integration.DesiredWorkspaceWorktree{}}}
	m.mode, m.err, m.message = "workspace_name", nil, ""
	return m, nil
}

func (m model) updateWorkspace(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.mode == "workspace_loading" || m.mode == "workspace_applying" {
		return m, nil
	}
	switch key.String() {
	case "esc", "ctrl+c":
		if m.mode == "workspace_confirm" {
			m.mode = "workspace_edit"
			return m, nil
		}
		m.mode, m.editor, m.err = "", workspaceEditor{}, nil
		return m, nil
	}
	if m.mode == "workspace_name" {
		switch key.String() {
		case "enter":
			if strings.TrimSpace(m.editor.create.Name) == "" {
				m.err = fmt.Errorf("workspace name is required")
				return m, nil
			}
			m.mode, m.err = "workspace_edit", nil
		case "backspace", "ctrl+h":
			m.editor.create.Name = dropLastRune(m.editor.create.Name)
		default:
			if text, ok := textInputValue(key); ok {
				m.editor.create.Name += text
			}
		}
		return m, nil
	}
	if m.mode == "workspace_confirm" {
		switch key.String() {
		case "j", "down", "ctrl+n":
			m.editor.scroll++
		case "k", "up", "ctrl+p":
			m.editor.scroll--
		case "ctrl+d":
			m.editor.scroll += m.workspacePageRows()
		case "ctrl+u":
			m.editor.scroll -= m.workspacePageRows()
		}
		m.editor.scroll = max(0, min(m.editor.scroll, len(m.workspaceConfirmationLines(m.contentWidth()))-m.workspacePageRows()))
		if key.String() == "y" || key.String() == "enter" {
			return m.applyWorkspace()
		}
		if key.String() == "n" {
			m.mode = "workspace_edit"
		}
		return m, nil
	}
	switch key.String() {
	case "j", "down", "ctrl+n":
		if m.editor.cursor+1 < len(m.editor.desired.Worktrees) {
			m.editor.cursor++
		}
	case "k", "up", "ctrl+p":
		if m.editor.cursor > 0 {
			m.editor.cursor--
		}
	case "a":
		m.create = newCreateForm()
		m.mode, m.err, m.message = "create_repo", nil, ""
		return m, m.loadRepos()
	case "x":
		members := m.editor.desired.Worktrees
		if len(members) == 0 {
			return m, nil
		}
		selected := members[m.editor.cursor]
		for _, actual := range m.editor.state.Members {
			if actual.Repository == selected.Repository && actual.Branch == selected.Branch && actual.Dirty {
				m.err = fmt.Errorf("cannot remove dirty worktree %s", actual.Path)
				return m, nil
			}
		}
		m.editor.desired.Worktrees = append(members[:m.editor.cursor:m.editor.cursor], members[m.editor.cursor+1:]...)
		if m.editor.cursor >= len(m.editor.desired.Worktrees) && m.editor.cursor > 0 {
			m.editor.cursor--
		}
		m.err = nil
	case "n":
		if m.editor.desired.Note != nil {
			return m, nil
		}
		if ref, ok := authoredTaskRef(m.editor.task); ok && ref.WorkspaceAnchorPath != "" {
			m.editor.desired.Note = &integration.DesiredWorkspaceNote{Path: ref.WorkspaceAnchorPath, LinkingKey: ref.ID}
			return m, nil
		}
		title := m.editor.create.Name
		if m.editor.state.Path != "" {
			title = m.editor.state.Name
		}
		if m.editor.task.Title != "" {
			title = m.editor.task.Title
		}
		m.mode, m.message = "workspace_loading", "Preparing note..."
		return m, func() tea.Msg {
			manager, err := app.DefaultIntegrations().WorkspaceManager()
			if err != nil {
				return workspaceNoteMsg{err: err}
			}
			note, err := manager.PrepareWorkspaceNote(context.Background(), title)
			return workspaceNoteMsg{note: note, err: err}
		}
	case "enter":
		return m.previewWorkspace()
	}
	return m, nil
}

func (e workspaceEditor) createRequest() integration.ManagedWorkspaceRequest {
	request := e.create
	request.Worktrees, request.Note = e.desired.Worktrees, e.desired.Note
	return request
}

func (e workspaceEditor) reconcileRequest() (integration.WorkspaceReconcileRequest, error) {
	cfg, err := config.Load()
	if err != nil {
		return integration.WorkspaceReconcileRequest{}, err
	}
	return integration.WorkspaceReconcileRequest{Workspace: e.state.Path, Revision: e.state.Revision, Desired: e.desired, AdditionalSandboxMounts: cfg.SBX.AdditionalMounts}, nil
}

func (m model) previewWorkspace() (tea.Model, tea.Cmd) {
	editor := m.editor
	m.mode, m.err, m.message = "workspace_loading", nil, "Reviewing workspace changes..."
	return m, func() tea.Msg {
		manager, err := app.DefaultIntegrations().WorkspaceManager()
		if err != nil {
			return workspacePlanMsg{err: err}
		}
		var plan integration.WorkspaceReconcilePlan
		if editor.state.Path == "" {
			plan, err = manager.PreviewCreate(context.Background(), editor.createRequest())
		} else {
			request, requestErr := editor.reconcileRequest()
			if requestErr != nil {
				return workspacePlanMsg{err: requestErr}
			}
			plan, err = manager.PreviewReconcile(context.Background(), request)
		}
		return workspacePlanMsg{plan: plan, err: err}
	}
}

func (m model) applyWorkspace() (tea.Model, tea.Cmd) {
	editor := m.editor
	m.mode, m.err, m.message = "workspace_applying", nil, "Applying workspace changes..."
	return m, func() tea.Msg {
		manager, err := app.DefaultIntegrations().WorkspaceManager()
		if err != nil {
			return workspaceAppliedMsg{err: err}
		}
		if editor.state.Path == "" {
			request := editor.createRequest()
			request.ExpectedPlanID = editor.plan.PlanID
			request.Switch = canSwitchMultiplexer()
			created, err := manager.CreateWorkspace(context.Background(), request)
			return workspaceAppliedMsg{created: created, err: err}
		}
		request, err := editor.reconcileRequest()
		if err != nil {
			return workspaceAppliedMsg{err: err}
		}
		request.ExpectedPlanID = editor.plan.PlanID
		logger, file, _, err := logging.New()
		if err != nil {
			return workspaceAppliedMsg{err: err}
		}
		defer file.Close()
		result, err := manager.ApplyReconcile(context.Background(), logger, request)
		return workspaceAppliedMsg{result: result, err: err}
	}
}

func (m model) workspaceView(width int) string {
	if m.mode == "workspace_name" {
		return titleStyle.Render("Create workspace") + "\nName: " + m.editor.create.Name + "\n\nenter continue • esc cancel"
	}
	if m.mode == "workspace_confirm" {
		lines := m.workspaceConfirmationLines(width)
		start := max(0, min(m.editor.scroll, len(lines)-1))
		end := min(len(lines), start+m.workspacePageRows())
		return strings.Join(lines[start:end], "\n") + fmt.Sprintf("\n\nj/k scroll • %d-%d of %d • y/enter apply • n/esc back", start+1, end, len(lines))
	}

	name := m.editor.state.Name
	if name == "" {
		name = m.editor.create.Name
	}
	lines := []string{titleStyle.Render("Workspace: " + name), ""}
	if note := m.editor.desired.Note; note != nil {
		label := "Note: " + shortenPath(note.Path)
		if note.Create {
			label += " [new]"
		}
		lines = append(lines, label)
	} else {
		lines = append(lines, "Note: none • n add note")
	}
	lines = append(lines, "", "Repositories")
	if len(m.editor.desired.Worktrees) == 0 {
		lines = append(lines, "  none")
	}
	start := max(0, m.editor.cursor-max(3, m.workspacePageRows()-6)+1)
	end := min(len(m.editor.desired.Worktrees), start+max(3, m.workspacePageRows()-6))
	if start > 0 {
		lines = append(lines, fmt.Sprintf("  %d more above", start))
	}
	for index := start; index < end; index++ {
		member := m.editor.desired.Worktrees[index]
		branch := member.Branch
		if member.BranchMode == integration.WorkspaceBranchNew {
			branch = member.Name + " [new from " + member.Base + "]"
		}
		label := filepath.Base(member.Repository) + "  " + branch
		for _, actual := range m.editor.state.Members {
			if actual.Repository == member.Repository && actual.Branch == member.Branch && actual.Dirty {
				label += "  [dirty]"
			}
		}
		if index == m.editor.cursor {
			label = selectedStyle.Render("› " + label)
		} else {
			label = "  " + label
		}
		lines = append(lines, label)
	}
	if end < len(m.editor.desired.Worktrees) {
		lines = append(lines, fmt.Sprintf("  %d more below", len(m.editor.desired.Worktrees)-end))
	}
	if sandbox := m.editor.desired.Sandbox; sandbox != nil {
		lines = append(lines, "", fmt.Sprintf("Sandbox: %d requested mounts, %d ports, unchanged", len(sandbox.AdditionalMounts), len(sandbox.Ports)))
	}
	lines = append(lines, "", "a add repository • x remove selected • enter review • esc cancel")
	return strings.Join(lines, "\n")
}

func (m model) refreshWorkspaceResult(message string, problem error) tea.Cmd {
	return func() tea.Msg {
		response, err := client.Call(m.socketPath, "refresh-local")
		if problem == nil && err != nil {
			problem = err
		}
		if problem == nil && !response.OK {
			problem = fmt.Errorf("%s", response.Error)
		}
		return actionMsg{response: &response, message: message, err: problem}
	}
}

func (m model) workspacePageRows() int {
	if m.height == 0 {
		return 20
	}
	return max(4, m.height-m.frameHeight()-8)
}

func (m model) workspaceConfirmationLines(width int) []string {
	lines := []string{titleStyle.Render("Apply workspace changes?"), ""}
	for _, change := range m.editor.plan.Changes {
		lines = append(lines, change.Summary)
	}
	for _, warning := range m.editor.plan.Warnings {
		lines = append(lines, errorStyle.Render(warning))
	}
	return strings.Split(ansi.Wrap(strings.Join(lines, "\n"), width, ""), "\n")
}
