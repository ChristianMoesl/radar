package obsidian

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/integration/contracttest"
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
	title := "Refine authentication: epic"
	identity, err := source.Create(context.Background(), title)
	if err != nil {
		t.Fatal(err)
	}
	result := source.Collect(context.Background(), integration.CollectRequest{})
	if !result.Complete || result.SourceStatus == nil || result.SourceStatus.Status != "ok" || len(result.Observations) != 1 {
		t.Fatalf("collection = %+v", result)
	}
	taskRef := result.Observations[0].Ref
	contracttest.AssertValidSourceRefs(t, "obsidian", []protocol.SourceRef{taskRef})
	if taskRef.ID != identity.SourceRefID || taskRef.Authority != protocol.SourceRefAuthorityPrimary || taskRef.Lifecycle != protocol.SourceRefLifecycleWorkItem || taskRef.Signal != "" || !strings.HasPrefix(taskRef.URL, "obsidian://open?") {
		t.Fatalf("task ref = %+v", taskRef)
	}
	notePath := taskRef.Metadata["note_path"]
	info, err := os.Stat(notePath)
	if err != nil || !info.Mode().IsRegular() || filepath.Base(filepath.Dir(notePath)) != taskDirectoryName(title, taskRef.Metadata["radar_id"]) {
		t.Fatalf("task note info = %+v, path=%s, err=%v", info, notePath, err)
	}
	if taskRef.Title != title || taskRef.Presentation.WorkspaceName != title || taskRef.Path != "" || taskRef.ProvidesWorkspace || len(taskRef.LinkingKeys) != 1 || taskRef.LinkingKeys[0] != identity.SourceRefID || taskRef.Metadata["task_directory"] != filepath.Dir(notePath) {
		t.Fatalf("task note ref = %+v", taskRef)
	}
	if result.Observations[0].Signal != integration.SignalLowPriority {
		t.Fatalf("normal task signal = %q, want low_priority", result.Observations[0].Signal)
	}
	content, err := os.ReadFile(notePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "radar-title:") || !strings.HasSuffix(string(content), "radar-completed-at:\n---\n") {
		t.Fatalf("task note must contain managed frontmatter and an empty body:\n%s", content)
	}
	updated := strings.Replace(string(content), "radar-completed-at:\n---", "radar-completed-at:\ncustom-owner: Christian\n---", 1) + "\nUser body stays.\n"
	if err := os.WriteFile(notePath, []byte(updated), 0o644); err != nil {
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
	mutated, err := os.ReadFile(notePath)
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
	if len(collected.Observations) != 1 || collected.Observations[0].Signal != integration.SignalDone {
		t.Fatalf("done collection = %+v", collected)
	}
}

func TestCreateRejectsDuplicateAndInvalidFilenames(t *testing.T) {
	vault := testVault(t)
	source := NewSourceAt(vault)
	if _, err := source.Create(context.Background(), "One task"); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Create(context.Background(), "One task"); err == nil {
		t.Fatal("duplicate task title succeeded")
	}
	for _, title := range []string{"nested/task", `nested\task`} {
		if _, err := source.Create(context.Background(), title); err == nil {
			t.Fatalf("invalid title %q succeeded", title)
		}
	}
	result := source.Collect(context.Background(), integration.CollectRequest{})
	if len(result.Observations) != 1 || result.Observations[0].Ref.Title != "One task" {
		t.Fatalf("tasks after rejected creates = %+v", result)
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
	duplicateDirectory := filepath.Join(taskRoot(vault), "Duplicate title--"+shortID(strings.TrimPrefix(identity.SourceRefID, "obsidian:task:")))
	if err := os.Mkdir(duplicateDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	duplicatePath := filepath.Join(duplicateDirectory, "Duplicate title.md")
	if err := os.WriteFile(duplicatePath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	result := source.Collect(context.Background(), integration.CollectRequest{Previous: previous})
	if result.Complete || result.SourceStatus.Status != "partial" || !strings.Contains(result.SourceStatus.Detail, "duplicate radar-id") {
		t.Fatalf("duplicate collection = %+v", result)
	}
	if len(result.Observations) != 1 {
		t.Fatalf("preserved observations = %+v", result.Observations)
	}

	if err := os.RemoveAll(duplicateDirectory); err != nil {
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

func TestRenameKeepsIdentityAndChangesTitle(t *testing.T) {
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
	oldPath := first.Observations[0].Ref.Metadata["note_path"]
	oldURL := first.Observations[0].Ref.URL
	newPath := filepath.Join(filepath.Dir(oldPath), "Renamed task.md")
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatal(err)
	}
	moved := source.Collect(context.Background(), integration.CollectRequest{})
	if len(moved.Observations) != 1 {
		t.Fatalf("moved observations = %+v", moved.Observations)
	}
	movedRef := moved.Observations[0].Ref
	if movedRef.ID != identity.SourceRefID || movedRef.Title != "Renamed task" || movedRef.Metadata["note_path"] != newPath || movedRef.URL == oldURL || movedRef.Path != "" || movedRef.ProvidesWorkspace {
		t.Fatalf("moved ref = %+v", movedRef)
	}
	if err := os.Remove(newPath); err != nil {
		t.Fatal(err)
	}
	deleted := source.Collect(context.Background(), integration.CollectRequest{Previous: observationsAsTasks(moved.Observations)})
	if deleted.Complete || deleted.SourceStatus.Status != "partial" || len(deleted.Observations) != 1 {
		t.Fatalf("deleted collection = %+v", deleted)
	}
}

func TestMalformedTaskDirectoriesArePartial(t *testing.T) {
	vault := testVault(t)
	source := NewSourceAt(vault)
	nested := filepath.Join(taskRoot(vault), "legacy")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "task.md"), []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := source.Collect(context.Background(), integration.CollectRequest{})
	if result.Complete || result.SourceStatus.Status != "partial" || len(result.Observations) != 0 {
		t.Fatalf("malformed directory collection = %+v", result)
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
