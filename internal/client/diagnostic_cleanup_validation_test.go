package client

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"radar/internal/protocol"
)

func TestDiagnosticManualCleanupProjectionAndRequestValidation(t *testing.T) {
	for _, kind := range []string{"valid_preview", "valid_apply", "valid_failed", "valid_cancelled", "valid_pathless", "valid_feedback", "valid_installation", "old_request", "wrong_operation", "wrong_scope", "request_mode", "preview_plan", "different_parent", "different_plan", "unsafe_task_id", "unsafe_revision", "zero_task", "missing_scope", "installation_with_scope", "feedback_mismatch", "expired", "far_future", "invalid_time", "invalid_id", "duplicate_id", "empty_targets", "many_targets", "oversized", "wrong_effect", "wrong_branch", "unknown_provider", "wrong_preserved", "wrong_task", "wrong_record", "expiry_mode", "missing_safety", "too_many_notices", "expiring_unknown", "invalid_safety_count", "false_notice_total", "false_blocker_total", "duplicate_notices", "unknown_ownership", "stopped_owner", "missing_owner", "contended", "pathless_owner", "pathless_lease", "ready", "apply_missing_count", "apply_missing_operation", "apply_not_pending", "apply_false_success", "apply_false_error", "apply_unknown_problem", "apply_false_dispatch"} {
		t.Run(kind, func(t *testing.T) {
			var response protocol.Response
			if err := json.Unmarshal(manualWireFixture(), &response); err != nil {
				t.Fatal(err)
			}
			v := response.DiagnosticCleanup
			req := protocol.DiagnosticMaintenanceRequest{Schema: protocol.DiagnosticsVersion, Operation: "cleanup", Mode: "preview", Scope: "origin", ParentID: "record"}
			if strings.Contains(kind, "apply") || kind == "valid_failed" || kind == "valid_cancelled" || kind == "different_plan" {
				req.Mode = "apply"
				req.PlanID = v.PlanID
				v.Mode, v.Status, v.OperationID = "apply", "returned", "operation:1"
				v.ResourceRefreshPending = true
				one := 1
				v.CompletedTargets = &one
				v.Execution = &protocol.OperationCleanupExecution{Stage: "completed", AttemptedTargets: 1}
			}
			target := &v.Targets[0]
			switch kind {
			case "valid_failed":
				zero := 0
				v.CompletedTargets = &zero
				v.Problem = "native_error"
				v.Execution = &protocol.OperationCleanupExecution{Stage: "provider_call", AttemptedTargets: 1}
			case "valid_cancelled":
				v.Problem = "cancelled_or_timed_out"
			case "valid_pathless":
				target.AgentOwnerCount = nil
				target.OwnershipEvidence, target.LeaseEvidence = "not_checked_no_target_path", "not_checked_no_target_path"
			case "valid_feedback":
				req.Scope, v.Scope = "feedback", "feedback"
				v.ScopeWorkspaceID = v.FeedbackWorkspaceID
			case "valid_installation":
				req.Scope, v.Scope = "installation", "installation"
				v.ScopeWorkspaceID = ""
			case "old_request":
				req.Schema--
			case "wrong_operation":
				req.Operation = "refresh"
			case "wrong_scope":
				req.Scope, v.Scope = "all", "all"
			case "request_mode":
				req.Mode = "expired"
			case "preview_plan":
				req.PlanID = v.PlanID
			case "different_parent":
				req.ParentID = "different"
			case "different_plan":
				req.Mode, v.Mode = "apply", "apply"
				req.PlanID = strings.Repeat("c", 64)
			case "unsafe_task_id":
				v.TaskID = int(9007199254740992)
				req.ParentID = v.RecordID
				target.TaskID = v.TaskID
			case "unsafe_revision":
				v.Revision = 9007199254740992
			case "zero_task":
				v.TaskID, target.TaskID = 0, 0
			case "missing_scope":
				v.ScopeWorkspaceID = ""
			case "installation_with_scope":
				req.Scope, v.Scope = "installation", "installation"
			case "feedback_mismatch":
				req.Scope, v.Scope = "feedback", "feedback"
			case "expired":
				v.ExpiresAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
			case "far_future":
				v.ExpiresAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
			case "invalid_time":
				v.ExpiresAt = "2026-02-30T00:00:00Z"
			case "invalid_id":
				v.PlanID = "private-sentinel"
			case "duplicate_id":
				v.Targets = append(v.Targets, *target)
			case "empty_targets":
				v.Targets = nil
			case "many_targets":
				v.Targets = make([]protocol.DiagnosticCleanupTarget, 513)
			case "oversized":
				v.Limitations = append(v.Limitations, strings.Repeat("x", 33*1024))
			case "wrong_effect":
				target.Effect = "private-sentinel"
			case "wrong_branch":
				target.BranchEffect = "private-sentinel"
			case "unknown_provider":
				target.Provider = "private-sentinel"
			case "wrong_preserved":
				target.Preserved = nil
			case "wrong_task":
				target.TaskID = 8
			case "wrong_record":
				target.RecordID = "different"
			case "expiry_mode":
				target.CleanupMode = "expired"
			case "missing_safety":
				target.Safety = nil
			case "too_many_notices":
				target.Safety = make([]protocol.DiagnosticCleanupSafety, 29)
			case "expiring_unknown":
				target.Safety[0].Kind = "other_notice"
			case "invalid_safety_count":
				target.Safety[0].Count = 1025
			case "false_notice_total":
				target.SafetyNoticeCount = 0
			case "false_blocker_total":
				target.AutomaticBlockerCount = 0
			case "duplicate_notices":
				target.Safety = append(target.Safety, target.Safety[0])
				target.SafetyNoticeCount, target.AutomaticBlockerCount = 2, 2
			case "unknown_ownership":
				target.OwnershipEvidence = "unavailable"
			case "stopped_owner":
				*target.AgentOwnerCount = 1
			case "missing_owner":
				target.AgentOwnerCount = nil
			case "contended":
				target.LeaseEvidence = "contended_at_probe"
			case "pathless_owner":
				target.OwnershipEvidence, target.LeaseEvidence = "not_checked_no_target_path", "not_checked_no_target_path"
			case "pathless_lease":
				target.OwnershipEvidence = "not_checked_no_target_path"
				target.AgentOwnerCount = nil
			case "ready":
				target.ExecutionEligibility = "ready"
			case "apply_missing_count":
				v.CompletedTargets = nil
			case "apply_missing_operation":
				v.OperationID = ""
			case "apply_not_pending":
				v.ResourceRefreshPending = false
			case "apply_false_success":
				*v.CompletedTargets = 0
			case "apply_false_error":
				v.Problem = "native_error"
			case "apply_unknown_problem":
				v.Problem = "private-sentinel"
			case "apply_false_dispatch":
				v.Execution.Stage = "provider_call"
			}
			if got, want := protocol.ValidDiagnosticCleanup(*v, req), strings.HasPrefix(kind, "valid_"); got != want {
				t.Fatalf("%s validity %v want %v", kind, got, want)
			}
		})
	}
}
