package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"radar/internal/protocol"
)

func overviewNoteRefs(count int) []protocol.SourceRef {
	var refs []protocol.SourceRef
	for i := range count {
		refs = append(refs, protocol.SourceRef{
			ID: fmt.Sprintf("obsidian:task:note-%d", i), Source: "obsidian", Kind: "task",
			Title: fmt.Sprintf("Attached note %d", i), Path: fmt.Sprintf("/notes/note-%d.md", i),
			URL: fmt.Sprintf("obsidian://open?vault=Fixture&file=note-%d.md", i),
		})
	}
	return refs
}

func TestOverviewNoteAttachmentsDoNotChangeRows(t *testing.T) {
	for _, workspace := range []bool{false, true} {
		for _, count := range []int{0, 1, 3} {
			for _, width := range []int{40, 60, 100} {
				for _, selected := range []bool{false, true} {
					t.Run(fmt.Sprintf("workspace=%t/notes=%d/width=%d/selected=%t", workspace, count, width, selected), func(t *testing.T) {
						baseline := protocol.Task{Title: "Plain task", Attention: "in_progress"}
						if workspace {
							baseline.SourceRefs = []protocol.SourceRef{{ID: "workspace:fixture", Source: "workspace", Kind: "workspace", Path: "/workspaces/fixture", ProvidesWorkspace: true}}
						}
						task := baseline
						task.SourceRefs = append(append([]protocol.SourceRef(nil), baseline.SourceRefs...), overviewNoteRefs(count)...)
						want, got := ansi.Strip(taskLine(baseline, selected, width)), ansi.Strip(taskLine(task, selected, width))
						if got != want {
							t.Fatalf("note attachment changed row or spacing: got %q, want %q", got, want)
						}
						plain := model{tasks: []protocol.Task{baseline}}
						withNotes := model{tasks: []protocol.Task{task}}
						if got, want := ansi.Strip(withNotes.taskList(width, 20)), ansi.Strip(plain.taskList(width, 20)); got != want {
							t.Fatalf("notes introduced an overview indicator or reference row:\n%s\nwant:\n%s", got, want)
						}
					})
				}
			}
		}
	}
}

func TestOverviewNotesPreservePortsAndActivity(t *testing.T) {
	baseline := resourceBadgeFixture()
	baseline.SourceRefs = append(baseline.SourceRefs[:6:6], baseline.SourceRefs[7:]...)
	baseline.Title = strings.Repeat("Long task 界 ", 20)
	baseline.SourceRefs[len(baseline.SourceRefs)-1].Presentation.PortForwards = []protocol.PortForward{{HostPort: 8080, SandboxPort: 3000}}
	for _, count := range []int{0, 1, 3} {
		for _, width := range []int{40, 60, 100} {
			for _, selected := range []bool{false, true} {
				task := baseline
				task.SourceRefs = append(append([]protocol.SourceRef(nil), baseline.SourceRefs...), overviewNoteRefs(count)...)
				row := ansi.Strip(taskLine(task, selected, width))
				if want := ansi.Strip(taskLine(baseline, selected, width)); row != want {
					t.Fatalf("notes=%d width=%d selected=%t changed other badges: %q, want %q", count, width, selected, row, want)
				}
				for _, want := range []string{"🔌 8080", "● busy"} {
					if !strings.Contains(row, want) {
						t.Fatalf("notes=%d width=%d lost %q: %s", count, width, want, row)
					}
				}
				if lipgloss.Width(row) > width {
					t.Fatalf("row exceeds width %d: %s", width, row)
				}
			}
		}
	}
}

func TestOverviewNotesRetainOpenActionsAndInspect(t *testing.T) {
	task := protocol.Task{Title: "Plain task", Attention: "in_progress", SourceRefs: overviewNoteRefs(3)}
	before := task
	before.SourceRefs = append([]protocol.SourceRef(nil), task.SourceRefs...)
	links := taskLinks(task)
	if len(links) != 3 {
		t.Fatalf("note actions missing before rendering: %+v", links)
	}
	m := model{tasks: []protocol.Task{task}}
	m.taskList(40, 20)
	inspect := ansi.Strip(taskDetailView(m.tasks[0], 120))
	for i, ref := range task.SourceRefs {
		for _, want := range []string{ref.ID, ref.Path, ref.URL} {
			if !strings.Contains(inspect, want) {
				t.Fatalf("Inspect lost note detail %q:\n%s", want, inspect)
			}
		}
		if links[i].Action == "" || links[i].Detail != "Open task note" || links[i].Ref.ID != ref.ID {
			t.Fatalf("note action changed: %+v", links[i])
		}
	}
	if !reflect.DeepEqual(m.tasks[0], before) || !reflect.DeepEqual(taskLinks(m.tasks[0]), links) {
		t.Fatal("overview rendering changed note attachment data or actions")
	}
}
