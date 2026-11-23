package protocol

import "regexp"

// DiagnosticRecreationPreview is a native proposal, not an executable plan.
// No private recipe, runtime name/ID, provider error or apply token is exposed.
type DiagnosticRecreationPreview struct {
	ID                      string `json:"id"`
	PreviewID               string `json:"preview_id"`
	Status                  string `json:"status"`
	PreviousState           string `json:"previous_state"`
	RuntimeIdentityObserved bool   `json:"runtime_identity_observed"`
	ExecutionEligibility    string `json:"execution_eligibility"`
	OwnershipEvidence       string `json:"ownership_evidence"`
	AgentOwnerCount         *int   `json:"agent_owner_count,omitempty"`
	LeaseEvidence           string `json:"lease_evidence"`
}

var recreationPreviewID = regexp.MustCompile(`^recreation-preview:[a-f0-9]{64}$`)

// ValidDiagnosticRecreationPreview validates the public finite contract, never
// recreation policy. Only the native planner decides recreate/blocked/skipped.
func ValidDiagnosticRecreationPreview(v DiagnosticRecreationPreview, id string) bool {
	if v.ID != id || !recreationPreviewID.MatchString(v.PreviewID) || v.ExecutionEligibility != "unknown_fresh_execution_guards_not_run" || (v.PreviousState != "running" && v.PreviousState != "stopped" && v.PreviousState != "unknown") {
		return false
	}
	if v.OwnershipEvidence == "fresh_bounded_registration" {
		if v.AgentOwnerCount == nil || *v.AgentOwnerCount < 0 || *v.AgentOwnerCount > 1024 {
			return false
		}
	} else if v.OwnershipEvidence != "unavailable" || v.AgentOwnerCount != nil {
		return false
	}
	switch v.LeaseEvidence {
	case "unknown_no_lock_file", "exclusive_available_at_probe", "contended_at_probe", "unavailable":
	default:
		return false
	}
	switch v.Status {
	case "recreate":
		return v.RuntimeIdentityObserved && v.PreviousState != "unknown"
	case "skipped":
		return !v.RuntimeIdentityObserved && v.PreviousState == "unknown"
	case "blocked":
		return v.RuntimeIdentityObserved || v.PreviousState == "unknown"
	}
	return false
}

func DiagnosticRecreationPreviewLimitations() []string {
	return []string{
		"explicit_exact_registered_workspace_native_recreation_proposal_only",
		"native_preflight_runtime_ports_secret_and_policy_metadata_reads",
		"native_local_mount_kit_and_env_file_readability_checks_not_contents",
		"sbx_read_commands_may_auto_start_its_daemon_not_a_sandbox",
		"private_registration_and_recipe_drift_checks_not_atomic_reservation",
		"bounded_registered_owner_counts_not_runtime_ownership_or_holder_identity",
		"stopped_registrations_count_unavailable_owner_evidence_has_no_count",
		"existing_inode_deletion_lease_probes_briefly_lock_release_without_creating_files",
		"lease_observation_not_native_writer_admission_reservation_or_aba_detection",
		"ownership_activity_and_leases_require_fresh_execution_guards",
		"proposed_recreation_discards_sandbox_local_state_and_interrupts_work",
		"native_workspace_files_registration_mounts_ports_and_prior_state_intended_preserved",
		"blocked_reasons_and_private_names_paths_payloads_and_errors_withheld",
		"preview_identity_is_not_native_apply_plan_or_consent",
		"no_recreation_cleanup_login_explicit_start_or_automatic_retry",
		"30_second_cooperative_budget_not_backend_health_or_resource_outcome",
	}
}
