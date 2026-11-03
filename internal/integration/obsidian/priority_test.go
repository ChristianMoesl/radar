package obsidian

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"

	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/state"
	"radar/internal/taskdb"
)

func TestPriorityPreservesPriorTaskJSONWithoutSchemaOrNoteMigration(t *testing.T) {
	source := NewSourceAt(testVault(t))
	ctx := context.Background()
	if _, err := source.Create(ctx, "Existing task"); err != nil {
		t.Fatal(err)
	}
	ref := collectedArchiveRef(t, source)
	id := ref.Metadata["radar_id"]
	db, err := taskdb.Current()
	if err != nil {
		t.Fatal(err)
	}
	var data []byte
	if err := db.QueryRow("SELECT data FROM authored_tasks WHERE id=?", id).Scan(&data); err != nil {
		db.Close()
		t.Fatal(err)
	}
	var previous map[string]json.RawMessage
	if err := json.Unmarshal(data, &previous); err != nil {
		db.Close()
		t.Fatal(err)
	}
	delete(previous, "LowPriorityBaseline")
	delete(previous, "AgentReviewGeneration")
	data, err = json.Marshal(previous)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE authored_tasks SET data=? WHERE id=?", data, id); err != nil {
		db.Close()
		t.Fatal(err)
	}
	var schema int
	if err := db.QueryRow("PRAGMA user_version").Scan(&schema); err != nil || schema != 1 {
		db.Close()
		t.Fatal("unexpected existing schema", schema, err)
	}
	db.Close()
	before, err := os.ReadFile(ref.Metadata["note_path"])
	if err != nil {
		t.Fatal(err)
	}
	ref = collectedArchiveRef(t, source)
	if ref.Metadata["priority"] != "normal" || ref.LowPriorityBaseline != nil || ref.AgentReview {
		t.Fatal("prior task JSON changed its meaning")
	}
	if _, err := source.SetPriority(ctx, protocol.Task{SourceRefs: []protocol.SourceRef{ref}}, "low"); err != nil {
		t.Fatal(err)
	}
	current, err := taskdb.Find(id)
	if err != nil || current.Priority != "low" || current.ID != id || current.Title != "Existing task" || current.State != "open" {
		t.Fatal("additive preference write changed existing task authority", err)
	}
	after, err := os.ReadFile(current.Path)
	if err != nil || string(before) != string(after) || taskdb.SchemaVersion != schema {
		t.Fatal("preference write migrated schema/note content", err)
	}
}

func TestPriorityCannotOverwriteConcurrentDoneWithAStaleOpenSnapshot(t *testing.T) {
	source := NewSourceAt(testVault(t))
	ctx := context.Background()
	if _, err := source.Create(ctx, "Concurrent completion"); err != nil {
		t.Fatal(err)
	}
	ref := collectedArchiveRef(t, source)
	if _, err := source.SetLifecycle(ctx, ref, "done", nil); err != nil {
		t.Fatal(err)
	}
	// Even if the caller has a stale (but correctly located) open snapshot,
	// reload and check canonical lifecycle while holding the existing note lock.
	ref = collectedArchiveRef(t, source)
	before, err := taskdb.Find(ref.Metadata["radar_id"])
	if err != nil {
		t.Fatal(err)
	}
	for _, priority := range []string{"low", "normal", "urgent"} {
		if _, err := source.SetPriority(ctx, protocol.Task{Attention: "low_priority", SourceRefs: []protocol.SourceRef{ref}}, priority); err == nil {
			t.Fatal("stale priority mutation changed a completed task")
		}
	}
	after, err := taskdb.Find(before.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("rejected priority mutation changed canonical data", err)
	}
}

