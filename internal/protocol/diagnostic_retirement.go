package protocol

import (
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"time"
)

// DiagnosticRetirement discloses the complete selected native runtime bundle,
// never executable operations. IDs bind freshness, never user consent.
type DiagnosticRetirement struct {
	Schema                 int                         `json:"diagnostics_schema"`
	Operation              string                      `json:"operation"`
	Mode                   string                      `json:"mode"`
	Scope                  string                      `json:"scope"`
	FeedbackWorkspaceID    string                      `json:"feedback_workspace_id"`
	ScopeWorkspaceID       string                      `json:"scope_workspace_id,omitempty"`
	Evidence               string                      `json:"evidence"`
	PlanID                 string                      `json:"plan_id"`
	ExpiresAt              string                      `json:"expires_at"`
	Revision               int64                       `json:"revision"`
	Proposal               DiagnosticRetirementPreview `json:"proposal"`
	Problem                string                      `json:"problem"`
	Outcome                string                      `json:"outcome"`
	OperationID            string                      `json:"operation_id,omitempty"`
	CompletedTargets       *int                        `json:"completed_targets,omitempty"`
	Execution              *OperationCleanupExecution  `json:"cleanup_execution,omitempty"`
	ResourceRefreshPending bool                        `json:"resource_refresh_pending"`
	Limitations            []string                    `json:"limitations"`
}

func DiagnosticRetirementLimitations() []string {
	return []string{
		"exact_registered_workspace_native_runtime_retirement_whole_ordered_bundle_no_subset",
		"preview_native_provider_reads_may_refresh_metadata_not_cleanup_or_execution_admission",
		"workspace_files_worktrees_branches_sandbox_local_state_registration_preserved",
		"sandbox_stop_interrupts_commands_services_terminal_removal_loses_processes_and_contents",
		"safety_notice_counts_not_private_messages_zero_automatic_blockers_not_execution_eligibility",
		"registered_owners_including_stopped_block_issuance_unknown_ownership_refuses",
		"preview_has_no_held_lease_runtime_ownership_or_fresh_authored_lifecycle_reservation",
		"latest_per_task_60_second_one_use_native_plan_is_freshness_not_consent",
		"apply_requires_separate_fresh_interactive_exact_target_whole_plan_confirmation",
		"original_native_retirement_mode_policy_time_and_fresh_lifecycle_resource_provider_guards_remain",
		"30_second_cooperative_deadline_disconnect_not_forced_cancellation_or_rollback",
		"returned_prefix_dispatch_and_runtime_flags_not_independent_resource_outcomes_or_safe_retry",
		"partial_cancelled_failed_or_unknown_outcomes_never_authorize_replay_retry_or_silent_replan",
		"current_daemon_exact_workspace_operation_and_resource_invalidation_not_complete_history",
		"private_paths_runtime_names_messages_operations_and_errors_withheld",
	}
}

func ValidDiagnosticRetirement(v DiagnosticRetirement, q DiagnosticMaintenanceRequest) bool {
	text := func(s string) bool { return s != "" && len(s) <= 512 }
	expiry, err := time.Parse(time.RFC3339Nano, v.ExpiresAt)
	_, hexErr := hex.DecodeString(v.PlanID)
	if q.Schema != DiagnosticsVersion || q.Operation != "retire" || (q.Scope != "origin" && q.Scope != "feedback" && q.Scope != "installation") || !text(q.ParentID) || v.Schema != DiagnosticsVersion || v.Operation != "retire" || v.Mode != q.Mode || v.Scope != q.Scope || !text(v.FeedbackWorkspaceID) || v.Evidence != "native_exact_workspace_retirement_plan_not_consent" || len(v.PlanID) != 64 || v.PlanID != strings.ToLower(v.PlanID) || hexErr != nil || err != nil || expiry.Year() < 1 || len(v.ExpiresAt) > 35 || expiry.UTC().Format(time.RFC3339Nano) != v.ExpiresAt || v.Revision < 0 || v.Revision > 9007199254740991 || !reflect.DeepEqual(v.Limitations, DiagnosticRetirementLimitations()) {
		return false
	}
	if q.Scope == "installation" {
		if v.ScopeWorkspaceID != "" {
			return false
		}
	} else if v.ScopeWorkspaceID != q.ParentID || q.Scope == "feedback" && v.FeedbackWorkspaceID != q.ParentID {
		return false
	}
	p := v.Proposal
	if p.ID != q.ParentID || !text(p.RecordID) || p.TaskID < 1 || int64(p.TaskID) > 9007199254740991 || p.Stage != "runtime_retirement" || len(p.Targets) < 1 || len(p.Targets) > 3 || !reflect.DeepEqual(p.Preserved, []string{"workspace_files", "worktrees_and_branches", "sandbox_and_local_state", "registration"}) || p.ExecutionEligibility != "unknown_fresh_execution_guards_not_run" || p.OwnershipEvidence != "fresh_bounded_registration" || p.AgentOwnerCount == nil || *p.AgentOwnerCount != 0 || p.LeaseEvidence != "unknown_not_checked" {
		return false
	}
	seen := map[string]bool{}
	ids := map[string]bool{}
	for i, t := range p.Targets {
		token := strings.TrimPrefix(t.ID, "retirement-target:")
		_, e := hex.DecodeString(token)
		if token == t.ID || len(token) != 64 || token != strings.ToLower(token) || e != nil || seen[t.Provider] || ids[t.ID] || t.SafetyNoticeCount < 0 || t.SafetyNoticeCount > 1024 || t.AutomaticBlockerCount != 0 {
			return false
		}
		seen[t.Provider] = true
		ids[t.ID] = true
		switch t.Provider {
		case "tmux":
			if t.Effect != "remove_terminal_session_and_processes" {
				return false
			}
		case "sbx":
			if t.Effect != "stop_sandbox_commands_and_services" {
				return false
			}
		case "workspace":
			if t.Effect != "retain_workspace_files_and_registration" || i != len(p.Targets)-1 {
				return false
			}
		default:
			return false
		}
	}
	if !seen["workspace"] {
		return false
	}
	raw, e := json.Marshal(v)
	if e != nil || len(raw) > 16*1024 {
		return false
	}
	if q.Mode == "preview" {
		return q.PlanID == "" && v.Outcome == "not_attempted" && v.Problem == "none" && v.OperationID == "" && v.CompletedTargets == nil && v.Execution == nil && !v.ResourceRefreshPending && expiry.After(time.Now()) && !expiry.After(time.Now().Add(65*time.Second))
	}
	if q.Mode != "apply" || q.PlanID != v.PlanID || !text(v.OperationID) || v.CompletedTargets == nil || *v.CompletedTargets < 0 || *v.CompletedTargets > len(p.Targets) || !v.ResourceRefreshPending {
		return false
	}
	completed := *v.CompletedTargets
	if v.Outcome == "native_bundle_returned_unverified" {
		if v.Problem != "none" || completed != len(p.Targets) {
			return false
		}
	} else if v.Outcome != "unverified_may_be_partial" || (v.Problem != "native_error" && v.Problem != "cancelled_or_timed_out") {
		return false
	}
	status := "failed"
	if completed > 0 {
		status = "partial"
	}
	if completed == len(p.Targets) {
		status = "succeeded"
	}
	return v.Execution == nil || ValidCleanupExecution(*v.Execution, status, completed, len(p.Targets))
}
