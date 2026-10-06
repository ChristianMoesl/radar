package obsidian

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
)

func TestReconcileBindingsAdoptedNotePreservesOldUnresolvedAndRuntimeLifetimes(t *testing.T) {
	source := NewSourceAt(testVault(t))
	ctx := context.Background()
	task := muteTask("Tracked identities")
	if _, err := source.SetMuted(ctx, task, true); err != nil {
		t.Fatal(err)
	}
	ref := collectedArchiveRef(t, source)
	// Unmute keeps tracking intent, and new identities still persist.
	if _, err := source.SetMuted(ctx, task, false); err != nil {
		t.Fatal(err)
	}
	ref = collectedArchiveRef(t, source)
	old := append([]protocol.SourceBinding(nil), ref.Bindings...)
	newRuntime := task.SourceRefs[2]
	newRuntime.BindingKey = "tmux:$9:2026-08-02T10:00:00Z"
	newWork := task.SourceRefs[0]
	newWork.ID = "jira:issue:ABC-456"
	// Previous work/resources vanished from this collection. Their bindings
	// must remain unresolved; disappearance is not deletion/completion intent.
	task.SourceRefs = []protocol.SourceRef{ref, newRuntime, newWork, task.SourceRefs[3]}
	observation, err := source.ReconcileBindings(ctx, ref, task)
	if err != nil || observation == nil || observation.Ref.Muted || observation.Ref.Status != "open" || len(observation.Ref.Bindings) != 5 {
		t.Fatalf("reconcile = %+v, %v", observation, err)
	}
	if !reflect.DeepEqual(observation.Ref.Bindings[:len(old)], old) {
		t.Fatal("reconciliation pruned/replaced old bindings")
	}
	for _, binding := range observation.Ref.Bindings {
		if binding.ID == "jira:issue:ABC-999" {
			t.Fatal("informational ref adopted")
		}
		if !containsString(observation.Ref.LinkingKeys, binding.LinkingKey()) {
			t.Fatalf("missing binding linking key: %+v", binding)
		}
	}
	if observation.Ref.Metadata["content_hash"] == ref.Metadata["content_hash"] {
		t.Fatal("binding observation has stale content hash")
	}
	path := observation.Ref.Metadata["note_path"]
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := source.ReconcileBindings(ctx, observation.Ref, task)
	if err != nil || again != nil {
		t.Fatalf("no-op reconcile = %+v, %v", again, err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("unchanged reconciliation rewrote note")
	}
}

func TestNoteOnlyMuteAdoptedEmptyBindingsPersistLaterContributors(t *testing.T) {
	source := NewSourceAt(testVault(t))
	ctx := context.Background()
	if _, err := source.Create(ctx, "Note-only task"); err != nil {
		t.Fatal(err)
	}
	ref := collectedArchiveRef(t, source)
	if _, err := source.SetMuted(ctx, protocol.Task{SourceRefs: []protocol.SourceRef{ref}}, true); err != nil {
		t.Fatal(err)
	}
	ref = collectedArchiveRef(t, source)
	current, err := readNote(ref.Metadata["note_path"])
	if err != nil || !strings.Contains(current.content, "radar-source-refs: []\n") {
		t.Fatalf("note-only adoption = %+v, %v", current, err)
	}
	// nil models the JSON omitempty boundary for an adopted zero-ref note.
	ref.Bindings = nil
	incoming := muteTask("muted")
	incoming.SourceRefs = append(incoming.SourceRefs, ref)
	observation, err := source.ReconcileBindings(ctx, ref, incoming)
	if err != nil || observation == nil || len(observation.Ref.Bindings) != 3 || !observation.Ref.Muted {
		t.Fatalf("empty adopted note did not acquire bindings: %+v, %v", observation, err)
	}
}

func TestReconcileBindingsNeverAdoptsOrdinaryNoteOrCreatesNote(t *testing.T) {
	source := NewSourceAt(testVault(t))
	ctx := context.Background()
	if _, err := source.Create(ctx, "Ordinary authored task"); err != nil {
		t.Fatal(err)
	}
	ref := collectedArchiveRef(t, source)
	before, err := os.Stat(ref.Metadata["note_path"])
	if err != nil {
		t.Fatal(err)
	}
	observation, err := source.ReconcileBindings(ctx, ref, muteTask("Source title"))
	if err != nil || observation != nil {
		t.Fatalf("ordinary note was adopted: %+v, %v", observation, err)
	}
	after, err := os.Stat(ref.Metadata["note_path"])
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("ordinary note was rewritten")
	}
	if _, err := source.ReconcileBindings(ctx, muteTask("").SourceRefs[0], muteTask("No authored task")); err == nil {
		t.Fatal("non-authored binding target succeeded")
	}
	if result := source.Collect(ctx, integration.CollectRequest{}); !result.Complete || len(result.Observations) != 1 {
		t.Fatalf("refresh created note: %+v", result)
	}
}

