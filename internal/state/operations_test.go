package state

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"radar/internal/protocol"
)

func TestOperationsAreRevisionedBoundedClonedAndNotPersisted(t *testing.T) {
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "state.db"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	before := store.Revision()
	id := store.BeginOperation("cleanup", 7, "workspace-one")
	first := store.DashboardSnapshot()
	if first.Revision <= before || len(first.Operations) != 1 || first.Operations[0].Status != "running" {
		t.Fatalf("start not published: %+v", first)
	}
	store.FinishOperation(id, protocol.ManagedOperation{Status: "failed", TaskID: 7})
	if store.WaitForRevision(context.Background(), first.Revision) <= first.Revision {
		t.Fatal("completion did not wake revision watch")
	}
	first.Operations[0].ID = "edited copy"
	latest := store.DashboardSnapshot().Operations[0]
	if latest.ID != id || latest.Status != "failed" || latest.TaskID != 7 || latest.WorkspaceID != "workspace-one" {
		t.Fatalf("identity/result overwritten: %+v", latest)
	}
	active := store.BeginOperation("cleanup", 8, "workspace-three")
	for range operationHistoryLimit + 10 {
		store.ReportResourceMutation(protocol.ResourceMutation{WorkspaceID: "workspace-two"})
	}
	snapshot := store.DashboardSnapshot()
	if len(snapshot.Operations) != operationHistoryLimit+1 || snapshot.Operations[0].ID != active || snapshot.Operations[1].Status != "reported" {
		t.Fatalf("history not bounded: %d", len(snapshot.Operations))
	}
	store.FinishOperation(active, protocol.ManagedOperation{Status: "succeeded", TaskID: 8})
	if len(store.DashboardSnapshot().Operations) != operationHistoryLimit {
		t.Fatal("terminal retention evicted active work or exceeded bound")
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	if len(restarted.DashboardSnapshot().Operations) != 0 {
		t.Fatal("daemon restart replayed operation evidence")
	}
	if err := store.Reset(); err != nil {
		t.Fatal(err)
	}
	if len(store.DashboardSnapshot().Operations) != 0 {
		t.Fatal("reset retained cache-local operation targets")
	}
}

func TestOperationInspectionSnapshotPairsRetainedScopeAndOperationRevision(t *testing.T) {
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "state.db"))
	store, err := NewStore(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	publish := func(n int) {
		store.mu.Lock()
		defer store.mu.Unlock()
		store.state.Records = []TaskRecord{{ID: "record", NumericID: n, SourceRefIDs: []string{"ref"}, Snapshot: protocol.Task{Metadata: map[string]string{"marker": strconv.Itoa(n)}}}}
		store.state.SourceRefs = []SourceRefRecord{{ID: "ref", TaskRecordID: "record", Snapshot: protocol.SourceRef{Path: strconv.Itoa(n), Metadata: map[string]string{"marker": strconv.Itoa(n)}}}}
		store.operations = []protocol.ManagedOperation{{ID: "op", TaskID: n, Status: "running", Kind: "cleanup"}}
		store.bumpRevisionLocked()
	}
	publish(1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 2; i < 200; i++ {
			publish(i)
		}
	}()
	for {
		records, refs, ops, rev := store.OperationInspectionSnapshot()
		if len(records) != 1 || len(refs) != 1 || len(ops) != 1 || rev <= 0 || records[0].NumericID != ops[0].TaskID || refs[0].Snapshot.Path != strconv.Itoa(ops[0].TaskID) || records[0].Snapshot.Metadata["marker"] != refs[0].Snapshot.Metadata["marker"] || records[0].SourceRefIDs[0] != "ref" || ops[0].Status != "running" {
			t.Fatal("scope/operation snapshot was torn or caller mutation escaped")
		}
		records[0].Snapshot.Metadata["marker"] = "caller changed clone"
		records[0].SourceRefIDs[0] = "caller changed clone"
		refs[0].Snapshot.Metadata["marker"] = "caller changed clone"
		ops[0].Status = "caller changed clone"
		select {
		case <-done:
			_, _, again, _ := store.OperationInspectionSnapshot()
			if again[0].Status != "running" {
				t.Fatal("operation clone escaped")
			}
			return
		default:
			runtime.Gosched()
		}
	}
}
