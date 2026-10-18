package obsidian

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
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
	content := strings.Replace(string(data), "radar-completed-at:\n---", "radar-completed-at:\ncustom-owner: Example\n---", 1) + "\nUser notes.\n"
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
	// Directly reopening an automatically completed note also keeps the baseline.
	content = strings.Replace(updated.content, "radar-state: done", "radar-state: open", 1)
	content = strings.Replace(content, "radar-completed-at: "+updated.CompletedAt, "radar-completed-at:", 1)
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
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
		if _, _, err := updateNoteContent(current, map[string]string{"radar-agent-review": value}); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []string{"\"true\"", "null", "yes", "[true]", "*unknown"} {
		if _, _, err := updateNoteContent(current, map[string]string{"radar-agent-review": value}); err == nil {
			t.Fatalf("accepted invalid handoff %s", value)
		}
	}
	content := strings.ReplaceAll(current.content, "\n", "\r\n") + "\r\nKeep this body.\r\nNo trailing newline"
	content = strings.Replace(content, "radar-completed-at:\r\n", "radar-completed-at:\r\ncustom: untouched # comment\r\n", 1)
	current, err = parseNote(content)
	if err != nil {
		t.Fatal(err)
	}
	held, updated, err := updateNoteContent(current, map[string]string{"radar-agent-review": "true"})
	if err != nil || !updated.AgentReview || !strings.HasSuffix(held, "\r\nKeep this body.\r\nNo trailing newline") || !strings.Contains(held, "custom: untouched # comment\r\n") {
		t.Fatalf("destructive field write: %s %v", held, err)
	}
	same, _, err := updateNoteContent(updated, map[string]string{"radar-agent-review": "true"})
	if err != nil || same != held {
		t.Fatal("no-op hold rewrote note")
	}
	if _, _, err := updateNoteContent(updated, map[string]string{"radar-state": "done", "radar-completed-at": "2026-01-01T00:00:00Z"}); err == nil {
		t.Fatal("done note with uncleared handoff accepted")
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
