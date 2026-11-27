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

// DiagnosticAgentRemove is a finite stopped-registration removal disclosure, not
// a private registration, executable plan, consent or whole-resource outcome.
// Trusted callers must obtain separate fresh exact-target removal consent. Counts
// describe captured schedules, never disabled schedules or post-apply ownership.
type DiagnosticAgentRemove struct {
	Schema                 int                              `json:"diagnostics_schema"`
	Operation              string                           `json:"operation"`
	Mode                   string                           `json:"mode"`
	Scope                  string                           `json:"scope"`
	RecordID               string                           `json:"record_id"`
	TaskID                 int                              `json:"task_id"`
	AgentID                string                           `json:"agent_id"`
	FeedbackWorkspaceID    string                           `json:"feedback_workspace_id"`
	ScopeWorkspaceID       string                           `json:"scope_workspace_id,omitempty"`
	AgentWorkspaceID       string                           `json:"agent_workspace_id"`
	Evidence               string                           `json:"evidence"`
	PlanID                 string                           `json:"plan_id"`
	ExpiresAt              string                           `json:"expires_at"`
	Revision               int64                            `json:"revision"`
	SchedulesBefore        int                              `json:"schedules_before"`
	EnabledSchedulesBefore int                              `json:"enabled_schedules_before"`
	DispatchEvidence       string                           `json:"dispatch_evidence"`
	Problem                string                           `json:"problem"`
	Outcome                string                           `json:"outcome"`
	OperationID            string                           `json:"operation_id,omitempty"`
	ResourceRefreshPending bool                             `json:"resource_refresh_pending"`
	Observation            DiagnosticAgentRemoveObservation `json:"observation"`
	Limitations            []string                         `json:"limitations"`
}

// Finite checkpoint evidence independent of handler status. No private hashes,
// paths or checkpoint contents; unknown is never inferred from handler return.
type DiagnosticAgentRemoveObservation struct {
	Evidence     string `json:"evidence"`
	Registration string `json:"registration"`
	Schedules    string `json:"schedules"`
	ObservedAt   string `json:"observed_at,omitempty"`
}

func UnknownDiagnosticAgentRemoveObservation() DiagnosticAgentRemoveObservation {
	return DiagnosticAgentRemoveObservation{Evidence: "unknown_not_observed", Registration: "unknown", Schedules: "unknown"}
}

func validDiagnosticAgentRemoveObservation(v DiagnosticAgentRemove) bool {
	o := v.Observation
	if o.Evidence == "unknown_not_observed" {
		return o == UnknownDiagnosticAgentRemoveObservation()
	}
	if v.Mode != "apply" || v.Problem == "cancelled_or_timed_out" || o.Evidence != "bounded_native_checkpoint_reads" ||
		(o.Registration != "unknown" && o.Registration != "original_present" && o.Registration != "absent") ||
		(o.Schedules != "unknown" && o.Schedules != "paused_or_empty_preserved" && o.Schedules != "original_preserved") ||
		(o.Schedules == "original_preserved" && (v.SchedulesBefore == 0 || v.EnabledSchedulesBefore == 0)) {
		return false
	}
	at, err := time.Parse(time.RFC3339Nano, o.ObservedAt)
	expires, expiryErr := time.Parse(time.RFC3339Nano, v.ExpiresAt)
	return err == nil && expiryErr == nil && at.Year() >= 1 && len(o.ObservedAt) <= 35 && at.UTC().Format(time.RFC3339Nano) == o.ObservedAt &&
		!at.Before(expires.Add(-30*time.Second)) && !at.After(expires) && !at.After(time.Now())
}

