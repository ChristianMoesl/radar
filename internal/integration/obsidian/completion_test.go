package obsidian

import (
	"context"
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
	observation, err := source.ReconcileCompletion(context.Background(), ref, items)
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
	again, err := source.ReconcileCompletion(context.Background(), observation.Ref, items)
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
	if observation, err := source.ReconcileCompletion(context.Background(), ref, items); err != nil || observation != nil {
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
