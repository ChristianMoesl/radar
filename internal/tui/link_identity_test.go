package tui

import (
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"radar/internal/protocol"
)

func linkIdentityFixture(t *testing.T) model {
	t.Helper()
	selected := protocol.Task{ID: 7, Title: "Selected review", Attention: "attention", URL: "https://example.test/selected", SourceRefs: []protocol.SourceRef{{ID: "github:pr:acme/app:7", Source: "github", Kind: "pull_request", Role: protocol.SourceRefRoleAuthoritative}}}
	neighbor := protocol.Task{ID: 8, Title: "Other work", Attention: "low_priority", URL: "https://example.test/neighbor", SourceRefs: []protocol.SourceRef{{ID: "jira:issue:ABC-123", Source: "jira", Kind: "issue", Role: protocol.SourceRefRoleAuthoritative}}}
	m := model{tasks: []protocol.Task{selected, neighbor}, selectedCurrentTask: true}
	m = pressSectionKey(t, m, "o")
	if m.mode != "open_link" || len(m.links) != 1 || !reflect.DeepEqual(m.linkTask, selected) {
		t.Fatal("opening the picker did not pin the selected task and its links")
	}
	return m
}

func TestLinkPickerAcknowledgesPinnedTaskAfterWatchRegrouping(t *testing.T) {
	for _, change := range []string{"reorder", "mute", "complete", "regroup task ID", "reuse task ID", "only done remain"} {
		for _, activation := range []string{"enter", "shortcut"} {
			t.Run(change+"/"+activation, func(t *testing.T) {
				m := linkIdentityFixture(t)
				original, neighbor := m.tasks[0], m.tasks[1]
				selected := original
				switch change {
				case "mute":
					selected.Muted = true
				case "complete", "only done remain":
					selected.Attention = "done"
				case "regroup task ID":
					selected.ID, selected.Muted = 9, true
				case "reuse task ID":
					selected.ID, selected.Muted = 9, true
					neighbor.ID = original.ID // A cold-cache ID must not retarget the picker.
				}
				tasks := []protocol.Task{neighbor, selected}
				if change == "only done remain" {
					neighbor.Attention = "done"
					tasks = []protocol.Task{neighbor, selected}
				}
				updated, _ := m.Update(watchMsg{response: protocol.Response{OK: true, Revision: 2, Tasks: tasks}})
				m = updated.(model)
				if m.mode != "open_link" || !reflect.DeepEqual(m.linkTask, original) || m.links[0].URL != original.URL {
					t.Fatal("watch replaced picker identity/choices with the overview fallback")
				}
				resolved, ok := m.linkPickerTask()
				if !ok || resolved.ID != selected.ID || resolved.Attention != selected.Attention || resolved.Muted != selected.Muted {
					t.Fatalf("picker resolved %+v, want latest pinned task %+v", resolved, selected)
				}
				if change == "mute" || change == "complete" || change == "regroup task ID" || change == "reuse task ID" {
					if overview, ok := m.selectedTask(); !ok || overview.ID != neighbor.ID {
						t.Fatal("regression fixture did not move overview focus away from the pinned task")
					}
				}
				if change == "only done remain" && m.selectedSection != "done" {
					t.Fatal("completion fixture did not select the collapsed Done header")
				}
				// Exercise the real acknowledgement request but prevent every browser
				// opener from running, on both macOS and Linux.
				t.Setenv("PATH", t.TempDir())
				path, requests := deletionSocket(t, protocol.Response{OK: true})
				m.socketPath = path
				key := "enter"
				if activation == "shortcut" {
					key = m.links[0].Key
				}
				updated, cmd := m.Update(inspectKey(key))
				m = updated.(model)
				if cmd == nil || !m.loading || m.mode != "" || m.links != nil || !reflect.DeepEqual(m.linkTask, protocol.Task{}) {
					t.Fatal("activating the pinned choice did not dispatch and clear picker state")
				}
				action := cmd().(actionMsg)
				if !errors.Is(action.err, exec.ErrNotFound) || action.response == nil {
					t.Fatalf("unexpected acknowledgement/opener result: %+v", action)
				}
				select {
				case request := <-requests:
					if request.Method != fmt.Sprintf("ack:%d", selected.ID) {
						t.Fatalf("acknowledged neighboring task: %+v; want task %d", request, selected.ID)
					}
				case <-time.After(time.Second):
					t.Fatal("pinned task was not acknowledged")
				}
				if m.sectionExpanded("muted") || m.sectionExpanded("done") {
					t.Fatal("opening a pinned history task expanded its overview section")
				}
			})
		}
	}
}

