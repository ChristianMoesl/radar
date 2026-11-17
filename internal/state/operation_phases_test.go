package state

import (
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"radar/internal/protocol"
)

func phaseStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "state.db"))
	store, err := NewStore(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestOperationPhasesAreNativeBoundedSortedCompletedSamplesOnly(t *testing.T) {
	store := phaseStore(t)
	id := store.BeginOperation("cleanup", 7, "")
	revision := store.Revision()
	store.RecordOperationPhase(id, "private-command-or-error", time.Second)
	store.RecordOperationPhase("missing", "cleanup_execution", time.Second)
	report := store.ReportResourceMutation(protocol.ResourceMutation{TaskID: 7})
	before := store.Revision()
	store.RecordOperationPhase(report, "cleanup_execution", time.Second)
	other := store.BeginOperation("task-note", 7, "")
	store.RecordOperationPhase(other, "cleanup_execution", time.Second)
	if store.Revision() != before+1 {
		t.Fatal("unsupported/report timing mutated state")
	}
	store.RecordOperationPhase(id, "cleanup_worktree", 1500*time.Microsecond)
	store.RecordOperationPhase(id, "cleanup_worktree", 1500*time.Microsecond)
	store.RecordOperationPhase(id, "cleanup_execution", 0)
	got := store.DashboardSnapshot().Operations[0]
	want := []protocol.OperationPhaseTiming{{Phase: "cleanup_execution", CompletedIntervals: 1}, {Phase: "cleanup_worktree", CompletedIntervals: 2, ElapsedMilliseconds: 2}}
	if !reflect.DeepEqual(got.PhaseTimings, want) || got.PhaseTimingsUnavailable || store.Revision() <= revision {
		t.Fatalf("phase aggregate=%+v", got)
	}
	store.FinishOperation(id, protocol.ManagedOperation{Status: "partial", PhaseTimings: []protocol.OperationPhaseTiming{{Phase: "private-forged", CompletedIntervals: 99}}, PhaseTimingsUnavailable: true})
	finished := store.DashboardSnapshot()
	op := finished.Operations[len(finished.Operations)-1]
	if !reflect.DeepEqual(op.PhaseTimings, want) || op.PhaseTimingsUnavailable {
		t.Fatal("result injected phase data")
	}
	store.RecordOperationPhase(id, "cleanup_execution", time.Second)
	store.FinishOperation(id, protocol.ManagedOperation{Status: "succeeded"})
	if !reflect.DeepEqual(finished, store.DashboardSnapshot()) {
		t.Fatal("late phase callback/duplicate completion changed first result")
	}
}

func TestOperationPhaseSnapshotIsolationConcurrentAggregationAndNoPersistence(t *testing.T) {
	store := phaseStore(t)
	id := store.BeginOperation("cleanup", 7, "")
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() { store.RecordOperationPhase(id, "cleanup_resource", time.Millisecond) })
	}
	wg.Wait()
	first := store.DashboardSnapshot()
	if first.Operations[0].PhaseTimings[0].CompletedIntervals != 100 || first.Operations[0].PhaseTimings[0].ElapsedMilliseconds != 100 {
		t.Fatal("lost concurrent completed intervals")
	}
	first.Operations[0].PhaseTimings[0].Phase = "private-edit"
	_, _, ops, _ := store.OperationInspectionSnapshot()
	ops[0].PhaseTimings[0].CompletedIntervals = 0
	fresh := store.DashboardSnapshot()
	if fresh.Operations[0].PhaseTimings[0].Phase != "cleanup_resource" || fresh.Operations[0].PhaseTimings[0].CompletedIntervals != 100 {
		t.Fatal("mutable snapshot escaped into store")
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(store.state)
	if strings.Contains(string(raw), "phase_timing") {
		t.Fatal("phases persisted")
	}
	restarted, err := NewStore(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil || len(restarted.DashboardSnapshot().Operations) != 0 {
		t.Fatal("restart replayed phase history", err)
	}
	for range operationHistoryLimit + 1 {
		next := store.BeginOperation("cleanup", 7, "")
		store.RecordOperationPhase(next, "cleanup_execution", 0)
		store.FinishOperation(next, protocol.ManagedOperation{Status: "failed"})
	}
	if len(store.DashboardSnapshot().Operations) != operationHistoryLimit+1 {
		t.Fatal("changed native retention")
	}
	if err := store.Reset(); err != nil {
		t.Fatal(err)
	}
	store.RecordOperationPhase(id, "cleanup_resource", time.Second)
	if len(store.DashboardSnapshot().Operations) != 0 {
		t.Fatal("reset retained late phase data")
	}
}

func TestOperationPhaseInvalidOrOverflowIsUnavailableNotPartialPlausibleSum(t *testing.T) {
	for _, kind := range []string{"negative", "count", "sum"} {
		t.Run(kind, func(t *testing.T) {
			store := phaseStore(t)
			id := store.BeginOperation("cleanup", 7, "")
			store.RecordOperationPhase(id, "cleanup_execution", time.Millisecond)
			switch kind {
			case "negative":
				store.RecordOperationPhase(id, "cleanup_execution", -time.Nanosecond)
			case "count":
				store.operations[0].PhaseTimings[0].CompletedIntervals = protocol.MaxOperationPhaseIntervals
				store.RecordOperationPhase(id, "cleanup_execution", 0)
			case "sum":
				store.operations[0].PhaseTimings[0].ElapsedMilliseconds = protocol.MaxOperationPhaseMilliseconds
				store.RecordOperationPhase(id, "cleanup_execution", time.Millisecond)
			}
			got := store.DashboardSnapshot()
			if !got.Operations[0].PhaseTimingsUnavailable || len(got.Operations[0].PhaseTimings) != 0 {
				t.Fatal("overflow exposed partial sum")
			}
			store.RecordOperationPhase(id, "cleanup_runtime", 0)
			if !reflect.DeepEqual(got, store.DashboardSnapshot()) {
				t.Fatal("invalid aggregate became apparently available")
			}
		})
	}
}
