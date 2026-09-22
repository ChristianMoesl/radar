package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"radar/internal/protocol"
)

// Operations belong to the initiating task, never the current selection. Only
// one resource operation runs at a time; browsing and Inspect remain available.
type taskOperation struct {
	task    protocol.Task
	kind    string
	label   string
	failure string
}

type taskFailure struct {
	operation taskOperation
	err       error
}

type operationTickMsg uint64

type taskActionMsg struct {
	action actionMsg
}

var operationFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (m model) operationTick() tea.Cmd {
	generation := m.operationGeneration
	return tea.Tick(80*time.Millisecond, func(time.Time) tea.Msg { return operationTickMsg(generation) })
}

func (m *model) startOperation(task protocol.Task, kind, label, failure string, cmd tea.Cmd) tea.Cmd {
	m.operation = taskOperation{task: task, kind: kind, label: label, failure: failure}
	m.operationGeneration++
	m.operationFrame = 0
	m.err, m.message = nil, ""
	return tea.Batch(cmd, m.operationTick())
}

func (m model) operationMarker() string {
	return operationFrames[m.operationFrame%len(operationFrames)]
}

func sameTask(left, right protocol.Task) bool {
	_, ok := matchingTaskCursor([]protocol.Task{left}, right)
	return ok
}

func (m model) operationOnRow() bool {
	_, ok := matchingTaskCursor(m.tasks, m.operation.task)
	return m.operation.kind != "" && ok
}

func (m *model) finishOperation(problem error, clearFailure bool) {
	op := m.operation
	m.operation = taskOperation{}
	if op.kind == "" || (op.task.ID == 0 && len(op.task.SourceRefs) == 0) {
		m.err = problem
		return
	}
	if problem == nil && !clearFailure {
		return
	}
	failures := make([]taskFailure, 0, len(m.taskFailures)+1)
	for _, failure := range m.taskFailures {
		if failure.operation.kind != op.kind || !sameTask(failure.operation.task, op.task) {
			failures = append(failures, failure)
		}
	}
	if problem != nil {
		failures = append(failures, taskFailure{operation: op, err: problem})
	}
	m.taskFailures = failures
	m.err = nil
}

// Tag only resource-operation results. Unrelated refreshes, watch updates, and
// source actions must not finish a spinner or clear a task's failure.
func taskAction(cmd tea.Cmd) tea.Cmd {
	return func() tea.Msg { return taskActionMsg{action: cmd().(actionMsg)} }
}

func (m model) taskOperationStatus(task protocol.Task) (marker, label string) {
	if m.operation.kind != "" && sameTask(m.operation.task, task) {
		return progressStyle.Render(m.operationMarker()), m.operation.label
	}
	for i := len(m.taskFailures) - 1; i >= 0; i-- {
		failure := m.taskFailures[i]
		if sameTask(failure.operation.task, task) {
			return errorStyle.Render("!"), failure.operation.failure + " · i inspect"
		}
	}
	return " ", ""
}

func (m model) operationDetails(task protocol.Task, width int) string {
	var lines []string
	if m.operation.kind != "" && sameTask(m.operation.task, task) {
		lines = append(lines, progressStyle.Render(m.operationMarker()+" "+m.operation.label))
	}
	for _, failure := range m.taskFailures {
		if sameTask(failure.operation.task, task) {
			lines = append(lines, errorStyle.Render(failure.operation.failure), failure.err.Error())
		}
	}
	return ansi.Wrap(strings.Join(lines, "\n"), max(1, width), "")
}

func operationNavigationKey(key string) bool {
	switch key {
	case "q", "ctrl+c", "esc", "backspace", "i", "right", "j", "down", "ctrl+n", "k", "up", "ctrl+p", "ctrl+d", "ctrl+u", "g", "home", "G", "end", "pgdown", "pgup":
		return true
	}
	return false
}
