package protocol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func retirementContractFixture() (DiagnosticRetirement, DiagnosticMaintenanceRequest) {
	zero := 0
	q := DiagnosticMaintenanceRequest{Schema: DiagnosticsVersion, Operation: "retire", Mode: "preview", Scope: "origin", ParentID: "origin"}
	v := DiagnosticRetirement{Schema: DiagnosticsVersion, Operation: "retire", Mode: "preview", Scope: "origin", FeedbackWorkspaceID: "feedback", ScopeWorkspaceID: "origin", Evidence: "native_exact_workspace_retirement_plan_not_consent", PlanID: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano), Revision: 1, Proposal: DiagnosticRetirementPreview{ID: "origin", RecordID: "record", TaskID: 1, Stage: "runtime_retirement", Targets: []DiagnosticRetirementTarget{{ID: "retirement-target:" + strings.Repeat("b", 64), Provider: "tmux", Effect: "remove_terminal_session_and_processes", SafetyNoticeCount: 1}, {ID: "retirement-target:" + strings.Repeat("c", 64), Provider: "sbx", Effect: "stop_sandbox_commands_and_services"}, {ID: "retirement-target:" + strings.Repeat("d", 64), Provider: "workspace", Effect: "retain_workspace_files_and_registration"}}, Preserved: []string{"workspace_files", "worktrees_and_branches", "sandbox_and_local_state", "registration"}, ExecutionEligibility: "unknown_fresh_execution_guards_not_run", OwnershipEvidence: "fresh_bounded_registration", AgentOwnerCount: &zero, LeaseEvidence: "unknown_not_checked"}, Problem: "none", Outcome: "not_attempted", Limitations: DiagnosticRetirementLimitations()}
	return v, q
}
func TestDiagnosticRetirementFiniteContract(t *testing.T) {
	for _, scope := range []string{"origin", "feedback", "installation"} {
		for _, mode := range []string{"preview", "apply"} {
			t.Run(scope+"/"+mode, func(t *testing.T) {
				v, q := retirementContractFixture()
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
				if !ValidDiagnosticRetirement(v, q) {
					t.Fatal("valid contract refused", v)
				}
				if mode == "apply" {
					for _, problem := range []string{"native_error", "cancelled_or_timed_out"} {
						for n := 0; n <= len(v.Proposal.Targets); n++ {
							v.Outcome, v.Problem = "unverified_may_be_partial", problem
							v.CompletedTargets = &n
							v.Execution = nil
							if !ValidDiagnosticRetirement(v, q) {
								t.Fatal(problem, n)
							}
						}
					}
				}
			})
		}
	}
	mutations := map[string]func(*DiagnosticRetirement, *DiagnosticMaintenanceRequest){
		"old_request": func(v *DiagnosticRetirement, q *DiagnosticMaintenanceRequest) { q.Schema-- }, "old_reply": func(v *DiagnosticRetirement, q *DiagnosticMaintenanceRequest) { v.Schema-- }, "scope": func(v *DiagnosticRetirement, q *DiagnosticMaintenanceRequest) { v.Scope = "installation" }, "parent": func(v *DiagnosticRetirement, q *DiagnosticMaintenanceRequest) { v.Proposal.ID = "other" }, "plan": func(v *DiagnosticRetirement, q *DiagnosticMaintenanceRequest) { v.PlanID = strings.Repeat("A", 64) }, "expiry": func(v *DiagnosticRetirement, q *DiagnosticMaintenanceRequest) {
			v.ExpiresAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
		}, "extended": func(v *DiagnosticRetirement, q *DiagnosticMaintenanceRequest) {
			v.ExpiresAt = time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339Nano)
		}, "revision": func(v *DiagnosticRetirement, q *DiagnosticMaintenanceRequest) { v.Revision = 9007199254740992 }, "stage": func(v *DiagnosticRetirement, q *DiagnosticMaintenanceRequest) { v.Proposal.Stage = "expired_cleanup" }, "unknown_owners": func(v *DiagnosticRetirement, q *DiagnosticMaintenanceRequest) { v.Proposal.AgentOwnerCount = nil }, "stopped_owner": func(v *DiagnosticRetirement, q *DiagnosticMaintenanceRequest) {
			n := 1
			v.Proposal.AgentOwnerCount = &n
		}, "eligibility": func(v *DiagnosticRetirement, q *DiagnosticMaintenanceRequest) {
			v.Proposal.ExecutionEligibility = "eligible"
		}, "lease": func(v *DiagnosticRetirement, q *DiagnosticMaintenanceRequest) { v.Proposal.LeaseEvidence = "PRIVATE" }, "preserved": func(v *DiagnosticRetirement, q *DiagnosticMaintenanceRequest) { v.Proposal.Preserved = nil }, "no_workspace": func(v *DiagnosticRetirement, q *DiagnosticMaintenanceRequest) {
			v.Proposal.Targets = v.Proposal.Targets[:2]
		}, "empty": func(v *DiagnosticRetirement, q *DiagnosticMaintenanceRequest) { v.Proposal.Targets = nil }, "order": func(v *DiagnosticRetirement, q *DiagnosticMaintenanceRequest) {
			v.Proposal.Targets[0], v.Proposal.Targets[2] = v.Proposal.Targets[2], v.Proposal.Targets[0]
		}, "duplicate_provider": func(v *DiagnosticRetirement, q *DiagnosticMaintenanceRequest) {
			v.Proposal.Targets[1].Provider = "tmux"
		}, "duplicate_id": func(v *DiagnosticRetirement, q *DiagnosticMaintenanceRequest) {
			v.Proposal.Targets[1].ID = v.Proposal.Targets[0].ID
		}, "notice": func(v *DiagnosticRetirement, q *DiagnosticMaintenanceRequest) {
			v.Proposal.Targets[0].SafetyNoticeCount = 1025
		}, "blocker": func(v *DiagnosticRetirement, q *DiagnosticMaintenanceRequest) {
			v.Proposal.Targets[0].AutomaticBlockerCount = 1
		}, "effect": func(v *DiagnosticRetirement, q *DiagnosticMaintenanceRequest) {
			v.Proposal.Targets[0].Effect = "delete_workspace"
		}, "private_limit": func(v *DiagnosticRetirement, q *DiagnosticMaintenanceRequest) { v.Limitations[0] = "PRIVATE" }, "pending": func(v *DiagnosticRetirement, q *DiagnosticMaintenanceRequest) { v.ResourceRefreshPending = true }, "preview_completed": func(v *DiagnosticRetirement, q *DiagnosticMaintenanceRequest) { n := 0; v.CompletedTargets = &n }, "preview_execution": func(v *DiagnosticRetirement, q *DiagnosticMaintenanceRequest) {
			v.Execution = &OperationCleanupExecution{Stage: "admission"}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			v, q := retirementContractFixture()
			mutate(&v, &q)
			if ValidDiagnosticRetirement(v, q) {
				t.Fatal("invalid contract accepted")
			}
		})
	}
	limits := DiagnosticRetirementLimitations()
	if len(limits) != 15 {
		t.Fatal("unexpected limitations", limits)
	}
	for _, s := range limits {
		if strings.Contains(s, "/") {
			t.Fatal("implementation import leaked into disclosure")
		}
	}
	v, _ := retirementContractFixture()
	raw, _ := json.Marshal(v)
	if strings.Contains(string(raw), "PRIVATE") {
		t.Fatal("fixture leaked")
	}
}
