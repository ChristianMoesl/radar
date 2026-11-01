package tui

import (
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"radar/internal/app"
	"radar/internal/integration"
	"radar/internal/pi"
)

type feedbackAvailabilityMsg bool

func checkFeedbackAvailability() tea.Msg { return feedbackAvailabilityMsg(pi.FeedbackAvailable()) }

func feedbackAvailabilityTick() tea.Cmd {
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return checkFeedbackAvailability() })
}

func (m model) newFeedbackWorkspace() (tea.Model, tea.Cmd) {
	current, _ := os.Getwd()
	if task, ok := m.selectedTask(); ok {
		if anchor, found := workspaceAnchorRef(task); found {
			current = anchor.Path
		}
	}
	origin := ""
	if manager, err := app.DefaultIntegrations().WorkspaceManager(); err == nil {
		if registered, found, err := manager.RegisteredWorkspace(current); err == nil && found {
			origin = registered.Path
		}
	}
	m.editor = workspaceEditor{
		active:  true,
		create:  integration.ManagedWorkspaceRequest{Name: "Radar: ", Feedback: true, FeedbackOrigin: origin},
		desired: integration.DesiredWorkspaceDescription{Worktrees: []integration.DesiredWorkspaceWorktree{}},
	}
	m.mode, m.err, m.message = "workspace_name", nil, ""
	return m, nil
}
