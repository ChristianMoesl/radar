package protocol

import (
	"testing"
	"time"
)

func TestDiagnosticAgentStopObservationsAreFiniteAndIndependent(t *testing.T) {
	apply := func() (DiagnosticAgentStop, DiagnosticMaintenanceRequest) {
		v, q := agentStopContractFixture()
		q.Mode, v.Mode, q.PlanID = "apply", "apply", v.PlanID
		v.OperationID, v.DispatchEvidence, v.ResourceRefreshPending, v.Outcome = "operation", "native_stop_callback_entered_not_process_outcome", true, "native_handler_returned_unverified"
		v.Observation = DiagnosticAgentStopObservation{Evidence: "bounded_native_stop_reads", Registration: "matching_stopped_definition", Runner: "no_listener_and_no_live_observed_owner", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		return v, q
	}
	for _, registration := range []string{"unknown", "matching_stopped_definition", "original_preserved"} {
		for _, runner := range []string{"unknown", "matching_listener_present", "no_listener_and_no_live_observed_owner"} {
			for _, failed := range []bool{false, true} {
				v, q := apply()
				v.Observation.Registration, v.Observation.Runner = registration, runner
				if failed {
					v.Outcome, v.Problem = "unverified_may_be_partial", "native_error"
				}
				if !ValidDiagnosticAgentStop(v, q) {
					t.Fatal("handler status substituted for independently observed checkpoints", v)
				}
			}
		}
	}
	mutations := map[string]func(*DiagnosticAgentStop, *DiagnosticMaintenanceRequest){
		"old_peer": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) { v.Schema, q.Schema = 42, 42 },
		"zero": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) {
			v.Observation = DiagnosticAgentStopObservation{}
		},
		"private_evidence": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) { v.Observation.Evidence = "PRIVATE" },
		"private_registration": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) {
			v.Observation.Registration = "PRIVATE"
		},
		"private_schedule": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) { v.Observation.Runner = "PRIVATE" },
		"unknown_with_timestamp": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) {
			v.Observation.Evidence = "unknown_not_observed"
		},
		"unknown_with_effect": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) {
			v.Observation.Evidence, v.Observation.ObservedAt = "unknown_not_observed", ""
		},
		"missing_timestamp": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) { v.Observation.ObservedAt = "" },
		"timestamp_private": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) { v.Observation.ObservedAt = "PRIVATE" },
		"future": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) {
			v.Observation.ObservedAt = time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano)
		},
		"stale": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) {
			v.Observation.ObservedAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
		},
		"noncanonical": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) {
			v.Observation.ObservedAt = time.Now().UTC().Format("2006-01-02T15:04:05.000000000") + "0Z"
		},
		"cancelled": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) {
			v.Problem, v.Outcome = "cancelled_or_timed_out", "unverified_may_be_partial"
		},
		"preview": func(v *DiagnosticAgentStop, q *DiagnosticMaintenanceRequest) {
			v.Mode, q.Mode = "preview", "preview"
			q.PlanID = ""
			v.Outcome, v.Problem, v.OperationID, v.DispatchEvidence, v.ResourceRefreshPending = "not_attempted", "none", "", "not_dispatched", false
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			v, q := apply()
			mutate(&v, &q)
			if ValidDiagnosticAgentStop(v, q) {
				t.Fatal("invalid/fabricated checkpoint observation accepted")
			}
		})
	}
	for _, problem := range []string{"none", "native_error", "cancelled_or_timed_out"} {
		v, q := apply()
		v.Observation, v.Problem = UnknownDiagnosticAgentStopObservation(), problem
		if problem != "none" {
			v.Outcome = "unverified_may_be_partial"
		}
		if !ValidDiagnosticAgentStop(v, q) {
			t.Fatal("unknown evidence was confused with handler outcome")
		}
	}
}
