package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"radar/internal/client"
	"radar/internal/protocol"
)

type taskDeletionState struct {
	task    protocol.Task
	preview protocol.TaskDeletionPreview
	scroll  int
}

type taskDeletionPreviewMsg struct {
	preview protocol.TaskDeletionPreview
	err     error
}

func (m model) previewTaskDeletion(task protocol.Task) tea.Cmd {
	return func() tea.Msg {
		response, err := client.PreviewTaskDeletion(m.socketPath, task.ID)
		if err != nil {
			return taskDeletionPreviewMsg{err: err}
		}
		if !response.OK {
			return taskDeletionPreviewMsg{err: fmt.Errorf("%s", response.Error)}
		}
		if response.TaskDeletionPreview == nil {
			return taskDeletionPreviewMsg{err: fmt.Errorf("task deletion preview response was empty")}
		}
		return taskDeletionPreviewMsg{preview: *response.TaskDeletionPreview}
	}
}

func (m model) deleteTask(preview protocol.TaskDeletionPreview) tea.Cmd {
	return func() tea.Msg {
		response, err := client.DeleteTask(m.socketPath, preview)
		if err != nil {
			return actionMsg{err: err}
		}
		if !response.OK {
			return actionMsg{err: fmt.Errorf("%s", response.Error)}
		}
		if response.TaskDeletionResult == nil {
			return actionMsg{err: fmt.Errorf("task deletion response was empty")}
		}
		return actionMsg{response: &response, message: "Task moved to trash: " + response.TaskDeletionResult.TrashPath}
	}
}

func (m model) updateTaskDeletion(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		if m.deletion.preview.Revision == "" {
			return m, nil
		}
		m.mode = ""
		cmd := m.startOperation(m.deletion.task, "task-delete", "Deleting task…", "Task deletion failed", taskAction(m.deleteTask(m.deletion.preview)))
		return m, cmd
	case "esc", "backspace", "n", "N":
		m.mode, m.err = "", nil
		m.deletion = taskDeletionState{}
	case "q", "ctrl+c":
		return m, tea.Quit
	case "j", "down", "ctrl+n", "k", "up", "ctrl+p", "pgdown", "pgup", "ctrl+d", "ctrl+u", "home", "end":
		lines, _, rows := m.taskDeletionViewport()
		limit := max(0, len(lines)-rows)
		m.deletion.scroll = min(m.deletion.scroll, limit)
		switch msg.String() {
		case "j", "down", "ctrl+n":
			m.deletion.scroll++
		case "k", "up", "ctrl+p":
			m.deletion.scroll--
		case "pgdown", "ctrl+d":
			m.deletion.scroll += rows
		case "pgup", "ctrl+u":
			m.deletion.scroll -= rows
		case "home":
			m.deletion.scroll = 0
		case "end":
			m.deletion.scroll = limit
		}
		m.deletion.scroll = max(0, min(m.deletion.scroll, limit))
	}
	// Enter and all overview shortcuts are deliberately inert in this modal.
	return m, nil
}

func (m model) taskDeletionViewport() (lines []string, footer string, rows int) {
	width := min(96, m.contentWidth())
	preview := m.deletion.preview
	body := strings.Join([]string{
		titleStyle.Render("Delete authored task?"), preview.TaskTitle, "",
		preview.Description, "", "Move: " + preview.Path, "Trash: " + preview.TrashDirectory, "",
		"Remote items and local resources stay unchanged; linked work may remain visible.",
	}, "\n")
	lines = strings.Split(ansi.Wrap(body, max(1, width), ""), "\n")
	footer = ansi.Wrap(errorStyle.Render("[y] Delete task")+"    [Esc/n] Cancel    [q] Quit", max(1, width), "")
	rows = len(lines)
	if m.height > 0 {
		rows = max(1, m.height-m.frameHeight()-lipgloss.Height(footer)-2)
	}
	if len(lines) > rows {
		footer = ansi.Wrap(helpStyle.Render(confirmationScrollHelp(width)), max(1, width), "") + "\n" + footer
		rows = max(1, m.height-m.frameHeight()-lipgloss.Height(footer)-2)
	}
	return lines, footer, rows
}

func (m model) taskDeletionScreen() string {
	lines, footer, rows := m.taskDeletionViewport()
	offset := max(0, min(m.deletion.scroll, len(lines)-min(rows, len(lines))))
	return m.renderFrame(strings.Join(lines[offset:min(len(lines), offset+rows)], "\n")+"\n\n"+footer, min(96, m.contentWidth()))
}
