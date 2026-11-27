package protocol

import (
	"encoding/hex"
	"encoding/json"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DiagnosticAgentStop is a finite native-handler disclosure, never a private
// registration, executable plan, user-consent record or process-outcome claim.
// Trusted adapters must separately obtain fresh exact-target interruption consent.
// Captured running/desired facts are explicitly before execution.
type DiagnosticAgentStop struct {
	Schema                 int      `json:"diagnostics_schema"`
	Operation              string   `json:"operation"`
	Mode                   string   `json:"mode"`
	Scope                  string   `json:"scope"`
	RecordID               string   `json:"record_id"`
	TaskID                 int      `json:"task_id"`
	AgentID                string   `json:"agent_id"`
	FeedbackWorkspaceID    string   `json:"feedback_workspace_id"`
	ScopeWorkspaceID       string   `json:"scope_workspace_id,omitempty"`
	AgentWorkspaceID       string   `json:"agent_workspace_id"`
	Evidence               string   `json:"evidence"`
	PlanID                 string   `json:"plan_id"`
	ExpiresAt              string   `json:"expires_at"`
	Revision               int64    `json:"revision"`
	DesiredBefore          bool     `json:"desired_before"`
	RunningBefore          bool     `json:"running_before"`
	DispatchEvidence       string   `json:"dispatch_evidence"`
	Problem                string   `json:"problem"`
	Outcome                string   `json:"outcome"`
	OperationID            string   `json:"operation_id,omitempty"`
	ResourceRefreshPending bool     `json:"resource_refresh_pending"`
	Limitations            []string `json:"limitations"`
}

func DiagnosticAgentStopLimitations() []string {
	return []string{
		"one_exact_current_task_agent_ref_and_registered_feedback_scope_no_subset",
		"preview_reads_native_registration_workspace_and_runner_owner_evidence_without_startup_or_shutdown",
		"running_and_desired_before_are_capture_facts_not_post_apply_state",
		"apply_sets_desired_false_and_uses_existing_native_shutdown_wait_may_interrupt_work",
		"stopped_registration_still_pins_workspace_data_sandbox_and_ownership_preserved",
		"latest_per_task_shared_maintenance_plan_one_use_original_native_30_second_expiry",
		"plan_identity_is_freshness_not_user_consent",
		"trusted_caller_requires_separate_fresh_interactive_exact_target_interruption_consent",
		"captured_final_guard_rechecks_scope_revision_publication_and_native_identity_before_effects",
		"point_in_time_guards_not_atomic_filesystem_or_collection_transactions_or_peer_pid_attestation",
		"desired_state_write_may_precede_failure_cancellation_or_lost_reply_no_rollback_or_safe_retry",
		"native_callback_and_handler_return_not_completed_work_process_exit_lease_release_or_resource_absence",
		"resource_refresh_pending_requires_independent_recollection_not_verified_resource_outcomes",
		"no_automatic_replay_retry_silent_replan_start_restart_remove_or_sandbox_stop",
		"current_daemon_managed_handler_evidence_not_complete_or_durable_history",
		"private_paths_pids_runtime_names_checkpoints_tokens_errors_and_provider_payloads_withheld",
	}
}

var diagnosticAgentStopID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func ValidDiagnosticAgentStop(v DiagnosticAgentStop, q DiagnosticMaintenanceRequest) bool {
	text := func(s string) bool { return s != "" && len(s) <= 512 && DiagnosticText(s) == s }
	expires, err := time.Parse(time.RFC3339Nano, v.ExpiresAt)
	_, hexErr := hex.DecodeString(v.PlanID)
	if q.Schema != DiagnosticsVersion || q.Operation != "agent_stop" || !diagnosticAgentStopID.MatchString(q.AgentID) || (q.Scope != "origin" && q.Scope != "feedback" && q.Scope != "installation") || v.Schema != DiagnosticsVersion || v.Operation != "agent_stop" || v.Mode != q.Mode || v.Scope != q.Scope || !text(v.RecordID) || v.TaskID < 1 || int64(v.TaskID) > 9007199254740991 || (q.ParentID != v.RecordID && q.ParentID != strconv.Itoa(v.TaskID)) || v.AgentID != q.AgentID || !text(v.FeedbackWorkspaceID) || !text(v.AgentWorkspaceID) || v.Evidence != "native_exact_agent_stop_plan_not_consent" || len(v.PlanID) != 64 || v.PlanID != strings.ToLower(v.PlanID) || hexErr != nil || err != nil || expires.Year() < 1 || len(v.ExpiresAt) > 35 || expires.UTC().Format(time.RFC3339Nano) != v.ExpiresAt || v.Revision < 0 || v.Revision > 9007199254740991 || !reflect.DeepEqual(v.Limitations, DiagnosticAgentStopLimitations()) {
		return false
	}
	if q.Scope == "installation" {
		if v.ScopeWorkspaceID != "" {
			return false
		}
	} else if !text(v.ScopeWorkspaceID) || v.ScopeWorkspaceID != v.AgentWorkspaceID || q.Scope == "feedback" && v.FeedbackWorkspaceID != v.ScopeWorkspaceID {
		return false
	}
	raw, err := json.Marshal(v)
	if err != nil || len(raw) > 32*1024 {
		return false
	}
	if q.Mode == "preview" {
		return q.PlanID == "" && v.Outcome == "not_attempted" && v.Problem == "none" && v.DispatchEvidence == "not_dispatched" && v.OperationID == "" && !v.ResourceRefreshPending && expires.After(time.Now()) && !expires.After(time.Now().Add(35*time.Second))
	}
	if q.Mode != "apply" || q.PlanID != v.PlanID || !text(v.OperationID) || !v.ResourceRefreshPending || v.DispatchEvidence != "native_stop_callback_entered_not_process_outcome" {
		return false
	}
	if v.Outcome == "native_handler_returned_unverified" {
		return v.Problem == "none"
	}
	return v.Outcome == "unverified_may_be_partial" && (v.Problem == "native_error" || v.Problem == "cancelled_or_timed_out")
}
