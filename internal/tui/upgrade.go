package tui

import (
	"context"
	"os"
	"os/exec"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"radar/internal/update"
	"radar/internal/version"
)

type releaseNoticeMsg update.Notice
type maintenanceMsg struct{ err error }

func checkReleaseNotice() tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return releaseNoticeMsg(update.CheckNotice(ctx, version.Number))
}
func runMaintenance(args ...string) tea.Cmd {
	executable, err := os.Executable()
	if err != nil {
		return func() tea.Msg { return maintenanceMsg{err} }
	}
	return tea.ExecProcess(exec.Command(executable, args...), func(err error) tea.Msg { return maintenanceMsg{err} })
}
