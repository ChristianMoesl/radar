package obsidian

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/integration/contracttest"
	"radar/internal/linking"
	"radar/internal/protocol"
)

func testVault(t *testing.T) string {
	t.Helper()
	vault := filepath.Join(t.TempDir(), "Work Vault")
	if err := os.MkdirAll(filepath.Join(vault, ".obsidian"), 0o755); err != nil {
		t.Fatal(err)
	}
	return vault
}

func TestCreateCollectAndMutateTaskNote(t *testing.T) {
	vault := testVault(t)
	source := NewSourceAt(vault)
	identity, err := source.Create(context.Background(), "Refine authentication: epic")
	if err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(vault, "Tasks", "*", "task.md"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("task notes = %v, err=%v", matches, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(matches[0]), "artifacts")); err != nil {
		t.Fatal(err)
	}

	result := source.Collect(context.Background(), integration.CollectRequest{})
	if !result.Complete || result.SourceStatus == nil || result.SourceStatus.Status != "ok" || len(result.Observations) != 1 {
		t.Fatalf("collection = %+v", result)
	}
	taskRef := result.Observations[0].Ref
	contracttest.AssertValidSourceRefs(t, "obsidian", []protocol.SourceRef{taskRef})
	taskDir := filepath.Dir(matches[0])
	if taskRef.ID != identity.SourceRefID || taskRef.Authority != protocol.SourceRefAuthorityPrimary || taskRef.Lifecycle != protocol.SourceRefLifecycleWorkItem || taskRef.Signal != "" || !strings.HasPrefix(taskRef.URL, "obsidian://open?") {
		t.Fatalf("task ref = %+v", taskRef)
	}
	if taskRef.Path != taskDir || !taskRef.ProvidesWorkspace || !slices.Contains(taskRef.LinkingKeys, identity.SourceRefID) || !slices.Contains(taskRef.LinkingKeys, linking.WorkspaceKey(taskDir)) {
		t.Fatalf("task workspace capability = %+v", taskRef)
	}
	if result.Observations[0].Signal != integration.SignalLowPriority {
		t.Fatalf("normal task signal = %q, want low_priority", result.Observations[0].Signal)
	}
	content, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	content = append(content, []byte("\ncustom-field: untouched\n")...)
	// Keep the custom field in frontmatter and user content in the body.
	updated := strings.Replace(string(content), "radar-completed-at:\n---", "radar-completed-at:\ncustom-owner: Christian\n---", 1) + "\nUser body stays.\n"
	if err := os.WriteFile(matches[0], []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := source.SetPriority(context.Background(), taskRef, "urgent"); err != nil {
		t.Fatal(err)
	}
	urgent := source.Collect(context.Background(), integration.CollectRequest{})
	if len(urgent.Observations) != 1 || urgent.Observations[0].Signal != integration.SignalImmediate {
		t.Fatalf("urgent collection = %+v", urgent)
	}
	if _, err := source.SetLifecycle(context.Background(), taskRef, "done"); err != nil {
		t.Fatal(err)
	}
	mutated, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	text := string(mutated)
	for _, want := range []string{"radar-state: done", "radar-priority: urgent", "radar-completed-at:", "custom-owner: Christian", "User body stays."} {
		if !strings.Contains(text, want) {
			t.Fatalf("mutated note missing %q:\n%s", want, text)
		}
	}
	collected := source.Collect(context.Background(), integration.CollectRequest{})
	for _, observation := range collected.Observations {
		if observation.Ref.ID == identity.SourceRefID && observation.Signal != integration.SignalDone {
			t.Fatalf("done observation = %+v", observation)
		}
	}
}

func TestMalformedAndDuplicateNotesArePartialAndPreserveKnownRefs(t *testing.T) {
	vault := testVault(t)
	source := NewSourceAt(vault)
	identity, err := source.Create(context.Background(), "Known task")
	if err != nil {
		t.Fatal(err)
	}
	initial := source.Collect(context.Background(), integration.CollectRequest{})
	previous := observationsAsTasks(initial.Observations)
	var notePath string
	for _, task := range previous {
		for _, ref := range task.SourceRefs {
			if ref.ID == identity.SourceRefID {
				notePath = ref.Metadata["note_path"]
			}
		}
	}
	data, err := os.ReadFile(notePath)
	if err != nil {
		t.Fatal(err)
	}
	duplicateDir := filepath.Join(taskRoot(vault), "duplicate")
	if err := os.Mkdir(duplicateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(duplicateDir, "task.md"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	result := source.Collect(context.Background(), integration.CollectRequest{Previous: previous})
	if result.Complete || result.SourceStatus.Status != "partial" || !strings.Contains(result.SourceStatus.Detail, "duplicate radar-id") {
		t.Fatalf("duplicate collection = %+v", result)
	}
	if len(result.Observations) != 1 {
		t.Fatalf("preserved observations = %+v", result.Observations)
	}

	if err := os.RemoveAll(duplicateDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notePath, []byte("---\nradar-id: broken\n---\nsecret body"), 0o644); err != nil {
		t.Fatal(err)
	}
	result = source.Collect(context.Background(), integration.CollectRequest{Previous: previous})
	if result.Complete || len(result.Observations) != 1 || strings.Contains(result.SourceStatus.Detail, "secret body") {
		t.Fatalf("malformed collection = %+v", result)
	}
	if _, err := source.SetPriority(context.Background(), previous[0].SourceRefs[0], "urgent"); err == nil {
		t.Fatal("malformed note mutation succeeded")
	}
}

func TestMoveKeepsIdentityAndDeletionRemovesObservations(t *testing.T) {
	vault := testVault(t)
	source := NewSourceAt(vault)
	identity, err := source.Create(context.Background(), "Move me")
	if err != nil {
		t.Fatal(err)
	}
	first := source.Collect(context.Background(), integration.CollectRequest{})
	if len(first.Observations) != 1 {
		t.Fatalf("initial observations = %+v", first.Observations)
	}
	oldDir := first.Observations[0].Ref.Path
	oldURL := first.Observations[0].Ref.URL
	previous := observationsAsTasks(first.Observations)
	previous[0].SourceRefs = append(previous[0].SourceRefs, protocol.SourceRef{
		ID: "obsidian:workspace:stale", Source: "obsidian", Kind: "task_workspace",
		Metadata: map[string]string{"radar_id": first.Observations[0].Ref.Metadata["radar_id"], "note_path": first.Observations[0].Ref.Metadata["note_path"]},
	})
	refreshed := source.Collect(context.Background(), integration.CollectRequest{Previous: previous})
	if !refreshed.Complete || len(refreshed.Observations) != 1 || refreshed.Observations[0].Ref.ID != identity.SourceRefID {
		t.Fatalf("complete refresh retained stale workspace ref: %+v", refreshed)
	}
	newDir := filepath.Join(taskRoot(vault), "renamed-directory")
	if err := os.Rename(oldDir, newDir); err != nil {
		t.Fatal(err)
	}
	moved := source.Collect(context.Background(), integration.CollectRequest{})
	if len(moved.Observations) != 1 {
		t.Fatalf("moved observations = %+v", moved.Observations)
	}
	movedRef := moved.Observations[0].Ref
	if movedRef.ID != identity.SourceRefID || movedRef.Path != newDir || movedRef.Metadata["task_directory"] != newDir || movedRef.Metadata["note_path"] != filepath.Join(newDir, "task.md") || movedRef.URL == oldURL || !slices.Contains(movedRef.LinkingKeys, linking.WorkspaceKey(newDir)) || slices.Contains(movedRef.LinkingKeys, linking.WorkspaceKey(oldDir)) {
		t.Fatalf("moved ref = %+v", movedRef)
	}
	if err := os.RemoveAll(newDir); err != nil {
		t.Fatal(err)
	}
	deleted := source.Collect(context.Background(), integration.CollectRequest{Previous: observationsAsTasks(moved.Observations)})
	if !deleted.Complete || len(deleted.Observations) != 0 {
		t.Fatalf("deleted collection = %+v", deleted)
	}
}

func observationsAsTasks(observations []integration.Observation) []protocol.Task {
	byID := map[string]*protocol.Task{}
	for _, observation := range observations {
		id := observation.Ref.Metadata["radar_id"]
		task := byID[id]
		if task == nil {
			task = &protocol.Task{Reason: observation.Reason}
			byID[id] = task
		}
		ref := observation.Ref
		ref.Signal = string(observation.Signal)
		task.SourceRefs = append(task.SourceRefs, ref)
	}
	result := make([]protocol.Task, 0, len(byID))
	for _, task := range byID {
		result = append(result, *task)
	}
	return result
}
