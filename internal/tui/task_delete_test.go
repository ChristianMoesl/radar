package tui

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"radar/internal/protocol"
)

func deletionFixture() model {
	task := authoredTaskForTUITest("open", "normal", "low_priority")
	preview := protocol.TaskDeletionPreview{TaskID: task.ID, TaskTitle: "Write release notes", SourceRefID: task.SourceRefs[0].ID, Path: "/vault/Tasks/Write release notes--12345678", TrashDirectory: "/vault/.trash", Description: "Move the entire private task directory, including accompanying files, to recoverable vault trash.", Revision: "confirmed"}
	return model{tasks: []protocol.Task{task}, mode: "task_delete_confirm", deletion: taskDeletionState{task: task, preview: preview}}
}

func TestTaskDeletionKeyStartsPreviewAndRequiresAuthoredTask(t *testing.T) {
	for _, tasks := range [][]protocol.Task{nil, {{ID: 7, Attention: "attention"}}, {authoredTaskForTUITest("done", "normal", "done")}} {
		m := model{tasks: tasks, expandedSections: map[string]bool{"done": true}}
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})
		got := updated.(model)
		if len(tasks) > 0 && len(tasks[0].SourceRefs) > 0 {
			if cmd == nil || got.operation.kind != "task-delete-check" || got.mode != "" || got.deletion.task.ID != 7 {
				t.Fatal("D should preview, not delete, including for done tasks")
			}
			updated, _ = got.Update(taskDeletionPreviewMsg{preview: deletionFixture().deletion.preview})
			got = updated.(model)
			if got.mode != "task_delete_confirm" || got.operation.kind != "" {
				t.Fatal("preview did not stop for confirmation")
			}
		} else if cmd != nil {
			t.Fatal("preview started without an authored task")
		}
	}
}

func TestTaskDeletionConfirmationExplainsScopeAndIsModal(t *testing.T) {
	m := deletionFixture()
	view := ansi.Strip(m.View())
	for _, want := range []string{"Delete authored task?", m.deletion.preview.TaskTitle, m.deletion.preview.Path, m.deletion.preview.TrashDirectory, "accompanying files", "Remote items and local resources stay unchanged", "may remain visible", "[y] Delete task", "[Esc/n] Cancel"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q:\n%s", want, view)
		}
	}
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyRunes, Runes: []rune{'D'}}, {Type: tea.KeyRunes, Runes: []rune{'d'}}, {Type: tea.KeyRunes, Runes: []rune{'x'}}, {Type: tea.KeyRunes, Runes: []rune{'c'}}, {Type: tea.KeyRunes, Runes: []rune{'r'}}} {
		updated, cmd := m.Update(key)
		if cmd != nil || !reflect.DeepEqual(updated.(model), m) {
			t.Fatalf("%s escaped the modal or triggered an action", key.String())
		}
	}
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEsc}, {Type: tea.KeyBackspace}, {Type: tea.KeyRunes, Runes: []rune{'n'}}} {
		updated, cmd := m.Update(key)
		got := updated.(model)
		if cmd != nil || got.mode != "" || got.operation.kind != "" || got.deletion.preview.Revision != "" {
			t.Fatalf("%s did not cancel safely", key.String())
		}
	}
	for _, key := range []rune{'y', 'Y'} {
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
		got := updated.(model)
		if cmd == nil || got.mode != "" || got.operation.kind != "task-delete" || got.operation.task.ID != 7 || got.deletion.preview != m.deletion.preview {
			t.Fatal("confirmation did not submit the unchanged preview")
		}
	}
}

func TestTaskDeletionPreviewFailureStaysOnInitiatingTask(t *testing.T) {
	m := deletionFixture()
	m.mode = ""
	m.startOperation(m.tasks[0], "task-delete-check", "Checking task deletion…", "Task deletion check failed", nil)
	updated, cmd := m.Update(taskDeletionPreviewMsg{err: errors.New("clean up its workspace first")})
	got := updated.(model)
	if cmd != nil || got.mode != "" || got.operation.kind != "" || len(got.taskFailures) != 1 || !strings.Contains(got.operationDetails(got.tasks[0], 100), "clean up its workspace first") {
		t.Fatal("preview failure was lost or showed a confirmation")
	}
}

