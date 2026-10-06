package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"radar/internal/protocol"
)

func TestIgnoreCommandsUseWholeTaskIDAndPublishOnlyOnSuccess(t *testing.T) {
	for _, ignored := range []bool{false, true} {
		for _, done := range []bool{false, true} {
			for _, authored := range []bool{false, true} {
				t.Run(fmt.Sprintf("ignored=%v/done=%v/authored=%v", ignored, done, authored), func(t *testing.T) {
					task := protocol.Task{ID: 7, Title: "Review work", Attention: "attention", Ignored: ignored, SourceRefs: []protocol.SourceRef{{ID: "github:pr:acme/app:7", Source: "github"}}}
					if authored {
						task.SourceRefs = append(task.SourceRefs, authoredTaskForTUITest("open", "normal", "attention").SourceRefs...)
					}
					if done {
						task.Attention = "done"
					}
					published := task
					published.Ignored = !ignored
					path, requests := deletionSocket(t, protocol.Response{OK: true, Revision: 2, Tasks: []protocol.Task{published}})
					m := model{socketPath: path, tasks: []protocol.Task{task}, expandedSections: map[string]bool{"ignored": true, "done": true}}
					updated, cmd := m.Update(inspectKey("m"))
					m = updated.(model)
					if cmd == nil || !m.loading || !reflect.DeepEqual(m.tasks[0], task) {
						t.Fatal("toggle is unavailable or changed local state before success")
					}
					action := cmd().(actionMsg)
					if action.err != nil || action.response == nil || action.response.Revision != 2 {
						t.Fatalf("toggle command failed: %+v", action)
					}
					method, message := "task-ignore", "Task ignored"
					if ignored {
						method, message = "task-unignore", "Task unignored"
					}
					select {
					case request := <-requests:
						if request.Method != method || request.TaskMutation == nil || request.TaskMutation.TaskID != task.ID {
							t.Fatalf("toggle targeted source rather than whole task: %+v", request)
						}
					case <-time.After(time.Second):
						t.Fatal("no ignore request arrived")
					}
					if action.message != message {
						t.Fatalf("message=%q, want %q", action.message, message)
					}
					updated, _ = m.Update(action)
					m = updated.(model)
					if m.tasks[0].Ignored != !ignored || m.tasks[0].Attention != task.Attention || m.loading {
						t.Fatal("published toggle altered lifecycle or failed to update preference")
					}
					if done {
						if selected, ok := m.selectedTask(); !ok || selected.Attention != "done" {
							t.Fatal("toggling ignored on Done moved/resurrected the selected task")
						}
					} else if ignored {
						if selected, ok := m.selectedTask(); !ok || selected.ID != 7 {
							t.Fatal("unignore did not follow the selected task back to active work")
						}
					} else if m.selectedSection != "ignored" {
						t.Fatal("ignoring last active task did not land on history header")
					}
				})
			}
		}
	}
}

func TestIgnoreCommandErrorsLeavePreferenceAndSelectionUntouched(t *testing.T) {
	for _, ignored := range []bool{false, true} {
		for _, transportFailure := range []bool{false, true} {
			t.Run(fmt.Sprintf("ignored=%v/transport=%v", ignored, transportFailure), func(t *testing.T) {
				task := protocol.Task{ID: 7, Attention: "attention", Ignored: ignored}
				path := "/not/a/radar/socket"
				if !transportFailure {
					path, _ = deletionSocket(t, protocol.Response{OK: false, Error: "note write failed"})
				}
				m := model{socketPath: path, tasks: []protocol.Task{task}, expandedSections: map[string]bool{"ignored": true}}
				updated, cmd := m.Update(inspectKey("m"))
				m = updated.(model)
				action := cmd().(actionMsg)
				if action.err == nil || action.response != nil || (!transportFailure && !strings.Contains(action.err.Error(), "note write failed")) {
					t.Fatalf("command falsely reported success: %+v", action)
				}
				updated, _ = m.Update(action)
				m = updated.(model)
				if !reflect.DeepEqual(m.tasks[0], task) || m.selectedSection != "" || m.cursor != 0 || m.err == nil || m.loading {
					t.Fatal("failed toggle changed preference, selection or swallowed the error")
				}
			})
		}
	}
}
