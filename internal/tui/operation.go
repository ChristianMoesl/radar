package tui

import (
	"fmt"
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
	cursor, ok := matchingTaskCursor(m.tasks, m.operation.task)
	if m.operation.kind == "" || !ok {
		return false
	}
	_, visible := m.overviewLayout().bounds[visibleEntry{task: cursor}]
	return visible
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
	if operation, ok := m.managedOperation(task); ok {
		marker := "·"
		if operation.Status == "running" {
			marker = "…"
		} else if operation.Status == "failed" || operation.Status == "partial" {
			marker = "!"
		}
		return marker, managedOperationLabel(operation)
	}
	return " ", ""
}

func (m model) operationDetails(task protocol.Task, width int) string {
	var lines []string
	if m.operation.kind != "" && sameTask(m.operation.task, task) {
		lines = append(lines, progressStyle.Render(m.operationMarker()+" Local request: "+m.operation.label))
	}
	for _, failure := range m.taskFailures {
		if sameTask(failure.operation.task, task) {
			lines = append(lines, errorStyle.Render(failure.operation.failure), failure.err.Error())
		}
	}
	if operation, ok := m.managedOperation(task); ok {
		lines = append(lines, managedOperationLabel(operation), "Operation: "+operation.ID)
	}
	return ansi.Wrap(strings.Join(lines, "\n"), max(1, width), "")
}

func (m model) managedOperation(task protocol.Task) (protocol.ManagedOperation, bool) {
	var latest protocol.ManagedOperation
	for i := len(m.managedOperations) - 1; i >= 0; i-- {
		op := m.managedOperations[i]
		matches := op.TaskID != 0 && op.TaskID == task.ID
		if op.WorkspaceID != "" {
			for _, ref := range task.SourceRefs {
				if ref.WorkspaceID == op.WorkspaceID {
					matches = true
				}
			}
		}
		if matches {
			if op.Status == "running" {
				return op, true
			}
			if latest.ID == "" {
				latest = op
			}
		}
	}
	return latest, latest.ID != ""
}

func managedOperationLabel(op protocol.ManagedOperation) string {
	if op.Status == "reported" {
		return "Workspace change reported · not daemon execution"
	}
	label := "Daemon: " + op.Kind + " " + op.Status
	if op.TotalTargets > 0 {
		label += fmt.Sprintf(" (%d/%d targets)", op.CompletedTargets, op.TotalTargets)
	}
	return label
}

func operationNavigationKey(key string) bool {
	switch key {
	case "/", "enter", "q", "ctrl+c", "esc", "backspace", "i", "right", "s", "j", "down", "ctrl+n", "k", "up", "ctrl+p", "ctrl+d", "ctrl+u", "g", "home", "G", "end", "pgdown", "pgup":
		return true
	}
	return false
}
