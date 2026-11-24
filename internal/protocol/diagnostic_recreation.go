package protocol

import (
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"time"
)

// DiagnosticRecreation exposes only a finite projection of the existing native
// adapter. PlanID is a one-use freshness identity, NOT the private native plan or
// consent. The native request, recipe, runtime identities and errors stay private.
type DiagnosticRecreation struct {
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
	Proposal               DiagnosticRecreationPreview `json:"proposal"`
	Problem                string                      `json:"problem"`
	Outcome                string                      `json:"outcome"`
	OperationID            string                      `json:"operation_id,omitempty"`
	ResourceRefreshPending bool                        `json:"resource_refresh_pending"`
	Limitations            []string                    `json:"limitations"`
}

func DiagnosticRecreationLimitations() []string {
	return []string{
		"exact_registered_workspace_native_recreation_no_subset",
		"preview_native_preflight_runtime_metadata_recipe_accessibility_and_existing_lease_probes",
		"sbx_reads_may_auto_start_its_daemon_not_a_sandbox",
		"apply_discards_sandbox_local_state_and_interrupts_commands_and_services",
		"native_workspace_files_registration_recorded_mounts_ports_and_prior_state_intended_preserved",
		"registered_owner_and_deletion_lease_observations_not_runtime_ownership_or_native_writer_admission",
		"latest_per_workspace_60_second_one_use_plan_is_freshness_not_consent",
		"apply_requires_separate_fresh_interactive_exact_target_destructive_confirmation",
		"native_full_plan_registration_recipe_runtime_deletion_writer_lifetime_and_restoration_guards_remain",
		"30_second_cooperative_deadline_disconnect_not_forced_cancellation_or_rollback",
		"returned_native_restoration_checks_not_independent_live_resource_verification",
		"partial_cancelled_failed_or_unknown_outcomes_never_authorize_replay_retry_or_silent_replan",
		"current_daemon_workspace_operation_and_resource_invalidation_not_complete_history",
		"private_native_plan_paths_runtime_names_recipes_reasons_and_errors_withheld",
	}
}

// ValidDiagnosticRecreation validates the complete finite public contract and
// request binding, not execution eligibility. Wire decoders also reject missing,
// duplicate, folded, null and extra members. The UI separately matches the exact
// immutable accepted preview and obtains fresh interactive destructive consent.
func ValidDiagnosticRecreation(v DiagnosticRecreation, req DiagnosticMaintenanceRequest) bool {
	text := func(s string) bool { return s != "" && len(s) <= 512 }
	expiry, err := time.Parse(time.RFC3339Nano, v.ExpiresAt)
	_, hexErr := hex.DecodeString(v.PlanID)
	if req.Schema != DiagnosticsVersion || req.Operation != "recreate" || (req.Scope != "origin" && req.Scope != "feedback" && req.Scope != "installation") || !text(req.ParentID) || v.Schema != DiagnosticsVersion || v.Operation != "recreate" || v.Mode != req.Mode || v.Scope != req.Scope || !text(v.FeedbackWorkspaceID) || v.Evidence != "native_exact_workspace_recreation_plan_not_consent" || len(v.PlanID) != 64 || v.PlanID != strings.ToLower(v.PlanID) || hexErr != nil || err != nil || expiry.Year() < 1 || len(v.ExpiresAt) > 35 || expiry.UTC().Format(time.RFC3339Nano) != v.ExpiresAt || v.Revision < 0 || v.Revision > 9007199254740991 || !ValidDiagnosticRecreationPreview(v.Proposal, req.ParentID) || !reflect.DeepEqual(v.Limitations, DiagnosticRecreationLimitations()) {
		return false
	}
	if req.Scope == "installation" {
		if v.ScopeWorkspaceID != "" {
			return false
		}
	} else if v.ScopeWorkspaceID != req.ParentID || req.Scope == "feedback" && v.FeedbackWorkspaceID != req.ParentID {
		return false
	}
	raw, err := json.Marshal(v)
	if err != nil || len(raw) > 16*1024 {
		return false
	}
	if req.Mode == "preview" {
		return req.PlanID == "" && v.Outcome == "not_attempted" && v.Problem == "none" && v.OperationID == "" && !v.ResourceRefreshPending && expiry.After(time.Now()) && !expiry.After(time.Now().Add(65*time.Second))
	}
	if req.Mode != "apply" || req.PlanID != v.PlanID || v.Proposal.Status != "recreate" || !text(v.OperationID) || !v.ResourceRefreshPending {
		return false
	}
	if v.Outcome == "native_restoration_checks_returned" {
		return v.Problem == "none"
	}
	return v.Outcome == "unverified_may_be_partial" && (v.Problem == "native_error" || v.Problem == "invalid_or_incomplete_native_result" || v.Problem == "cancelled_or_timed_out")
}