func TestLinkPickerDisappearanceFailsSafelyIncludingReusedNumericTaskID(t *testing.T) {
	for _, remaining := range []string{"neighbor", "empty", "reused task ID"} {
		for _, activation := range []string{"enter", "shortcut", "source action"} {
			t.Run(remaining+"/"+activation, func(t *testing.T) {
				m := linkIdentityFixture(t)
				neighbor := m.tasks[1]
				tasks := []protocol.Task{neighbor}
				if remaining == "empty" {
					tasks = []protocol.Task{}
				} else if remaining == "reused task ID" {
					neighbor.ID = m.linkTask.ID
					tasks = []protocol.Task{neighbor}
				}
				if activation == "source action" {
					m.links[0].Action = "test-action-must-not-run"
				}
				updated, _ := m.Update(watchMsg{response: protocol.Response{OK: true, Revision: 2, Tasks: tasks}})
				m = updated.(model)
				if _, ok := m.linkPickerTask(); ok {
					t.Fatal("disappeared picker task was replaced by a neighbor")
				}
				key := "enter"
				if activation == "shortcut" {
					key = m.links[0].Key
				}
				updated, cmd := m.Update(inspectKey(key))
				m = updated.(model)
				if cmd != nil || m.loading || m.mode != "open_link" || m.err == nil || !strings.Contains(m.err.Error(), "task no longer available") {
					t.Fatalf("vanished task did not fail safely: mode=%q loading=%v err=%v cmd=%v", m.mode, m.loading, m.err, cmd)
				}
				if len(m.links) != 1 || m.linkTask.ID != 7 {
					t.Fatal("failure substituted another task or lost the pinned identity")
				}
				m = pressSectionKey(t, m, "esc")
				if m.mode != "" || m.links != nil || !reflect.DeepEqual(m.linkTask, protocol.Task{}) {
					t.Fatal("unavailable picker could not be cancelled safely")
				}
				assertVisibleSelection(t, m)
			})
		}
	}
}

func TestLinkPickerCancelThenReopenPinsTheNewSelection(t *testing.T) {
	for _, back := range []string{"esc", "backspace"} {
		m := linkIdentityFixture(t)
		m = pressSectionKey(t, m, back)
		if !reflect.DeepEqual(m.linkTask, protocol.Task{}) {
			t.Fatal("cancel retained the old picker identity")
		}
		m = pressSectionKey(t, m, "down")
		m = pressSectionKey(t, m, "o")
		if m.linkTask.ID != 8 || m.links[0].URL != m.tasks[1].URL {
			t.Fatal("reopening inherited the previous task's identity or link")
		}
	}
}

func TestLinkPickerCanResolvePinnedTaskWithOnlySourceIdentity(t *testing.T) {
	m := linkIdentityFixture(t)
	m.linkTask.ID = 0
	m.tasks = []protocol.Task{m.tasks[1], m.tasks[0]}
	m.tasks[1].ID = 9
	if task, ok := m.linkPickerTask(); !ok || task.ID != 9 {
		t.Fatal("picker lost pinned source identity after numeric ID reassignment")
	}
	m.linkTask = protocol.Task{}
	if _, ok := m.linkPickerTask(); ok {
		t.Fatal("missing pin fell back to the overview task")
	}
}

func TestLinkPickerRejectsSharedInformationalRefsAndReusedResourceLifetimes(t *testing.T) {
	for _, change := range []string{"shared informational", "former authoritative now informational", "other provider same ID", "resource name reused", "identity unavailable"} {
		t.Run(change, func(t *testing.T) {
			m := linkIdentityFixture(t)
			original, neighbor := m.tasks[0], m.tasks[1]
			switch change {
			case "shared informational":
				shared := protocol.SourceRef{ID: "jira:issue:ABC-456", Source: "jira", Kind: "issue", Role: protocol.SourceRefRoleInformational}
				m.linkTask.SourceRefs = append(m.linkTask.SourceRefs, shared)
				neighbor.SourceRefs = append(neighbor.SourceRefs, shared)
			case "former authoritative now informational":
				shared := original.SourceRefs[0]
				shared.Role = protocol.SourceRefRoleInformational
				neighbor.SourceRefs = append(neighbor.SourceRefs, shared)
			case "other provider same ID":
				shared := original.SourceRefs[0]
				shared.Source = "other-provider"
				neighbor.SourceRefs = append(neighbor.SourceRefs, shared)
			case "resource name reused":
				resource := protocol.SourceRef{ID: "tmux:session:example", Source: "tmux", Kind: "session", Role: protocol.SourceRefRoleAuthoritative, BindingKey: "provider-owned-old-lifetime"}
				m.linkTask.SourceRefs = []protocol.SourceRef{resource}
				resource.BindingKey = "provider-owned-new-lifetime"
				neighbor.SourceRefs = []protocol.SourceRef{resource}
			case "identity unavailable":
				m.linkTask.SourceRefs = nil
			}
			neighbor.ID = m.linkTask.ID // Numeric identity alone cannot prove safety.
			m.tasks = []protocol.Task{neighbor}
			if task, ok := m.linkPickerTask(); ok {
				t.Fatalf("unsafe identity resolved a neighboring task: %+v", task)
			}
			updated, cmd := m.Update(inspectKey("enter"))
			if cmd != nil || updated.(model).err == nil {
				t.Fatal("unsafe identity dispatched a link or acknowledgement")
			}
		})
	}
}

func TestLinkPickerFollowsSameResourceLifetimeAcrossRename(t *testing.T) {
	m := linkIdentityFixture(t)
	resource := protocol.SourceRef{ID: "tmux:session:old-name", Source: "tmux", Kind: "session", Role: protocol.SourceRefRoleAuthoritative, BindingKey: "provider-owned-lifetime"}
	m.linkTask.SourceRefs = []protocol.SourceRef{resource}
	resource.ID = "tmux:session:new-name"
	latest := protocol.Task{ID: 9, Attention: "in_progress", SourceRefs: []protocol.SourceRef{resource}}
	m.tasks = []protocol.Task{m.tasks[1], latest}
	if task, ok := m.linkPickerTask(); !ok || task.ID != latest.ID {
		t.Fatal("provider-owned lifetime identity was lost when its locator changed")
	}
}
