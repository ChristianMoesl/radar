package tui

import (
	"encoding/json"
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
	"radar/internal/workspacegc"
)

func expiryDetailFixture() protocol.Task {
	return protocol.Task{
		ID:        1,
		Title:     "ABC-123 completed task",
		Attention: "done",
		DoneAt:    "2030-04-02T17:45:12+02:00",
		Metadata:  map[string]string{"context": "task metadata"},
		SourceRefs: []protocol.SourceRef{{
			ID:                "workspace:one",
			Source:            "workspace",
			Kind:              "workspace",
			Role:              protocol.SourceRefRoleAuthoritative,
			Path:              "/workspaces/ABC-123",
			WorkspaceID:       "one",
			ProvidesWorkspace: true,
			WorkspaceEntry:    true,
			CleanupIssues:     []string{"workspace anchor contains unknown files: /workspaces/ABC-123/draft.txt"},
		}},
	}
}

func TestInspectWorkspaceExpiryDeadlineAndPermanentLoss(t *testing.T) {
	task := expiryDetailFixture()
	doneAt, err := time.Parse(time.RFC3339, task.DoneAt)
	if err != nil {
		t.Fatal(err)
	}
	deadline := doneAt.Add(workspacegc.ExpiryRetention)
	for _, test := range []struct {
		name, eligibility string
		now               time.Time
	}{
		{name: "before", eligibility: "Eligible at ", now: deadline.Add(-time.Second)},
		{name: "at deadline", eligibility: "Eligible since ", now: deadline},
		{name: "past", eligibility: "Eligible since ", now: deadline.Add(time.Hour)},
	} {
		t.Run(test.name, func(t *testing.T) {
			view := ansi.Strip(taskDetailViewAt(task, 200, test.now))
			for _, want := range []string{
				"Workspace expiry",
				test.eligibility + deadline.Local().Format("2006-01-02 15:04:05 MST (UTC-07:00)"),
				"Permanent loss:", "local changes", "unpublished commits", "unknown workspace files",
				"No recovery archive.", "next hourly GC run after the deadline",
				"structural safety checks can still block cleanup",
				"Clean workspaces may be removed earlier after 24 hours done",
				"Reopening cancels expiry", "file timestamps do not extend it",
			} {
				if !strings.Contains(view, want) {
					t.Fatalf("missing %q:\n%s", want, view)
				}
			}
			previous := -1
			for _, heading := range []string{"Title", "Metadata", "Workspace expiry", "Unresolved", "Source refs"} {
				position := strings.Index(view, heading)
				if position <= previous {
					t.Fatalf("missing or out-of-order %q:\n%s", heading, view)
				}
				previous = position
			}
		})
	}
}

func TestInspectWorkspaceExpiryOnlyForCompletedRegisteredWorkspaces(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*protocol.Task)
		want   bool
	}{
		{name: "done registered workspace", want: true},
		{name: "clean registered workspace", change: func(task *protocol.Task) { task.SourceRefs[0].CleanupIssues = nil }, want: true},
		{name: "active", change: func(task *protocol.Task) { task.Attention = "in_progress" }},
		{name: "reopened with stale completion time", change: func(task *protocol.Task) { task.Attention = "low_priority" }},
		{name: "missing completion time", change: func(task *protocol.Task) { task.DoneAt = "" }},
		{name: "malformed completion time", change: func(task *protocol.Task) { task.DoneAt = "not-a-timestamp" }},
		{name: "completion date without time", change: func(task *protocol.Task) { task.DoneAt = "2030-04-02" }},
		{name: "zero completion time", change: func(task *protocol.Task) { task.DoneAt = "0001-01-01T00:00:00Z" }},
		{name: "standalone observed workspace", change: func(task *protocol.Task) { task.SourceRefs[0].WorkspaceID = "" }},
		{name: "missing workspace", change: func(task *protocol.Task) { task.SourceRefs = nil }},
		{name: "missing workspace path", change: func(task *protocol.Task) { task.SourceRefs[0].Path = "" }},
		{name: "member without workspace owner", change: func(task *protocol.Task) { task.SourceRefs[0].ProvidesWorkspace = false }},
	} {
		t.Run(test.name, func(t *testing.T) {
			task := expiryDetailFixture()
			if test.change != nil {
				test.change(&task)
			}
			view := ansi.Strip(taskDetailViewAt(task, 120, time.Date(2030, 4, 7, 0, 0, 0, 0, time.UTC)))
			if got := strings.Contains(view, "Workspace expiry"); got != test.want {
				t.Fatalf("expiry section=%v, want %v:\n%s", got, test.want, view)
			}
			if !test.want && (strings.Contains(view, "Eligible at") || strings.Contains(view, "Eligible since") || strings.Contains(view, "Permanent loss:")) {
				t.Fatalf("ineligible task retained expiry details:\n%s", view)
			}
		})
	}
}

