package state

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"radar/internal/protocol"
)

func collectionAttempt(outcome string, minute string) protocol.DiagnosticCollectionAttempt {
	a := protocol.DiagnosticCollectionAttempt{StartedAt: "2026-11-16T17:" + minute + ":00Z", FinishedAt: "2026-11-16T17:" + minute + ":02Z", Outcome: outcome}
	if outcome != "skipped_before_collection" {
		a.CollectionStartedAt = "2026-11-16T17:" + minute + ":01Z"
	}
	return a
}
func TestCollectionAttemptsAreIsolatedTransientEvidence(t *testing.T) {
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "state.db"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	store.SetSources([]protocol.SourceStatus{{Name: "example", Status: "ok"}})
	before, err := os.ReadFile(os.Getenv("RADAR_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	revision := store.Revision()
	changed := store.notify
	complete := collectionAttempt("complete", "01")
	later := collectionAttempt("incomplete", "02")
	store.RecordCollectionAttempts(map[string]protocol.DiagnosticCollectionAttempt{"example": complete})
	store.RecordCollectionAttempts(map[string]protocol.DiagnosticCollectionAttempt{"example": later})
	// Late completion of an older concurrent return must not replace the latest.
	store.RecordCollectionAttempts(map[string]protocol.DiagnosticCollectionAttempt{"example": complete})
	_, _, attempts, afterRevision := store.SourceInspectionSnapshot()
	want := protocol.DiagnosticCollectionEvidence{Latest: later, LastCompleteAt: complete.FinishedAt}
	if attempts["example"] != want || afterRevision != revision {
		t.Fatalf("attempts=%+v revision=%d", attempts, afterRevision)
	}
	select {
	case <-changed:
		t.Fatal("diagnostic evidence notified task consumers")
	default:
	}
	attempts["example"] = protocol.DiagnosticCollectionEvidence{}
	_, _, again, _ := store.SourceInspectionSnapshot()
	if again["example"] != want {
		t.Fatal("snapshot shares mutable evidence map")
	}
	after, _ := os.ReadFile(os.Getenv("RADAR_STATE"))
	if !reflect.DeepEqual(before, after) {
		t.Fatal("attempts changed persisted projection")
	}
	restarted, err := NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	_, _, history, _ := restarted.SourceInspectionSnapshot()
	if len(history) != 0 {
		t.Fatal("restart invented collection history")
	}
	skipped := collectionAttempt("skipped_before_collection", "03")
	store.RecordCollectionAttempts(map[string]protocol.DiagnosticCollectionAttempt{"example": skipped})
	_, _, again, _ = store.SourceInspectionSnapshot()
	if again["example"].Latest != skipped || again["example"].LastCompleteAt != complete.FinishedAt {
		t.Fatal("skip lost genuine last complete evidence")
	}
}
func TestCollectionAttemptsRejectInvalidEvidence(t *testing.T) {
	for _, mutate := range []func(*protocol.DiagnosticCollectionAttempt){
		func(a *protocol.DiagnosticCollectionAttempt) { a.Outcome = "private-error" },
		func(a *protocol.DiagnosticCollectionAttempt) { a.StartedAt = "unknown" },
		func(a *protocol.DiagnosticCollectionAttempt) { a.FinishedAt = "2026-11-16T16:00:00Z" },
		func(a *protocol.DiagnosticCollectionAttempt) { a.CollectionStartedAt = "2026-11-16T18:00:00Z" },
		func(a *protocol.DiagnosticCollectionAttempt) { a.CollectionStartedAt = "" },
		func(a *protocol.DiagnosticCollectionAttempt) { a.StartedAt = "2026-11-16T17:01:00+01:00" },
		func(a *protocol.DiagnosticCollectionAttempt) { a.Outcome = "skipped_before_collection" },
	} {
		a := collectionAttempt("complete", "01")
		mutate(&a)
		store := &Store{}
		store.RecordCollectionAttempts(map[string]protocol.DiagnosticCollectionAttempt{"example": a})
		_, _, attempts, _ := store.SourceInspectionSnapshot()
		if len(attempts) != 0 {
			t.Fatalf("accepted invalid attempt: %+v", a)
		}
	}
}
func TestCollectionAttemptConcurrentSnapshots(t *testing.T) {
	store := &Store{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				store.RecordCollectionAttempts(map[string]protocol.DiagnosticCollectionAttempt{"example": collectionAttempt("complete", "01")})
				_, _, attempts, _ := store.SourceInspectionSnapshot()
				if attempts["example"].Latest.FinishedAt != attempts["example"].LastCompleteAt {
					t.Error("torn attempt evidence")
				}
			}
		}()
	}
	wg.Wait()
}
