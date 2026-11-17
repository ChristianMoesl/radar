package state

import (
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"radar/internal/protocol"
)

func TestNativeOperationTimingIsStoreOwnedMonotonicAndFirstCompletionOnly(t *testing.T) {
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "state.db"))
	store, err := NewStore(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	id := store.BeginOperation("cleanup", 7, "")
	running := store.DashboardSnapshot()
	op := running.Operations[0]
	start, err := time.Parse(time.RFC3339Nano, op.StartedAt)
	if err != nil || start.Before(before) || start.After(time.Now()) || op.FinishedAt != "" || op.ElapsedMilliseconds != 0 || len(store.operationStarts) != 1 {
		t.Fatalf("invalid native start: %+v %v", op, err)
	}
	// Keep the real monotonic component but deliberately change the wall timestamp
	// in the projection. Finish must use the private instant, not parse this text.
	store.operationStarts[id] = time.Now().Add(-2 * time.Second)
	store.operations[0].StartedAt = "9999-01-01T00:00:00Z"
	store.FinishOperation(id, protocol.ManagedOperation{Status: "partial", CompletedTargets: 1, TotalTargets: 2, StartedAt: "private-forged-start", FinishedAt: "private-forged-finish", ElapsedMilliseconds: 999999})
	terminal := store.DashboardSnapshot()
	done := terminal.Operations[0]
	end, err := time.Parse(time.RFC3339Nano, done.FinishedAt)
	if err != nil || end.Before(before) || end.After(time.Now()) || done.StartedAt != "9999-01-01T00:00:00Z" || done.ElapsedMilliseconds < 2000 || done.ElapsedMilliseconds > time.Since(before).Milliseconds()+2000 || done.Status != "partial" || len(store.operationStarts) != 0 || terminal.Revision <= running.Revision {
		t.Fatalf("finish not native/monotonic: %+v %v", done, err)
	}
	store.FinishOperation(id, protocol.ManagedOperation{Status: "succeeded", CompletedTargets: 2, TotalTargets: 2})
	if again := store.DashboardSnapshot(); again.Revision != terminal.Revision || again.Operations[0] != done {
		t.Fatal("duplicate finish replaced first native result/timing")
	}
	report := store.ReportResourceMutation(protocol.ResourceMutation{TaskID: 7})
	reported := store.DashboardSnapshot()
	store.FinishOperation(report, protocol.ManagedOperation{Status: "succeeded", StartedAt: op.StartedAt, FinishedAt: done.FinishedAt, ElapsedMilliseconds: 1})
	if again := store.DashboardSnapshot(); again.Revision != reported.Revision || again.Operations[len(again.Operations)-1].Status != "reported" || len(store.operationStarts) != 0 {
		t.Fatal("client report became native execution/timing")
	}
	id = store.BeginOperation("task-note", 7, "")
	revision := store.Revision()
	for _, status := range []string{"", "running", "reported", "private-status"} {
		store.FinishOperation(id, protocol.ManagedOperation{Status: status})
		if store.Revision() != revision || len(store.operationStarts) != 1 {
			t.Fatal("invalid finish consumed active timing")
		}
	}
	if err := store.Reset(); err != nil {
		t.Fatal(err)
	}
	if len(store.operationStarts) != 0 || len(store.DashboardSnapshot().Operations) != 0 {
		t.Fatal("reset retained timing")
	}
}

func TestNativeOperationTimingCopiesAreIndependentBoundedAndNotPersisted(t *testing.T) {
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "state.db"))
	store, err := NewStore(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	active := store.BeginOperation("task-title", 7, "")
	for range operationHistoryLimit + 10 {
		id := store.BeginOperation("cleanup", 7, "")
		store.FinishOperation(id, protocol.ManagedOperation{Status: "failed"})
	}
	if len(store.operationStarts) != 1 || store.operationStarts[active].IsZero() || len(store.DashboardSnapshot().Operations) != operationHistoryLimit+1 {
		t.Fatal("terminal timing leaked private active-start records or changed retention")
	}
	first := store.DashboardSnapshot()
	first.Operations[0].StartedAt = "caller edit"
	_, _, ops, _ := store.OperationInspectionSnapshot()
	ops[0].FinishedAt = "caller edit"
	again := store.DashboardSnapshot()
	if again.Operations[0].StartedAt == "caller edit" || again.Operations[0].FinishedAt != "" {
		t.Fatal("timing snapshot mutation escaped")
	}
	// JSON scalar copies preserve native zero milliseconds on a terminal record;
	// status/start/finish, not omitempty alone, establish that zero was measured.
	for _, op := range again.Operations[1:] {
		if op.StartedAt == "" || op.FinishedAt == "" || op.ElapsedMilliseconds < 0 {
			t.Fatal("terminal timing missing")
		}
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(store.state)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"started_at", "finished_at", "elapsed_milliseconds"} {
		if strings.Contains(string(raw), key) {
			t.Fatal("timing entered persisted projection")
		}
	}
	restarted, err := NewStore(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if len(restarted.DashboardSnapshot().Operations) != 0 || len(restarted.operationStarts) != 0 {
		t.Fatal("restart replayed operation timing")
	}
	store.FinishOperation(active, protocol.ManagedOperation{Status: "succeeded"})
	if len(store.operationStarts) != 0 || len(store.DashboardSnapshot().Operations) != operationHistoryLimit {
		t.Fatal("finish retained active timing or exceeded terminal bound")
	}
}
