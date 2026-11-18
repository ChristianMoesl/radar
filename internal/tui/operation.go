package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"radar/internal/protocol"
)

// Operations belong to the initiating request and task, never the selection.
// Cleanup has its own slot. Admission remains serialized until backend resource
// identities make overlapping native activation safe.
type taskOperation struct {
	id      uint64
	frame   int
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

type operationResultMsg struct {
	id     uint64
	result tea.Msg
}

var operationFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (m model) operationTick(id uint64) tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(time.Time) tea.Msg { return operationTickMsg(id) })
}

func (m *model) startOperation(task protocol.Task, kind, label, failure string, cmd tea.Cmd) tea.Cmd {
	slot := &m.operation
	if kind == "cleanup" || kind == "cleanup-check" {
		slot = &m.cleanupOperation
	}
	if slot.kind != "" {
		return nil
	}
	m.operationGeneration++
	id := m.operationGeneration
	*slot = taskOperation{id: id, task: task, kind: kind, label: label, failure: failure}
	m.err, m.message = nil, ""
	var result tea.Cmd
	if cmd != nil {
		result = func() tea.Msg { return operationResultMsg{id: id, result: cmd()} }
	}
	return tea.Batch(result, m.operationTick(id))
}

func (m model) operationMarker(op taskOperation) string {
	return operationFrames[op.frame%len(operationFrames)]
}

func (m model) localOperations() [2]taskOperation {
	return [2]taskOperation{m.operation, m.cleanupOperation}
}

func (m model) hasActiveOperation() bool {
	return m.operation.kind != "" || m.cleanupOperation.kind != ""
}

func (m *model) operationByID(id uint64) *taskOperation {
	if id == 0 {
		return nil
	}
	for _, op := range []*taskOperation{&m.operation, &m.cleanupOperation} {
		if op.kind != "" && op.id == id {
			return op
		}
	}
	return nil
}

// Only the expected typed result can finish a live request. Untagged, stale,
// duplicate and wrong-phase results must not publish snapshots or hand off the
// terminal either, even if they happen to name the same task.
func operationResultMatches(kind string, result tea.Msg) bool {
	switch result.(type) {
	case taskActionMsg:
		return kind == "session" || kind == "cleanup" || kind == "task-delete"
	case cleanupPreviewMsg:
		return kind == "cleanup-check"
	case workspaceStateMsg:
		return kind == "workspace-inspect"
	case workspacePlanMsg:
		return kind == "workspace-prepare"
	case workspaceAppliedMsg:
		return kind == "workspace"
	case taskDeletionPreviewMsg:
		return kind == "task-delete-check"
	}
	return false
}

func isOperationResult(msg tea.Msg) bool {
	switch msg.(type) {
	case taskActionMsg, cleanupPreviewMsg, workspaceStateMsg, workspacePlanMsg, workspaceAppliedMsg, taskDeletionPreviewMsg:
		return true
	}
	return false
}

func sameTask(left, right protocol.Task) bool {
	_, ok := matchingTaskCursor([]protocol.Task{left}, right)
	return ok
}

func (m model) operationOnRow(op taskOperation) bool {
	cursor, ok := matchingTaskCursor(m.tasks, op.task)
	if op.kind == "" || !ok {
		return false
	}
	_, visible := m.overviewLayout().bounds[visibleEntry{task: cursor}]
	return visible
}

func (m *model) finishOperation(id uint64, problem error, clearFailure bool) {
	slot := m.operationByID(id)
	if slot == nil {
		return
	}
	op := *slot
	*slot = taskOperation{}
	if op.task.ID == 0 && len(op.task.SourceRefs) == 0 {
		if problem != nil {
			m.err = problem
		}
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
}

// Tag only resource-operation results. Unrelated refreshes, watch updates, and
// source actions must not finish a spinner or clear a task's failure.
func taskAction(cmd tea.Cmd) tea.Cmd {
	return func() tea.Msg { return taskActionMsg{action: cmd().(actionMsg)} }
}

func (m model) taskOperationStatus(task protocol.Task) (marker, label string) {
	var labels []string
	for _, op := range m.localOperations() {
		if op.kind != "" && sameTask(op.task, task) {
			marker = progressStyle.Render(m.operationMarker(op))
			labels = append(labels, op.label)
		}
	}
	if len(labels) > 0 {
		return marker, strings.Join(labels, " · ")
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
	for _, op := range m.localOperations() {
		if op.kind != "" && sameTask(op.task, task) {
			lines = append(lines, progressStyle.Render(m.operationMarker(op)+" Local request: "+op.label))
		}
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
