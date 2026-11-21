package diagnostics

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"radar/internal/protocol"
)

func dispatchSummary(stage, status string, attempted, completed, total int) protocol.ManagedOperation {
	op := protocol.ManagedOperation{ID: "native-operation", Kind: "cleanup", Status: status, CompletedTargets: completed, TotalTargets: total, CleanupTargetsCaptured: true, CleanupTargetsTruncated: total > protocol.DiagnosticChildLimit, CleanupExecution: &protocol.OperationCleanupExecution{Stage: stage, AttemptedTargets: attempted}}
	for i := 0; i < min(total, protocol.DiagnosticChildLimit); i++ {
		outcome := "not_completed_may_be_partial"
		if i < completed {
			outcome = "returned_success_unverified"
		}
		op.CleanupTargets = append(op.CleanupTargets, protocol.OperationCleanupTarget{Index: i, ID: fmt.Sprintf("cleanup-result:%064x", i), Provider: "git", Kind: "worktree", Outcome: outcome})
	}
	return op
}

func TestCleanupTargetOutcomesUseValidatedNativeDispatch(t *testing.T) {
	for _, tc := range []struct {
		stage, status               string
		attempted, completed, total int
	}{
		{"admission", "failed", 0, 0, 3},
		{"ownership_and_leases", "failed", 0, 0, 3},
		{"bundle_preflight", "failed", 0, 0, 3},
		{"target_dispatch", "failed", 0, 0, 3},
		{"target_dispatch", "partial", 1, 1, 3},
		{"provider_call", "failed", 1, 0, 3},
		{"provider_call", "partial", 2, 1, 3},
		{"provider_call", "partial", 3, 2, 3},
		{"provider_call", "partial", 16, 15, 20},
		{"provider_call", "partial", 17, 16, 20},
		{"provider_call", "partial", 512, 511, 512},
		{"completed", "succeeded", 3, 3, 3},
	} {
		t.Run(fmt.Sprintf("%s/%d", tc.stage, tc.attempted), func(t *testing.T) {
			op := dispatchSummary(tc.stage, tc.status, tc.attempted, tc.completed, tc.total)
			before, _ := json.Marshal(op)
			view := operationItems(protocol.DiagnosticsRequest{}, nil, nil, []protocol.ManagedOperation{op})[0].Operation
			if view.CleanupTargetEvidence != "native_target_returns_with_dispatch_boundaries_not_resource_verification" || view.CleanupExecution == nil || len(view.CleanupTargets) != min(tc.total, 16) || view.CleanupTargetsTruncated == nil || *view.CleanupTargetsTruncated != (tc.total > 16) {
				t.Fatal("exact dispatch evidence not carried into bounded target projection", view)
			}
			for i, target := range view.CleanupTargets {
				want := "not_dispatched_by_cleanup_executor"
				if i < tc.completed {
					want = "returned_success_unverified"
				} else if tc.stage == "provider_call" && i == tc.completed {
					want = "returned_error_may_be_partial"
				}
				if target.Outcome != want || target.Index != i || target.ID != op.CleanupTargets[i].ID || target.Provider != "git" || target.Kind != "worktree" {
					t.Fatal("failed call confused with undispatched targets or verified outcome", target, want)
				}
			}
			view.CleanupTargets[0].Outcome = "caller mutation"
			view.CleanupExecution.Stage = "caller mutation"
			after, _ := json.Marshal(op)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("projection changed native capture")
			}
		})
	}
}

func TestCleanupTargetOutcomesNeverGuessMissingOrInvalidDispatch(t *testing.T) {
	for _, mode := range []string{"missing", "unavailable", "invalid-stage", "invalid-count", "false-status", "false-capture"} {
		t.Run(mode, func(t *testing.T) {
			op := dispatchSummary("provider_call", "partial", 2, 1, 3)
			switch mode {
			case "missing":
				op.CleanupExecution = nil
			case "unavailable":
				op.CleanupExecutionUnavailable = true
			case "invalid-stage":
				op.CleanupExecution.Stage = "private-provider-payload"
			case "invalid-count":
				op.CleanupExecution.AttemptedTargets = 3
			case "false-status":
				op.Status = "reported"
			case "false-capture":
				op.CleanupTargets[1].Outcome = "returned_error_may_be_partial"
			}
			view := operationItems(protocol.DiagnosticsRequest{}, nil, nil, []protocol.ManagedOperation{op})[0].Operation
			if mode == "false-status" || mode == "false-capture" {
				if len(view.CleanupTargets) != 0 {
					t.Fatal("uncaptured or forged precise result retained", view)
				}
			} else if view.CleanupTargetEvidence != "native_returned_target_prefix_not_resource_verification" || len(view.CleanupTargets) != 3 || view.CleanupTargets[1].Outcome != "not_completed_may_be_partial" || view.CleanupTargets[2].Outcome != "not_completed_may_be_partial" {
				t.Fatal("unknown dispatch became invented per-target failure/skip", view)
			}
			raw, _ := json.Marshal(view)
			if strings.Contains(string(raw), "private-provider-payload") {
				t.Fatal("raw dispatch escaped allowlist")
			}
		})
	}
}
