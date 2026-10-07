package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"radar/internal/update"
	"radar/internal/version"
)

type releaseNoticeMsg update.Notice

func checkReleaseNotice() tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return releaseNoticeMsg(update.CheckNotice(ctx, version.Number))
}
