package taskservice

import (
	"context"
	"strings"
	"testing"

	"radar/internal/protocol"
	"radar/internal/taskdb"
)

func TestManualDoneCapturesLatestKnownWorkflowAtMutation(t *testing.T) {
	remote := &testSource{name: "remote"}
	f := newFixture(t, remote)
	remote.ref = f.linkedRef("remote", "in_progress")
	remote.ref.WorkflowState = "in review"
	f.service.Refresh(context.Background(), false)
	done := f.mutate(t, "task-done", &protocol.TaskMutation{TaskID: f.task.ID})
	ref, _ := authoredRef(done, "obsidian")
	persisted, err := taskdb.Find(strings.TrimPrefix(ref.ID, "obsidian:task:"))
	if err != nil || persisted.ManualCompletionStates[remote.ref.ID] != "in review" {
		t.Fatalf("manual Done did not atomically save its known state: %+v %v", persisted, err)
	}
	calls := remote.calls.Load()
	if calls != 1 { // Only the explicit full refresh, not task creation or Done.
		t.Fatalf("manual completion unexpectedly collected remote state: calls=%d", calls)
	}
	// Change before the first post-Done refresh. Delayed baseline initialization
	// would swallow this transition and incorrectly keep the task done.
	remote.ref.WorkflowState = "in progress"
	f.service.Refresh(context.Background(), false)
	current, _ := taskByID(f.store.Tasks(), done.ID)
	if current.Attention == "done" {
		t.Fatal("first remote workflow transition after manual Done was swallowed")
	}
}

func TestManualReopenClearsDoneWorkflowBaseline(t *testing.T) {
	f := newFixture(t)
	done := f.mutate(t, "task-done", &protocol.TaskMutation{TaskID: f.task.ID})
	ref, _ := authoredRef(done, "obsidian")
	persisted, err := taskdb.Find(strings.TrimPrefix(ref.ID, "obsidian:task:"))
	if err != nil || persisted.ManualCompletionStates == nil {
		t.Fatalf("empty explicit-Done baseline not persisted: %+v %v", persisted, err)
	}
	f.mutate(t, "task-reopen", &protocol.TaskMutation{TaskID: done.ID})
	persisted, err = taskdb.Find(persisted.ID)
	if err != nil || persisted.ManualCompletionStates != nil || persisted.CompletionBaseline != "pending" {
		t.Fatalf("manual reopening retained Done intent: %+v %v", persisted, err)
	}
}

func TestManualDoneIncludesRetainedContributorStates(t *testing.T) {
	remote := &testSource{name: "remote"}
	f := newFixture(t, remote)
	remote.ref = f.linkedRef("remote", "in_progress")
	remote.ref.WorkflowState = "in review"
	f.service.Refresh(context.Background(), false)
	// The item disappears from active discovery, but its last confirmed fact
	// remains available to collection. Manual Done must acknowledge that fact.
	f.store.SetTasksForSources(nil, []string{"remote"})
	visible, _ := taskByID(f.store.Tasks(), f.task.ID)
	for _, ref := range visible.SourceRefs {
		if ref.ID == remote.ref.ID {
			t.Fatal("fixture still exposes its retained contributor")
		}
	}
	done := f.mutate(t, "task-done", &protocol.TaskMutation{TaskID: f.task.ID})
	f.service.Refresh(context.Background(), false)
	current, _ := taskByID(f.store.Tasks(), done.ID)
	if current.Attention != "done" {
		t.Fatal("unchanged retained contributor incorrectly reopened manual Done")
	}
	remote.ref.WorkflowState = "in progress"
	f.service.Refresh(context.Background(), false)
	current, _ = taskByID(f.store.Tasks(), done.ID)
	if current.Attention == "done" {
		t.Fatal("retained contributor's meaningful transition was ignored")
	}
}
