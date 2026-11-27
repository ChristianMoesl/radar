package protocol

import (
	"strings"
	"testing"
	"time"
)

func agentRemoveContractFixture() (DiagnosticAgentRemove, DiagnosticMaintenanceRequest) {
	q := DiagnosticMaintenanceRequest{Schema: DiagnosticsVersion, Operation: "agent_remove", Mode: "preview", Scope: "origin", ParentID: "record", AgentID: "worker"}
	v := DiagnosticAgentRemove{Schema: DiagnosticsVersion, Operation: "agent_remove", Mode: "preview", Scope: "origin", RecordID: "record", TaskID: 7, AgentID: "worker", FeedbackWorkspaceID: "feedback", ScopeWorkspaceID: "origin", AgentWorkspaceID: "origin", Evidence: "native_exact_agent_removal_plan_not_consent", PlanID: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(30 * time.Second).UTC().Format(time.RFC3339Nano), Revision: 1, SchedulesBefore: 2, EnabledSchedulesBefore: 1, DispatchEvidence: "not_dispatched", Problem: "none", Outcome: "not_attempted", Observation: UnknownDiagnosticAgentRemoveObservation(), Limitations: DiagnosticAgentRemoveLimitations()}
	return v, q
}
func TestDiagnosticAgentRemoveCheckpointObservationsAreFiniteAndIndependent(t *testing.T) {
	apply := func() (DiagnosticAgentRemove, DiagnosticMaintenanceRequest) {
		v, q := agentRemoveContractFixture()
		q.Mode, v.Mode, q.PlanID = "apply", "apply", v.PlanID
		v.OperationID, v.DispatchEvidence, v.ResourceRefreshPending, v.Outcome = "operation", "native_remove_callback_entered_not_resource_outcome", true, "native_handler_returned_unverified"
		v.Observation = DiagnosticAgentRemoveObservation{Evidence: "bounded_native_checkpoint_reads", Registration: "absent", Schedules: "paused_or_empty_preserved", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		return v, q
	}
	for _, registration := range []string{"unknown", "original_present", "absent"} {
		for _, schedules := range []string{"unknown", "original_preserved", "paused_or_empty_preserved"} {
			for _, failed := range []bool{false, true} {
				v, q := apply()
				v.Observation.Registration, v.Observation.Schedules = registration, schedules
				if failed {
					v.Outcome, v.Problem = "unverified_may_be_partial", "native_error"
				}
				if !ValidDiagnosticAgentRemove(v, q) {
					t.Fatal("handler status substituted for independently observed checkpoints", v)
				}
			}
		}
	}
	mutations := map[string]func(*DiagnosticAgentRemove, *DiagnosticMaintenanceRequest){
		"old_peer": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) { v.Schema, q.Schema = 41, 41 },
		"zero": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) {
			v.Observation = DiagnosticAgentRemoveObservation{}
		},
		"private_evidence": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) { v.Observation.Evidence = "PRIVATE" },
		"private_registration": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) {
			v.Observation.Registration = "PRIVATE"
		},
		"private_schedule": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) { v.Observation.Schedules = "PRIVATE" },
		"unknown_with_timestamp": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) {
			v.Observation.Evidence = "unknown_not_observed"
		},
		"unknown_with_effect": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) {
			v.Observation.Evidence, v.Observation.ObservedAt = "unknown_not_observed", ""
		},
		"missing_timestamp": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) { v.Observation.ObservedAt = "" },
		"timestamp_private": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) { v.Observation.ObservedAt = "PRIVATE" },
		"future": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) {
			v.Observation.ObservedAt = time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano)
		},
		"stale": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) {
			v.Observation.ObservedAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
		},
		"noncanonical": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) {
			v.Observation.ObservedAt = time.Now().UTC().Format("2006-01-02T15:04:05.000000000") + "0Z"
		},
		"cancelled": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) {
			v.Problem, v.Outcome = "cancelled_or_timed_out", "unverified_may_be_partial"
		},
		"preview": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) {
			v.Mode, q.Mode = "preview", "preview"
			q.PlanID = ""
			v.Outcome, v.Problem, v.OperationID, v.DispatchEvidence, v.ResourceRefreshPending = "not_attempted", "none", "", "not_dispatched", false
		},
		"empty_original_preserved": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) {
			v.SchedulesBefore, v.EnabledSchedulesBefore = 0, 0
			v.Observation.Schedules = "original_preserved"
		},
		"disabled_original_preserved": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) {
			v.EnabledSchedulesBefore = 0
			v.Observation.Schedules = "original_preserved"
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			v, q := apply()
			mutate(&v, &q)
			if ValidDiagnosticAgentRemove(v, q) {
				t.Fatal("invalid/fabricated checkpoint observation accepted")
			}
		})
	}
	for _, problem := range []string{"none", "native_error", "cancelled_or_timed_out"} {
		v, q := apply()
		v.Observation, v.Problem = UnknownDiagnosticAgentRemoveObservation(), problem
		if problem != "none" {
			v.Outcome = "unverified_may_be_partial"
		}
		if !ValidDiagnosticAgentRemove(v, q) {
			t.Fatal("unknown evidence was confused with handler outcome")
		}
	}
}

