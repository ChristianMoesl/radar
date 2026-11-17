package diagnostics

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/protocol"
)

func TestOperationTimingProjectionLabelsRunningReturnedAndUnknown(t *testing.T) {
	start, end := "2026-11-17T14:00:00Z", "2026-11-17T14:00:01.000000001Z"
	for _, status := range []string{"running", "succeeded", "failed", "partial", "reported", "unknown", "private-status"} {
		t.Run(status, func(t *testing.T) {
			native := protocol.ManagedOperation{ID: "op", Kind: "cleanup", Status: status, StartedAt: start}
			if status != "running" {
				native.FinishedAt = end
				native.ElapsedMilliseconds = 1000
			}
			rows := operationItems(protocol.DiagnosticsRequest{}, nil, nil, []protocol.ManagedOperation{native})
			op := rows[0].Operation
			switch status {
			case "running":
				if op.TimingEvidence != "native_handler_start_only" || op.StartedAt != start || op.FinishedAt != "" || op.ElapsedMilliseconds != nil {
					t.Fatalf("start=%+v", op)
				}
			case "succeeded", "failed", "partial":
				if op.TimingEvidence != "native_handler_monotonic_elapsed" || op.StartedAt != start || op.FinishedAt != end || op.ElapsedMilliseconds == nil || *op.ElapsedMilliseconds != 1000 {
					t.Fatalf("returned=%+v", op)
				}
			default:
				if op.TimingEvidence != "unknown_not_captured" || op.StartedAt != "" || op.FinishedAt != "" || op.ElapsedMilliseconds != nil {
					t.Fatalf("unsupported/report timing=%+v", op)
				}
			}
		})
	}
	// A backward wall clock and sub-millisecond zero do not negate native elapsed
	// evidence or imply success. The diagnostic must not calculate wall duration.
	rows := operationItems(protocol.DiagnosticsRequest{}, nil, nil, []protocol.ManagedOperation{{ID: "op", Kind: "cleanup", Status: "failed", StartedAt: end, FinishedAt: start}})
	op := rows[0].Operation
	raw, _ := json.Marshal(op)
	if op.TimingEvidence != "native_handler_monotonic_elapsed" || op.ElapsedMilliseconds == nil || *op.ElapsedMilliseconds != 0 || op.Status != "failed" || !json.Valid(raw) {
		t.Fatalf("clock/zero=%s", raw)
	}
}

func TestOperationTimingRejectsInvalidPartialAndPrivateEvidence(t *testing.T) {
	good := protocol.ManagedOperation{ID: "op", Kind: "cleanup", Status: "failed", StartedAt: "2026-11-17T14:00:00Z", FinishedAt: "2026-11-17T14:00:01Z", ElapsedMilliseconds: 1000}
	cases := map[string]func(*protocol.ManagedOperation){
		"missing start":                   func(op *protocol.ManagedOperation) { op.StartedAt = "" },
		"missing finish":                  func(op *protocol.ManagedOperation) { op.FinishedAt = "" },
		"zero start":                      func(op *protocol.ManagedOperation) { op.StartedAt = "0001-01-01T00:00:00Z" },
		"year zero":                       func(op *protocol.ManagedOperation) { op.FinishedAt = "0000-01-01T00:00:00Z" },
		"invalid date":                    func(op *protocol.ManagedOperation) { op.FinishedAt = "2026-02-30T00:00:00Z" },
		"offset time":                     func(op *protocol.ManagedOperation) { op.FinishedAt = "2026-11-17T14:00:01+00:00" },
		"oversized time":                  func(op *protocol.ManagedOperation) { op.FinishedAt = "private-oversized-error-and-payload-string" },
		"negative elapsed":                func(op *protocol.ManagedOperation) { op.ElapsedMilliseconds = -1 },
		"out of monotonic duration bound": func(op *protocol.ManagedOperation) { op.ElapsedMilliseconds = int64((1<<63-1)/time.Millisecond) + 1 },
		"running with finish":             func(op *protocol.ManagedOperation) { op.Status = "running" },
		"running with elapsed":            func(op *protocol.ManagedOperation) { op.Status = "running"; op.FinishedAt = "" },
		"report with forged timing":       func(op *protocol.ManagedOperation) { op.Status = "reported" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			native := good
			mutate(&native)
			op := operationItems(protocol.DiagnosticsRequest{}, nil, nil, []protocol.ManagedOperation{native})[0].Operation
			if op.TimingEvidence != "unknown_not_captured" || op.StartedAt != "" || op.FinishedAt != "" || op.ElapsedMilliseconds != nil {
				t.Fatalf("invalid became evidence: %+v", op)
			}
		})
	}
}

func TestOperationTimingInspectionDoesNotTickAndCompletionInvalidatesPages(t *testing.T) {
	store, _, path := fixture(t)
	store.SetTasks([]protocol.Task{task(path, "test:ABC-123")})
	taskID := store.Records()[0].NumericID
	firstID := store.BeginOperation("task-title", taskID, "")
	secondID := store.BeginOperation("cleanup", taskID, "")
	req := protocol.DiagnosticsRequest{Section: "operations", Workspace: path, Limit: 1}
	first, err := Read(store, req, false, integration.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if first.NextCursor == "" || first.Total != 2 {
		t.Fatal("incomplete fixture")
	}
	before := store.DashboardSnapshot()
	again, err := Read(store, req, false, integration.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if first.Snapshot != again.Snapshot || first.Revision != again.Revision || !reflect.DeepEqual(first.Items, again.Items) || !reflect.DeepEqual(before, store.DashboardSnapshot()) {
		t.Fatal("inspection ticked a timer or mutated revision")
	}
	// Complete the row beyond the first page: all shown scoped timing participates
	// in the snapshot, not merely the returned first item.
	finishID := secondID
	if first.Items[0].ID == secondID {
		finishID = firstID
	}
	store.FinishOperation(finishID, protocol.ManagedOperation{Status: "failed", TotalTargets: 2})
	req.Cursor = first.NextCursor
	if _, err := Read(store, req, false, integration.NewRegistry()); err == nil {
		t.Fatal("cursor survived off-page lifecycle/timing change")
	}
	one, err := Read(store, protocol.DiagnosticsRequest{Section: "operations", ID: finishID}, false, integration.NewRegistry())
	if err != nil || one.Total != 1 || one.Items[0].Operation.TimingEvidence != "native_handler_monotonic_elapsed" {
		t.Fatalf("native terminal timing absent: %+v %v", one, err)
	}
	copy := one.Items[0].Operation.ElapsedMilliseconds
	*copy = -1
	fresh, err := Read(store, protocol.DiagnosticsRequest{Section: "operations", ID: finishID}, false, integration.NewRegistry())
	if err != nil || *fresh.Items[0].Operation.ElapsedMilliseconds < 0 {
		t.Fatal("projected duration pointer mutated store")
	}
}