func TestInspectWorkspaceExpiryRefreshHandlesReopeningAndMissingWorkspace(t *testing.T) {
	task := expiryDetailFixture()
	m := model{width: 100, height: 30, tasks: []protocol.Task{task}, expandedSections: map[string]bool{"done": true}}
	updated, cmd := m.Update(inspectKey("i"))
	m = updated.(model)
	if cmd != nil || !strings.Contains(m.View(), "Workspace expiry") {
		t.Fatal("Inspect did not render the completed workspace expiry read-only")
	}
	for _, change := range []func(*protocol.Task){
		func(task *protocol.Task) { task.Attention = "low_priority" }, // Reopening cancels even with stale DoneAt.
		func(task *protocol.Task) { task.SourceRefs = nil },
	} {
		refreshed := expiryDetailFixture()
		change(&refreshed)
		m.applyResponse(protocol.Response{Tasks: []protocol.Task{refreshed}}, false)
		if m.mode != "detail" || !m.detail.available || m.detail.task.ID != task.ID {
			t.Fatal("refresh lost the inspected identity")
		}
		if strings.Contains(m.View(), "Workspace expiry") {
			t.Fatalf("refresh retained cancelled expiry:\n%s", m.View())
		}
		m.applyResponse(protocol.Response{Tasks: []protocol.Task{task}}, false)
	}
	m.applyResponse(protocol.Response{Tasks: []protocol.Task{}}, false)
	if view := m.View(); !strings.Contains(view, "Task no longer available") || strings.Contains(view, "Workspace expiry") {
		t.Fatalf("missing task retained an expiry notice:\n%s", view)
	}
}

func TestInspectWorkspaceExpiryWrapsWithoutTruncation(t *testing.T) {
	task := expiryDetailFixture()
	now := time.Date(2030, 4, 7, 0, 0, 0, 0, time.UTC)
	unwrapped := strings.Join(strings.Fields(ansi.Strip(taskDetailViewAt(task, 1000, now))), "")
	for _, width := range []int{24, 40, 80} {
		view := taskDetailViewAt(task, width, now)
		if lipgloss.Width(view) > width {
			t.Fatalf("expiry view width %d exceeds %d:\n%s", lipgloss.Width(view), width, view)
		}
		if got := strings.Join(strings.Fields(ansi.Strip(view)), ""); got != unwrapped {
			t.Fatalf("wrapping at %d truncated expiry details:\n%s", width, view)
		}
	}

	m := model{tasks: []protocol.Task{task}, expandedSections: map[string]bool{"done": true}}
	updated, _ := m.Update(inspectKey("i"))
	m = updated.(model)
	for _, size := range []struct{ width, height int }{{32, 20}, {60, 24}, {100, 30}} {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
		m = updated.(model)
		for _, key := range []string{"end", "pgup", "home"} {
			updated, cmd := m.Update(inspectKey(key))
			m = updated.(model)
			if view := m.View(); cmd != nil || lipgloss.Width(view) > size.width || lipgloss.Height(view) != size.height {
				t.Fatalf("expiry details broke %dx%d viewport:\n%s", size.width, size.height, view)
			}
		}
	}
}

func TestInspectWorkspaceExpiryRenderingIsReadOnly(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "command-called")
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"git", "gh", "tmux", "sbx", "radar"} {
		if err := os.WriteFile(filepath.Join(bin, command), []byte("#!/bin/sh\necho called >> '"+marker+"'\nexit 1\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	task := expiryDetailFixture()
	task.SourceRefs[0].Path = root
	file := filepath.Join(root, "draft.txt")
	contents := []byte("local work to preserve\n")
	if err := os.WriteFile(file, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 4, 20, 0, 0, 0, 0, time.UTC)
	view := taskDetailViewAt(task, 80, now)
	for i := 0; i < 3; i++ {
		if got := taskDetailViewAt(task, 80, now); got != view {
			t.Fatal("repeated rendering changed expiry details")
		}
	}
	after, err := json.Marshal(task)
	if err != nil || string(after) != string(before) {
		t.Fatalf("rendering mutated the task: before=%s after=%s err=%v", before, after, err)
	}
	m := model{width: 100, height: 30, mode: "detail", tasks: []protocol.Task{task}, detail: detailState{task: task, available: true}}
	for _, key := range []string{"enter", "d", "D", "x", "X", "r", "w"} {
		updated, cmd := m.Update(inspectKey(key))
		if cmd != nil || !reflect.DeepEqual(updated.(model), m) {
			t.Fatalf("%s triggered a mutation from the expiry view", key)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("rendering invoked a provider command: %v", err)
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != string(contents) {
		t.Fatalf("rendering changed workspace contents: got=%q err=%v", got, err)
	}
}
