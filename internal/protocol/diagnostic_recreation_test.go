package protocol

import (
	"strings"
	"testing"
	"time"
)

func recreationContractFixture() (DiagnosticRecreation, DiagnosticMaintenanceRequest) {
	req := DiagnosticMaintenanceRequest{Schema: DiagnosticsVersion, Operation: "recreate", Mode: "preview", Scope: "origin", ParentID: "origin"}
	return DiagnosticRecreation{Schema: DiagnosticsVersion, Operation: "recreate", Mode: "preview", Scope: "origin", FeedbackWorkspaceID: "feedback", ScopeWorkspaceID: "origin", Evidence: "native_exact_workspace_recreation_plan_not_consent", PlanID: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano), Revision: 1, Proposal: DiagnosticRecreationPreview{ID: "origin", PreviewID: "recreation-preview:" + strings.Repeat("b", 64), Status: "recreate", PreviousState: "running", RuntimeIdentityObserved: true, ExecutionEligibility: "unknown_fresh_execution_guards_not_run", OwnershipEvidence: "unavailable", LeaseEvidence: "unavailable"}, Problem: "none", Outcome: "not_attempted", Limitations: DiagnosticRecreationLimitations()}, req
}
func TestDiagnosticRecreationFiniteContract(t *testing.T) {
	for _, scope := range []string{"origin", "feedback", "installation"} {
		for _, mode := range []string{"preview", "apply"} {
			t.Run(scope+"/"+mode, func(t *testing.T) {
				v, q := recreationContractFixture()
				v.Scope, q.Scope = scope, scope
				if scope == "feedback" {
					v.FeedbackWorkspaceID = "origin"
				}
				if scope == "installation" {
					v.ScopeWorkspaceID = ""
				}
				if mode == "apply" {
					v.Mode, q.Mode = mode, mode
					q.PlanID = v.PlanID
					v.Outcome = "native_restoration_checks_returned"
					v.OperationID = "operation"
					v.ResourceRefreshPending = true
				}
				if !ValidDiagnosticRecreation(v, q) {
					t.Fatal("valid exact native contract refused")
				}
				if mode == "apply" {
					for _, problem := range []string{"native_error", "invalid_or_incomplete_native_result", "cancelled_or_timed_out"} {
						v.Outcome, v.Problem = "unverified_may_be_partial", problem
						if !ValidDiagnosticRecreation(v, q) {
							t.Fatal(problem)
						}
					}
				}
			})
		}
	}
	for name, mutate := range map[string]func(*DiagnosticRecreation, *DiagnosticMaintenanceRequest){
		"old_request": func(v *DiagnosticRecreation, q *DiagnosticMaintenanceRequest) { q.Schema-- }, "old_reply": func(v *DiagnosticRecreation, q *DiagnosticMaintenanceRequest) { v.Schema-- },
		"operation": func(v *DiagnosticRecreation, q *DiagnosticMaintenanceRequest) { v.Operation = "cleanup" }, "mode": func(v *DiagnosticRecreation, q *DiagnosticMaintenanceRequest) { v.Mode = "apply" }, "scope": func(v *DiagnosticRecreation, q *DiagnosticMaintenanceRequest) { v.Scope = "installation" }, "feedback": func(v *DiagnosticRecreation, q *DiagnosticMaintenanceRequest) { v.FeedbackWorkspaceID = "" }, "anchor": func(v *DiagnosticRecreation, q *DiagnosticMaintenanceRequest) { v.ScopeWorkspaceID = "other" }, "target": func(v *DiagnosticRecreation, q *DiagnosticMaintenanceRequest) { v.Proposal.ID = "other" }, "status": func(v *DiagnosticRecreation, q *DiagnosticMaintenanceRequest) { v.Proposal.Status = "healthy" }, "private_lease": func(v *DiagnosticRecreation, q *DiagnosticMaintenanceRequest) { v.Proposal.LeaseEvidence = "PRIVATE" }, "eligibility": func(v *DiagnosticRecreation, q *DiagnosticMaintenanceRequest) {
			v.Proposal.ExecutionEligibility = "eligible"
		}, "upper_plan": func(v *DiagnosticRecreation, q *DiagnosticMaintenanceRequest) { v.PlanID = strings.Repeat("A", 64) }, "display_plan": func(v *DiagnosticRecreation, q *DiagnosticMaintenanceRequest) { v.PlanID = v.Proposal.PreviewID }, "expired": func(v *DiagnosticRecreation, q *DiagnosticMaintenanceRequest) {
			v.ExpiresAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
		}, "extended": func(v *DiagnosticRecreation, q *DiagnosticMaintenanceRequest) {
			v.ExpiresAt = time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339Nano)
		}, "bad_time": func(v *DiagnosticRecreation, q *DiagnosticMaintenanceRequest) { v.ExpiresAt = "2026-02-30T00:00:00Z" }, "negative_revision": func(v *DiagnosticRecreation, q *DiagnosticMaintenanceRequest) { v.Revision = -1 }, "unsafe_revision": func(v *DiagnosticRecreation, q *DiagnosticMaintenanceRequest) { v.Revision = 9007199254740992 }, "pending": func(v *DiagnosticRecreation, q *DiagnosticMaintenanceRequest) { v.ResourceRefreshPending = true }, "preview_operation": func(v *DiagnosticRecreation, q *DiagnosticMaintenanceRequest) { v.OperationID = "operation" }, "preview_outcome": func(v *DiagnosticRecreation, q *DiagnosticMaintenanceRequest) {
			v.Outcome = "native_restoration_checks_returned"
		}, "problem": func(v *DiagnosticRecreation, q *DiagnosticMaintenanceRequest) { v.Problem = "PRIVATE" }, "limitations": func(v *DiagnosticRecreation, q *DiagnosticMaintenanceRequest) { v.Limitations = nil }, "preview_identity": func(v *DiagnosticRecreation, q *DiagnosticMaintenanceRequest) { q.PlanID = v.PlanID },
	} {
		t.Run(name, func(t *testing.T) {
			v, q := recreationContractFixture()
			mutate(&v, &q)
			if ValidDiagnosticRecreation(v, q) {
				t.Fatal("invalid finite contract accepted")
			}
		})
	}
	for _, status := range []string{"blocked", "skipped"} {
		t.Run(status, func(t *testing.T) {
			v, q := recreationContractFixture()
			v.Proposal.Status = status
			v.Proposal.PreviousState = "unknown"
			v.Proposal.RuntimeIdentityObserved = false
			if !ValidDiagnosticRecreation(v, q) {
				t.Fatal("non-executable preview refused")
			}
			q.Mode, v.Mode = "apply", "apply"
			q.PlanID = v.PlanID
			v.Outcome = "native_restoration_checks_returned"
			v.OperationID = "op"
			v.ResourceRefreshPending = true
			if ValidDiagnosticRecreation(v, q) {
				t.Fatal("blocked/skipped apply admitted")
			}
		})
	}
}
