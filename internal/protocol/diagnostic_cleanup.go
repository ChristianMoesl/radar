package protocol

// DiagnosticCleanup is an exact whole-bundle native manual-cleanup disclosure.
// It never contains executable targets, provider messages, commands or paths.
// A plan binds freshness only; the interactive client must separately obtain
// fresh destructive consent for this complete immutable disclosure.
type DiagnosticCleanup struct {
	Schema                 int                        `json:"diagnostics_schema"`
	Operation              string                     `json:"operation"`
	Mode                   string                     `json:"mode"`
	Scope                  string                     `json:"scope"`
	RecordID               string                     `json:"record_id"`
	TaskID                 int                        `json:"task_id"`
	FeedbackWorkspaceID    string                     `json:"feedback_workspace_id"`
	ScopeWorkspaceID       string                     `json:"scope_workspace_id,omitempty"`
	Evidence               string                     `json:"evidence"`
	Status                 string                     `json:"status"`
	Problem                string                     `json:"problem"`
	PlanID                 string                     `json:"plan_id"`
	ExpiresAt              string                     `json:"expires_at"`
	Revision               int64                      `json:"revision"`
	Targets                []DiagnosticCleanupTarget  `json:"targets"`
	OperationID            string                     `json:"operation_id,omitempty"`
	CompletedTargets       *int                       `json:"completed_targets,omitempty"`
	Execution              *OperationCleanupExecution `json:"cleanup_execution,omitempty"`
	ResourceRefreshPending bool                       `json:"resource_refresh_pending"`
	Limitations            []string                   `json:"limitations"`
}

func DiagnosticCleanupLimitations() []string {
	return []string{
		"exact_current_production_task_confirmed_manual_whole_ordered_bundle_no_subset",
		"scope_selects_task_including_related_resources_in_other_workspaces",
		"provider_reads_git_fetch_prune_and_transient_lease_probes_may_have_metadata_effects",
		"proposed_local_changes_branches_sandbox_state_terminal_processes_workspace_files_registration_and_completed_note_may_be_removed",
		"preview_ownership_and_lease_observations_are_not_full_execution_eligibility_or_reservations",
		"latest_60_second_one_use_native_plan_is_freshness_not_consent",
		"apply_requires_fresh_interactive_exact_plan_whole_bundle_destructive_confirmation",
		"native_last_moment_ownership_activity_alias_lease_and_provider_guards_still_apply",
		"30_second_cooperative_deadline_disconnect_is_not_forced_cancellation_or_rollback",
		"returned_prefix_and_dispatch_boundaries_are_not_verified_resource_outcomes",
		"partial_cancelled_or_unknown_outcomes_never_authorize_replay_retry_or_silent_replan",
		"current_daemon_operation_evidence_only_no_pre_restart_history",
		"private_paths_commands_messages_owners_and_native_operations_withheld",
	}
}
