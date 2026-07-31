package state

import (
	"testing"

	"radar/internal/protocol"
)

func TestUrgentOverridePersistsAndRestoresManualPriority(t *testing.T) {
	store := newManualTestStore(t)
	manual, err := store.CreateManualTask("Write notes")
	if err != nil {
		t.Fatal(err)
	}
	urgent, err := store.SetTaskPriority(manual.ID, "urgent")
	if err != nil {
		t.Fatal(err)
	}
	if urgent.Attention != "immediate" || urgent.Metadata["priority_override"] != "urgent" {
		t.Fatalf("urgent task = %+v", urgent)
	}
	store.SetTasks(nil)
	if got := store.Tasks()[0]; got.Attention != "immediate" {
		t.Fatalf("task after refresh = %+v", got)
	}
	reloaded, err := NewStore(store.logger)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Tasks()[0]; got.Attention != "immediate" {
		t.Fatalf("reloaded task = %+v", got)
	}
	normal, err := reloaded.SetTaskPriority(manual.ID, "normal")
	if err != nil {
		t.Fatal(err)
	}
	if normal.Attention != "low_priority" || normal.Metadata["priority_override"] != "" {
		t.Fatalf("normal task = %+v", normal)
	}
}

func TestClearingUrgentRestoresSourceDerivedPriority(t *testing.T) {
	tests := []struct {
		name      string
		natural   string
		reason    string
		sourceRef protocol.SourceRef
	}{
		{name: "Jira", natural: "in_progress", reason: "In Progress", sourceRef: protocol.SourceRef{ID: "jira:issue:RAD-1", Source: "jira", Kind: "issue", Role: protocol.SourceRefRoleAuthoritative, Signal: "in_progress", Status: "In Progress"}},
		{name: "GitHub feedback", natural: "attention", reason: "review requested", sourceRef: protocol.SourceRef{ID: "github:pr:org/repo:1", Source: "github", Kind: "pull_request", Role: protocol.SourceRefRoleAuthoritative, Signal: "attention", Status: "review requested"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newManualTestStore(t)
			store.SetTasks([]protocol.Task{{Title: tt.name, Attention: tt.natural, Reason: tt.reason, SourceRefs: []protocol.SourceRef{tt.sourceRef}}})
			id := store.Tasks()[0].ID
			urgent, err := store.SetTaskPriority(id, "urgent")
			if err != nil {
				t.Fatal(err)
			}
			if urgent.Attention != "immediate" {
				t.Fatalf("urgent task = %+v", urgent)
			}
			normal, err := store.SetTaskPriority(id, "normal")
			if err != nil {
				t.Fatal(err)
			}
			if normal.Attention != tt.natural || normal.Reason != tt.reason {
				t.Fatalf("normal task = %+v, want %s/%s", normal, tt.natural, tt.reason)
			}
		})
	}
}

func TestUrgentOverrideDoesNotReopenDoneTask(t *testing.T) {
	store := newManualTestStore(t)
	manual, err := store.CreateManualTask("Done work")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteManualTask(manual.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetTaskPriority(manual.ID, "urgent"); err == nil {
		t.Fatal("SetTaskPriority() error = nil for done task")
	}
	if got := store.Tasks()[0]; got.Attention != "done" {
		t.Fatalf("done task = %+v", got)
	}
}