func TestDiagnosticAgentRemoveFiniteContract(t *testing.T) {
	for _, scope := range []string{"origin", "feedback", "installation"} {
		for _, mode := range []string{"preview", "apply"} {
			t.Run(scope+"/"+mode, func(t *testing.T) {
				v, q := agentRemoveContractFixture()
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
					v.DispatchEvidence = "native_remove_callback_entered_not_resource_outcome"
					v.ResourceRefreshPending = true
					v.Outcome = "native_handler_returned_unverified"
				}
				if !ValidDiagnosticAgentRemove(v, q) {
					t.Fatal("valid handler contract refused")
				}
				q.ParentID = "7"
				if !ValidDiagnosticAgentRemove(v, q) {
					t.Fatal("exact numeric task parent refused")
				}
				if mode == "apply" {
					for _, problem := range []string{"native_error", "cancelled_or_timed_out"} {
						v.Problem, v.Outcome = problem, "unverified_may_be_partial"
						if !ValidDiagnosticAgentRemove(v, q) {
							t.Fatal("partial uncertainty refused")
						}
					}
				}
			})
		}
	}
	changes := map[string]func(*DiagnosticAgentRemove, *DiagnosticMaintenanceRequest){
		"old_request": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) { q.Schema-- }, "negative_schedules": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) { v.SchedulesBefore = -1 },
		"oversized_schedules": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) {
			v.SchedulesBefore = AgentMaxFrameBytes + 1
		},
		"negative_enabled":      func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) { v.EnabledSchedulesBefore = -1 },
		"enabled_exceeds_total": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) { v.EnabledSchedulesBefore = 3 }, "old_view": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) { v.Schema-- },
		"parent": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) { q.ParentID = "other" }, "agent": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) { q.AgentID = "other" },
		"unknown_agent": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) {
			v.AgentID, q.AgentID = "PRIVATE bad/id", "PRIVATE bad/id"
		}, "scope": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) { v.Scope = "installation" },
		"foreign_workspace": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) { v.AgentWorkspaceID = "other" }, "feedback_identity": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) {
			q.Scope, v.Scope = "feedback", "feedback"
		},
		"expiry": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) {
			v.ExpiresAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
		}, "extended": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) {
			v.ExpiresAt = time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)
		},
		"plan": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) { v.PlanID = strings.Repeat("A", 64) }, "revision": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) { v.Revision = 9007199254740992 },
		"task": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) { v.TaskID = 0 }, "private_error": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) { v.Problem = "PRIVATE" },
		"outcome": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) { v.Outcome = "registration_absent" }, "dispatch": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) {
			v.DispatchEvidence = "native_remove_callback_entered_not_resource_outcome"
		},
		"pending": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) { v.ResourceRefreshPending = true }, "operation": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) { v.OperationID = "operation" },
		"limitations": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) { v.Limitations[0] = "PRIVATE" }, "empty_feedback": func(v *DiagnosticAgentRemove, q *DiagnosticMaintenanceRequest) { v.FeedbackWorkspaceID = "" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			v, q := agentRemoveContractFixture()
			change(&v, &q)
			if ValidDiagnosticAgentRemove(v, q) {
				t.Fatal("invalid contract accepted")
			}
		})
	}
}