func DiagnosticAgentRemoveLimitations() []string {
	return []string{
		"one_exact_current_task_agent_ref_and_registered_feedback_scope_no_subset",
		"preview_reads_stopped_registration_workspace_schedules_and_independent_owner_evidence_without_startup",
		"schedule_counts_are_capture_facts_not_disabled_or_post_apply_state",
		"apply_pauses_retained_schedules_then_unlinks_registration_releasing_only_this_workspace_association",
		"durable_data_ownership_intent_workspace_sandbox_and_canonical_note_preserved",
		"other_owners_activity_leases_and_separate_cleanup_expiry_policy_still_apply",
		"latest_per_task_shared_maintenance_plan_one_use_original_native_30_second_expiry",
		"plan_identity_is_freshness_not_user_consent",
		"trusted_caller_requires_separate_fresh_interactive_exact_target_registration_removal_consent",
		"captured_final_guard_rechecks_scope_revision_publication_and_native_identity_before_both_effects",
		"point_in_time_guards_not_atomic_filesystem_or_collection_transactions_or_peer_pid_attestation",
		"schedule_pause_may_precede_unlink_failure_cancellation_or_lost_reply_no_rollback_or_safe_retry",
		"native_callback_and_handler_return_not_resource_absence_cleanup_eligibility_or_independent_outcomes",
		"observation_independently_rereads_only_exact_native_registration_and_complete_retained_schedule_state",
		"original_directory_and_leaf_identity_checks_point_in_time_not_atomic_or_later_freshness",
		"unknown_observations_never_inferred_from_handler_success_or_capture_counts",
		"observed_registration_absence_not_runner_workspace_sandbox_absence_other_owner_release_or_safe_retry",
		"resource_refresh_pending_requires_independent_recollection_not_verified_resource_outcomes",
		"no_automatic_replay_retry_silent_replan_start_stop_restart_cleanup_or_sandbox_deletion",
		"current_daemon_managed_handler_evidence_not_complete_or_durable_history",
		"private_paths_pids_runtime_names_schedule_names_payloads_checkpoints_tokens_and_errors_withheld",
	}
}

var diagnosticAgentRemoveID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func ValidDiagnosticAgentRemove(v DiagnosticAgentRemove, q DiagnosticMaintenanceRequest) bool {
	text := func(s string) bool { return s != "" && len(s) <= 512 && DiagnosticText(s) == s }
	expires, err := time.Parse(time.RFC3339Nano, v.ExpiresAt)
	_, hexErr := hex.DecodeString(v.PlanID)
	if q.Schema != DiagnosticsVersion || q.Operation != "agent_remove" || !diagnosticAgentRemoveID.MatchString(q.AgentID) || (q.Scope != "origin" && q.Scope != "feedback" && q.Scope != "installation") || v.Schema != DiagnosticsVersion || v.Operation != "agent_remove" || v.Mode != q.Mode || v.Scope != q.Scope || !text(v.RecordID) || v.TaskID < 1 || int64(v.TaskID) > 9007199254740991 || (q.ParentID != v.RecordID && q.ParentID != strconv.Itoa(v.TaskID)) || v.AgentID != q.AgentID || !text(v.FeedbackWorkspaceID) || !text(v.AgentWorkspaceID) || v.Evidence != "native_exact_agent_removal_plan_not_consent" || len(v.PlanID) != 64 || v.PlanID != strings.ToLower(v.PlanID) || hexErr != nil || err != nil || expires.Year() < 1 || len(v.ExpiresAt) > 35 || expires.UTC().Format(time.RFC3339Nano) != v.ExpiresAt || v.Revision < 0 || v.Revision > 9007199254740991 || v.SchedulesBefore < 0 || v.SchedulesBefore > AgentMaxFrameBytes || v.EnabledSchedulesBefore < 0 || v.EnabledSchedulesBefore > v.SchedulesBefore || !reflect.DeepEqual(v.Limitations, DiagnosticAgentRemoveLimitations()) {
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
	if err != nil || len(raw) > 32*1024 || !validDiagnosticAgentRemoveObservation(v) {
		return false
	}
	if q.Mode == "preview" {
		return q.PlanID == "" && v.Outcome == "not_attempted" && v.Problem == "none" && v.DispatchEvidence == "not_dispatched" && v.OperationID == "" && !v.ResourceRefreshPending && expires.After(time.Now()) && !expires.After(time.Now().Add(35*time.Second))
	}
	if q.Mode != "apply" || q.PlanID != v.PlanID || !text(v.OperationID) || !v.ResourceRefreshPending || v.DispatchEvidence != "native_remove_callback_entered_not_resource_outcome" {
		return false
	}
	if v.Outcome == "native_handler_returned_unverified" {
		return v.Problem == "none"
	}
	return v.Outcome == "unverified_may_be_partial" && (v.Problem == "native_error" || v.Problem == "cancelled_or_timed_out")
}
