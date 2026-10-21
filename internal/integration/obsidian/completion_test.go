package obsidian

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/taskdb"
)

func TestCompletionPreservesNoteContentAndIsIdempotent(t *testing.T) {
	source := NewSourceAt(testVault(t))
	if _, err := source.Create(context.Background(), "Ship task"); err != nil {
		t.Fatal(err)
	}
	ref := source.Collect(context.Background(), integration.CollectRequest{}).Observations[0].Ref
	path := ref.Metadata["note_path"]
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data) + "---\ncustom-owner: Example\n---\n\nUser notes.\n"
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	ref = source.Collect(context.Background(), integration.CollectRequest{}).Observations[0].Ref
	items := []protocol.SourceRef{{ID: "github:pr:acme/app:7", Signal: "done"}}
	observation, err := source.ReconcileLifecycle(context.Background(), ref, integration.TaskLifecycleRequest{WorkItems: items})
	if err != nil || observation == nil || observation.Signal != integration.SignalDone {
		t.Fatalf("completion=%+v err=%v", observation, err)
	}
	path = observation.Ref.Metadata["note_path"]
	updated, err := readNote(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(updated.content, "\nUser notes.\n") || !strings.Contains(updated.content, "custom-owner: Example\n") || !validCompletionBaseline.MatchString(updated.CompletionBaseline) {
		t.Fatalf("updated note = %+v", updated)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := source.ReconcileLifecycle(context.Background(), observation.Ref, integration.TaskLifecycleRequest{WorkItems: items})
	if err != nil || again != nil {
		t.Fatalf("repeat completion=%+v err=%v", again, err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(after.ModTime()) || info.Mode() != after.Mode() {
		t.Fatal("idempotent completion rewrote note")
	}
	// Reopening changes database authority while preserving the existing baseline.
	updated.State = "open"
	updated.CompletedAt = ""
	if err := taskdb.Put(&updated.Task); err != nil {
		t.Fatal(err)
	}

	ref = source.Collect(context.Background(), integration.CollectRequest{}).Observations[0].Ref
	if observation, err := source.ReconcileLifecycle(context.Background(), ref, integration.TaskLifecycleRequest{WorkItems: items}); err != nil || observation != nil {
		t.Fatalf("reopened note immediately completed: %+v %v", observation, err)
	}
}

func TestOptionalCompletionBaselineValidation(t *testing.T) {
	content := "---\nradar-id: 12345678-1234-4234-8234-123456789abc\nradar-title: Plan\nradar-state: open\nradar-priority: normal\nradar-created-at: 2026-08-01T10:00:00Z\nradar-completed-at:\n"
	for _, value := range []string{"", "pending", strings.Repeat("a", 64)} {
		if _, err := parseNote(content + "radar-completion-baseline: " + value + "\n---\n"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := parseNote(content + "---\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := parseNote(content + "radar-completion-baseline: invalid\n---\n"); err == nil {
		t.Fatal("invalid baseline accepted")
	}
}

func TestAgentReviewFieldValidationAndPreservingWrites(t *testing.T) {
	source := NewSourceAt(testVault(t))
	if _, err := source.Create(context.Background(), "Agent handoff"); err != nil {
		t.Fatal(err)
	}
	ref := source.Collect(context.Background(), integration.CollectRequest{}).Observations[0].Ref
	current, err := readNote(ref.Metadata["note_path"])
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"true", "false"} {
		if _, err := applyStateUpdates(current, map[string]string{"agent_review": value}); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []string{"\"true\"", "null", "yes", "[true]", "*unknown"} {
		if _, err := applyStateUpdates(current, map[string]string{"agent_review": value}); err == nil {
			t.Fatalf("accepted invalid handoff %s", value)
		}
	}
	current.content = "---\r\ncustom: untouched # comment\r\n---\r\nKeep this body.\r\nNo trailing newline"
	updated, err := applyStateUpdates(current, map[string]string{"agent_review": "true"})
	if err != nil || !updated.AgentReview || updated.content != current.content {
		t.Fatal("state update changed note bytes", err)
	}
	if _, err := applyStateUpdates(updated, map[string]string{"state": "done", "completed_at": "2026-01-01T00:00:00Z"}); err == nil {
		t.Fatal("uncleared review accepted")
	}

}

func TestStaleHandoffWriteReportsPendingIntentWithoutFabricatingObservation(t *testing.T) {
	source := NewSourceAt(testVault(t))
	if _, err := source.Create(context.Background(), "Concurrent edit"); err != nil {
		t.Fatal(err)
	}
	ref := source.Collect(context.Background(), integration.CollectRequest{}).Observations[0].Ref
	path := ref.Metadata["note_path"]
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, []byte("\nEdited during collection\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	observation, err := source.ReconcileLifecycle(context.Background(), ref, integration.TaskLifecycleRequest{WorkItems: []protocol.SourceRef{{ID: "pr", Signal: "done"}}, Activity: protocol.ActivityBusy})
	var pending *integration.AgentReviewPersistenceError
	if observation != nil || !errors.As(err, &pending) {
		t.Fatalf("handoff failure=%+v %v", observation, err)
	}
	current, err := readNote(path)
	if err != nil || current.AgentReview {
		t.Fatal("failed write fabricated persisted hold")
	}
}

func TestManualCompletionSnapshotExcludesResourcesAndRemembersUnresolvedBindings(t *testing.T) {
	ref := protocol.SourceRef{Bindings: []protocol.SourceBinding{
		{Source: "jira", Kind: "issue", ID: "jira:issue:ABC-7", WorkItem: true},
		{Source: "git", Kind: "worktree", ID: "git:worktree:one"},
	}}
	info := protocol.SourceRef{ID: "jira:issue:ABC-8", Role: protocol.SourceRefRoleInformational, WorkflowState: "in review"}
	resource := protocol.SourceRef{ID: "git:worktree:two", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleResource}
	states := completionStates(ref, []protocol.SourceRef{info, resource})
	if len(states) != 1 || states["jira:issue:ABC-7"] != "" {
		t.Fatalf("snapshot = %+v", states)
	}
	items := []protocol.SourceRef{{ID: "jira:issue:ABC-7", Signal: "in_progress", WorkflowState: "in review"}}
	updates := reconcileManualCompletion(states, items)
	if updates["state"] != "" || updates["manual_completion_states"] == "" {
		t.Fatalf("first known state was treated as new active work: %+v", updates)
	}
	current, err := applyStateUpdates(note{Task: taskdb.Task{State: "done", Priority: "normal", CompletedAt: "2026-10-01T10:00:00Z"}}, updates)
	if err != nil || current.ManualCompletionStates[items[0].ID] != "in review" {
		t.Fatal("confirmed state was not retained", err)
	}
	items[0].WorkflowState = "in progress"
	if updates := reconcileManualCompletion(current.ManualCompletionStates, items); updates["state"] != "open" {
		t.Fatalf("later workflow change ignored: %+v", updates)
	}
}

func TestManualCompletionStateUpdatesDoNotMutatePreviousSnapshot(t *testing.T) {
	current := note{Task: taskdb.Task{State: "done", Priority: "normal", CompletedAt: "2026-10-01T10:00:00Z", ManualCompletionStates: map[string]string{"jira:issue:ABC-7": "in review"}}}
	updated, err := applyStateUpdates(current, map[string]string{"manual_completion_states": `{"jira:issue:ABC-7":"resolved"}`})
	if err != nil || current.ManualCompletionStates["jira:issue:ABC-7"] != "in review" || updated.ManualCompletionStates["jira:issue:ABC-7"] != "resolved" {
		t.Fatalf("state update mutated its previous snapshot: %+v %+v %v", current, updated, err)
	}
	if _, err := applyStateUpdates(current, map[string]string{"manual_completion_states": `{"jira:issue:ABC-7":42}`}); err == nil {
		t.Fatal("invalid workflow snapshot accepted")
	}
}
