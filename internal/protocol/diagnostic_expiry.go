package protocol

import (
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"time"
)

// DiagnosticExpiry discloses the complete selected native destructive workspace bundle,
// never executable operations. IDs bind freshness, never user consent.
type DiagnosticExpiry struct {
	Schema                 int                        `json:"diagnostics_schema"`
	Operation              string                     `json:"operation"`
	Mode                   string                     `json:"mode"`
	Scope                  string                     `json:"scope"`
	FeedbackWorkspaceID    string                     `json:"feedback_workspace_id"`
	ScopeWorkspaceID       string                     `json:"scope_workspace_id,omitempty"`
	Evidence               string                     `json:"evidence"`
	PlanID                 string                     `json:"plan_id"`
	ExpiresAt              string                     `json:"expires_at"`
	Revision               int64                      `json:"revision"`
	Proposal               DiagnosticExpiryPreview    `json:"proposal"`
	Problem                string                     `json:"problem"`
	Outcome                string                     `json:"outcome"`
	OperationID            string                     `json:"operation_id,omitempty"`
	CompletedTargets       *int                       `json:"completed_targets,omitempty"`
	Execution              *OperationCleanupExecution `json:"cleanup_execution,omitempty"`
	ResourceRefreshPending bool                       `json:"resource_refresh_pending"`
	Limitations            []string                   `json:"limitations"`
}

func DiagnosticExpiryLimitations() []string {
	return []string{
		"exact_registered_seven_day_native_expiry_whole_ordered_bundle_no_subset",
		"preview_native_provider_reads_may_refresh_metadata_not_cleanup_or_execution_admission",
		"destructive_expiry_discards_workspace_local_changes_unknown_files_sandbox_state_and_eligible_local_branches",
		"workspace_registration_removed_completed_canonical_note_may_be_archived_remote_repositories_preserved",
		"expiring_data_warnings_do_not_bypass_structural_ownership_activity_or_lease_guards",
		"safety_notice_counts_not_private_messages_zero_automatic_blockers_not_execution_eligibility",
		"registered_owners_including_stopped_block_issuance_unknown_ownership_refuses",
		"preview_has_no_held_lease_runtime_ownership_or_fresh_authored_lifecycle_reservation",
		"latest_per_task_60_second_one_use_native_plan_is_freshness_not_consent",
		"trusted_caller_must_obtain_separate_fresh_exact_target_whole_plan_destructive_consent",
		"original_native_expiry_mode_policy_time_and_fresh_lifecycle_resource_provider_guards_remain",
		"30_second_cooperative_deadline_disconnect_not_forced_cancellation_or_rollback",
		"returned_prefix_dispatch_and_runtime_flags_not_independent_resource_outcomes_or_safe_retry",
		"partial_cancelled_failed_or_unknown_outcomes_never_authorize_replay_retry_or_silent_replan",
		"current_daemon_exact_workspace_operation_and_resource_invalidation_not_complete_history",
		"private_paths_runtime_names_branches_messages_operations_and_errors_withheld",
	}
}

