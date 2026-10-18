package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"radar/internal/protocol"
)

func TestAgentHandoffShowsAttentionAndCompletedSource(t *testing.T) {
	task, visible := protocol.ProjectAttention(protocol.Task{ID: 7, Title: "Ship feature", SourceRefs: []protocol.SourceRef{
		{ID: "note", Source: "obsidian", Kind: "task", Role: protocol.SourceRefRoleAuthoritative, Authored: true, AgentReview: true, Signal: "low_priority"},
		{ID: "github:pr:acme/app:7", Source: "github", Kind: "pull_request", Role: protocol.SourceRefRoleAuthoritative, Signal: "done", Status: "merged"},
	}}, nil)
	if !visible || task.Attention != "attention" {
		t.Fatalf("projection=%+v", task)
	}
	m := model{tasks: []protocol.Task{task}}
	view := ansi.Strip(m.taskList(160, 20))
	for _, want := range []string{"Need attention", "Ship feature", "Agent finished — review result", "github:pr:acme/app:7", "merged"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q in %s", want, view)
		}
	}
	details := ansi.Strip(taskDetailView(task, 120))
	if strings.Contains(details, "Workspace expiry") {
		t.Fatal("handoff displayed as expiring")
	}
	task.Activity = protocol.ActivityBusy
	task, _ = protocol.ProjectAttention(task, nil)
	if task.Attention != "in_progress" || strings.Contains(task.Reason, "Agent finished") {
		t.Fatalf("resumed run=%+v", task)
	}
}
