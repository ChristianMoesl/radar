package protocol

import (
	"strings"
	"testing"
	"time"
)

func agentStopContractFixture() (DiagnosticAgentStop, DiagnosticMaintenanceRequest) {
	q := DiagnosticMaintenanceRequest{Schema: DiagnosticsVersion, Operation: "agent_stop", Mode: "preview", Scope: "origin", ParentID: "record", AgentID: "worker"}
	v := DiagnosticAgentStop{Schema: DiagnosticsVersion, Operation: "agent_stop", Mode: "preview", Scope: "origin", RecordID: "record", TaskID: 7, AgentID: "worker", FeedbackWorkspaceID: "feedback", ScopeWorkspaceID: "origin", AgentWorkspaceID: "origin", Evidence: "native_exact_agent_stop_plan_not_consent", PlanID: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(30 * time.Second).UTC().Format(time.RFC3339Nano), Revision: 1, DesiredBefore: true, RunningBefore: true, DispatchEvidence: "not_dispatched", Problem: "none", Outcome: "not_attempted", Limitations: DiagnosticAgentStopLimitations()}
	return v, q
}
func TestDiagnosticAgentStopFiniteContract(t *testing.T) {
	for _, scope := range []string{"origin", "feedback", "installation"} {
		for _, mode := range []string{"preview", "apply"} {
			t.Run(scope+"/"+mode, func(t *testing.T) {
				v, q := agentStopContractFixture()
				v.Scope, q.Scope = scope, scope
				if scope == "installation" {
					v.ScopeWorkspaceID = ""
				}
				if scope == "feedback" {
					v.ScopeWorkspaceID, v.AgentWorkspaceID = "feedback", "feedback"
				}
				if mode == "apply" {
					q.Mode, v.Mode = "apply", "apply"
					q.PlanID = v.PlanID
					v.OperationID = "operation"
					v.DispatchEvidence = "native_stop_callback_entered_not_process_outcome"
					v.ResourceRefreshPending = true
					v.Outcome = "native_handler_returned_unverified"
				}
				if !ValidDiagnosticAgentStop(v, q) {
					t.Fatal("valid handler contract refused")
				}
				q.ParentID = "7"
				if !ValidDiagnosticAgentStop(v, q) {
					t.Fatal("exact numeric task parent refused")
				}
				if mode == "apply" {
					for _, problem := range []string{"native_error", "cancelled_or_timed_out"} {
						v.Problem, v.Outcome = problem, "unverified_may_be_partial"
						if !ValidDiagnosticAgentStop(v, q) {
							t.Fatal("partial uncertainty refused")
						}
					}
				}
			})
		}
	}
	changes := map[string]func(*DiagnosticAgentStop, *DiagnosticMaintenanceRequest){
		"old_request": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) { q.Schema-- }, "old_view": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) { v.Schema-- },
		"parent": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) { q.ParentID = "other" }, "agent": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) { q.AgentID = "other" },
		"unknown_agent": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) {
			v.AgentID, q.AgentID = "PRIVATE bad/id", "PRIVATE bad/id"
		}, "scope": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) { v.Scope = "installation" },
		"foreign_workspace": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) { v.AgentWorkspaceID = "other" }, "feedback_identity": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) {
			q.Scope, v.Scope = "feedback", "feedback"
		},
		"expiry": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) {
			v.ExpiresAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
		}, "extended": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) {
			v.ExpiresAt = time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)
		},
		"plan": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) { v.PlanID = strings.Repeat("A", 64) }, "revision": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) { v.Revision = 9007199254740992 },
		"task": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) { v.TaskID = 0 }, "private_error": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) { v.Problem = "PRIVATE" },
		"outcome": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) { v.Outcome = "process_exited" }, "dispatch": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) {
			v.DispatchEvidence = "native_stop_callback_entered_not_process_outcome"
		},
		"pending": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) { v.ResourceRefreshPending = true }, "operation": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) { v.OperationID = "operation" },
		"limitations": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) { v.Limitations[0] = "PRIVATE" }, "empty_feedback": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) { v.FeedbackWorkspaceID = "" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			v, q := agentStopContractFixture()
			change(&v, &q)
			if ValidDiagnosticAgentStop(v, q) {
				t.Fatal("invalid contract accepted")
			}
		})
	}
}
