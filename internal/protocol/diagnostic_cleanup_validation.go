package protocol

import (
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// ValidDiagnosticCleanup validates the entire finite public projection, not
// execution eligibility. Wire decoding must independently reject extra/missing
// members. Approval and matching a previously displayed plan belong to the client.
func ValidDiagnosticCleanup(v DiagnosticCleanup, req DiagnosticMaintenanceRequest) bool {
	text := func(s string) bool { return len(s) > 0 && len(s) <= 512 }
	hexID := func(s string) bool {
		if len(s) != 64 || s != strings.ToLower(s) {
			return false
		}
		_, err := hex.DecodeString(s)
		return err == nil
	}
	if req.Schema != DiagnosticsVersion || req.Operation != "cleanup" || (req.Scope != "origin" && req.Scope != "feedback" && req.Scope != "installation") || (req.Mode == "preview" && req.PlanID != "") {
		return false
	}
	expiry, err := time.Parse(time.RFC3339Nano, v.ExpiresAt)
	if err != nil || len(v.ExpiresAt) > 35 || expiry.UTC().Format(time.RFC3339Nano) != v.ExpiresAt || v.Schema != DiagnosticsVersion || v.Operation != "cleanup" || v.Mode != req.Mode || v.Scope != req.Scope || !text(v.RecordID) || v.TaskID < 1 || int64(v.TaskID) > 9007199254740991 || (req.ParentID != v.RecordID && req.ParentID != strconv.Itoa(v.TaskID)) || !text(v.FeedbackWorkspaceID) || len(v.ScopeWorkspaceID) > 512 || (v.Scope == "installation") != (v.ScopeWorkspaceID == "") || v.Evidence != "native_exact_manual_cleanup_plan_not_consent" || !hexID(v.PlanID) || v.Revision < 0 || v.Revision > 9007199254740991 || len(v.Targets) < 1 || len(v.Targets) > 512 || !reflect.DeepEqual(v.Limitations, DiagnosticCleanupLimitations()) {
		return false
	}
	if v.Scope == "feedback" && v.ScopeWorkspaceID != v.FeedbackWorkspaceID {
		return false
	}
	raw, err := json.Marshal(v)
	if err != nil || len(raw) > 32*1024 {
		return false
	}
	seen := map[string]bool{}
	for _, target := range v.Targets {
		token := strings.TrimPrefix(target.ID, "cleanup-target:")
		if target.ID == token || !hexID(token) || seen[target.ID] || target.RecordID != v.RecordID || target.TaskID != v.TaskID || target.CleanupMode != "manual" || len(target.WorkspaceID) > 512 || target.ExpiryAt != "" || target.ExecutionEligibility != "unknown_fresh_execution_guards_not_run" || !reflect.DeepEqual(target.Preserved, []string{"remote_repositories"}) || target.Safety == nil || len(target.Safety) > 28 || target.SafetyNoticeCount < 0 || target.SafetyNoticeCount > 1024 {
			return false
		}
		seen[target.ID] = true
		switch target.Provider {
		case "git":
			if target.Effect != "remove_worktree_and_local_changes" || (target.BranchEffect != "retain_local_branch" && target.BranchEffect != "delete_local_branch") {
				return false
			}
		case "sbx":
			if target.Effect != "remove_sandbox_and_local_state" || target.BranchEffect != "" {
				return false
			}
		case "tmux":
			if target.Effect != "remove_terminal_session_and_processes" || target.BranchEffect != "" {
				return false
			}
		case "workspace":
			if target.Effect == "forget_missing_worktree_registration" {
				if target.BranchEffect != "retain_local_branch" {
					return false
				}
			} else if target.Effect != "remove_workspace_local_files_registration_and_archive_completed_note" || target.BranchEffect != "" {
				return false
			}
		default:
			return false
		}
		if target.OwnershipEvidence == "fresh_bounded_registration" {
			if target.AgentOwnerCount == nil || *target.AgentOwnerCount != 0 || (target.LeaseEvidence != "exclusive_available_at_probe" && target.LeaseEvidence != "unknown_no_lock_file" && target.LeaseEvidence != "unavailable") {
				return false
			}
		} else if target.OwnershipEvidence != "not_checked_no_target_path" || target.AgentOwnerCount != nil || target.LeaseEvidence != "not_checked_no_target_path" {
			return false
		}
		notices := map[DiagnosticCleanupSafety]bool{}
		total, blocking := 0, 0
		for _, notice := range target.Safety {
			key := notice
			key.Count = 0
			if notices[key] || notice.Count < 1 || notice.Count > 1024 {
				return false
			}
			notices[key] = true
			switch notice.Kind {
			case "local_changes", "unpublished_data", "safety_check_unavailable", "unknown_entries":
			case "deletes_local_data", "disposable_data", "other_notice":
				if notice.Expires {
					return false
				}
			default:
				return false
			}
			total += notice.Count
			if notice.BlocksAutomatic {
				blocking += notice.Count
			}
		}
		if total != target.SafetyNoticeCount || blocking != target.AutomaticBlockerCount {
			return false
		}
	}
	if req.Mode == "preview" {
		return v.Status == "preview" && v.Problem == "none" && v.OperationID == "" && v.CompletedTargets == nil && v.Execution == nil && !v.ResourceRefreshPending && expiry.After(time.Now()) && !expiry.After(time.Now().Add(65*time.Second))
	}
	if req.Mode != "apply" || v.PlanID != req.PlanID || v.Status != "returned" || !text(v.OperationID) || v.CompletedTargets == nil || *v.CompletedTargets < 0 || *v.CompletedTargets > len(v.Targets) || !v.ResourceRefreshPending {
		return false
	}
	completed := *v.CompletedTargets
	switch v.Problem {
	case "none":
		if completed != len(v.Targets) {
			return false
		}
	case "native_error":
		if completed == len(v.Targets) {
			return false
		}
	case "cancelled_or_timed_out":
	default:
		return false
	}
	status := "partial"
	if completed == 0 {
		status = "failed"
	} else if completed == len(v.Targets) {
		status = "succeeded"
	}
	return v.Execution == nil || ValidCleanupExecution(*v.Execution, status, completed, len(v.Targets))
}