func ValidDiagnosticExpiry(v DiagnosticExpiry, q DiagnosticMaintenanceRequest) bool {
	text := func(s string) bool { return s != "" && len(s) <= 512 }
	expiry, err := time.Parse(time.RFC3339Nano, v.ExpiresAt)
	_, hexErr := hex.DecodeString(v.PlanID)
	if q.Schema != DiagnosticsVersion || q.Operation != "expire" || (q.Scope != "origin" && q.Scope != "feedback" && q.Scope != "installation") || !text(q.ParentID) || v.Schema != DiagnosticsVersion || v.Operation != "expire" || v.Mode != q.Mode || v.Scope != q.Scope || !text(v.FeedbackWorkspaceID) || v.Evidence != "native_exact_workspace_expiry_plan_not_consent" || len(v.PlanID) != 64 || v.PlanID != strings.ToLower(v.PlanID) || hexErr != nil || err != nil || expiry.Year() < 1 || len(v.ExpiresAt) > 35 || expiry.UTC().Format(time.RFC3339Nano) != v.ExpiresAt || v.Revision < 0 || v.Revision > 9007199254740991 || !reflect.DeepEqual(v.Limitations, DiagnosticExpiryLimitations()) {
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
	if p.ID != q.ParentID || !text(p.RecordID) || p.TaskID < 1 || int64(p.TaskID) > 9007199254740991 || p.Stage != "expired_cleanup" || !validExpiryDeadline(p.ExpiryAt) || !reflect.DeepEqual(p.DataLoss, DiagnosticExpiryDataLoss()) || len(p.Targets) < 1 || len(p.Targets) > 512 || !reflect.DeepEqual(p.Preserved, []string{"remote_repositories"}) || p.ExecutionEligibility != "unknown_fresh_execution_guards_not_run" || p.OwnershipEvidence != "fresh_bounded_registration" || p.AgentOwnerCount == nil || *p.AgentOwnerCount != 0 || p.LeaseEvidence != "unknown_not_checked" {
		return false
	}
	seen := map[string]bool{}
	ids := map[string]bool{}
	lastOrder := -1
	for i, t := range p.Targets {
		order, known := map[string]int{"tmux": 0, "sbx": 1, "git": 2, "workspace": 3}[t.Provider]
		if !known || order < lastOrder {
			return false
		}
		lastOrder = order
		token := strings.TrimPrefix(t.ID, "expiry-target:")
		_, e := hex.DecodeString(token)
		if token == t.ID || len(token) != 64 || token != strings.ToLower(token) || e != nil || ids[t.ID] || t.SafetyNoticeCount < 0 || t.SafetyNoticeCount > 1024 || t.AutomaticBlockerCount != 0 {
			return false
		}
		if t.Provider != "git" && t.Effect != "forget_missing_worktree_registration" {
			if seen[t.Provider] {
				return false
			}
			seen[t.Provider] = true
		}
		ids[t.ID] = true
		switch t.Provider {
		case "tmux":
			if t.Effect != "remove_terminal_session_and_processes" {
				return false
			}
		case "sbx":
			if t.Effect != "remove_sandbox_and_local_state" {
				return false
			}
		case "workspace":
			if t.Effect == "forget_missing_worktree_registration" {
				if t.BranchEffect != "retain_local_branch" {
					return false
				}
				continue
			}
			if t.Effect != "remove_workspace_local_files_registration_and_archive_completed_note" || i != len(p.Targets)-1 {
				return false
			}
		case "git":
			if t.Effect != "remove_worktree_and_local_changes" || (t.BranchEffect != "retain_local_branch" && t.BranchEffect != "delete_local_branch") {
				return false
			}
		default:
			return false
		}
		if t.Provider != "git" && t.BranchEffect != "" {
			return false
		}
	}
	if !seen["workspace"] {
		return false
	}
	raw, e := json.Marshal(v)
	if e != nil || len(raw) > 32*1024 {
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

// Finite disclosure only, not an executable candidate or accepted consent.
type DiagnosticExpiryPreview struct {
	ID                   string                   `json:"id"`
	RecordID             string                   `json:"record_id"`
	TaskID               int                      `json:"task_id"`
	Stage                string                   `json:"stage"`
	ExpiryAt             string                   `json:"expiry_at"`
	Targets              []DiagnosticExpiryTarget `json:"targets"`
	Preserved            []string                 `json:"preserved"`
	DataLoss             []string                 `json:"data_loss"`
	ExecutionEligibility string                   `json:"execution_eligibility"`
	OwnershipEvidence    string                   `json:"ownership_evidence"`
	AgentOwnerCount      *int                     `json:"agent_owner_count,omitempty"`
	LeaseEvidence        string                   `json:"lease_evidence"`
}
type DiagnosticExpiryTarget struct {
	ID                    string `json:"id"`
	Provider              string `json:"provider"`
	Effect                string `json:"effect"`
	BranchEffect          string `json:"branch_effect,omitempty"`
	SafetyNoticeCount     int    `json:"safety_notice_count"`
	AutomaticBlockerCount int    `json:"automatic_blocker_count"`
}

func DiagnosticExpiryDataLoss() []string {
	return []string{
		"workspace_local_changes_and_unknown_files",
		"sandbox_local_state_and_runtime_processes",
		"eligible_local_branches_including_unpublished_commits",
		"workspace_registration",
	}
}
func validExpiryDeadline(value string) bool {
	t, e := time.Parse(time.RFC3339Nano, value)
	return e == nil && len(value) <= 35 && t.Year() > 0 && t.UTC().Format(time.RFC3339Nano) == value && !t.After(time.Now())
}
