package protocol

// DiagnosticCleanupCheck describes only shared guard return, never complete
// execution eligibility, a held reservation or an independently verified effect.
type DiagnosticCleanupCheck struct {
	Schema                 int      `json:"diagnostics_schema"`
	Operation              string   `json:"operation"`
	Mode                   string   `json:"mode"`
	Scope                  string   `json:"scope"`
	RecordID               string   `json:"record_id"`
	TaskID                 int      `json:"task_id"`
	Evidence               string   `json:"evidence"`
	Status                 string   `json:"status"`
	TargetCount            int      `json:"target_count"`
	Revision               int64    `json:"revision"`
	ObservedAt             string   `json:"observed_at"`
	ExecutionEligibility   string   `json:"execution_eligibility"`
	CleanupDispatched      bool     `json:"cleanup_dispatched"`
	ResourceRefreshPending bool     `json:"resource_refresh_pending"`
	Limitations            []string `json:"limitations"`
}

func DiagnosticCleanupCheckLimitations() []string {
	return []string{
		"explicit_exact_retained_task_manual_whole_bundle_only",
		"scope_selects_task_including_related_resources_in_other_workspaces",
		"native_provider_reads_and_git_fetch_prune_may_refresh_metadata",
		"native_admission_may_create_coordination_files_directories_and_transiently_contend",
		"native_runtime_admission_and_protected_tree_checks_may_run",
		"all_acquired_admission_released_before_return_not_a_reservation",
		"passed_is_shared_bundle_guard_return_not_last_moment_provider_eligibility",
		"runtime_presence_lifetime_and_resource_outcomes_remain_unknown",
		"no_provider_cleanup_dispatch_apply_token_or_deletion_consent",
		"30_second_cooperative_deadline_disconnect_is_not_metadata_rollback",
		"no_automatic_retry_replay_or_pre_restart_history",
		"private_targets_paths_commands_owners_and_errors_withheld",
	}
}
