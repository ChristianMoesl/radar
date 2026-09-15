package state

import (
	"encoding/json"
	"radar/internal/protocol"
	"testing"
	"time"
)

func TestActivityProjectionUsesOnlyCurrentAuthoritativeFacts(t *testing.T) {
	ref := func(id string, activity protocol.Activity) protocol.SourceRef {
		return protocol.SourceRef{
			ID: "fake:runtime:" + id, EntityID: "fake:runtime:" + id, Source: "fake", Kind: "runtime",
			Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleResource,
			Authority: protocol.SourceRefAuthorityNone, LinkingKeys: []string{"workspace:/work/activity"},
			Title: "Activity", Activity: activity,
		}
	}
	busy, waiting := ref("a", protocol.ActivityBusy), ref("b", protocol.ActivityWaiting)
	info := ref("info", protocol.ActivityWaiting)
	info.Role = protocol.SourceRefRoleInformational
	state := reconcileState(persistedState{Version: stateVersion}, []protocol.Task{
		makeTask("in_progress", "runtime", busy), makeTask("in_progress", "runtime", waiting),
	}, time.Now())
	// Keep unrelated metadata and acknowledgment policy stable across projection.
	state.Records[0].Ack = TaskAckState{Cursor: "seen"}
	check := func(want protocol.Activity) {
		t.Helper()
		tasks := projectTasks(state)
		if len(tasks) != 1 || tasks[0].Activity != want || tasks[0].Attention != "in_progress" || tasks[0].AcknowledgementCursor != "seen" {
			t.Fatalf("projection = %+v, want %q without attention/ack changes", tasks, want)
		}
	}
	check(protocol.ActivityWaiting)
	// Informational refs do not win even if a malformed provider supplied activity.
	if got := sourceRefsActivity([]protocol.SourceRef{busy, info}); got != protocol.ActivityBusy {
		t.Fatalf("informational activity contributed: %q", got)
	}
	for i := range state.SourceRefs {
		if state.SourceRefs[i].ID == waiting.ID {
			state.SourceRefs[i].Active = false
		}
	}
	check(protocol.ActivityBusy)
	for i := range state.SourceRefs {
		state.SourceRefs[i].Snapshot.Activity = protocol.ActivityIdle
	}
	check(protocol.ActivityIdle)
	for i := range state.SourceRefs {
		state.SourceRefs[i].Snapshot.Activity = protocol.ActivityWaiting
	}
	state.Records[0].State = "done"
	state.Records[0].DoneAt = time.Now().UTC().Format(time.RFC3339)
	tasks := projectTasks(state)
	if len(tasks) != 1 || tasks[0].Activity != protocol.ActivityIdle || tasks[0].Attention != "done" {
		t.Fatalf("completed projection = %+v", tasks)
	}
}

func TestMergedObservationsPreserveWaitingRegardlessOfOrder(t *testing.T) {
	for _, pair := range [][2]protocol.Activity{
		{protocol.ActivityBusy, protocol.ActivityWaiting},
		{protocol.ActivityWaiting, protocol.ActivityBusy},
	} {
		got := mergeTasks(protocol.Task{Activity: pair[0]}, protocol.Task{Activity: pair[1]})
		if got.Activity != protocol.ActivityWaiting {
			t.Fatalf("merged activity = %q", got.Activity)
		}
	}
}

func TestActivitySnapshotRoundTripPreservesIdentityAndAcknowledgement(t *testing.T) {
	source := testGitWorktreeRef("git:worktree:/work/fixture", "/work/fixture", "acme/app", "fixture")
	source.Activity = protocol.ActivityWaiting
	original := reconcileState(persistedState{Version: stateVersion}, []protocol.Task{makeTask("in_progress", "runtime", source)}, time.Now())
	original.Records[0].Ack = TaskAckState{Cursor: "seen"}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var restored persistedState
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	tasks := projectTasks(restored)
	if len(tasks) != 1 || tasks[0].ID != original.Records[0].NumericID || tasks[0].Activity != protocol.ActivityWaiting || tasks[0].AcknowledgementCursor != "seen" {
		t.Fatalf("restored projection = %+v", tasks)
	}
	// Missing transient activity needs no reset of the containing records.
	for i := range restored.SourceRefs {
		restored.SourceRefs[i].Snapshot.Activity = protocol.ActivityIdle
	}
	tasks = projectTasks(restored)
	if len(tasks) != 1 || tasks[0].ID != original.Records[0].NumericID || tasks[0].Activity != protocol.ActivityIdle || tasks[0].AcknowledgementCursor != "seen" {
		t.Fatalf("idle projection lost durable state: %+v", tasks)
	}
}
