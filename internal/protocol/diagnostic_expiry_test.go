package protocol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func expiryContractFixture() (DiagnosticExpiry, DiagnosticMaintenanceRequest) {
	zero := 0
	q := DiagnosticMaintenanceRequest{Schema: DiagnosticsVersion, Operation: "expire", Mode: "preview", Scope: "origin", ParentID: "origin"}
	v := DiagnosticExpiry{Schema: DiagnosticsVersion, Operation: "expire", Mode: "preview", Scope: "origin", FeedbackWorkspaceID: "feedback", ScopeWorkspaceID: "origin", Evidence: "native_exact_workspace_expiry_plan_not_consent", PlanID: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano), Revision: 1, Proposal: DiagnosticExpiryPreview{ID: "origin", RecordID: "record", TaskID: 1, Stage: "expired_cleanup", Targets: []DiagnosticExpiryTarget{{ID: "expiry-target:" + strings.Repeat("b", 64), Provider: "tmux", Effect: "remove_terminal_session_and_processes", SafetyNoticeCount: 1}, {ID: "expiry-target:" + strings.Repeat("c", 64), Provider: "sbx", Effect: "remove_sandbox_and_local_state"}, {ID: "expiry-target:" + strings.Repeat("d", 64), Provider: "workspace", Effect: "remove_workspace_local_files_registration_and_archive_completed_note"}}, ExpiryAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), DataLoss: DiagnosticExpiryDataLoss(), Preserved: []string{"remote_repositories"}, ExecutionEligibility: "unknown_fresh_execution_guards_not_run", OwnershipEvidence: "fresh_bounded_registration", AgentOwnerCount: &zero, LeaseEvidence: "unknown_not_checked"}, Problem: "none", Outcome: "not_attempted", Limitations: DiagnosticExpiryLimitations()}
	return v, q
}
func TestDiagnosticExpiryFiniteContract(t *testing.T) {
	for _, scope := range []string{"origin", "feedback", "installation"} {
		for _, mode := range []string{"preview", "apply"} {
			t.Run(scope+"/"+mode, func(t *testing.T) {
				v, q := expiryContractFixture()
				q.Scope, v.Scope = scope, scope
				if scope == "feedback" {
					v.FeedbackWorkspaceID = q.ParentID
				}
				if scope == "installation" {
					v.ScopeWorkspaceID = ""
				}
				if mode == "apply" {
					q.Mode, v.Mode = mode, mode
					q.PlanID = v.PlanID
					v.OperationID = "operation"
					v.Outcome = "native_bundle_returned_unverified"
					n := len(v.Proposal.Targets)
					v.CompletedTargets = &n
					v.ResourceRefreshPending = true
					v.Execution = &OperationCleanupExecution{Stage: "completed", AttemptedTargets: n}
				}
				if !ValidDiagnosticExpiry(v, q) {
					t.Fatal("valid contract refused", v)
				}
				if mode == "apply" {
					for _, problem := range []string{"native_error", "cancelled_or_timed_out"} {
						for n := 0; n <= len(v.Proposal.Targets); n++ {
							v.Outcome, v.Problem = "unverified_may_be_partial", problem
							v.CompletedTargets = &n
							v.Execution = nil
							if !ValidDiagnosticExpiry(v, q) {
								t.Fatal(problem, n)
							}
						}
					}
				}
			})
		}
	}
	mutations := map[string]func(*DiagnosticExpiry, *DiagnosticMaintenanceRequest){
		"old_request": func(v *DiagnosticExpiry, q *DiagnosticMaintenanceRequest) { q.Schema-- }, "old_reply": func(v *DiagnosticExpiry, q *DiagnosticMaintenanceRequest) { v.Schema-- }, "scope": func(v *DiagnosticExpiry, q *DiagnosticMaintenanceRequest) { v.Scope = "installation" }, "parent": func(v *DiagnosticExpiry, q *DiagnosticMaintenanceRequest) { v.Proposal.ID = "other" }, "plan": func(v *DiagnosticExpiry, q *DiagnosticMaintenanceRequest) { v.PlanID = strings.Repeat("A", 64) }, "expiry": func(v *DiagnosticExpiry, q *DiagnosticMaintenanceRequest) {
			v.ExpiresAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
		}, "extended": func(v *DiagnosticExpiry, q *DiagnosticMaintenanceRequest) {
			v.ExpiresAt = time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339Nano)
		}, "revision": func(v *DiagnosticExpiry, q *DiagnosticMaintenanceRequest) { v.Revision = 9007199254740992 }, "stage": func(v *DiagnosticExpiry, q *DiagnosticMaintenanceRequest) { v.Proposal.Stage = "runtime_retirement" }, "unknown_owners": func(v *DiagnosticExpiry, q *DiagnosticMaintenanceRequest) { v.Proposal.AgentOwnerCount = nil }, "stopped_owner": func(v *DiagnosticExpiry, q *DiagnosticMaintenanceRequest) {
			n := 1
			v.Proposal.AgentOwnerCount = &n
		}, "eligibility": func(v *DiagnosticExpiry, q *DiagnosticMaintenanceRequest) {
			v.Proposal.ExecutionEligibility = "eligible"
		}, "lease": func(v *DiagnosticExpiry, q *DiagnosticMaintenanceRequest) { v.Proposal.LeaseEvidence = "PRIVATE" }, "preserved": func(v *DiagnosticExpiry, q *DiagnosticMaintenanceRequest) { v.Proposal.Preserved = nil }, "no_workspace": func(v *DiagnosticExpiry, q *DiagnosticMaintenanceRequest) {
			v.Proposal.Targets = v.Proposal.Targets[:2]
		}, "empty": func(v *DiagnosticExpiry, q *DiagnosticMaintenanceRequest) { v.Proposal.Targets = nil }, "order": func(v *DiagnosticExpiry, q *DiagnosticMaintenanceRequest) {
			v.Proposal.Targets[0], v.Proposal.Targets[2] = v.Proposal.Targets[2], v.Proposal.Targets[0]
		}, "duplicate_provider": func(v *DiagnosticExpiry, q *DiagnosticMaintenanceRequest) {
			v.Proposal.Targets[1].Provider = "tmux"
		}, "duplicate_id": func(v *DiagnosticExpiry, q *DiagnosticMaintenanceRequest) {
			v.Proposal.Targets[1].ID = v.Proposal.Targets[0].ID
		}, "notice": func(v *DiagnosticExpiry, q *DiagnosticMaintenanceRequest) {
			v.Proposal.Targets[0].SafetyNoticeCount = 1025
		}, "blocker": func(v *DiagnosticExpiry, q *DiagnosticMaintenanceRequest) {
			v.Proposal.Targets[0].AutomaticBlockerCount = 1
		}, "effect": func(v *DiagnosticExpiry, q *DiagnosticMaintenanceRequest) {
			v.Proposal.Targets[0].Effect = "delete_workspace"
		}, "private_limit": func(v *DiagnosticExpiry, q *DiagnosticMaintenanceRequest) { v.Limitations[0] = "PRIVATE" }, "pending": func(v *DiagnosticExpiry, q *DiagnosticMaintenanceRequest) { v.ResourceRefreshPending = true }, "preview_completed": func(v *DiagnosticExpiry, q *DiagnosticMaintenanceRequest) { n := 0; v.CompletedTargets = &n }, "preview_execution": func(v *DiagnosticExpiry, q *DiagnosticMaintenanceRequest) {
			v.Execution = &OperationCleanupExecution{Stage: "admission"}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			v, q := expiryContractFixture()
			mutate(&v, &q)
			if ValidDiagnosticExpiry(v, q) {
				t.Fatal("invalid contract accepted")
			}
		})
	}
	limits := DiagnosticExpiryLimitations()
	if len(limits) != 16 {
		t.Fatal("unexpected limitations", limits)
	}
	for _, s := range limits {
		if strings.Contains(s, "/") {
			t.Fatal("implementation import leaked into disclosure")
		}
	}
	v, _ := expiryContractFixture()
	raw, _ := json.Marshal(v)
	if strings.Contains(string(raw), "PRIVATE") {
		t.Fatal("fixture leaked")
	}
}