func TestLowPriorityAdoptionSurvivesRestartAndCacheResetWithoutNoteMetadata(t *testing.T) {
	vault := testVault(t)
	source := NewSourceAt(vault)
	task := muteTask("Defer review")
	task.SourceRefs[0].Signal = "attention"
	task.SourceRefs[0].ActionableSignals = []string{"reply:one"}
	want := protocol.CaptureLowPriorityBaseline(task)
	identity, err := source.SetPriority(context.Background(), task, "low")
	if err != nil {
		t.Fatal(err)
	}
	ref := collectedArchiveRef(t, source)
	if ref.ID != identity.SourceRefID || ref.Metadata["priority"] != "low" || !reflect.DeepEqual(ref.LowPriorityBaseline, want) || len(ref.Bindings) != 3 || ref.Muted || ref.ProvidesWorkspace {
		t.Fatalf("Low adoption lost preference, baseline or binding safety: %+v", ref)
	}
	before, err := os.ReadFile(ref.Metadata["note_path"])
	if err != nil || strings.Contains(string(before), "reply:one") || strings.Contains(string(before), "priority") {
		t.Fatal("Low state was written into note metadata", err)
	}
	store, err := state.NewStore(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Reset(); err != nil {
		t.Fatal(err)
	}
	ref = collectedArchiveRef(t, NewSourceAt(vault))
	if ref.Metadata["priority"] != "low" || !reflect.DeepEqual(ref.LowPriorityBaseline, want) {
		t.Fatal("restart/cache reset erased durable Low intent")
	}
	row, err := taskdb.Find(strings.TrimPrefix(ref.ID, "obsidian:task:"))
	if err != nil || row.Priority != "low" || !reflect.DeepEqual(row.LowPriorityBaseline, want) {
		t.Fatal("canonical SQLite row lost baseline", err)
	}
	root, err := workspacegroup.DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := workspacegroup.Load(root)
	if err != nil || len(registry.Workspaces) != 0 {
		t.Fatal("priority adoption created workspace resources", err)
	}
	after, err := os.ReadFile(ref.Metadata["note_path"])
	if err != nil || string(before) != string(after) {
		t.Fatal("reset/restart changed the note body", err)
	}
}

func TestPriorityRedeferralAndClearingPreserveMuteBindingsAndRawFacts(t *testing.T) {
	source := NewSourceAt(testVault(t))
	ctx := context.Background()
	task := muteTask("Track request")
	task.SourceRefs[0].Signal = "attention"
	task.SourceRefs[0].ActionableSignals = []string{"request:one"}
	if _, err := source.SetMuted(ctx, task, true); err != nil {
		t.Fatal(err)
	}
	for index, priority := range []string{"low", "low", "normal", "urgent"} {
		if priority == "low" {
			task.SourceRefs[0].ActionableSignals = append(task.SourceRefs[0].ActionableSignals, []string{"request:two", "request:three"}[index])
		}
		if _, err := source.SetPriority(ctx, task, priority); err != nil {
			t.Fatal(err)
		}
		ref := collectedArchiveRef(t, source)
		if ref.Metadata["priority"] != priority || !ref.Muted || len(ref.Bindings) != 3 || ref.AgentReview || ref.Status != "open" {
			t.Fatalf("priority changed independent preferences/lifecycle: %+v", ref)
		}
		if priority == "low" && !reflect.DeepEqual(ref.LowPriorityBaseline, protocol.CaptureLowPriorityBaseline(task)) {
			t.Fatal("selecting Low again did not defer outstanding actions")
		}
		if priority != "low" && ref.LowPriorityBaseline != nil {
			t.Fatal("explicit Normal/Urgent did not clear obsolete Low intent")
		}
		if task.SourceRefs[0].Signal != "attention" || task.SourceRefs[0].Acknowledgement != nil {
			t.Fatal("priority mutation acknowledged or rewrote source facts")
		}
	}
}

func TestLowPriorityRejectsMissingBaselineAndInvalidPriorityWithoutWrites(t *testing.T) {
	source := NewSourceAt(testVault(t))
	task := muteTask("Reject invalid intent")
	if _, err := source.SetPriority(context.Background(), task, "snooze"); err == nil {
		t.Fatal("invalid priority accepted")
	}
	if result := source.Collect(context.Background(), integration.CollectRequest{}); len(result.Observations) != 0 {
		t.Fatal("invalid priority adopted a note")
	}
	desired, err := source.PrepareWorkspaceNote(context.Background(), "Validate baseline")
	if err != nil {
		t.Fatal(err)
	}
	current := newTaskNote(desired)
	if _, err := applyStateUpdates(current, map[string]string{"priority": "low"}); err == nil {
		t.Fatal("Low without a captured baseline was accepted")
	}
}

func TestAgentHandoffOccurrenceSurvivesLowAndEditsButAdvancesForNewHold(t *testing.T) {
	source := NewSourceAt(testVault(t))
	ctx := context.Background()
	if _, err := source.Create(ctx, "Review result"); err != nil {
		t.Fatal(err)
	}
	ref := collectedArchiveRef(t, source)
	hold := func(ref protocol.SourceRef) protocol.SourceRef {
		t.Helper()
		observation, err := source.ReconcileLifecycle(ctx, ref, integration.TaskLifecycleRequest{RequireAgentReview: true, Task: protocol.Task{SourceRefs: []protocol.SourceRef{ref}}})
		if err != nil || observation == nil || len(observation.Ref.ActionableSignals) != 1 {
			t.Fatalf("new handoff missing stable occurrence: %+v %v", observation, err)
		}
		return observation.Ref
	}
	ref = hold(ref)
	first := ref.ActionableSignals[0]
	if _, err := source.SetPriority(ctx, protocol.Task{SourceRefs: []protocol.SourceRef{ref}}, "low"); err != nil {
		t.Fatal(err)
	}
	ref = collectedArchiveRef(t, source)
	if _, err := source.SetTitle(ctx, ref, "Renamed presentation"); err != nil {
		t.Fatal(err)
	}
	ref = collectedArchiveRef(t, source)
	if ref.ActionableSignals[0] != first {
		t.Fatal("Low or title edit changed the outstanding handoff identity")
	}
	quiet, _ := protocol.ProjectAttention(protocol.Task{SourceRefs: []protocol.SourceRef{ref}}, nil)
	if quiet.Priority != "low" || quiet.Attention != "low_priority" || !ref.AgentReview {
		t.Fatal("Low acknowledged/cleared review safety or failed to defer a known handoff")
	}
	if _, err := source.SetLifecycle(ctx, ref, "done", nil); err != nil {
		t.Fatal(err)
	}
	ref = hold(collectedArchiveRef(t, source))
	if ref.ActionableSignals[0] == first {
		t.Fatal("a new review hold reused an already-deferred occurrence")
	}
	resurfaced, _ := protocol.ProjectAttention(protocol.Task{SourceRefs: []protocol.SourceRef{ref}}, nil)
	if resurfaced.Priority != "low" || resurfaced.Attention != "attention" {
		t.Fatal("new handoff failed to resurface while retaining Low")
	}
}

func TestQuietLowDoesNotPredeferFutureOrPendingAgentHandoff(t *testing.T) {
	source := NewSourceAt(testVault(t))
	ctx := context.Background()
	if _, err := source.Create(ctx, "Pending result"); err != nil {
		t.Fatal(err)
	}
	ref := collectedArchiveRef(t, source)
	if _, err := source.SetPriority(ctx, protocol.Task{SourceRefs: []protocol.SourceRef{ref}}, "low"); err != nil {
		t.Fatal(err)
	}
	ref = collectedArchiveRef(t, source)
	pending := protocol.Task{AgentReviewPending: true, SourceRefs: []protocol.SourceRef{ref}}
	view, _ := protocol.ProjectAttention(pending, nil)
	if view.Attention != "attention" || view.Priority != "low" || !view.AgentReviewPending {
		t.Fatal("quiet Low pre-deferred a future handoff or concealed its persistence failure")
	}
	if _, err := source.SetPriority(ctx, pending, "low"); err != nil {
		t.Fatal(err)
	}
	ref = collectedArchiveRef(t, source)
	view, _ = protocol.ProjectAttention(protocol.Task{AgentReviewPending: true, SourceRefs: []protocol.SourceRef{ref}}, nil)
	if view.Attention != "low_priority" || !view.AgentReviewPending {
		t.Fatal("explicit deferral did not quiet known pending work or cleared its safety veto")
	}
	observation, err := source.ReconcileLifecycle(ctx, ref, integration.TaskLifecycleRequest{RequireAgentReview: true, Task: protocol.Task{SourceRefs: []protocol.SourceRef{ref}}})
	if err != nil || observation == nil {
		t.Fatal("pending hold could not be persisted", err)
	}
	view, _ = protocol.ProjectAttention(protocol.Task{SourceRefs: []protocol.SourceRef{observation.Ref}}, nil)
	if view.Attention != "low_priority" || !observation.Ref.AgentReview {
		t.Fatal("saving the same deferred handoff fabricated a second occurrence or lost review safety")
	}
}

func TestNormalClearsLowWithoutRebindingAnUnverifiableSupportingResource(t *testing.T) {
	source := NewSourceAt(testVault(t))
	task := muteTask("Clear deferral")
	ctx := context.Background()
	if _, err := source.SetPriority(ctx, task, "low"); err != nil {
		t.Fatal(err)
	}
	ref := collectedArchiveRef(t, source)
	task.SourceRefs = append(task.SourceRefs, ref)
	task.SourceRefs[2].BindingError = "resource lifetime is temporarily unreadable"
	if _, err := source.SetPriority(ctx, task, "normal"); err != nil {
		t.Fatal(err)
	}
	current := collectedArchiveRef(t, source)
	if current.Metadata["priority"] != "normal" || current.LowPriorityBaseline != nil || !reflect.DeepEqual(current.Bindings, ref.Bindings) {
		t.Fatal("clearing Low changed bindings or failed to clear the preference")
	}
}