func TestDeletionConfirmationKeepsOriginalTargetDuringWatchUpdates(t *testing.T) {
	m := deletionFixture()
	preview := m.deletion.preview
	m.applyResponse(protocol.Response{Revision: 2, Tasks: []protocol.Task{{ID: 42, Attention: "low_priority"}}}, false)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	got := updated.(model)
	if got.deletion.preview != preview || got.operation.task.ID != preview.TaskID {
		t.Fatal("live update retargeted a pending deletion")
	}
}

func TestDeletionRemovesLastRowAndIgnoresOlderSnapshots(t *testing.T) {
	m := deletionFixture()
	m.mode = ""
	oldTasks := m.tasks
	m.startOperation(m.tasks[0], "task-delete", "Deleting task…", "Task deletion failed", nil)
	updated, _ := m.Update(taskActionMsg{action: actionMsg{response: &protocol.Response{OK: true, Revision: 3, Tasks: []protocol.Task{}, Summary: &protocol.Summary{}}, message: "Task moved to trash"}})
	m = updated.(model)
	if m.operation.kind != "" || len(m.tasks) != 0 || m.cursor != 0 || m.message == "" {
		t.Fatal("deleting last task did not clear the dashboard")
	}
	m.applyResponse(protocol.Response{Revision: 2, Tasks: oldTasks}, false)
	if len(m.tasks) != 0 {
		t.Fatal("an older watch snapshot resurrected the task")
	}
}

func TestDeletionRevisionFenceAllowsDaemonRestart(t *testing.T) {
	m := model{revision: 100, watching: true}
	updated, _ := m.Update(watchMsg{err: errors.New("daemon disconnected")})
	m = updated.(model)
	if m.revision != 0 {
		t.Fatal("reconnect retained the previous daemon's revision")
	}
	m.applyResponse(protocol.Response{Revision: 1, Tasks: []protocol.Task{{ID: 7, Title: "Current task"}}}, false)
	if len(m.tasks) != 1 || m.revision != 1 {
		t.Fatal("new daemon's lower revision was rejected")
	}
}

func TestTaskDeletionScreenScrollsLongPathsWithoutHidingConfirmation(t *testing.T) {
	m := deletionFixture()
	m.width, m.height = 60, 16
	m.deletion.preview.Path = "/vault/" + strings.Repeat("long-path/", 50)
	if lipgloss.Height(m.View()) > m.height || !strings.Contains(ansi.Strip(m.View()), "[y] Delete task") {
		t.Fatalf("confirmation does not fit viewport:\n%s", m.View())
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = updated.(model)
	if m.deletion.scroll == 0 || !strings.Contains(ansi.Strip(m.View()), "may remain visible") || !strings.Contains(ansi.Strip(m.View()), "[y] Delete task") {
		t.Fatalf("cannot inspect full deletion scope:\n%s", m.View())
	}
}

// Exercise the frontend commands and client serialization without a real daemon
// or vault. Use a short socket path for macOS as well as Linux.
func deletionSocket(t *testing.T, response protocol.Response) (string, <-chan protocol.Request) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "radar-delete-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	requests := make(chan protocol.Request, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		var request protocol.Request
		if json.NewDecoder(conn).Decode(&request) == nil {
			requests <- request
			_ = json.NewEncoder(conn).Encode(response)
		}
	}()
	return path, requests
}

func TestTaskDeletionCommandsUseConfirmedPreviewAndResult(t *testing.T) {
	m := deletionFixture()
	preview := m.deletion.preview
	path, requests := deletionSocket(t, protocol.Response{OK: true, TaskDeletionPreview: &preview})
	m.socketPath = path
	msg := m.previewTaskDeletion(m.deletion.task)().(taskDeletionPreviewMsg)
	if msg.err != nil || msg.preview != preview {
		t.Fatalf("preview command failed: %+v", msg)
	}
	if request := <-requests; request.Method != "task-delete-preview" || request.TaskID != preview.TaskID {
		t.Fatalf("wrong preview request: %+v", request)
	}
	result := protocol.TaskDeletionResult{TaskID: preview.TaskID, TrashPath: "/vault/.trash/radar-123/task"}
	path, requests = deletionSocket(t, protocol.Response{OK: true, TaskDeletionResult: &result, Tasks: []protocol.Task{}})
	m.socketPath = path
	action := m.deleteTask(preview)().(actionMsg)
	if action.err != nil || action.response.Tasks == nil || !strings.Contains(action.message, result.TrashPath) {
		t.Fatalf("delete command failed: %+v", action)
	}
	if request := <-requests; request.Method != "task-delete" || request.TaskDeletion == nil || *request.TaskDeletion != preview {
		t.Fatalf("wrong delete request: %+v", request)
	}
}
