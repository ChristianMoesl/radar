package diagnostics

import (
	"encoding/json"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
)

func TestCleanupExecutionProjectionValidatesEveryNativeBoundary(t *testing.T) {
	for _, tc := range []struct {
		stage, status               string
		attempted, completed, total int
	}{
		{"admission", "failed", 0, 0, 0}, {"admission", "failed", 0, 0, 3},
		{"ownership_and_leases", "failed", 0, 0, 3}, {"bundle_preflight", "failed", 0, 0, 3},
		{"target_dispatch", "failed", 0, 0, 3}, {"target_dispatch", "partial", 1, 1, 3},
		{"provider_call", "failed", 1, 0, 3}, {"provider_call", "partial", 2, 1, 3},
		{"provider_call", "partial", 512, 511, 512}, {"completed", "succeeded", 3, 3, 3},
	} {
		op := protocol.ManagedOperation{ID: "op", Kind: "cleanup", Status: tc.status, CompletedTargets: tc.completed, TotalTargets: tc.total, CleanupExecution: &protocol.OperationCleanupExecution{Stage: tc.stage, AttemptedTargets: tc.attempted}}
		view := operationItems(protocol.DiagnosticsRequest{}, nil, nil, []protocol.ManagedOperation{op})[0].Operation
		if view.CleanupExecutionEvidence != "native_executor_dispatch_boundaries_not_resource_verification" || view.CleanupExecution == nil || *view.CleanupExecution != *op.CleanupExecution {
			t.Fatal("valid native boundary lost", tc, view)
		}
		view.CleanupExecution.Stage = "caller-edit"
		if op.CleanupExecution.Stage != tc.stage {
			t.Fatal("aliased projection")
		}
	}
	for _, mode := range []string{"unknown", "unavailable", "private-stage", "negative", "too-many", "below-completed", "false-completed", "false-failed", "false-success", "false-partial", "zero-preflight", "over-budget", "running", "reported", "other-kind"} {
		op := returnedSummaryFixture()
		op.CleanupExecution = &protocol.OperationCleanupExecution{Stage: "provider_call", AttemptedTargets: 2}
		want := "unavailable_invalid_or_over_budget"
		switch mode {
		case "unknown":
			op.CleanupExecution = nil
			want = "unknown_not_captured"
		case "unavailable":
			op.CleanupExecutionUnavailable = true
		case "private-stage":
			op.CleanupExecution.Stage = "private-raw-error"
		case "negative":
			op.CleanupExecution.AttemptedTargets = -1
		case "too-many":
			op.CleanupExecution.AttemptedTargets = 3
		case "below-completed":
			op.CleanupExecution.AttemptedTargets = 0
		case "false-completed":
			op.CleanupExecution.Stage = "completed"
		case "false-failed":
			op.Status = "failed"
		case "false-success":
			op.Status = "succeeded"
		case "false-partial":
			op.CompletedTargets = 0
		case "zero-preflight":
			op.Status = "failed"
			op.CompletedTargets = 0
			op.TotalTargets = 0
			op.CleanupExecution = &protocol.OperationCleanupExecution{Stage: "bundle_preflight"}
		case "over-budget":
			op.TotalTargets = 513
		case "running":
			op.Status = "running"
			want = "unknown_not_captured"
		case "reported":
			op.Status = "reported"
			want = "unknown_not_captured"
		case "other-kind":
			op.Kind = "task-note"
			want = "unknown_not_captured"
		}
		view := operationItems(protocol.DiagnosticsRequest{}, nil, nil, []protocol.ManagedOperation{op})[0].Operation
		raw, _ := json.Marshal(view)
		if view.CleanupExecutionEvidence != want || view.CleanupExecution != nil || strings.Contains(string(raw), "private-raw-error") {
			t.Fatal(mode, string(raw))
		}
	}
}

func TestCleanupExecutionCaptureInvalidatesScopedCursorWithoutRerunningProviders(t *testing.T) {
	store, _, path := fixture(t)
	store.SetTasks([]protocol.Task{task(path, "test:ABC-123")})
	taskID := store.Records()[0].NumericID
	a, b := store.BeginOperation("cleanup", taskID, ""), store.BeginOperation("cleanup", taskID, "")
	req := protocol.DiagnosticsRequest{Section: "operations", Workspace: path, Limit: 1}
	first, err := Read(store, req, false, integration.NewRegistry(diagnosticIntegrationTrap{"trap"}))
	if err != nil || first.NextCursor == "" {
		t.Fatal("fixture", err)
	}
	offpage := b
	if first.Items[0].ID == b {
		offpage = a
	}
	store.FinishCleanupOperation(offpage, protocol.ManagedOperation{Status: "failed", TotalTargets: 1}, []protocol.CleanupTarget{{Source: "git", Kind: "worktree", ResourceID: "opaque"}}, &protocol.OperationCleanupExecution{Stage: "provider_call", AttemptedTargets: 1})
	req.Cursor = first.NextCursor
	if _, err := Read(store, req, false, integration.NewRegistry()); err == nil {
		t.Fatal("cursor survived dispatch capture")
	}
	req.Cursor = ""
	req.ID = offpage
	before := store.Revision()
	page, err := Read(store, req, true, integration.NewRegistry(diagnosticIntegrationTrap{"trap"}))
	if err != nil || page.Total != 1 || page.Items[0].Operation.CleanupExecution.AttemptedTargets != 1 || !page.ResourceRefreshPending || store.Revision() != before {
		t.Fatal("scoped read lost/mutated evidence", page, err)
	}
	page, err = Read(store, protocol.DiagnosticsRequest{Section: "operations", Workspace: "/unrelated"}, false, integration.NewRegistry())
	if err != nil || page.Total != 0 {
		t.Fatal("crossed scope", err)
	}
}
