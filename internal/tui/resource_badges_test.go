package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"radar/internal/protocol"
)

func resourceBadgeFixture() protocol.Task {
	return protocol.Task{
		Title: "Resource badges", Attention: "attention", Busy: true,
		SourceRefs: []protocol.SourceRef{
			{ID: "git:worktree:/repo/one", Source: "git", Kind: "worktree", Path: "/repo/one", Status: "2 dirty, ahead 1", Metadata: map[string]string{"dirty_files": "2", "ahead": "1"}},
			{ID: "git:worktree:/repo/two", Source: "git", Kind: "worktree", Path: "/repo/two", Status: "clean"},
			{ID: "sbx:sandbox:dev", Source: "sbx", Kind: "sandbox"},
			{ID: "tmux:session:dev", Source: "tmux", Kind: "session"},
			{ID: "jira:issue:ABC-123", Source: "jira", Kind: "issue"},
			{ID: "github:pr:owner/repo:42", Source: "github", Kind: "pull_request"},
			{ID: "obsidian:task:note", Source: "obsidian", Kind: "task"},
		},
	}
}

func TestTaskResourceBadges(t *testing.T) {
	for _, cursor := range []int{0, 1} {
		m := model{cursor: cursor, tasks: []protocol.Task{resourceBadgeFixture(), {Title: "Other", Attention: "attention"}}}
		view := ansi.Strip(m.taskList(140, 20))
		for _, want := range []string{"● busy  Resource badges", gitWorktreeIcon + " 2  " + sandboxIcon + " 1  " + tmuxIcon + " 1", dirtyIcon + " dirty", "jira:issue:ABC-123", "github:pr:owner/repo:42", "obsidian:task:note"} {
			if !strings.Contains(view, want) {
				t.Fatalf("cursor %d: missing %q:\n%s", cursor, want, view)
			}
		}
		for _, hidden := range []string{"git:worktree:", "sbx:sandbox:", "tmux:session:", "ahead 1"} {
			if strings.Contains(view, hidden) {
				t.Fatalf("overview contains %q:\n%s", hidden, view)
			}
		}
	}
}

func TestResourceBadgesRetainInspectDetailsAndSourceRefs(t *testing.T) {
	task := resourceBadgeFixture()
	m := model{tasks: []protocol.Task{task}}
	m.taskList(100, 20)
	view := ansi.Strip(m.detailView(100))
	for _, ref := range task.SourceRefs {
		if !strings.Contains(view, ref.ID) {
			t.Fatalf("inspect lost %q:\n%s", ref.ID, view)
		}
	}
	for _, want := range []string{"2 dirty, ahead 1", "/repo/one", "dirty_files"} {
		if !strings.Contains(view, want) {
			t.Fatalf("inspect lost %q:\n%s", want, view)
		}
	}
	if !reflect.DeepEqual(m.tasks[0], resourceBadgeFixture()) {
		t.Fatal("rendering modified the task or its source refs")
	}
}

func TestDirtyBadgeUsesWorktreeMetadata(t *testing.T) {
	for _, tc := range []struct {
		name string
		ref  protocol.SourceRef
		want bool
	}{
		{"managed member", protocol.SourceRef{Source: "git", Kind: "worktree", ProvidesWorkspace: false, Metadata: map[string]string{"dirty_files": "2"}}, true},
		{"standalone", protocol.SourceRef{Source: "git", Kind: "worktree", ProvidesWorkspace: true, Metadata: map[string]string{"dirty_files": "1"}}, true},
		{"clean", protocol.SourceRef{Source: "git", Kind: "worktree", Status: "clean"}, false},
		{"ahead only", protocol.SourceRef{Source: "git", Kind: "worktree", Status: "ahead 2", Metadata: map[string]string{"ahead": "2"}}, false},
		{"zero", protocol.SourceRef{Source: "git", Kind: "worktree", Metadata: map[string]string{"dirty_files": "0"}}, false},
		{"invalid", protocol.SourceRef{Source: "git", Kind: "worktree", Metadata: map[string]string{"dirty_files": "unknown"}}, false},
		{"other source", protocol.SourceRef{Source: "sbx", Kind: "sandbox", Metadata: map[string]string{"dirty_files": "1"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			badges := taskResourceBadges(protocol.Task{SourceRefs: []protocol.SourceRef{tc.ref}})
			if got := strings.Contains(badges, dirtyIcon+" dirty"); got != tc.want {
				t.Fatalf("dirty = %v, want %v: %s", got, tc.want, badges)
			}
		})
	}
}

func TestResourceBadgesOmitZeroCountsAndKeepOtherKinds(t *testing.T) {
	if got := taskResourceBadges(protocol.Task{}); got != "" {
		t.Fatalf("empty task badges = %q", got)
	}
	task := protocol.Task{SourceRefs: []protocol.SourceRef{
		{Source: "git", Kind: "worktree"},
		{Source: "git", Kind: "branch"},
		{Source: "custom", Kind: "worktree"},
		{Source: "workspace", Kind: "workspace"},
	}}
	if got := taskResourceBadges(task); got != gitWorktreeIcon+" 1" {
		t.Fatalf("badges = %q", got)
	}
	if got := overviewSourceRefs(task); !reflect.DeepEqual(got, task.SourceRefs[1:]) {
		t.Fatalf("filtered refs = %+v", got)
	}
}

func TestResourceBadgesStayVisibleOnNarrowRows(t *testing.T) {
	task := resourceBadgeFixture()
	task.Title = strings.Repeat("Long title 界 ", 20)
	for _, width := range []int{60, 80, 100} {
		m := model{tasks: []protocol.Task{task}}
		view := ansi.Strip(m.taskList(width, 20))
		for _, want := range []string{gitWorktreeIcon + " 2", sandboxIcon + " 1", tmuxIcon + " 1", dirtyIcon + " dirty", "● busy"} {
			if !strings.Contains(strings.Split(view, "\n")[1], want) {
				t.Fatalf("width %d: task row missing %q:\n%s", width, want, view)
			}
		}
		for _, line := range strings.Split(view, "\n") {
			if lipgloss.Width(line) > width {
				t.Fatalf("line exceeds width %d: %s", width, line)
			}
		}
	}
}

func TestResourceBadgeRowPositionsMatchRendering(t *testing.T) {
	localOnly := protocol.Task{Title: "Local only", Attention: "in_progress", SourceRefs: resourceBadgeFixture().SourceRefs[:4]}
	m := model{tasks: []protocol.Task{resourceBadgeFixture(), localOnly, {Title: "No refs", Attention: "in_progress"}}}
	positions, count := m.taskRowPositions()
	lines, _, _ := m.taskLines(140)
	if count != len(lines) {
		t.Fatalf("row count = %d, rendered %d", count, len(lines))
	}
	for i, task := range m.tasks {
		if !strings.Contains(ansi.Strip(lines[positions[i]]), task.Title) {
			t.Fatalf("task %q at wrong row %d", task.Title, positions[i])
		}
	}
	if positions[2] != positions[1]+1 {
		t.Fatalf("local-only task occupies extra rows: %v", positions)
	}
}
