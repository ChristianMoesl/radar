package protocol

// DiagnosticMaintenanceRequest is deliberately separate from read/probe selectors.
// Supports collection refresh, manual bundle guards and exact whole-plan manual cleanup.
// Plans are freshness evidence, never consent or arbitrary execution parameters.
type DiagnosticMaintenanceRequest struct {
	Schema            int    `json:"diagnostics_schema"`
	Operation         string `json:"operation"`
	Mode              string `json:"mode"`
	Scope             string `json:"scope"`
	PlanID            string `json:"plan_id,omitempty"`
	ParentID          string `json:"parent_id,omitempty"`
	Workspace         string `json:"workspace,omitempty"`          // CLI-resolved registered scope, never a target path.
	FeedbackWorkspace string `json:"feedback_workspace,omitempty"` // Exact registered feedback caller for manual cleanup.
}

type DiagnosticRefreshTarget struct {
	ID      string `json:"id"`
	Outcome string `json:"outcome"`
}

type DiagnosticRefreshCounts struct {
	Complete   int `json:"complete"`
	Incomplete int `json:"incomplete"`
	Skipped    int `json:"skipped"`
	Unverified int `json:"unverified"`
}

// Returned counts describe native collection attempts, not reconciled resource
// outcomes. No provider errors, task/note data or executable payloads escape.
type DiagnosticMaintenance struct {
	Schema                 int                       `json:"diagnostics_schema"`
	Operation              string                    `json:"operation"`
	Mode                   string                    `json:"mode"`
	Scope                  string                    `json:"scope"`
	Evidence               string                    `json:"evidence"`
	Status                 string                    `json:"status"`
	Problem                string                    `json:"problem"`
	PlanID                 string                    `json:"plan_id"`
	ExpiresAt              string                    `json:"expires_at"`
	Revision               int64                     `json:"revision"`
	OperationID            string                    `json:"operation_id,omitempty"`
	Targets                []DiagnosticRefreshTarget `json:"targets"`
	Counts                 DiagnosticRefreshCounts   `json:"counts"`
	ResourceRefreshPending bool                      `json:"resource_refresh_pending"`
	Limitations            []string                  `json:"limitations"`
}

func DiagnosticRefreshLimitations() []string {
	return []string{
		"installation_wide_all_registered_collection_sources_no_subset",
		"native_collection_reconciliation_may_update_notes_bindings_lifecycle_and_deliver_events",
		"provider_reads_may_use_existing_authentication_and_refresh_git_metadata",
		"no_workspace_gc_cleanup_login_setup_or_runtime_start_requested",
		"latest_preview_only_expires_after_60_seconds_or_revision_drift",
		"plan_identity_is_freshness_not_user_consent",
		"apply_requires_fresh_exact_installation_target_confirmation",
		"consumed_plan_never_reexecutes_even_after_disconnect",
		"30_second_cooperative_native_deadline_not_rollback",
		"client_cancellation_after_dispatch_may_leave_partial_changes",
		"outcomes_are_returned_collection_attempts_not_reconciliation_or_resource_verification",
		"missing_or_cancelled_attempts_unverified_not_success_or_absence",
		"resource_refresh_pending_is_separate_from_remote_provider_success",
		"no_pre_restart_history_or_automatic_retry",
		"raw_errors_paths_notes_and_provider_payloads_withheld",
	}
}
