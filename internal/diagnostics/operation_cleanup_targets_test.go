package diagnostics

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
)

func returnedSummaryFixture() protocol.ManagedOperation {
	return protocol.ManagedOperation{ID: "op", Kind: "cleanup", Status: "partial", CompletedTargets: 1, TotalTargets: 2, CleanupTargetsCaptured: true, CleanupTargets: []protocol.OperationCleanupTarget{
		{Index: 0, ID: "cleanup-result:" + strings.Repeat("a", 64), Provider: "git", Kind: "worktree", Outcome: "returned_success_unverified"},
		{Index: 1, ID: "cleanup-result:" + strings.Repeat("b", 64), Provider: "sbx", Kind: "sandbox", Outcome: "not_completed_may_be_partial"},
	}}
}

func TestCleanupReturnedTargetProjectionWithholdsInvalidAndUncapturedEvidence(t *testing.T) {
	for _, mode := range []string{"valid", "unknown", "unavailable", "running", "reported", "other kind", "missing targets", "private id", "uppercase id", "duplicate id", "ordinal", "private provider", "private kind", "private outcome", "false success", "false failed", "false partial", "false total", "large total", "truncation", "outcome reversal", "missing captured"} {
		t.Run(mode, func(t *testing.T) {
			native := returnedSummaryFixture()
			unknown := false
			switch mode {
			case "unknown":
				native.CleanupTargetsCaptured = false
				native.CleanupTargets = nil
				unknown = true
			case "unavailable":
				native.CleanupTargetsUnavailable = true
			case "running":
				native.Status = "running"
				unknown = true
			case "reported":
				native.Status = "reported"
				unknown = true
			case "other kind":
				native.Kind = "task-note"
				unknown = true
			case "missing targets":
				native.CleanupTargets = nil
			case "private id":
				native.CleanupTargets[0].ID = "private-payload"
			case "uppercase id":
				native.CleanupTargets[0].ID = "cleanup-result:" + strings.Repeat("A", 64)
			case "duplicate id":
				native.CleanupTargets[1].ID = native.CleanupTargets[0].ID
			case "ordinal":
				native.CleanupTargets[1].Index = 4
			case "private provider":
				native.CleanupTargets[0].Provider = "private-payload"
			case "private kind":
				native.CleanupTargets[0].Kind = "private-payload"
			case "private outcome":
				native.CleanupTargets[0].Outcome = "private-payload"
			case "false success":
				native.Status = "succeeded"
			case "false failed":
				native.Status = "failed"
			case "false partial":
				native.CompletedTargets = 0
			case "false total":
				native.TotalTargets = 3
			case "large total":
				native.TotalTargets = 513
			case "truncation":
				native.CleanupTargetsTruncated = true
			case "outcome reversal":
				native.CleanupTargets[0].Outcome = "not_completed_may_be_partial"
			case "missing captured":
				native.CleanupTargetsCaptured = false
				unknown = true
			}
			view := operationItems(protocol.DiagnosticsRequest{}, nil, nil, []protocol.ManagedOperation{native})[0].Operation
			if mode == "valid" {
				if view.CleanupTargetEvidence != "native_returned_target_prefix_not_resource_verification" || view.CleanupTargetsTruncated == nil || *view.CleanupTargetsTruncated || !reflect.DeepEqual(view.CleanupTargets, native.CleanupTargets) {
					t.Fatal("valid lost", view)
				}
				view.CleanupTargets[0].ID = "caller-edit"
				if native.CleanupTargets[0].ID == "caller-edit" {
					t.Fatal("projection aliases native")
				}
				return
			}
			want := "unavailable_invalid_or_over_budget"
			if unknown {
				want = "unknown_not_captured"
			}
			raw, _ := json.Marshal(view)
			if view.CleanupTargetEvidence != want || len(view.CleanupTargets) != 0 || view.CleanupTargetsTruncated != nil || strings.Contains(string(raw), "private-payload") {
				t.Fatal("invalid/unknown invented summary", string(raw))
			}
		})
	}
}

func TestCleanupReturnedTargetSummaryIsScopedSnapshotBoundAndReadOnly(t *testing.T) {
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
	store.FinishCleanupOperation(offpage, protocol.ManagedOperation{Status: "failed", TotalTargets: 1}, []protocol.CleanupTarget{{Source: "git", Kind: "worktree", ResourceID: "private-resource"}})
	req.Cursor = first.NextCursor
	if _, err := Read(store, req, false, integration.NewRegistry()); err == nil {
		t.Fatal("cursor survived off-page capture")
	}
	req.Cursor, req.ID, req.Limit = "", offpage, 20
	before := store.DashboardSnapshot()
	page, err := Read(store, req, true, integration.NewRegistry(diagnosticIntegrationTrap{"trap"}))
	if err != nil || page.Total != 1 || !page.ResourceRefreshPending || page.Items[0].Operation.CleanupTargets[0].Outcome != "not_completed_may_be_partial" || !reflect.DeepEqual(before, store.DashboardSnapshot()) {
		t.Fatal("scope/capture/read mutation", page, err)
	}
	page, err = Read(store, protocol.DiagnosticsRequest{Section: "operations", Workspace: "/unrelated"}, false, integration.NewRegistry())
	if err != nil || page.Total != 0 {
		t.Fatal("summary crossed workspace scope", err)
	}
}