func TestReconcileBindingsProtectsContentHashRaces(t *testing.T) {
	source := NewSourceAt(testVault(t))
	ctx := context.Background()
	task := muteTask("Race-safe")
	if _, err := source.SetMuted(ctx, task, true); err != nil {
		t.Fatal(err)
	}
	ref := collectedArchiveRef(t, source)
	path := ref.Metadata["note_path"]
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := string(data) + "User edited between collection and reconciliation.\n"
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	newRef := task.SourceRefs[0]
	newRef.ID = "jira:issue:ABC-456"
	task.SourceRefs = append(task.SourceRefs, newRef)
	if observation, err := source.ReconcileBindings(ctx, ref, task); err == nil || observation != nil || !strings.Contains(err.Error(), "changed since collection") {
		t.Fatalf("stale bindings accepted: %+v, %v", observation, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != edited {
		t.Fatal("stale collection overwrote user content")
	}
	fresh := collectedArchiveRef(t, source)
	if observation, err := source.ReconcileBindings(ctx, fresh, task); err != nil || observation == nil {
		t.Fatalf("fresh retry failed: %+v, %v", observation, err)
	}
}

func TestReconcileBindingsDoesNotRelocateDoneOrArchivedNotes(t *testing.T) {
	for _, archived := range []bool{false, true} {
		t.Run(map[bool]string{false: "private", true: "archived"}[archived], func(t *testing.T) {
			source := NewSourceAt(testVault(t))
			ctx := context.Background()
			task := muteTask("Done binding task")
			task.Attention, task.DoneAt = "done", "2026-08-01T10:00:00Z"
			if _, err := source.SetMuted(ctx, task, true); err != nil {
				t.Fatal(err)
			}
			ref := collectedArchiveRef(t, source)
			if archived {
				if _, err := source.SetLifecycle(ctx, ref, "done"); err != nil {
					t.Fatal(err)
				}
				ref = collectedArchiveRef(t, source)
			}
			path := ref.Metadata["note_path"]
			old, err := readNote(path)
			if err != nil {
				t.Fatal(err)
			}
			newRef := task.SourceRefs[0]
			newRef.ID = "jira:issue:ABC-456"
			task.SourceRefs = []protocol.SourceRef{ref, newRef}
			observation, err := source.ReconcileBindings(ctx, ref, task)
			if err != nil || observation == nil || observation.Ref.Metadata["note_path"] != path || !observation.Ref.Muted || observation.Ref.Status != "done" {
				t.Fatalf("binding mutation moved/reopened task: %+v, %v", observation, err)
			}
			updated, err := readNote(path)
			if err != nil || updated.CompletedAt != old.CompletedAt || updated.CompletionBaseline != old.CompletionBaseline || updated.Priority != old.Priority {
				t.Fatalf("binding mutation touched lifecycle: %+v, %v", updated, err)
			}
		})
	}
}

func TestMuteAndReconcileFailClosedOnAmbiguousNotes(t *testing.T) {
	for _, conflict := range []string{"multiple authored", "multiple bindings", "duplicate binding owner", "duplicate ID", "malformed note", "missing authored"} {
		t.Run(conflict, func(t *testing.T) {
			source := NewSourceAt(testVault(t))
			ctx := context.Background()
			task := muteTask("First")
			if _, err := source.SetMuted(ctx, task, true); err != nil {
				t.Fatal(err)
			}
			ref := collectedArchiveRef(t, source)
			if _, err := source.Create(ctx, "Second"); err != nil {
				t.Fatal(err)
			}
			result := source.Collect(ctx, integration.CollectRequest{})
			var other protocol.SourceRef
			for _, item := range result.Observations {
				if item.Ref.ID != ref.ID {
					other = item.Ref
				}
			}
			original, err := os.ReadFile(ref.Metadata["note_path"])
			if err != nil {
				t.Fatal(err)
			}
			otherData, err := os.ReadFile(other.Metadata["note_path"])
			if err != nil {
				t.Fatal(err)
			}
			selected := task
			switch conflict {
			case "multiple authored":
				selected.SourceRefs = append(append([]protocol.SourceRef(nil), task.SourceRefs...), ref, other)
			case "multiple bindings":
				otherBinding := task.SourceRefs[0]
				otherBinding.ID = "jira:issue:ABC-456"
				otherTask := protocol.Task{SourceRefs: []protocol.SourceRef{other, otherBinding}}
				if _, err := source.SetMuted(ctx, otherTask, true); err != nil {
					t.Fatal(err)
				}
				selected.SourceRefs = append(append([]protocol.SourceRef(nil), task.SourceRefs...), otherBinding)
			case "duplicate binding owner":
				content := string(otherData[:len(otherData)-4]) + "radar-source-refs:" + encodeBindings([]protocol.SourceBinding{task.SourceRefs[0].Binding()}) + "\n---\n"
				if err := os.WriteFile(other.Metadata["note_path"], []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			case "duplicate ID":
				id := ref.Metadata["radar_id"]
				dir := filepath.Join(taskRoot(source.vaultPath), "Duplicate--"+shortID(id))
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				duplicate := strings.Replace(string(original), `radar-title: "First"`, `radar-title: "Duplicate"`, 1)
				if err := os.WriteFile(filepath.Join(dir, "Duplicate.md"), []byte(duplicate), 0o644); err != nil {
					t.Fatal(err)
				}
			case "malformed note":
				if err := os.WriteFile(other.Metadata["note_path"], []byte("---\ninvalid: [\n---\nPrivate body"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "missing authored":
				other.ID = "obsidian:task:87654321-1234-4234-8234-123456789abc"
				other.Metadata = nil
				selected.SourceRefs = append(append([]protocol.SourceRef(nil), task.SourceRefs...), other)
			}
			if _, err := source.SetMuted(ctx, selected, false); err == nil {
				t.Fatal("ambiguous unmute selected a note")
			}
			if _, err := source.SetMuted(ctx, selected, true); err == nil {
				t.Fatal("ambiguous mute selected a note")
			}
			if observation, err := source.ReconcileBindings(ctx, ref, selected); err == nil || observation != nil {
				t.Fatalf("ambiguous reconciliation selected a note: %+v, %v", observation, err)
			}
			after, err := os.ReadFile(ref.Metadata["note_path"])
			if err != nil || string(after) != string(original) {
				t.Fatal("ambiguous mutation overwrote original note")
			}
		})
	}
}

func TestCollectionRejectsConflictingBindingOwnersAndPreservesPreviousRefs(t *testing.T) {
	source := NewSourceAt(testVault(t))
	ctx := context.Background()
	task := muteTask("First owner")
	if _, err := source.SetMuted(ctx, task, true); err != nil {
		t.Fatal(err)
	}
	ref := collectedArchiveRef(t, source)
	if _, err := source.Create(ctx, "Second owner"); err != nil {
		t.Fatal(err)
	}
	collected := source.Collect(ctx, integration.CollectRequest{})
	var other protocol.SourceRef
	for _, item := range collected.Observations {
		if item.Ref.ID != ref.ID {
			other = item.Ref
		}
	}
	current, err := readNote(other.Metadata["note_path"])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := persistPreference(current, map[string]string{"radar-source-refs": encodeBindings(ref.Bindings)}); err != nil {
		t.Fatal(err)
	}
	result := source.Collect(ctx, integration.CollectRequest{Previous: observationsAsTasks(collected.Observations)})
	if result.Complete || result.SourceStatus.Status != "partial" || !strings.Contains(result.SourceStatus.Detail, "conflicting source binding ownership") || len(result.Observations) != 2 {
		t.Fatalf("conflicting owners collection = %+v", result)
	}
	for _, observation := range result.Observations {
		if observation.Ref.ID == ref.ID && (!observation.Ref.Muted || !reflect.DeepEqual(observation.Ref.Bindings, ref.Bindings)) {
			t.Fatal("conflicting ownership dropped existing preference")
		}
	}
	cold := source.Collect(ctx, integration.CollectRequest{})
	if cold.Complete || len(cold.Observations) != 0 {
		t.Fatalf("cold collection joined conflicting owners: %+v", cold)
	}
}

func TestUnadoptedNoteUnreadableResourceDoesNotBlockOrdinaryCompletion(t *testing.T) {
	source := NewSourceAt(testVault(t))
	ctx := context.Background()
	if _, err := source.Create(ctx, "Ordinary lifecycle"); err != nil {
		t.Fatal(err)
	}
	ref := collectedArchiveRef(t, source)
	resource := protocol.SourceRef{ID: "git:/worktree", Source: "git", Kind: "worktree", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleResource, BindingError: "cannot read worktree creation time"}
	task := protocol.Task{SourceRefs: []protocol.SourceRef{ref, resource}}
	if observation, err := source.ReconcileBindings(ctx, ref, task); err != nil || observation != nil {
		t.Fatalf("unadopted note validated unsupported bindings: %+v, %v", observation, err)
	}
	work := []protocol.SourceRef{{ID: "github:pr:acme/app:7", Signal: "done"}}
	observation, err := source.ReconcileLifecycle(ctx, ref, work)
	if err != nil || observation == nil || observation.Signal != integration.SignalDone {
		t.Fatalf("ordinary completion blocked: %+v, %v", observation, err)
	}
}

func TestUnreadableBindingFailsAdoptionAndReconcileButAuthoredUnmuteStillWorks(t *testing.T) {
	source := NewSourceAt(testVault(t))
	ctx := context.Background()
	task := muteTask("Operational identity failure")
	unsafe := task.SourceRefs[2]
	unsafe.BindingKey = ""
	unsafe.BindingError = "resource identity could not be read"
	unsafeTask := task
	unsafeTask.SourceRefs = append([]protocol.SourceRef(nil), task.SourceRefs...)
	unsafeTask.SourceRefs[2] = unsafe
	if _, err := source.SetMuted(ctx, unsafeTask, true); err == nil || !strings.Contains(err.Error(), unsafe.BindingError) {
		t.Fatalf("unsafe adoption was not rejected with provider reason: %v", err)
	}
	if result := source.Collect(ctx, integration.CollectRequest{}); !result.Complete || len(result.Observations) != 0 {
		t.Fatalf("unsafe adoption created note: %+v", result)
	}
	if _, err := source.SetMuted(ctx, task, true); err != nil {
		t.Fatal(err)
	}
	ref := collectedArchiveRef(t, source)
	original, err := os.ReadFile(ref.Metadata["note_path"])
	if err != nil {
		t.Fatal(err)
	}
	unsafeTask.SourceRefs = append(unsafeTask.SourceRefs, ref)
	if observation, err := source.ReconcileBindings(ctx, ref, unsafeTask); err == nil || observation != nil || !strings.Contains(err.Error(), unsafe.BindingError) {
		t.Fatalf("unsafe binding persisted: %+v, %v", observation, err)
	}
	if _, err := source.SetMuted(ctx, unsafeTask, true); err == nil {
		t.Fatal("repeat mute persisted unsafe identity")
	}
	after, err := os.ReadFile(ref.Metadata["note_path"])
	if err != nil || string(after) != string(original) {
		t.Fatal("unavailable identity contaminated existing note")
	}
	if _, err := source.SetMuted(ctx, unsafeTask, false); err != nil {
		t.Fatalf("operational failure blocked explicit authored unmute: %v", err)
	}
	unmuted := collectedArchiveRef(t, source)
	if unmuted.Muted || !reflect.DeepEqual(unmuted.Bindings, ref.Bindings) {
		t.Fatalf("unmute changed safe bindings: %+v", unmuted)
	}
	// Informational identities never become tracking intent, even if broken.
	informational := unsafe
	informational.Role = protocol.SourceRefRoleInformational
	task.SourceRefs = append(task.SourceRefs, informational)
	if _, err := source.SetMuted(ctx, task, true); err != nil {
		t.Fatalf("informational identity failure blocked mute: %v", err)
	}
}
